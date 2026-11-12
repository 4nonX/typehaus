#!/bin/sh
# Builds typehaus-linux-<arch>.deb and .rpm from prebuilt agent binaries.
# Usage: packaging/linux/build.sh VERSION BINDIR OUTDIR
#   BINDIR contains typehaus-agent-linux-amd64 and typehaus-agent-linux-arm64.
# Needs nfpm (go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest).
set -eu
VERSION="${1#v}"; BINDIR="$2"; OUTDIR="$3"
cd "$(dirname "$0")/../.."
NFPM="${NFPM:-nfpm}"
mkdir -p build/linux "$OUTDIR"
for arch in amd64 arm64; do
  cp "$BINDIR/typehaus-agent-linux-$arch" build/linux/typehaus
  for fmt in deb rpm; do
    VERSION="$VERSION" ARCH="$arch" "$NFPM" pkg -f packaging/linux/nfpm.yaml -p "$fmt" -t "$OUTDIR/typehaus-linux-$arch.$fmt"
  done
done
rm -rf build/linux
