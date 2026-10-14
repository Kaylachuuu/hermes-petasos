#!/bin/sh
# Build the device program into dist/. The plugin serves dist/petasos-linux-arm64 to the Car
# Thing, which updates itself whenever VERSION changes, so bump VERSION with every change. The
# macOS builds go inside the Mac app (see ../mac/build.sh).
set -e
cd "$(dirname "$0")"
v=$(cat VERSION)
for arch in arm64 ${EXTRA_ARCH:-}; do
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -buildid= -X main.version=$v" \
        -o "dist/petasos-linux-$arch" .
done
for arch in arm64 amd64; do
    CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "-s -w -buildid= -X main.version=$v" \
        -o "dist/petasos-darwin-$arch" .
done
# The Windows app is petasos.exe itself (app_windows.go); rsrc_windows_*.syso carries its icon,
# made from winres/ with: go run github.com/tc-hib/go-winres@latest make --in winres/winres.json --arch amd64,arm64
for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=windows GOARCH=$arch go build -trimpath -ldflags "-s -w -buildid= -H windowsgui -X main.version=$v" \
        -o "dist/petasos-windows-$arch.exe" .
done
sha256sum dist/petasos-*
