#!/usr/bin/env bash
set -Eeuo pipefail

# Bootstrap installer for proxy-manager v2.2 + vpn-manager subscriptions.
# Usage:
#   sudo bash install-with-subscriptions.sh [path/to/proxy-manager-v2.2.sh] [installer args...]
# If the core installer path is omitted, one proxy-manager-v2.2*.sh file is auto-detected next to this script.

[[ $EUID -eq 0 ]] || { echo "[x] Run as root (sudo -i or sudo bash ...)." >&2; exit 1; }

SELF_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
VPNM_SOURCE="$SELF_DIR/vpn-manager"
[[ -x $VPNM_SOURCE ]] || { echo "[x] Missing executable: $VPNM_SOURCE" >&2; exit 1; }

CORE_SOURCE=""
if [[ ${1:-} == *.sh && -f ${1:-} ]]; then
  CORE_SOURCE=$(readlink -f "$1")
  shift
else
  mapfile -t candidates < <(find "$SELF_DIR" -maxdepth 1 -type f \( -name 'proxy-manager-v2.2*.sh' -o -name 'proxy-manager*.sh' \) ! -name 'install-with-subscriptions.sh' -print | sort)
  if (( ${#candidates[@]} == 1 )); then
    CORE_SOURCE=${candidates[0]}
  elif (( ${#candidates[@]} == 0 )); then
    echo "[x] Core installer not found next to this script." >&2
    echo "    Put proxy-manager-v2.2.sh here or pass its path as the first argument." >&2
    exit 1
  else
    echo "[x] Multiple possible core installers found; pass the desired path explicitly:" >&2
    printf '    %s\n' "${candidates[@]}" >&2
    exit 1
  fi
fi

CORE_INSTALLED=/usr/local/libexec/proxy-manager-core
WRAPPER_TEMPLATE=/usr/local/libexec/proxy-manager-wrapper
SYNC_HELPER=/usr/local/libexec/proxy-manager-subscription-sync
PUBLIC_MANAGER=/usr/local/sbin/proxy-manager
VPNM_BIN=/usr/local/bin/vpn-manager
VPNM_DIR=/etc/vpn-manager
VPNM_SERVER=$VPNM_DIR/server.yaml
VPNM_USERS=$VPNM_DIR/users.yaml
VPNM_UNIT=/etc/systemd/system/vpn-manager.service
VPNM_USER=vpn-manager
VPNM_GROUP=vpn-manager
STATE_FILE=/etc/proxy-manager/state.json
NGINX_TLS_CONF=/etc/nginx/conf.d/proxy-tls.conf
BACKUP_ROOT=/var/backups/proxy-setup

log() { echo "[+] $*" >&2; }
warn() { echo "[!] $*" >&2; }
die() { echo "[x] $*" >&2; exit 1; }

install_runtime_files() {
  install -d -m 755 /usr/local/libexec

  # The original installer installs itself as /usr/local/sbin/proxy-manager.
  # Preserve that exact installed version as the core implementation.
  [[ -x $PUBLIC_MANAGER ]] || die "Core installer did not create $PUBLIC_MANAGER"
  install -m 755 "$PUBLIC_MANAGER" "$CORE_INSTALLED"
  install -m 755 "$VPNM_SOURCE" "$VPNM_BIN"

  cat >"$SYNC_HELPER" <<'EOSYNC'
#!/usr/bin/env bash
set -Eeuo pipefail
STATE_FILE=/etc/proxy-manager/state.json
VPNM_DIR=/etc/vpn-manager
VPNM_SERVER=$VPNM_DIR/server.yaml
VPNM_USERS=$VPNM_DIR/users.yaml
VPNM_BIN=/usr/local/bin/vpn-manager
VPNM_UNIT=/etc/systemd/system/vpn-manager.service
VPNM_USER=vpn-manager
VPNM_GROUP=vpn-manager
NGINX_TLS_CONF=/etc/nginx/conf.d/proxy-tls.conf
BACKUP_ROOT=/var/backups/proxy-setup

log() { echo "[+] $*" >&2; }
warn() { echo "[!] $*" >&2; }
die() { echo "[x] $*" >&2; exit 1; }

ensure_identity() {
  getent group "$VPNM_GROUP" >/dev/null 2>&1 || groupadd --system "$VPNM_GROUP"
  if ! id "$VPNM_USER" >/dev/null 2>&1; then
    useradd --system --gid "$VPNM_GROUP" --home-dir /nonexistent --shell /usr/sbin/nologin "$VPNM_USER"
  fi
  install -d -m 750 -o root -g "$VPNM_GROUP" "$VPNM_DIR"
}

backup_configs() {
  [[ -e $VPNM_SERVER || -e $VPNM_USERS || -e $VPNM_UNIT ]] || return 0
  local ts dir
  ts=$(date +%Y%m%d-%H%M%S)
  dir="$BACKUP_ROOT/subscription-$ts-$$"
  mkdir -p "$dir"
  chmod 700 "$BACKUP_ROOT" "$dir" 2>/dev/null || true
  [[ -e $VPNM_SERVER ]] && cp -a "$VPNM_SERVER" "$dir/server.yaml"
  [[ -e $VPNM_USERS ]] && cp -a "$VPNM_USERS" "$dir/users.yaml"
  [[ -e $VPNM_UNIT ]] && cp -a "$VPNM_UNIT" "$dir/vpn-manager.service"
}

write_server_snapshot() {
  [[ -s $STATE_FILE ]] || die "State not found: $STATE_FILE"
  local host port xsni hsni hinsecure pub sid
  host=$(jq -er '.server.public_ip' "$STATE_FILE")
  port=$(jq -er '.server.port' "$STATE_FILE")
  xsni=$(jq -er '.server.xray_sni' "$STATE_FILE")
  hsni=$(jq -er '.server.hy_sni' "$STATE_FILE")
  hinsecure=$(jq -r '.server.hy_insecure // false' "$STATE_FILE")
  pub=$(jq -er '.server.reality_public_key' "$STATE_FILE")
  sid=$(jq -er '.server.reality_short_id' "$STATE_FILE")

  cat >"$VPNM_SERVER.tmp" <<EOF_SERVER
server:
  host: "$host"

vless:
  enabled: true
  port: $port
  sni: "$xsni"
  public_key: "$pub"
  short_id: "$sid"
  flow: "xtls-rprx-vision"
  fingerprint: "chrome"

hysteria2:
  enabled: true
  port: $port
  sni: "$hsni"
  insecure: $hinsecure
  pin_sha256: ""
EOF_SERVER
  chown root:"$VPNM_GROUP" "$VPNM_SERVER.tmp"
  chmod 640 "$VPNM_SERVER.tmp"
  mv "$VPNM_SERVER.tmp" "$VPNM_SERVER"
}

sync_users() {
  "$VPNM_BIN" sync-users --state "$STATE_FILE" --users-config "$VPNM_USERS"
  chown root:"$VPNM_GROUP" "$VPNM_USERS"
  chmod 640 "$VPNM_USERS"
}

write_unit() {
  cat >"$VPNM_UNIT" <<'EOUNIT'
[Unit]
Description=Minimal sing-box subscription server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=vpn-manager
Group=vpn-manager
ExecStart=/usr/local/bin/vpn-manager -server-config /etc/vpn-manager/server.yaml -users-config /etc/vpn-manager/users.yaml -listen 127.0.0.1:8080
Restart=on-failure
RestartSec=2s
TimeoutStopSec=15s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=multi-user.target
EOUNIT
  chmod 644 "$VPNM_UNIT"
  systemctl daemon-reload
}

inject_nginx() {
  [[ -f $NGINX_TLS_CONF ]] || die "nginx TLS config not found: $NGINX_TLS_CONF"
  if grep -q 'Managed by vpn-manager subscription integration' "$NGINX_TLS_CONF"; then
    nginx -t >/dev/null
    return 0
  fi
  local tmp="$NGINX_TLS_CONF.vpn-manager.tmp"
  # proxy-setup owns this file and writes one server block ending at EOF.
  sed '$d' "$NGINX_TLS_CONF" >"$tmp"
  cat >>"$tmp" <<'EONGINX'

    # Managed by vpn-manager subscription integration.
    # The URL contains a bearer token, so do not access-log it.
    location ^~ /sub/ {
        access_log off;
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        add_header Cache-Control "no-store" always;
    }
}
EONGINX
  mv "$tmp" "$NGINX_TLS_CONF"
  nginx -t >/dev/null || die "nginx rejected subscription location"
  systemctl reload nginx
}

main() {
  command -v jq >/dev/null || die "jq is required"
  [[ -x $VPNM_BIN ]] || die "vpn-manager binary missing: $VPNM_BIN"
  ensure_identity
  backup_configs
  write_server_snapshot
  sync_users
  write_unit
  inject_nginx
  systemctl enable vpn-manager >/dev/null 2>&1 || true
  systemctl restart vpn-manager
  local ready=0 i
  for i in $(seq 1 30); do
    if curl -fsS --max-time 1 http://127.0.0.1:8080/health >/dev/null 2>&1; then
      ready=1
      break
    fi
    if ! systemctl is-active --quiet vpn-manager; then
      journalctl -u vpn-manager -n 30 --no-pager >&2 || true
      die "vpn-manager failed to start"
    fi
    sleep 0.2
  done
  (( ready == 1 )) || {
    journalctl -u vpn-manager -n 30 --no-pager >&2 || true
    die "vpn-manager health check failed"
  }
  log "Subscription backend synchronized. Existing subscription tokens were preserved."
}
main "$@"
EOSYNC
  chmod 755 "$SYNC_HELPER"

  cat >"$WRAPPER_TEMPLATE" <<'EOWRAP'
#!/usr/bin/env bash
set -Eeuo pipefail
CORE=/usr/local/libexec/proxy-manager-core
SYNC=/usr/local/libexec/proxy-manager-subscription-sync
SELF=/usr/local/sbin/proxy-manager
WRAPPER=/usr/local/libexec/proxy-manager-wrapper

[[ -x $CORE ]] || { echo "[x] Missing proxy-manager core: $CORE" >&2; exit 1; }

set +e
"$CORE" "$@"
rc=$?
set -e
(( rc == 0 )) || exit "$rc"

# Reinstalling the core writes itself over /usr/local/sbin/proxy-manager.
# Restore this wrapper after every successful invocation if necessary.
if [[ -f $WRAPPER ]] && ! cmp -s "$WRAPPER" "$SELF"; then
  install -m 755 "$WRAPPER" "$SELF"
fi

need_sync=0
case "${1:-}" in
  account)
    case "${2:-}" in add|enable|disable|rotate|delete) need_sync=1 ;; esac
    ;;
  apply) need_sync=1 ;;
  install|-S|--preserve-secrets|"") need_sync=1 ;;
esac

if (( need_sync )) && [[ -x $SYNC ]]; then
  if ! "$SYNC"; then
    echo "[!] VPN account operation succeeded, but subscription synchronization failed." >&2
    echo "    Retry with: sudo $SYNC" >&2
    exit 1
  fi
fi

# Print the stable subscription/import links immediately after account creation,
# and also when explicitly showing an account.
show_subscription_links=0
case "${1:-}" in
  account)
    case "${2:-}" in add|show) show_subscription_links=1 ;; esac
    ;;
esac

if (( show_subscription_links )) && [[ -n ${3:-} ]]; then
  xsni=$(jq -r '.server.xray_sni // empty' /etc/proxy-manager/state.json 2>/dev/null || true)
  if [[ -n $xsni ]]; then
    echo
    /usr/local/bin/vpn-manager links \
      --users-config /etc/vpn-manager/users.yaml \
      --base-url "https://${xsni}" \
      --profile-name "Proxy VPN" \
      "$3" || true
  fi
fi
EOWRAP
  chmod 755 "$WRAPPER_TEMPLATE"
  install -m 755 "$WRAPPER_TEMPLATE" "$PUBLIC_MANAGER"
}

log "Running existing proxy-manager installer: $CORE_SOURCE"
bash "$CORE_SOURCE" "$@"

log "Installing subscription backend and persistent manager wrapper..."
install_runtime_files
"$SYNC_HELPER"

XRAY_SNI=$(jq -r '.server.xray_sni' "$STATE_FILE")
log "Installation complete."
echo
echo "Subscription backend: active on 127.0.0.1:8080"
echo "Public subscription pattern: https://${XRAY_SNI}/sub/<TOKEN>"
echo "Users file: $VPNM_USERS"
echo
echo "Normal management remains the same:"
echo "  proxy-manager account add NAME"
echo "  proxy-manager account rotate NAME"
echo "  proxy-manager account disable NAME"
echo "  proxy-manager account enable NAME"
echo "  proxy-manager account delete NAME"
echo "Subscription credentials are synchronized automatically after those commands."
