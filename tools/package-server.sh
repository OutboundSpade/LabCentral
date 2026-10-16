#!/usr/bin/env bash
# Build a Linux amd64 hosted bundle; credentials and environment stay external.
set -euo pipefail
cd "$(dirname "$0")/.."
npm --prefix web ci
npm --prefix web run check
npm --prefix web run build
bundle=$(mktemp -d)
chmod 0755 "$bundle"
trap 'rm -rf "$bundle"' EXIT
version="${TAG:-dev}"
for name in labcentral-server labcentral-provider labcentral; do
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X github.com/OutboundSpade/LabCentral/v2/internal/cli.Version=$version" -o "$bundle/$name" "./cmd/$name"
done
cp LICENSE tools/toolchain.json "$bundle/"
cp -R web/build "$bundle/web"
cp -R deploy "$bundle/deploy"
mkdir -p dist
tar -C "$bundle" --owner=0 --group=0 -czf dist/labcentral-server-linux-amd64.tar.gz .
