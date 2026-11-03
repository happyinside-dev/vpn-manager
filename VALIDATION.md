# Validation report

Validation performed in the build sandbox on 2026-11-01 after integrating the supplied `proxy-setup.sh`.

## Passed

- `gofmt -l cmd internal` -> clean.
- `bash -n install/proxy-setup.sh` -> clean.
- `bash -n install/proxy-setup.integration.sh` -> clean.
- `install/proxy-setup.sh --help` executes and documents `-sni`, `-xsni`, `-hsni`, `-s` and Proxy Manager build/admin inputs.
- SQLite migration smoke test using Python's SQLite binding -> `001_init.sql` + `002_legacy_hysteria.sql` applied successfully and expected tables/columns exist.
- Legacy detection was corrected for the supplied old Xray config, whose single VLESS inbound has no `tag`; a single untagged legacy VLESS inbound is accepted and the managed config is subsequently tagged `vless-reality`.
- The integrated installer keeps the Xray API on `127.0.0.1:10085`, Proxy Manager UI on `127.0.0.1:18080`, and Hysteria auth on `127.0.0.1:18081`; none are added to UFW.
- The installer preserves the Proxy Manager SQLite database across reruns and blocks an unsafe first legacy migration without `-s`.

## Environment-limited checks

This sandbox cannot perform a real Debian VPS integration run with systemd, Xray and Hysteria bound to TCP/UDP 443. Those checks must run on the target VPS.

The environment also does not have a guaranteed populated Go/npm dependency cache, so if dependency resolution is unavailable, the complete build/test commands must be run on a network-enabled host:

```bash
go mod tidy
make fmt
make test
make vet
make build
```

Then on the VPS:

```bash
systemctl is-active proxy-manager xray hysteria-server
curl -f http://127.0.0.1:18081/internal/health
proxy-manager --config /etc/proxy-manager/config.yaml reconcile
ss -lntup
```

Confirm that management listeners are loopback-only and the public listeners remain Xray TCP/443 and Hysteria UDP/443.
