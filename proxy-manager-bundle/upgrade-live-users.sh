#!/usr/bin/env bash
# Upgrade an existing bundle installation, preserving every credential/token.
set -Eeuo pipefail
[[ $EUID -eq 0 ]] || { echo 'Run via sudo' >&2; exit 1; }
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
SOURCE_BIN="$ROOT/vpn-manager"
[[ ! -x "$ROOT/vpn-manager-live" ]] || SOURCE_BIN="$ROOT/vpn-manager-live"
[[ -x /usr/local/libexec/proxy-manager-core ]] || { echo 'Existing bundle installation required' >&2; exit 1; }
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
install -m 755 "$ROOT/proxy-manager-v2.2.sh" /usr/local/libexec/proxy-manager-core
install -m 755 "$BACKUP/wrapper-new" /usr/local/libexec/proxy-manager-wrapper
install -m 755 "$BACKUP/wrapper-new" /usr/local/sbin/proxy-manager
# This explicit migration is the only VPN restart in this upgrade.
/usr/local/libexec/proxy-manager-core enable-live-users
echo "Live user provisioning enabled. Core backup: $BACKUP"
