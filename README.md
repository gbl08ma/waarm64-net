# webpc-net

A small program you run on your own computer to give a [webpc](https://vm.tny.im) virtual machine a fast network:
the page in your browser connects to it over a WebSocket on `127.0.0.1`, and the guest's traffic goes out through
this computer's own sockets, at loopback's latency and your own uplink's speed. Without it, a browser can reach the
internet only through a relay (tailcat, Tailscale's DERP), because pages have no UDP.

> **Status: in development** (webpc's `docs/plans/networking-local.md`, steps L0 and L1). It lives in webpc's
> repository for now and moves to `github.com/gbl08ma/webpc-net`, its module path already, once the bulk of it is
> done. No release yet. Every name (the program's, the subprotocol, the port) is provisional.

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

## Running it

No release yet. From a checkout of webpc (Go 1.27 or later; `go.mod` fetches the pinned toolchain):

```sh
cd net/webpc-net
go run .                                  # prints the link to open
go run . --origin http://localhost:8080   # also accept webpc's dev server (npm run serve)
```

It prints a link like `https://vm.tny.im/#net=ws://127.0.0.1:8740/<token>`; open it. Until the page's connection
kind is built (L2), the page takes it as `?localnet=ws://…` or `#net=ws://…` at the start of the machine and puts its
first network adapter on the helper (a spike: the Network menu doesn't know about it). Chromium asks once whether
the page may reach devices on your local network: allow it.

Flags: `--listen 127.0.0.1:<port>` (default 8740), `--origin <origin>` (repeatable), `--new-token`,
`--host-loopback` (this computer's `127.0.0.1` at `192.168.127.254` for the guest), `--no-ipv6`, `--mtu` (1500),
`--version`.

**Planned (L1):** release binaries for Linux, macOS and Windows on x86_64 and arm64, with install lines that download
one with `curl` (`Invoke-WebRequest` on Windows), and `go install github.com/gbl08ma/webpc-net@<version>`. The
latter waits on IPv6 landing upstream (below): `go install` refuses a module whose `go.mod` has a `replace`.

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
./build.sh v0.1.0      # dist/: the six binaries and SHA256SUMS
```

`CGO_ENABLED=0`, `-trimpath`, no build ID, no VCS stamp, the toolchain `go.mod` pins: the same version from the same
source gives the same bytes on any host. Two clean builds of all six on one host matched (2026-11-01: 12.5–13.8 MB
each, 4.7–5.3 MB gzipped); a second host's check is L1's.

## Protocol

`ws://127.0.0.1:<port>/<token>`, subprotocol `webpc-net.v1`. Each binary message holds one or more whole Ethernet
frames, each after its 4-byte big-endian length (QEMU's stream framing), both ways. Refusals complete the handshake
and close with 4001 (another version), 4003 (origin not accepted) or 4004 (wrong token), so the page can say why.

## Developing

`go test -race ./...` runs a guest on gVisor's netstack against the helper over a real WebSocket (DHCP, router
advertisements following the IPv6 reach, TCP both ways to this computer, the refusals). webpc's
`tests/net-local.test.ts` boots Debian on the page through it (`scripts/ci-net.sh`). See [CLAUDE.md](CLAUDE.md).
