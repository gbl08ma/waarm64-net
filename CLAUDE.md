# waarm64-net

The local network helper for waarm64 (README.md): a Go program the user runs, which the page reaches over a WebSocket on
loopback and whose gvisor-tap-vsock network puts the guest on the host's sockets. Module
`github.com/gbl08ma/waarm64-net`.

**Where it stands:** its own repository since 2026-11-02 (it began in waarm64's `net/waarm64-net/`; `git log` keeps that
history). How waarm64 uses it is waarm64's `docs/networking/local-helper.md`, the decisions that bind are in its
`docs/decisions.md`, and the track's state is waarm64's `NET-HANDOVER.md` while the track is open (waarm64 is `github.com/gbl08ma/waarm64`, usually checked out beside
this one as `../waarm64`). It imports nothing from waarm64; waarm64's tests build it at a commit they pin (waarm64's
`tests/waarm64-net.ts`, `WAARM64_NET_SRC=../waarm64-net` for a local checkout), and its page's install lines are a copy of
`build.sh`'s (waarm64's `web/local-release.ts`, which waarm64's `tests/local-release.test.ts` checks against `build.sh
--install`).

## Rules

- **Little code of ours** is the trust argument (README "What you're trusting"): glue over gvisor-tap-vsock. Before
  adding code, look for the library's way; when a line count changes much, update the README's.
- **Dependencies:** ask the user before adding a module (as waarm64's rule). Each one linked into the binary is a row in
  THIRD_PARTY.md, its license read from the module's own file; recheck the table when `go.mod` changes
  (`GOOS=<os> go list -deps -f '{{with .Module}}{{.Path}}{{end}}' . | sort -u` for linux, darwin, windows).
- **The fork:** `go.mod` replaces `github.com/containers/gvisor-tap-vsock` with evidolob's fork at one commit of
  [#745](https://github.com/containers/gvisor-tap-vsock/pull/745) (IPv6). Move it deliberately: read the new commits,
  bump the pseudo-version (`go mod download -json github.com/evidolob/gvisor-tap-vsock@<sha>`), run the tests, update
  THIRD_PARTY.md. `leases.go` reads the network's DHCP pool from an unexported field (`ipPool`), as the tests read its
  stack: `TestLeasesKept` fails if a bump moves it. When #745 lands in a release, drop the `replace` and require the release (that also unblocks
  `go install`).
- **Reproducible:** `./build.sh <version>` builds the six targets with the pinned toolchain and baseline levels, the
  release's `LICENSES.txt` (`licenses.sh`) and `INSTALL.md` (the install lines with the hashes); two builds give the same
  SHA256SUMS on any host (`--check`). Don't add anything that stamps the build (time, paths, VCS state, the host's
  environment) or needs cgo. The install lines' shape is in `build.sh` and in waarm64's `web/local-release.ts`: change
  both together.
- **Security:** listen on loopback only; a WebSocket only with the token (constant-time compare) and from an allowed
  origin, exact match; nothing about the traffic in the logs; no guest-reachable API.
- **IPv6 out:** the guest's IPv6 default route only while the host reaches the internet over IPv6 (README "IPv6"); keep
  it in step with waarm64's page (`web/reach6.ts`, docs/networking/adapters.md §4 "IPv6 out").
- **Docs:** the README, THIRD_PARTY.md, SECURITY.md and this file say what exists now; history only where it's worth not
  re-discovering. Change them with the code.
- **Bound every run** (`timeout N`), and cap the memory of heavy ones (`systemd-run --user --scope -q -p MemoryMax=4G
  -p MemorySwapMax=0 -- timeout N <cmd>` in waarm64's dev VM).
- **Git:** push to `origin`; commits end with the attribution lines the session gives. Releases are tags only (below).

## Commands

```sh
go vet ./... && go test -race ./...      # ~10 s: a netstack guest over a real WebSocket
./build.sh v0.0.0-test /tmp/b1           # the release (~2 min for the six); `./build.sh --check /tmp/b1/SHA256SUMS v0.0.0-test` rebuilds and compares
go run . --origin http://127.0.0.1:8080  # beside waarm64's `npm run serve`; open the printed link for that origin (the Network wizard's This computer)
```

From waarm64's root: `scripts/ci-net.sh` runs `tests/net-local.test.ts` (Debian on the page through the helper;
`WAARM64_NET_SRC=../waarm64-net` to test this checkout rather than waarm64's pin). The
cloud container has no IPv6 at all (no `AF_INET6`), so there the guest-to-internet IPv6 path is tested only for being
off; the probe's "reached" side runs over IPv4 in `helper_test.go`.

## GitHub Actions

`ci.yml`: `go vet` and `go test -race` on one Linux runner, on pushes to `main` and pull requests that touch Go
code, `go.mod`/`go.sum`, the scripts or the workflows. `release.yml`: on a `v*` tag (or by hand, building without
releasing), `build.sh` on an x86_64 and an arm64 Linux runner; the release is published, with `INSTALL.md` as its
notes, only when both SHA256SUMS are identical. No macOS or Windows runners: the builds are
cross-compiled and reproducible anyway. Releasing is separate from waarm64's deploy: a release changes nothing on
waarm64's page until waarm64's `web/local-release.ts` takes its SHA256SUMS.

A release: only after an out-of-context review of what it adds (the user's, 2026-11-03: one cut too early means
another), and after `ci.yml` passed on its commit; then tag `vX.Y.Z` on `main` (the Go
module's version too) and push the tag. Then waarm64's `web/local-release.ts`
takes the release's version and sums.

Status and the next steps: waarm64's `NET-HANDOVER.md` while the track is open.
