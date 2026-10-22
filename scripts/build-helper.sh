#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
case "${1:-}" in
  ''|--universal) ;;
  *) echo 'Usage: sh scripts/build-helper.sh [--universal]' >&2; exit 2 ;;
esac
npm run build
go -C go-casthelper test -race ./...
mkdir -p build
if [ "${1:-}" = '--universal' ]; then
  CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go -C go-casthelper build -trimpath -ldflags '-s -w' -o ../build/casthelper-arm64 .
  CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go -C go-casthelper build -trimpath -ldflags '-s -w' -o ../build/casthelper-amd64 .
  lipo -create -output build/casthelper build/casthelper-arm64 build/casthelper-amd64
  rm build/casthelper-arm64 build/casthelper-amd64
else
  go -C go-casthelper build -trimpath -ldflags '-s -w' -o ../build/casthelper .
fi
codesign --force --sign - --identifier casthelper build/casthelper
codesign --verify --strict build/casthelper
