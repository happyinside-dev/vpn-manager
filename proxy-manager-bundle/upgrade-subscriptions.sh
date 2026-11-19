#!/usr/bin/env bash
# Upgrade subscription service only, using the bundled Linux amd64 binary.
set -Eeuo pipefail
[[ $EUID -eq 0 ]] || { echo 'Run via sudo' >&2; exit 1; }
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
SOURCE_BIN="$ROOT/vpn-manager-live"
BIN=/usr/local/bin/vpn-manager
DROPIN=/etc/systemd/system/vpn-manager.service.d/rules.conf
for file in "$BIN" /etc/vpn-manager/server.yaml /etc/vpn-manager/users.yaml; do
 [[ -s $file ]] || { echo "Missing existing subscription installation: $file" >&2; exit 1; }
done
[[ -x $SOURCE_BIN ]] || { echo "Missing bundled executable: $SOURCE_BIN" >&2; exit 1; }
for command in curl systemctl sha256sum; do command -v "$command" >/dev/null; done
(cd "$ROOT" && sha256sum --check binaries.sha256)
command -v sing-box >/dev/null || { echo 'Install sing-box 1.14.4 on this server for SRS validation before upgrading. Go is not required.' >&2; exit 1; }
help=$("$SOURCE_BIN" --help 2>&1)
[[ $help == *'-rules-config'* ]] || { echo 'Bundled binary does not support rule management or cannot run on this server.' >&2; exit 1; }
systemctl cat vpn-manager.service >/dev/null
BACKUP="/var/backups/proxy-setup/subscription-upgrade-$(date +%Y%m%d-%H%M%S)-$$"
install -d -m 700 "$BACKUP"
cp -a "$BIN" "$BACKUP/vpn-manager"
if [[ -f $DROPIN ]]; then cp -a "$DROPIN" "$BACKUP/rules.conf"; fi
rollback() {
 echo "Upgrade failed; restoring subscription binary and drop-in. Backup: $BACKUP" >&2
 install -m 755 "$BACKUP/vpn-manager" "$BIN.rollback"
 mv -f "$BIN.rollback" "$BIN"
 if [[ -f $BACKUP/rules.conf ]]; then cp -a "$BACKUP/rules.conf" "$DROPIN"; else rm -f "$DROPIN"; fi
 systemctl daemon-reload
 systemctl restart vpn-manager || true
}
trap rollback ERR
install -d -m 755 "$(dirname "$DROPIN")"
printf '[Service]\nStateDirectory=vpn-manager\n' >"$DROPIN"
install -m 755 "$SOURCE_BIN" "$BIN.new"
mv -f "$BIN.new" "$BIN"
systemctl daemon-reload
systemctl restart vpn-manager
ready=0
for ((i=0; i<30; i++)); do
 if curl -fsS --max-time 1 http://127.0.0.1:8080/health >/dev/null; then ready=1; break; fi
 sleep 0.2
done
(( ready == 1 ))
systemctl is-active --quiet vpn-manager
trap - ERR
echo "Subscription service updated. Backup: $BACKUP"
echo 'Xray, Hysteria and Telegram services were not restarted. Subscription URLs are unchanged.'
if [[ ! -s /etc/vpn-manager/rules.json && ! -e /var/lib/vpn-manager/rules/current/snapshot.json ]]; then
 echo 'Routing modes need an initial rule snapshot: configure /etc/vpn-manager/rules.json (see RULESETS.md), then restart vpn-manager.' >&2
fi
echo 'Check loading: journalctl -u vpn-manager -n 50 --no-pager; then refresh the client subscription.'
