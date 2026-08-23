#!/usr/bin/env bash
# Builds the release artifacts for every supported platform into dist/.
#
#   scripts/build-release.sh VERSION [COMMIT]
#
# Produces griglia_VERSION_GOOS_GOARCH.tar.gz (zip on Windows) plus
# checksums.txt. Pure-Go SQLite keeps every target CGO-free.
set -euo pipefail

version="${1:?usage: build-release.sh VERSION [COMMIT]}"
commit="${2:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}"
build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ldflags="-s -w -X main.version=${version} -X main.commit=${commit} -X main.date=${build_date}"
targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64"

cd "$(dirname "$0")/.."
rm -rf dist
mkdir -p dist

for target in $targets; do
  goos="${target%/*}"
  goarch="${target#*/}"
  binary="griglia"
  if [ "$goos" = "windows" ]; then
    binary="griglia.exe"
  fi
  name="griglia_${version}_${goos}_${goarch}"
  stage="dist/${name}"
  mkdir -p "$stage"
  echo "building ${goos}/${goarch}" >&2
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "$ldflags" -o "${stage}/${binary}" ./cmd/griglia
  if [ "$goos" = "windows" ]; then
    if command -v zip >/dev/null; then
      (cd dist && zip -q -r "${name}.zip" "$name")
    else
      (cd dist && python3 -m zipfile -c "${name}.zip" "$name")
    fi
  else
    tar -czf "dist/${name}.tar.gz" -C dist "$name"
  fi
  rm -rf "$stage"
done

(cd dist && sha256sum -- *.tar.gz *.zip > checksums.txt)
echo "artifacts in dist/:" >&2
ls -1 dist >&2
