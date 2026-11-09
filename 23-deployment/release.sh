#!/usr/bin/env bash
# Builds the service for every platform into dist/, with checksums.
# Run from anywhere:  ./23-deployment/release.sh [version]
set -euo pipefail

cd "$(dirname "$0")/.."

# Uses the git tag if there is one, or else the short commit.
version="${1:-$(git describe --tags --always --dirty)}"
targets=(linux/amd64 linux/arm64 darwin/arm64 windows/amd64)

rm -rf dist
mkdir -p dist

for target in "${targets[@]}"; do
  os="${target%/*}"
  arch="${target#*/}"
  out="dist/go-service-$os-$arch"
  if [ "$os" = windows ]; then
    out="$out.exe"
  fi

  echo "building $target"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$out" ./23-deployment
done

# Lets you confirm a file wasn't corrupted or changed on its way to the server:
#   sha256sum --check --ignore-missing SHA256SUMS
(cd dist && sha256sum go-service-* > SHA256SUMS)

echo "version $version"
ls -l dist
