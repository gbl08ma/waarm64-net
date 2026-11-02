#!/bin/sh
# The release, reproducibly: the six binaries with the toolchain go.mod pins, no cgo, no paths, no build ID, no VCS
# stamp, each architecture's baseline level; LICENSES.txt, the texts of everything linked in (licenses.sh); and
# SHA256SUMS over all of them, what the release, the README and the page publish. The same version from the same source
# gives the same bytes on any host (README "Reproducible builds"); --check compares a build with published sums.
#   ./build.sh <version> [outdir]                 (outdir: dist/; also INSTALL.md, the install lines with the hashes)
#   ./build.sh --check <SHA256SUMS> <version>     (builds into a temporary directory and compares)
#   ./build.sh --install <SHA256SUMS> <version>   (prints INSTALL.md for published sums: no build)
set -eu
# The install lines, one block per target with its hash (README "Install" takes them at a release; the page has its own
# copy, webpc's web/local-release.ts, which tests/local-release.test.ts checks against these). Plain steps, no `curl | sh`:
# download, check the hash, mark it executable, run it.
install() { # <version> <SHA256SUMS>
  version=$1 sums=$2
  base=${WEBPC_NET_DOWNLOAD:-https://github.com/gbl08ma/webpc-net/releases/download/$version}
  sum() { sed -n "s/^\([0-9a-f]*\)  $1\$/\1/p" "$sums"; }
  echo "# Installing webpc-net $version"
  for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
    os=${target%/*} arch=${target#*/}
    exe=webpc-net-$version-$os-$arch
    check='sha256sum -c'
    [ "$os" = darwin ] && check='shasum -a 256 -c'
    cat <<EOF

## $os $arch

\`\`\`sh
mkdir -p ~/.local/bin
curl -fLo ~/.local/bin/webpc-net $base/$exe
echo "$(sum "$exe")  \$HOME/.local/bin/webpc-net" | $check || rm ~/.local/bin/webpc-net
chmod +x ~/.local/bin/webpc-net
~/.local/bin/webpc-net
\`\`\`
EOF
  done
  for arch in amd64 arm64; do
    exe=webpc-net-$version-windows-$arch.exe
    cat <<EOF

## windows $arch (PowerShell)

\`\`\`powershell
\$d = "\$env:LOCALAPPDATA\\Programs\\webpc-net"; New-Item -ItemType Directory -Force \$d | Out-Null; \$ProgressPreference = 'SilentlyContinue'
Invoke-WebRequest $base/$exe -OutFile "\$d\\webpc-net.exe"
if ((Get-FileHash "\$d\\webpc-net.exe").Hash -ne '$(sum "$exe")') { Remove-Item "\$d\\webpc-net.exe"; throw 'not the published hash' }
& "\$d\\webpc-net.exe"
\`\`\`
EOF
  done
}

if [ "${1-}" = --install ]; then
  install "${3:?usage: build.sh --install <SHA256SUMS> <version>}" "$2"
  exit 0
fi
if [ "${1-}" = --check ]; then
  sums=$(cd "$(dirname "${2:?usage: build.sh --check <SHA256SUMS> <version>}")" && pwd)/$(basename "$2")
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  "$(dirname "$0")/build.sh" "${3:?usage: build.sh --check <SHA256SUMS> <version>}" "$tmp" > /dev/null
  if cmp -s "$sums" "$tmp/SHA256SUMS"; then
    echo "build.sh: $3 matches $2"
    exit 0
  fi
  echo "build.sh: $3 doesn't match $2:" >&2
  diff "$sums" "$tmp/SHA256SUMS" >&2 || true
  exit 1
fi
version=${1:?usage: build.sh <version> [outdir]}
out=${2:-$(cd "$(dirname "$0")" && pwd)/dist}
case $out in /*) ;; *) out=$(pwd)/$out ;; esac
cd "$(dirname "$0")"
toolchain=$(sed -n 's/^toolchain //p' go.mod)
export GOTOOLCHAIN=$toolchain CGO_ENABLED=0 GOFLAGS= GOEXPERIMENT= GOAMD64=v1 GOARM64=v8.0
rm -rf "$out"
mkdir -p "$out"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${target%/*} arch=${target#*/}
  exe=webpc-net-$version-$os-$arch
  [ "$os" = windows ] && exe=$exe.exe
  GOOS=$os GOARCH=$arch go build -trimpath -buildvcs=false -ldflags="-s -w -buildid= -X main.version=$version" -o "$out/$exe" .
done
./licenses.sh "$version" > "$out/LICENSES.txt"
(cd "$out" && LC_ALL=C sha256sum LICENSES.txt webpc-net-* > SHA256SUMS && cat SHA256SUMS)
install "$version" "$out/SHA256SUMS" > "$out/INSTALL.md"
