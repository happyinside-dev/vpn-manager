#!/usr/bin/env bash
set -Eeuo pipefail
# Surgical upgrade of Telegram UI and subscription-sync, no Xray/Hysteria reinstall.
[[ $EUID -eq 0 ]] || { echo "Run via sudo" >&2; exit 1; }
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
SOURCE_BIN="$ROOT/vpn-manager"
[[ ! -x "$ROOT/vpn-manager-live" ]] || SOURCE_BIN="$ROOT/vpn-manager-live"
BIN=/usr/local/bin/vpn-manager
SYNC=/usr/local/libexec/proxy-manager-subscription-sync
INSTALLER="$ROOT/install-with-subscriptions.sh"
[[ -x "$SOURCE_BIN" && -f "$INSTALLER" && -x "$SYNC" ]] || { echo "Missing bundle binary or existing subscription-sync" >&2; exit 1; }
BACKUP="/var/backups/proxy-setup/telegram-upgrade-$(date +%Y%m%d-%H%M%S)-$$"
install -d -m 700 "$BACKUP"
cp -a "$BIN" "$BACKUP/vpn-manager"
cp -a "$SYNC" "$BACKUP/proxy-manager-subscription-sync"
rollback() {
 echo "[!] Upgrade failed; restoring existing binary and sync script" >&2
 cp -a "$BACKUP/vpn-manager" "$BIN"
 cp -a "$BACKUP/proxy-manager-subscription-sync" "$SYNC"
 systemctl restart vpn-manager || true
 systemctl restart vpn-manager-telegram || true
}
trap rollback ERR
# Validate the embedded sync helper before touching the installed copy.
awk '/^  cat >"\$SYNC_HELPER" <<.EOSYNC./ {copy=1;next} copy && /^EOSYNC$/ {exit} copy {print}' "$INSTALLER" >"$BACKUP/sync-new"
[[ -s "$BACKUP/sync-new" ]] || { echo "Unable to extract sync helper" >&2; exit 1; }
bash -n "$BACKUP/sync-new"
install -m 755 "$SOURCE_BIN" "$BIN"
install -m 755 "$BACKUP/sync-new" "$SYNC"
# This sync deliberately does not restart the Telegram service.
"$SYNC"
# An upgrade should restart the bot exactly once to load the new binary.
if systemctl is-enabled --quiet vpn-manager-telegram && [[ -s /etc/vpn-manager/telegram.json ]]; then
 systemctl restart vpn-manager-telegram
 systemctl is-active --quiet vpn-manager-telegram
fi
systemctl is-active --quiet vpn-manager
trap - ERR
echo "[+] Telegram bot upgraded. Backup: $BACKUP"
echo "[+] No VPN credentials, ports, Xray or Hysteria binaries changed."
