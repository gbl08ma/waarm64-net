#!/bin/sh
# The release binaries, reproducibly: the six targets with the toolchain go.mod pins, no cgo, no paths, no build ID,
# no VCS stamp (this directory is inside webpc's repository for now). The same version from the same source gives
# the same bytes on any host; SHA256SUMS is what the release, the README and the page publish.
#   ./build.sh v0.1.0 [outdir]     (outdir: dist/)
set -eu
cd "$(dirname "$0")"
version=${1:?usage: build.sh <version> [outdir]}
out=${2:-dist}
toolchain=$(sed -n 's/^toolchain //p' go.mod)
rm -rf "$out"
mkdir -p "$out"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${target%/*} arch=${target#*/}
  exe=webpc-net-$version-$os-$arch
  [ "$os" = windows ] && exe=$exe.exe
  GOTOOLCHAIN=$toolchain CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOFLAGS= \
    go build -trimpath -buildvcs=false -ldflags="-s -w -buildid= -X main.version=$version" -o "$out/$exe" .
done
(cd "$out" && sha256sum webpc-net-* > SHA256SUMS && cat SHA256SUMS)
