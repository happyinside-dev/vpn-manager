# Proxy Manager

🌐 Language: **English** | [Русский](README_RU.md)

Small self-hosted control plane for one Xray VLESS/REALITY + Hysteria 2 VPS. SQLite is authoritative; Xray's in-memory user list is reconciled from SQLite, while Hysteria delegates authentication to Proxy Manager over a loopback-only HTTP endpoint.

## Architecture

```text
Browser --(127.0.0.1:18080 / SSH tunnel)--> Proxy Manager
                                            |-- SQLite
                                            |-- Xray HandlerService gRPC 127.0.0.1:10085
Hysteria --HTTP auth 127.0.0.1:18081--------|
```

The public proxy ports remain Xray TCP/443 and Hysteria UDP/443. Never open 10085, 18080, or 18081 in UFW. `internal_listen` is intentionally separate from the panel listener so an HTTPS reverse proxy cannot accidentally publish Hysteria's auth endpoint.

## Security properties

- Admin password: Argon2id, never stored plaintext.
- Session: 256-bit random opaque token; only SHA-256 of the token is stored in SQLite. Cookie is HttpOnly + SameSite=Strict; set `session_secure: true` when the panel is served via HTTPS.
- Mutating API calls require an authenticated session plus `X-CSRF-Token`.
- Login attempts are rate-limited in memory.
- Hysteria passwords are random 192-bit values and are never written to logs or audit metadata.
- REALITY private key and Hysteria TLS private key are **not** stored in Proxy Manager.
- Xray API and Hysteria auth listeners are loopback-only.
- The service runs as the dedicated `proxy-manager` user with systemd hardening.

## Build

Requirements: Go 1.23+, Node 20+ (22 recommended), npm, a C compiler and SQLite development headers (`libsqlite3-dev`) because the project uses `mattn/go-sqlite3`.

```bash
make build
```

`make build-web` compiles React/Vite and copies the resulting assets into `internal/api/static`; Go then embeds those assets into the final binary.

Checks:

```bash
make fmt
make test
make vet
```

The Xray Go dependency is pinned to `github.com/xtls/xray-core v1.241231.0` for a stable protobuf client surface; it talks successfully to newer compatible Xray servers over HandlerService. Current Xray documentation describes the API as gRPC and supports adding/removing VLESS users via HandlerService. See: https://xtls.github.io/en/config/api.html

Hysteria HTTP auth request/response and masquerade syntax are based on the official Hysteria 2 docs: https://v2.hysteria.network/docs/advanced/Full-Server-Config/ . URI encoding follows https://v2.hysteria.network/docs/developers/URI-Scheme/ .

## Install layout

```text
/usr/local/bin/proxy-manager
/etc/proxy-manager/config.yaml
/var/lib/proxy-manager/proxy-manager.db
/var/log/proxy-manager/
/etc/systemd/system/proxy-manager.service
```

Recommended permissions:

```bash
chown proxy-manager:proxy-manager /var/lib/proxy-manager
chmod 0700 /var/lib/proxy-manager
chmod 0600 /var/lib/proxy-manager/proxy-manager.db*
chown root:proxy-manager /etc/proxy-manager/config.yaml
chmod 0640 /etc/proxy-manager/config.yaml
```

## Configuration

Start from `config.example.yaml`. Only the REALITY **public** key and short ID belong here. `public_host` is used in generated links; `public_ip` is used as fallback.

Xray can use its simplified API configuration (supported by modern versions):

```json
"api": {
  "tag": "api",
  "listen": "127.0.0.1:10085",
  "services": ["HandlerService", "StatsService"]
}
```

The managed VLESS/REALITY inbound must have a stable tag, default `vless-reality`. It may start with an empty `clients` array; Proxy Manager adds users dynamically.

Hysteria:

```yaml
auth:
  type: http
  http:
    url: http://127.0.0.1:18081/internal/hysteria/auth

masquerade:
  type: proxy
  proxy:
    url: https://HY_SNI
    rewriteHost: true
```

The Hysteria HTTP backend receives `{addr, auth, tx}` and Proxy Manager returns HTTP 200 with `{ok,id}`. New accounts expect `auth` to contain `username:password`.

## Initialize

```bash
install -d -o proxy-manager -g proxy-manager -m 0700 /var/lib/proxy-manager
proxy-manager --config /etc/proxy-manager/config.yaml init-admin \
  --username admin --password-file /run/secure-admin-password
systemctl enable --now proxy-manager.service
```

Do not pass the initial password on the command line from an installer; use a root-created, short-lived password file owned by `proxy-manager`, then remove it.

## Safest access: SSH forwarding

Backend default: `127.0.0.1:18080`.

```bash
ssh -L 8080:127.0.0.1:18080 proxy@SERVER
```

Open `http://127.0.0.1:8080`. No panel firewall port is required.

An optional HTTPS reverse proxy may point at `127.0.0.1:18080`; enable `session_secure: true` and **never** proxy port 18081 or `/internal`.

## CLI

```bash
proxy-manager user add alice
proxy-manager user list
proxy-manager user list --json
proxy-manager user show alice
proxy-manager user enable alice
proxy-manager user disable alice
proxy-manager user delete alice

proxy-manager xray enable alice
proxy-manager xray disable alice
proxy-manager xray regenerate alice

proxy-manager hysteria enable alice
proxy-manager hysteria disable alice
proxy-manager hysteria regenerate alice

proxy-manager links alice
proxy-manager reconcile
```

The CLI reads the same SQLite DB and uses the same service layer as the web API.

## REST API

Implemented endpoints:

```text
POST   /api/login
POST   /api/logout
GET    /api/session
GET    /api/status
GET    /api/settings
POST   /api/admin/password

GET    /api/users
POST   /api/users
GET    /api/users/:id
PATCH  /api/users/:id
DELETE /api/users/:id
POST   /api/users/:id/enable
POST   /api/users/:id/disable
POST   /api/users/:id/xray/enable
POST   /api/users/:id/xray/disable
POST   /api/users/:id/xray/regenerate
POST   /api/users/:id/hysteria/enable
POST   /api/users/:id/hysteria/disable
POST   /api/users/:id/hysteria/regenerate
GET    /api/users/:id/links
```

Errors use `{ "error": { "code": "...", "message": "..." } }`.

## Reconciliation / restart recovery

At startup and every 30 seconds Proxy Manager:

1. reads enabled Xray profiles from SQLite;
2. reads live users from Xray HandlerService `GetInboundUsers`;
3. adds missing managed users;
4. removes stale users whose `email` has Proxy Manager's private `pm-<id>@proxy-manager` namespace.

It does not remove unrelated/static Xray users. This periodic loop also repairs state after an independent Xray restart.

## Atomicity

Account/Xray mutations run inside a SQLite transaction while the Xray gRPC operation is attempted. If Xray rejects the change, the DB transaction is rolled back. Credential regeneration also attempts to restore the old Xray credential if adding the replacement fails. Reconciliation is the second line of defense against process crashes at awkward moments.

Hysteria has no runtime provisioning operation: the HTTP auth callback reads the committed SQLite state directly.

## Legacy single-user migration

Before changing either daemon config, back up:

- Xray config;
- Hysteria config;
- Hysteria certificate/key;
- existing Proxy Manager SQLite DB, if any.

Then, while the old configs still contain the original credentials:

```bash
proxy-manager --config /etc/proxy-manager/config.yaml migrate-legacy \
  --xray-config /usr/local/etc/xray/config.json \
  --hysteria-config /etc/hysteria/config.yaml
```

Automatic migration is intentionally conservative. It refuses an Xray config containing more than one legacy VLESS client and refuses non-`password` Hysteria auth because those cases are ambiguous.

For a normal old single-user install it creates `legacy`, preserving the old Xray UUID and old Hysteria password. The Hysteria profile gets a compatibility flag so both the old raw-password auth string and the new `legacy:password` representation work.

**Important Xray ordering:** after importing, remove that migrated legacy client from the static VLESS `clients` list but keep its UUID in SQLite; then restart Xray and run `proxy-manager reconcile`. This prevents duplicate-UUID conflicts while preserving the existing VLESS link.

## Integration with `proxy-setup.sh`

`install/proxy-setup.sh` is the fully integrated version of the supplied bootstrap installer. It keeps Bash responsible for host bootstrap (Xray/Hysteria installation, SSH, UFW/BBR, backups and logs) while all account state and day-to-day user management lives in Proxy Manager. `install/proxy-setup.integration.sh` remains as a smaller reference showing the integration points.

The integrated installer can install Proxy Manager from a prebuilt binary (`PROXY_MANAGER_BIN=/path/to/proxy-manager`) or, when run from this source tree, build it from `PROXY_MANAGER_SOURCE` (defaults to the project root). Source builds require Go 1.23+, a compatible Node/npm toolchain, a C compiler and SQLite headers.

The existing flags keep their semantics:

- `-sni DOMAIN`: set both Xray and Hysteria SNI;
- `-xsni DOMAIN`: override only Xray SNI;
- `-hsni DOMAIN`: override only Hysteria SNI;
- `-s`: preserve server-level REALITY keys and Hysteria TLS certificate/key during reruns.

The installer should calculate effective values in this order:

```bash
XRAY_SNI="$SNI"
HY_SNI="$SNI"
[[ -n "$XSNI" ]] && XRAY_SNI="$XSNI"
[[ -n "$HSNI" ]] && HY_SNI="$HSNI"
```

Hysteria masquerade follows `HY_SNI`; there is no independent masquerade UI setting.

### Safe first migration from the old single-user installer

If the integrated installer detects the old static Xray UUID and/or Hysteria `auth.type: password` while Proxy Manager has no users yet, it refuses to continue unless `-s` is supplied. This prevents an accidental REALITY key or Hysteria certificate rotation from silently invalidating the old links. With `-s`, the old credentials are imported as the `legacy` account before the daemon configs are replaced.

The old Xray installer had no inbound tag. The migration logic accepts a single untagged VLESS inbound as legacy input, then the new Xray config uses the stable `vless-reality` tag and an empty static client list. Proxy Manager reconciles the imported UUID into the runtime through HandlerService.

On a fresh install with no legacy user, the installer creates one initial account named `default`. On later reruns it preserves the SQLite database and does not regenerate user credentials.

Reruns must never delete `/var/lib/proxy-manager/proxy-manager.db` and must not rotate REALITY keys or Hysteria TLS material as a side effect of creating/managing users.

## Health

`GET /api/status` reports safe booleans/metadata for SQLite, Xray API, Xray systemd service, Hysteria systemd service, Proxy Manager state, configured endpoint/port/SNIs and enabled-user count. It never returns private keys or passwords.

For installer-local checks:

```bash
curl -f http://127.0.0.1:18081/internal/health
systemctl is-active proxy-manager xray hysteria-server
proxy-manager reconcile
```

## Tests

Automated tests cover migrations, duplicate account creation, account/protocol toggles, credential regeneration, Hysteria auth allow/deny/disabled behavior, URL escaping, and Xray reconciliation through a mock adapter.
