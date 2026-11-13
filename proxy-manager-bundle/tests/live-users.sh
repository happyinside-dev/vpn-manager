#!/usr/bin/env bash
# Integration of real account functions with simulated daemon APIs. No root needed.
set -Eeuo pipefail
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
TEST_DIR=$(mktemp -d)
trap 'rm -rf "$TEST_DIR"' EXIT
export TEST_DIR
WORK=$TEST_DIR/work; mkdir "$WORK"
STATE_DIR=$TEST_DIR/state; mkdir "$STATE_DIR"
STATE_FILE=$STATE_DIR/state.json
HY_DIR=$TEST_DIR/hysteria; mkdir "$HY_DIR"
HY_CONF=$HY_DIR/config.yaml
XRAY_CONF=$TEST_DIR/xray.json
BACKUP_ROOT=$TEST_DIR/backups
NGINX_DEST_PORT=8443
VPNM_BIN=$TEST_DIR/sync
export PATH="$TEST_DIR:$PATH"
cat >"$TEST_DIR/xray" <<'MOCK'
#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
p=Path(os.environ['TEST_DIR'])
if sys.argv[1]=='run': sys.exit(0)
users=json.loads((p/'runtime.json').read_text())
if sys.argv[2]=='rmu':
 name=sys.argv[-1]
 existed=name in users
 users.pop(name,None)
 print('Removed 1 user(s) in total.' if existed else 'User '+name+' not found.\nRemoved 0 user(s) in total.')
else:
 conf=json.loads(Path(sys.argv[-1]).read_text())
 user=conf['inbounds'][0]['settings']['clients'][0]
 name=user['email']
 if (p/'fail-add').exists():
  (p/'fail-add').unlink()
  print('RPC rejected\nAdded 0 user(s) in total.')
  sys.exit(0) # Xray CLI reports operation failures with exit zero.
 users[name]=user['id']
 print('Added 1 user(s) in total.')
(p/'runtime.json').write_text(json.dumps(users))
MOCK
cat >"$VPNM_BIN" <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
chmod +x "$TEST_DIR/xray" "$VPNM_BIN"
# Load function definitions without running the root installer or its traps.
source <(sed -n '/^valid_account_name()/,/^# ---------- main ----------/p' "$ROOT/proxy-manager.sh" | sed '$d')
log(){ :; }; warn(){ :; }; die(){ echo "$*" >&2; exit 1; }
chown(){ :; }
systemctl(){ [[ $1 == show ]] || { echo 'UNEXPECTED SERVICE ACTION' >&2; return 90; }; }
curl(){
 if [[ $* == *'/kick'* ]]; then
  jq -e 'length == 1 and .[0] != "bob"' "$WORK/kick.json" >/dev/null
 fi
}
cat >"$STATE_FILE" <<'STATE'
{"version":2,"server":{"port":443,"public_ip":"127.0.0.1","xray_sni":"example.com","hy_sni":"example.com","reality_private_key":"private","reality_public_key":"public","reality_short_id":"1234","sentinel_uuid":"sentinel","sentinel_hy_password":"sentinel-pass"},"accounts":{"alice":{"name":"alice","enabled":true,"xray_uuid":"old","hysteria_password":"old"},"bob":{"name":"bob","enabled":true,"xray_uuid":"bob-id","hysteria_password":"bob-pass"}}}
STATE
printf '%s' '{"alice":"old","bob":"bob-id"}' >"$TEST_DIR/runtime.json"
printf '%s\n' 'url: http://127.0.0.1:18081/auth' >"$HY_CONF"
touch "$STATE_DIR/live-users"
load_state
write_xray
assert_bob(){
 jq -e '.bob == "bob-id"' "$TEST_DIR/runtime.json" >/dev/null
 jq -e '.accounts.bob.xray_uuid == "bob-id"' "$STATE_FILE" >/dev/null
 jq -e '.bob == "bob-pass"' "$HY_DIR/auth-users.json" >/dev/null
}
transactional_account_change rotate alice
assert_bob
NEW_ID=$(jq -r '.accounts.alice.xray_uuid' "$STATE_FILE")
[[ $NEW_ID != old ]]
jq -e --arg id "$NEW_ID" '.alice == $id' "$TEST_DIR/runtime.json" >/dev/null
transactional_account_change disable alice
assert_bob
jq -e 'has("alice") | not' "$TEST_DIR/runtime.json" >/dev/null
jq -e 'has("alice") | not' "$HY_DIR/auth-users.json" >/dev/null
transactional_account_change enable alice
assert_bob
transactional_account_change add charlie
assert_bob
transactional_account_change delete charlie
assert_bob
# A zero-exit RPC failure must rollback UUID/password rather than claim success.
cp "$STATE_FILE" "$TEST_DIR/before.json"
touch "$TEST_DIR/fail-add"
if (transactional_account_change rotate alice); then echo 'Missing rollback' >&2; exit 1; fi
cmp "$STATE_FILE" "$TEST_DIR/before.json"
assert_bob
jq -e --arg id "$NEW_ID" '.alice == $id' "$TEST_DIR/runtime.json" >/dev/null
[[ ! -e $STATE_DIR/pending-user ]]
# Simulate crash after state persisted but before runtime/auth/config update.
printf 'alice\n' >"$STATE_DIR/pending-user"
jq '.accounts.alice.xray_uuid="crash-recovered"' "$STATE_FILE" >"$STATE_FILE.tmp"
mv "$STATE_FILE.tmp" "$STATE_FILE"
recover_live_users
assert_bob
jq -e '.alice == "crash-recovered"' "$TEST_DIR/runtime.json" >/dev/null
[[ ! -e $STATE_DIR/pending-user ]]
echo 'Live account operations, rollback and crash recovery passed; no daemon restarts.'
