#!/bin/sh
# Build the device program into dist/. The plugin serves dist/petasos-linux-arm64 to the Car
# Thing, which updates itself whenever VERSION changes, so bump VERSION with every change.
set -e
cd "$(dirname "$0")"
v=$(cat VERSION)
for arch in arm64 ${EXTRA_ARCH:-}; do
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -buildid= -X main.version=$v" \
        -o "dist/petasos-linux-$arch" .
done
sha256sum dist/petasos-linux-*
