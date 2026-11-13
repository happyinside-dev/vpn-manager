#!/usr/bin/env bash
# Validate installation detection against isolated files; never invoke an upgrade.
set -Eeuo pipefail
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
TEST_DIR=$(mktemp -d)
trap 'rm -rf "$TEST_DIR"' EXIT
source <(sed -n '/^check_installation() {/,/^}/p' "$ROOT/upgrade-live-users.sh")
if check_installation "$TEST_DIR" >"$TEST_DIR/output" 2>&1; then
 echo 'Empty source checkout was accepted as an installation' >&2; exit 1
fi
[[ $(grep -c 'Missing installed file:' "$TEST_DIR/output") == 8 ]]
grep -q 'No installed files or services have been changed' "$TEST_DIR/output"
for file in \
 /usr/local/libexec/proxy-manager-core \
 /usr/local/libexec/proxy-manager-wrapper \
 /usr/local/libexec/proxy-manager-subscription-sync \
 /usr/local/sbin/proxy-manager \
 /usr/local/bin/vpn-manager \
 /etc/proxy-manager/state.json \
 /etc/vpn-manager/server.yaml \
 /etc/vpn-manager/users.yaml; do
 mkdir -p "$TEST_DIR$(dirname "$file")"
 printf 'fixture\n' >"$TEST_DIR$file"
done
check_installation "$TEST_DIR"
: >"$TEST_DIR/etc/proxy-manager/state.json"
if check_installation "$TEST_DIR" >"$TEST_DIR/output" 2>&1; then
 echo 'Empty account state was accepted' >&2; exit 1
fi
[[ $(grep -c 'Missing installed file:' "$TEST_DIR/output") == 1 ]]
echo 'Upgrade preflight: missing, complete and partial installations passed.'
