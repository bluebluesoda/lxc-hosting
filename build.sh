#!/usr/bin/env bash
# build.sh — compile the vpsmgr Go binary into ./bin (requires Go 1.22+).
set -euo pipefail
cd "$(dirname "$0")"
ROOT="$PWD"

log(){ echo "[build] $*"; }

if ! command -v go >/dev/null 2>&1; then
  log "error: go not found"
  log "run ./install.sh (installs golang-go) or: apt-get install -y golang-go"
  exit 1
fi

log "go $(go version | awk '{print $3}')"
mkdir -p bin
(cd src && CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$ROOT/bin/vpsmgr" .)
log "built bin/vpsmgr"
