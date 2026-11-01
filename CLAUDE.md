# webpc-net

The local network helper for webpc (README.md): a Go program the user runs, which the page reaches over a WebSocket on
loopback and whose gvisor-tap-vsock network puts the guest on the host's sockets. Module
`github.com/gbl08ma/webpc-net`.

**Where it lives:** inside webpc's repository (`net/webpc-net/`) during its early development, the user's choice
(2026-11-01), so page and helper change together. It moves to its own repository, `github.com/gbl08ma/webpc-net`, once
the bulk is done; keep it movable: everything it needs is in this directory (its own `go.mod`, README, THIRD_PARTY.md,
this file, `build.sh`), it imports nothing from webpc, and webpc reaches it only by building it (`tests/net-local.test.ts`,
`scripts/ci-net.sh`). Releases, the second host's reproducibility check and `go install` wait on the move (and
`go install` also on IPv6 upstream, below). The plan and its decisions are webpc's `docs/plans/networking-local.md`
(§1 binds); webpc's CLAUDE.md applies here too, except as below.

## Rules

- **Little code of ours** is the trust argument (README "What you're trusting"): glue over gvisor-tap-vsock. Before
  adding code, look for the library's way; when a line count changes much, update the README's.
- **Dependencies:** ask the user before adding a module (as webpc's rule). Each one linked into the binary is a row in
  THIRD_PARTY.md, its license read from the module's own file; recheck the table when `go.mod` changes
  (`GOOS=<os> go list -deps -f '{{with .Module}}{{.Path}}{{end}}' . | sort -u` for linux, darwin, windows).
- **The fork:** `go.mod` replaces `github.com/containers/gvisor-tap-vsock` with evidolob's fork at one commit of
  [#745](https://github.com/containers/gvisor-tap-vsock/pull/745) (IPv6). Move it deliberately: read the new commits,
  bump the pseudo-version (`go mod download -json github.com/evidolob/gvisor-tap-vsock@<sha>`), run the tests, update
  THIRD_PARTY.md. When #745 lands in a release, drop the `replace` and require the release (that also unblocks
  `go install`).
- **Reproducible:** `./build.sh <version>` builds the six targets with the pinned toolchain; two builds give the same
  SHA256SUMS. Don't add anything that stamps the build (time, paths, VCS state) or needs cgo.
- **Security:** listen on loopback only; a WebSocket only with the token (constant-time compare) and from an allowed
  origin, exact match; nothing about the traffic in the logs; no guest-reachable API.
- **IPv6 out:** the guest's IPv6 default route only while the host reaches the internet over IPv6 (README "IPv6"); keep
  it in step with webpc's page (`web/reach6.ts`, docs/network-adapters.md §4 "IPv6 out").

## Commands

```sh
go vet ./... && go test -race ./...      # ~10 s: a netstack guest over a real WebSocket
./build.sh v0.0.0-test /tmp/b1           # the release binaries (twice, then diff the SHA256SUMS)
go run . --origin http://127.0.0.1:8080  # beside webpc's `npm run serve`; open the printed link's ws:// as ?localnet=
```

From webpc's root: `scripts/ci-net.sh` runs `tests/net-local.test.ts` (Debian on the page through the helper). The
cloud container has no IPv6 at all (no `AF_INET6`), so there the guest-to-internet IPv6 path is tested only for being
off; the probe's "reached" side runs over IPv4 in `helper_test.go`.

Status and the next steps: webpc's `NET-HANDOVER.md` while the track is open.
