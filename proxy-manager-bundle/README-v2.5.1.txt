Proxy Manager v2.5.1 - Telegram add/delete sync fix

Fix: backup_configs() in install-with-subscriptions.sh returned exit code 1
when the optional vpn-manager-telegram.service did not yet exist. Under
set -e this aborted subscription sync after a core account mutation, so
Telegram /add and /delete reported failure while the core account had
already been created/deleted.

The corrected function ends in `return 0` after optional backups.
No Go code or VPN protocol settings changed compared to v2.5.0.

QUICK FIX ON ALREADY INSTALLED VPS (no reinstall needed):
  sudo cp -a /usr/local/libexec/proxy-manager-subscription-sync{,.bak}
  sudo python3 - <<'PY'
from pathlib import Path
p=Path('/usr/local/libexec/proxy-manager-subscription-sync')
s=p.read_text()
needle='  [[ -e /etc/systemd/system/vpn-manager-telegram.service ]] && cp -a /etc/systemd/system/vpn-manager-telegram.service "$dir/vpn-manager-telegram.service"\n'
assert s.count(needle)==1
s=s.replace(needle, needle+'  return 0\n', 1)
p.write_text(s)
PY
  sudo bash -n /usr/local/libexec/proxy-manager-subscription-sync
  sudo /usr/local/libexec/proxy-manager-subscription-sync

To install clean v2.5.1 bundle later use the same v2.5.0 installer command,
with -S to preserve protocol credentials and subscription tokens.
