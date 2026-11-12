#!/usr/bin/env bash
# Builds the macOS agent with menu-bar support (cgo) for arm64 and amd64,
# a universal typehaus.app bundle, and zips everything into dist/agents.
# Must run on macOS (Xcode command line tools). Usage: build-app.sh VERSION
set -euo pipefail
VERSION="${1:-dev}"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/dist/agents"
WORK="$(mktemp -d)"
mkdir -p "$OUT"
cd "$ROOT"

LDFLAGS="-s -w -X main.version=${VERSION}"
for arch in arm64 amd64; do
  clangarch=$arch; [ "$arch" = amd64 ] && clangarch=x86_64
  CGO_ENABLED=1 GOOS=darwin GOARCH=$arch CC="clang -arch $clangarch" \
    MACOSX_DEPLOYMENT_TARGET=11.0 CGO_CFLAGS="-mmacosx-version-min=11.0" CGO_LDFLAGS="-mmacosx-version-min=11.0" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/typehaus-agent-darwin-$arch" ./cmd/typehaus-agent
done
lipo -create -output "$WORK/typehaus-agent" "$OUT/typehaus-agent-darwin-arm64" "$OUT/typehaus-agent-darwin-amd64"

APP="$WORK/typehaus.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$WORK/typehaus-agent" "$APP/Contents/MacOS/typehaus-agent"
sed "s/__VERSION__/${VERSION#v}/g" packaging/macos/Info.plist > "$APP/Contents/Info.plist"
iconutil -c icns -o "$APP/Contents/Resources/typehaus.icns" packaging/macos/icon.iconset
# ad-hoc signature (required on Apple Silicon; not notarised)
codesign --force --deep --sign - "$APP"
ditto -c -k --keepParent "$APP" "$OUT/typehaus-agent-darwin-universal.app.zip"
for arch in arm64 amd64; do codesign --force --sign - "$OUT/typehaus-agent-darwin-$arch"; done
ls -la "$OUT"
