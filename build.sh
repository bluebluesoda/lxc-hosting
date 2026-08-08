#!/usr/bin/env bash
# build.sh — compile the vpsmgr Go binary into ./bin (requires Go 1.22+).
# Usage: ./build.sh [VERSION]          # VERSION defaults to the source default
#        ./build.sh v0.1.0             # strip leading 'v' and inject version
#        GOOS=linux GOARCH=arm64 ./build.sh  # cross-compile (CGO_ENABLED=0)
set -euo pipefail
cd "$(dirname "$0")"
ROOT="$PWD"

log(){ echo "[build] $*"; }

if ! command -v go >/dev/null 2>&1; then
  log "error: go not found"
  log "run ./install.sh (installs golang-go) or: apt-get install -y golang-go"
  exit 1
fi

GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"
VERSION="${1:-}"
if [[ -n "$VERSION" ]]; then
  VERSION="${VERSION#v}"   # strip leading 'v' (v0.1.0 -> 0.1.0)
fi

log "go $(go version | awk '{print $3}') os=${GOOS} arch=${GOARCH} version=${VERSION:-<source default>}"
mkdir -p bin

LDFLAGS="-s -w"
if [[ -n "$VERSION" ]]; then
  LDFLAGS="$LDFLAGS -X vpsmgr/internal/ver.Version=$VERSION"
fi

OUT="$ROOT/bin/vpsmgr"
[[ "$GOOS" == "windows" ]] && OUT="$OUT.exe"
(cd src && CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build \
  -trimpath -buildvcs=false \
  -ldflags="$LDFLAGS" \
  -o "$OUT" .)
log "built bin/vpsmgr"

if [[ "$GOOS" == "$(go env GOOS)" && "$GOARCH" == "$(go env GOARCH)" ]]; then
  log "version: $("$OUT" version 2>/dev/null || true)"
fi
