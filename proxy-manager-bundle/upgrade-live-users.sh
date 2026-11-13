#!/usr/bin/env bash
# Upgrade an existing bundle installation, preserving every credential/token.
set -Eeuo pipefail
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
SOURCE_BIN="$ROOT/vpn-manager"
[[ ! -x "$ROOT/vpn-manager-live" ]] || SOURCE_BIN="$ROOT/vpn-manager-live"
# Check before any backup, binary replacement or service action.
check_installation() {
 local prefix=${1:-} missing=0 file
 for file in \
   /usr/local/libexec/proxy-manager-core \
   /usr/local/libexec/proxy-manager-wrapper \
   /usr/local/libexec/proxy-manager-subscription-sync \
   /usr/local/sbin/proxy-manager \
   /usr/local/bin/vpn-manager \
   /etc/proxy-manager/state.json \
   /etc/vpn-manager/server.yaml \
   /etc/vpn-manager/users.yaml; do
  if [[ ! -s "$prefix$file" ]]; then
   printf '[x] Missing installed file: %s\n' "$file" >&2
   missing=1
  fi
 done
 if (( missing )); then
  cat >&2 <<'EOMISSING'

This upgrades an existing bundle installation on the VPN server.
Downloading or cloning this repository does not install the VPN services.

If VPN runs on another VPS: copy this updated bundle there, connect over SSH,
and run: sudo bash upgrade-live-users.sh

For a NEW server, use the installer instead (it configures VPN, SSH and firewall):
  sudo bash install-with-subscriptions.sh ./proxy-manager.sh

If this IS the existing VPN server, do not reinstall blindly: check its original
installation layout and the paths listed above. The separate SQLite/web-panel
installation cannot be upgraded by this bundle script.
No installed files or services have been changed by this failed preflight.
EOMISSING
  return 1
 fi
}
case "${1:-}" in
 --check) check_installation; echo '[+] Existing bundle installation found.'; exit 0 ;;
 '') ;;
 *) echo 'Usage: sudo bash upgrade-live-users.sh [--check]' >&2; exit 2 ;;
esac
[[ $EUID -eq 0 ]] || { echo 'Run via sudo' >&2; exit 1; }
check_installation
for source in "$ROOT/proxy-manager.sh" "$ROOT/install-with-subscriptions.sh" "$ROOT/upgrade-telegram-only.sh"; do
 [[ -s $source ]] || { printf 'Missing bundle source: %s\n' "$source" >&2; exit 1; }
 bash -n "$source"
done
[[ -x "$SOURCE_BIN" ]] || { echo 'Build the updated vpn-manager binary first' >&2; exit 1; }
# Refuse stale bundle binaries before touching the installed services.
timeout 5 "$SOURCE_BIN" live-auth --help >/dev/null 2>&1
BACKUP="/var/backups/proxy-setup/live-upgrade-$(date +%Y%m%d-%H%M%S)-$$"
install -d -m 700 "$BACKUP"
cp -a /usr/local/libexec/proxy-manager-core "$BACKUP/core"
cp -a /usr/local/sbin/proxy-manager "$BACKUP/manager-wrapper"
cp -a /usr/local/libexec/proxy-manager-wrapper "$BACKUP/wrapper-template"
# Update the wrapper as well: account changes no longer run the full sync installer.
awk '/^  cat >"\$WRAPPER_TEMPLATE" <<.EOWRAP./ {copy=1;next} copy && /^EOWRAP$/ {exit} copy {print}' "$ROOT/install-with-subscriptions.sh" >"$BACKUP/wrapper-new"
[[ -s "$BACKUP/wrapper-new" ]]
bash -n "$BACKUP/wrapper-new"
# Upgrade subscription reader, bot and sync helper without reinstalling VPN.
bash "$ROOT/upgrade-telegram-only.sh"
install -m 755 "$ROOT/proxy-manager.sh" /usr/local/libexec/proxy-manager-core
install -m 755 "$BACKUP/wrapper-new" /usr/local/libexec/proxy-manager-wrapper
install -m 755 "$BACKUP/wrapper-new" /usr/local/sbin/proxy-manager
# This explicit migration is the only VPN restart in this upgrade.
/usr/local/libexec/proxy-manager-core enable-live-users
echo "Live user provisioning enabled. Core backup: $BACKUP"
