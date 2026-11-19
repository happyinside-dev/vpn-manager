#!/usr/bin/env bash
# Release preparation on a machine with Go. The VPS only runs the output binaries.
set -Eeuo pipefail
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
BUILD_DIR=$(mktemp -d)
trap 'rm -rf "$BUILD_DIR"' EXIT
cd "$ROOT/src"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o "$BUILD_DIR/vpn-manager" ./cmd/vpn-manager
install -m 755 "$BUILD_DIR/vpn-manager" "$ROOT/vpn-manager"
install -m 755 "$BUILD_DIR/vpn-manager" "$ROOT/vpn-manager-live"
(cd "$ROOT" && sha256sum vpn-manager vpn-manager-live > binaries.sha256)
echo 'Linux amd64 binaries and checksums updated. Include them in the release commit.'
