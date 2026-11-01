#!/bin/sh
# LICENSES.txt, the release's: the license texts of the helper and of everything a build for any of the six targets
# links (Go's standard library and runtime, and each module `go list -deps` names for linux, darwin and windows), each
# module's own files from the module cache, which go.sum checks. BSD, MIT, ISC and Apache-2.0 ask for their texts beside
# a binary. Deterministic: the same go.mod and go.sum give the same file on any host (build.sh hashes it).
#   ./licenses.sh [version] > LICENSES.txt
set -eu
cd "$(dirname "$0")"
export GOTOOLCHAIN=$(sed -n 's/^toolchain //p' go.mod) GOFLAGS= LC_ALL=C
version=${1:-devel}
goroot=$(go env GOROOT)

section() { # <title> <dir> <files...>
  title=$1 dir=$2
  shift 2
  printf '\n%s\n%s\n' "================================================================================" "$title"
  for f in "$@"; do
    printf '%s\n\n' "--- $f ---"
    cat "$dir/$f"
    printf '\n'
  done
}
licenseFiles() { # the license, notice and patent files at the top of a module
  (cd "$1" && ls | grep -i -E '^(licen[cs]e|copying|notice|patents)([.-].*)?$' | sort) || true
}

cat <<EOF
webpc-net $version: the license of webpc-net (MIT) and of everything its binaries link, in full.
The helper's binaries for Linux, macOS and Windows link Go's standard library and runtime and the modules below
(each in at least one of them); THIRD_PARTY.md in the source says what each is for.
EOF
# shellcheck disable=SC2046 # the files' names have no spaces
section "webpc-net (github.com/gbl08ma/webpc-net)" . LICENSE
section "Go's standard library and runtime ($(go env GOVERSION))" "$goroot" $(licenseFiles "$goroot")
for os in linux darwin windows; do
  GOOS=$os go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}}{{with .Replace}} {{.Path}} {{.Version}}{{end}}{{end}}{{end}}' .
done | sort -u | while read -r path ver rpath rver; do
  if [ -n "${rpath-}" ]; then
    dir=$(go list -m -f '{{.Replace.Dir}}' "$path")
    title="$path (from $rpath $rver)"
  else
    dir=$(go list -m -f '{{.Dir}}' "$path")
    title="$path $ver"
  fi
  files=$(licenseFiles "$dir")
  [ -n "$files" ] || { echo "licenses.sh: $path has no license file in $dir" >&2; exit 1; }
  # shellcheck disable=SC2086
  section "$title" "$dir" $files
done
