# webpc-net

A small program you run on your own computer to give a [webpc](https://vm.tny.im) virtual machine a fast network:
the page in your browser connects to it over a WebSocket on `127.0.0.1`, and the guest's traffic goes out through
this computer's own sockets, at loopback's latency and your own uplink's speed. Without it, a browser can reach the
internet only through a relay (tailcat, Tailscale's DERP), because pages have no UDP.

> **Status: in development** (webpc's `docs/plans/networking-local.md`). It lives in webpc's repository for now and
> moves to `github.com/gbl08ma/webpc-net`, its module path already, once the bulk of it is done. No release yet. Every
> name (the program's, the subprotocol, the port) is provisional.

## What you're trusting

**About 650 lines of ours** (`*.go` here, tests, comments and blank lines aside), on **Podman's network stack**: the network itself (a
switch, DHCP, DHCPv6, DNS, router advertisements, and the forwarders that turn the guest's TCP, UDP and ICMP into
this computer's connections) is [gvisor-tap-vsock](https://github.com/containers/gvisor-tap-vsock), the library
behind Podman machine on macOS, Windows and Linux, Lima and CRC, on [gVisor](https://gvisor.dev)'s TCP/IP stack.
The WebSocket is [coder/websocket](https://github.com/coder/websocket). Everything linked in is listed, with its
license, in [THIRD_PARTY.md](THIRD_PARTY.md). Ours:

| File | What it does |
|---|---|
| `main.go` | flags, the listener (loopback only), the pairing link |
| `helper.go` | the network's settings; accepts a WebSocket only with the token and from an allowed origin, and closes it with a reason otherwise |
| `link.go` | moves frames between the WebSocket and the network's switch |
| `ra.go`, `reach6.go` | IPv6: the guest's default route only while this computer reaches the internet over IPv6, and the gateway saying it's a router (below) |
| `token.go` | the kept token, the version |

**What the guest can reach:** the internet and your LAN, as you (as a VPN's exit node would). Not this computer's own
loopback services, unless you pass `--host-loopback`. There is no API for the guest: gvisor-tap-vsock's
port-forwarding endpoints aren't served.

**Who can use it:** only a page that has your link (its token, 256 random bits, kept in your config directory so
a paired page keeps working after a restart; `--new-token` replaces it) *and* comes from an allowed origin
(`https://vm.tny.im`, plus each `--origin`). It listens on `127.0.0.1` only. Without both checks, any site you
visit could use it as a way into your LAN.

## Install

**No release yet:** the lines below are their shape, and each release's are in its notes (`INSTALL.md`, which
`build.sh` writes with that release's hashes), here and on the page. Each downloads one binary to `~/.local/bin`
(`%LOCALAPPDATA%\Programs\webpc-net` on Windows), checks it against the published SHA-256, marks it executable and
runs it. Nothing installs itself, asks for administrator rights or starts at login: you run it when you want the
network. On Linux, x86_64 (`linux-arm64`, `darwin-amd64` and `darwin-arm64` are the same lines, `shasum -a 256 -c`
on macOS):

```sh
mkdir -p ~/.local/bin
curl -fLo ~/.local/bin/webpc-net https://github.com/gbl08ma/webpc-net/releases/download/<version>/webpc-net-<version>-linux-amd64
echo "<its sha256>  $HOME/.local/bin/webpc-net" | sha256sum -c || rm ~/.local/bin/webpc-net
chmod +x ~/.local/bin/webpc-net
~/.local/bin/webpc-net
```

On Windows, in PowerShell (`windows-arm64.exe` for ARM):

```powershell
$d = "$env:LOCALAPPDATA\Programs\webpc-net"; New-Item -ItemType Directory -Force $d | Out-Null; $ProgressPreference = 'SilentlyContinue'
Invoke-WebRequest https://github.com/gbl08ma/webpc-net/releases/download/<version>/webpc-net-<version>-windows-amd64.exe -OutFile "$d\webpc-net.exe"
if ((Get-FileHash "$d\webpc-net.exe").Hash -ne '<its sha256>') { Remove-Item "$d\webpc-net.exe"; throw 'not the published hash' }
& "$d\webpc-net.exe"
```

The binaries aren't signed (Apple's or Microsoft's signatures would change the bytes the hashes and the rebuild check
are about). macOS doesn't quarantine what `curl` downloads, so Gatekeeper doesn't stop it; Windows' SmartScreen may
warn the first time. **From source**, with Go 1.27 or later (`go.mod` fetches the pinned toolchain): a checkout
(below), or, once IPv6 lands upstream, `go install github.com/gbl08ma/webpc-net@<version>` (it refuses a module whose
`go.mod` has a `replace`, which IPv6 needs for now: "IPv6" below).

## Running it

```sh
webpc-net                                  # prints the link to open
webpc-net --origin http://localhost:8080   # also accept webpc's dev server (npm run serve)
```

From a checkout of webpc, `go run .` in `net/webpc-net` instead. It prints a link like
`https://vm.tny.im/#net=ws://127.0.0.1:8740/<token>`: open it, and the page offers to add the helper as a connection
(its Network wizard, "This computer") for a machine's network adapter. Chromium asks once whether the page may reach
devices on your local network: allow it (if you refused, the page says where to change it). The link stays the same
across restarts (the token is kept), so a page paired once reconnects whenever the helper runs. Stop it with Ctrl-C.

| Flag | |
|---|---|
| `--listen 127.0.0.1:<port>` | where to listen (default `127.0.0.1:8740`; a loopback address only: `[::1]:<port>` too) |
| `--origin <origin>` | a page origin to accept besides `https://vm.tny.im`, exactly (repeatable): a copy of webpc you serve yourself, or its dev server |
| `--new-token` | a new token: pages paired before must pair again |
| `--host-loopback` | the guest reaches this computer's own `127.0.0.1` at `192.168.127.254` (off: only the internet and the LAN) |
| `--no-ipv6` | IPv4 only |
| `--mtu <bytes>` | the guest link's MTU (default 1500; 1280 to 65520, 576 with `--no-ipv6`) |
| `--version` | print the version |

It logs a line when it starts, for each page that connects or disconnects (its origin, how long) and for each refused
one (why), and when this computer's IPv6 reach changes; nothing about the traffic. The token is in your config
directory (`~/.config/webpc-net/token` on Linux, `~/Library/Application Support/webpc-net/token` on macOS,
`%AppData%\webpc-net\token` on Windows).

## The network

The guest gets `192.168.127.0/24` by DHCP (gateway and DNS at `.1`, as Podman machine's), and, unless `--no-ipv6`,
`fd77:6562:7063::/64` by router advertisement and DHCPv6 (gateway and DNS at `::1`). DNS answers come from this
computer's resolver. Every page connected to one helper is on the same switch: two machines (or two adapters)
attached to it share a LAN.

### IPv6

IPv6 is gvisor-tap-vsock's [#745](https://github.com/containers/gvisor-tap-vsock/pull/745) (unmerged), which
`go.mod` takes from its author's fork at one commit. It is on by default, and the guest is offered a default IPv6
route **only while this computer reaches the internet over IPv6**: the helper asks Cloudflare's, Google's and Quad9's
resolvers over IPv6 at the start and every minute (any answer counts; three lost in a row and it's out), and while it
is out the router advertisements it sends carry no default route. The guest then keeps its address on the link, and
its IPv6 to the internet fails at once ("network unreachable"), so programs fall back to IPv4 without waiting. When
the answer changes, the guest hears a new router advertisement at once. (webpc's page does the same for a tunnel's far
end: its `docs/network-adapters.md` §4 "IPv6 out".)

Upstream's switch answers router solicitations only and its advertisements have a 1000 s lifetime, so the helper
solicits one on each guest's behalf every 5 minutes, and whenever a guest first appears on a link. Its gateway's neighbor
advertisements lack the Router flag (its stack doesn't forward), which makes Linux drop the default route a few seconds after
traffic pauses, so the helper sets the flag on them.

## Reproducible builds

```sh
./build.sh v0.1.0                               # dist/: the six binaries, LICENSES.txt, SHA256SUMS, INSTALL.md
./build.sh --check SHA256SUMS v0.1.0            # rebuilds v0.1.0 from this checkout and compares with a release's sums
```

`CGO_ENABLED=0`, `-trimpath`, no build ID, no VCS stamp, each architecture's baseline (`GOAMD64=v1`, `GOARM64=v8.0`),
the toolchain `go.mod` pins (Go downloads it, checked against its checksum database): the same version from the same
source gives the same bytes on any host. `LICENSES.txt` is the licenses of everything linked in, in full, from the
module cache (`licenses.sh`); SHA256SUMS covers it too. To check a release yourself: check out its tag, run
`./build.sh --check` with the release's SHA256SUMS, and compare those with the hashes the README and the page publish.
Checked so far: two clean builds on one host (2026-11-01, x86_64 Linux: webpc's
`bench/net/results/local-helper/build-x86_64.txt`); a second host's check against it is webpc's NET-HANDOVER.md's.

## Protocol

`ws://127.0.0.1:<port>/<token>`, subprotocol `webpc-net.v1`. Each binary message holds one or more whole Ethernet
frames, each after its 4-byte big-endian length (QEMU's stream framing), both ways. Refusals complete the handshake
and close with 4001 (another version), 4003 (origin not accepted) or 4004 (wrong token), so the page can say why.

## Developing

`go test -race ./...` runs a guest on gVisor's netstack against the helper over a real WebSocket (DHCP, router
advertisements following the IPv6 reach, TCP both ways to this computer, the refusals). webpc's
`tests/net-local.test.ts` boots Debian on the page through it (`scripts/ci-net.sh`). See [CLAUDE.md](CLAUDE.md).
