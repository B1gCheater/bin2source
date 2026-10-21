#!/usr/bin/env bash
# Build bin2source. With no arguments this produces the host binary in bin/.
# Cross compilation needs nothing extra: the tool is pure Go with no cgo.
#
#   ./build.sh                     -> bin/bin2source.exe        (host)
#   ./build.sh linux   amd64       -> bin/bin2source_linux_amd64
#   ./build.sh darwin  arm64       -> bin/bin2source_darwin_arm64
set -euo pipefail
cd "$(dirname "$0")"

GOOS="${1:-$(go env GOOS)}"
GOARCH="${2:-$(go env GOARCH)}"

ext=""
[ "$GOOS" = "windows" ] && ext=".exe"

name="bin2source_${GOOS}_${GOARCH}${ext}"
[ "$GOOS" = "$(go env GOOS)" ] && [ "$GOARCH" = "$(go env GOARCH)" ] && name="bin2source${ext}"

mkdir -p bin
echo "building bin/$name"
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
	go build -trimpath -ldflags="-s -w" -o "bin/$name" ./src
