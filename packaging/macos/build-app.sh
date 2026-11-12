#!/usr/bin/env bash
# Builds the macOS agent with menu-bar support (cgo) for arm64 and amd64,
# a universal typehaus.app bundle, and zips everything into dist/agents.
# Must run on macOS (Xcode command line tools). Usage: build-app.sh VERSION
#
# Signing and notarization switch on automatically when these variables are
# set (see docs/signing.md); otherwise everything is ad-hoc signed:
#   MACOS_CERTIFICATE_P12_BASE64  Developer ID Application certificate (.p12, base64)
#   MACOS_CERTIFICATE_PASSWORD    password of the .p12
#   MACOS_SIGNING_IDENTITY        e.g. "Developer ID Application: Jane Doe (TEAMID1234)"
#   APPLE_ID, APPLE_TEAM_ID, APPLE_APP_PASSWORD   for notarization (notarytool)
set -euo pipefail
VERSION="${1:-dev}"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/dist/agents"
WORK="$(mktemp -d)"
mkdir -p "$OUT"
cd "$ROOT"

# ---- signing identity --------------------------------------------------------
SIGNED=0
if [ -n "${MACOS_CERTIFICATE_P12_BASE64:-}" ] && [ -n "${MACOS_SIGNING_IDENTITY:-}" ]; then
  KEYCHAIN="$WORK/build.keychain-db"
  KPW="$(openssl rand -hex 16)"
  security create-keychain -p "$KPW" "$KEYCHAIN"
  security set-keychain-settings -lut 21600 "$KEYCHAIN"
  security unlock-keychain -p "$KPW" "$KEYCHAIN"
  echo "$MACOS_CERTIFICATE_P12_BASE64" | base64 --decode > "$WORK/cert.p12"
  security import "$WORK/cert.p12" -k "$KEYCHAIN" -P "${MACOS_CERTIFICATE_PASSWORD:-}" -T /usr/bin/codesign
  rm -f "$WORK/cert.p12"
  security set-key-partition-list -S apple-tool:,apple: -s -k "$KPW" "$KEYCHAIN" >/dev/null
  # shellcheck disable=SC2046
  security list-keychains -d user -s "$KEYCHAIN" $(security list-keychains -d user | tr -d '"')
  SIGN=(codesign --force --options runtime --timestamp --sign "$MACOS_SIGNING_IDENTITY")
  SIGNED=1
  echo "Signing with: $MACOS_SIGNING_IDENTITY"
else
  SIGN=(codesign --force --sign -)
  echo "No Developer ID configured – ad-hoc signing (Gatekeeper will warn users)"
fi

notarize() { # notarize <zip>
  if [ "$SIGNED" = 1 ] && [ -n "${APPLE_ID:-}" ] && [ -n "${APPLE_TEAM_ID:-}" ] && [ -n "${APPLE_APP_PASSWORD:-}" ]; then
    xcrun notarytool submit "$1" --apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" \
      --password "$APPLE_APP_PASSWORD" --wait --timeout 30m
    return 0
  fi
  return 1
}

# ---- build -------------------------------------------------------------------
LDFLAGS="-s -w -X main.version=${VERSION}"
for arch in arm64 amd64; do
  clangarch=$arch; [ "$arch" = amd64 ] && clangarch=x86_64
  CGO_ENABLED=1 GOOS=darwin GOARCH=$arch CC="clang -arch $clangarch" \
    MACOSX_DEPLOYMENT_TARGET=11.0 CGO_CFLAGS="-mmacosx-version-min=11.0" CGO_LDFLAGS="-mmacosx-version-min=11.0" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/typehaus-agent-darwin-$arch" ./cmd/typehaus-agent
  "${SIGN[@]}" "$OUT/typehaus-agent-darwin-$arch"
done
lipo -create -output "$WORK/typehaus-agent" "$OUT/typehaus-agent-darwin-arm64" "$OUT/typehaus-agent-darwin-amd64"

APP="$WORK/typehaus.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$WORK/typehaus-agent" "$APP/Contents/MacOS/typehaus-agent"
sed "s/__VERSION__/${VERSION#v}/g" packaging/macos/Info.plist > "$APP/Contents/Info.plist"
iconutil -c icns -o "$APP/Contents/Resources/typehaus.icns" packaging/macos/icon.iconset
"${SIGN[@]}" "$APP/Contents/MacOS/typehaus-agent"
"${SIGN[@]}" "$APP"
codesign --verify --strict --verbose=2 "$APP"

# ---- notarize ----------------------------------------------------------------
ditto -c -k --keepParent "$APP" "$WORK/typehaus-notarize.zip"
if notarize "$WORK/typehaus-notarize.zip"; then
  xcrun stapler staple "$APP"
  spctl --assess --type execute --verbose=2 "$APP" || true
  # bare CLI binaries can't be stapled; notarizing registers them with
  # Apple so Gatekeeper accepts them online
  ditto -c -k "$OUT/typehaus-agent-darwin-arm64" "$WORK/cli-arm64.zip"
  ditto -c -k "$OUT/typehaus-agent-darwin-amd64" "$WORK/cli-amd64.zip"
  notarize "$WORK/cli-arm64.zip"
  notarize "$WORK/cli-amd64.zip"
  echo "Notarized and stapled."
else
  echo "Notarization skipped (no Developer ID / Apple credentials)."
fi

rm -f "$OUT/typehaus-agent-darwin-universal.app.zip"
ditto -c -k --keepParent "$APP" "$OUT/typehaus-agent-darwin-universal.app.zip"
[ -n "${KEYCHAIN:-}" ] && security delete-keychain "$KEYCHAIN" || true
ls -la "$OUT"
