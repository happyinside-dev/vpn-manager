#!/usr/bin/env bash
# Enable local statistics APIs and update the existing bundled installation.
set -Eeuo pipefail
[[ $EUID -eq 0 ]] || { echo 'Run via sudo' >&2; exit 1; }
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
XRAY_CONF=/usr/local/etc/xray/config.json
HY_CONF=/etc/hysteria/config.yaml
CORE=/usr/local/libexec/proxy-manager-core
for file in "$XRAY_CONF" "$HY_CONF" "$CORE" "$ROOT/vpn-manager" "$ROOT/proxy-manager-v2.2.sh"; do
 [[ -f $file ]] || { echo "Missing: $file" >&2; exit 1; }
done
command -v python3 >/dev/null
BACKUP="/var/backups/proxy-setup/traffic-upgrade-$(date +%Y%m%d-%H%M%S)-$$"
install -d -m 700 "$BACKUP"
cp -a "$XRAY_CONF" "$BACKUP/xray.json"
cp -a "$HY_CONF" "$BACKUP/hysteria.yaml"
cp -a "$CORE" "$BACKUP/core"
# Prepare and validate config changes before touching installed files.
python3 - "$BACKUP" <<'PY'
import json, pathlib, re, sys
root = pathlib.Path(sys.argv[1])
x = json.loads((root / 'xray.json').read_text())
api = x.setdefault('api', {})
if api.get('listen', '127.0.0.1:10085') != '127.0.0.1:10085':
    raise SystemExit('Custom Xray API address: configure statistics manually; see TRAFFIC.md')
api.setdefault('tag', 'api')
api['listen'] = '127.0.0.1:10085'
api['services'] = sorted(set(api.get('services', []) + ['StatsService']))
x.setdefault('stats', {})
level = x.setdefault('policy', {}).setdefault('levels', {}).setdefault('0', {})
level.update(statsUserUplink=True, statsUserDownlink=True)
(root / 'xray-new.json').write_text(json.dumps(x, indent=2) + '\n')
hy = (root / 'hysteria.yaml').read_text()
if re.search(r'^trafficStats\s*:', hy, re.M):
    section = re.search(r'^trafficStats\s*:\s*\n((?:[ \t]+[^\n]*\n|\n)*)', hy+'\n', re.M)
    if not section or not re.search(r'^\s+listen:\s*[\'\"]?127\.0\.0\.1:10086[\'\"]?\s*$', section[1], re.M) or re.search(r'^\s+secret:', section[1], re.M):
        raise SystemExit('Custom Hysteria statistics: configure manually; see TRAFFIC.md')
else:
    hy += '\ntrafficStats:\n  listen: 127.0.0.1:10086\n'
(root / 'hysteria-new.yaml').write_text(hy)
PY
xray run -test -config "$BACKUP/xray-new.json"
bash -n "$ROOT/proxy-manager-v2.2.sh"
rollback() {
 echo "Upgrade failed; restoring daemon configurations and core. Backup: $BACKUP" >&2
 cp -a "$BACKUP/xray.json" "$XRAY_CONF"
 cp -a "$BACKUP/hysteria.yaml" "$HY_CONF"
 cp -a "$BACKUP/core" "$CORE"
 systemctl restart xray hysteria-server || true
}
trap rollback ERR
# Preserve existing config ownership and modes.
cat "$BACKUP/xray-new.json" >"$XRAY_CONF"
cat "$BACKUP/hysteria-new.yaml" >"$HY_CONF"
install -m 755 "$ROOT/proxy-manager-v2.2.sh" "$CORE"
systemctl restart xray hysteria-server
systemctl is-active --quiet xray
systemctl is-active --quiet hysteria-server
bash "$ROOT/upgrade-telegram-only.sh"
trap - ERR
echo "Traffic collection enabled. Backup: $BACKUP"
