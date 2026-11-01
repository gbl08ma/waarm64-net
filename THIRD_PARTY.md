# Third-party code in webpc-net

webpc-net's own code is MIT ([LICENSE](LICENSE), webpc's, copied here so the directory moves whole). Nothing in this directory is copied from another project: the files are
ours, written against the libraries below. Checked 2026-11-01 from the module cache (each module's own `LICENSE`
file), for the modules a build for any of the six targets links (`go list -deps` with `GOOS` linux, darwin and
windows; `go version -m` on a binary lists them too).

## What the helper is made of

| Module | Version | License | What for |
|---|---|---|---|
| [gvisor-tap-vsock](https://github.com/containers/gvisor-tap-vsock) | **[#745](https://github.com/containers/gvisor-tap-vsock/pull/745)'s head, `7fea9a947613`** (evidolob's fork, branch `guillaumerose-ipv62-fixes`, 2026-10-21; `go.mod`'s `replace`), over v0.9.0 | Apache-2.0 | the whole network: the switch, DHCP and DHCPv6, DNS, router advertisements, the TCP, UDP and ICMP forwarders to the host's sockets (Podman machine's and Lima's network) |
| [gVisor](https://github.com/google/gvisor) (`gvisor.dev/gvisor`) | `v0.0.0-20260413194555-9680d69bf798` | Apache-2.0 | the userspace TCP/IP stack under it; `ra.go` uses its packet headers |
| [coder/websocket](https://github.com/coder/websocket) | v1.8.15 | ISC | the WebSocket |
| [miekg/dns](https://github.com/miekg/dns) | v1.1.73 | BSD-3-Clause | gvisor-tap-vsock's DNS server |
| [insomniacslk/dhcp](https://github.com/insomniacslk/dhcp) | `v0.0.0-20240710054256-ddd8a41251c9` | BSD-3-Clause | gvisor-tap-vsock's DHCP and DHCPv6 servers |
| [google/gopacket](https://github.com/google/gopacket) | v1.1.19 | BSD-3-Clause | gvisor-tap-vsock's packet logging (and the tests' packets) |
| [inetaf/tcpproxy](https://github.com/inetaf/tcpproxy) | `v0.0.0-20260515195445-c159a6051109` | Apache-2.0 | gvisor-tap-vsock's TCP forwarder |
| [sirupsen/logrus](https://github.com/sirupsen/logrus) | v1.10.2 | MIT | gvisor-tap-vsock's logs |
| [google/btree](https://github.com/google/btree) | v1.1.2 | Apache-2.0 | gVisor |
| [u-root/uio](https://github.com/u-root/uio) | `v0.0.0-20240224005618-d2acac8f3701` | BSD-3-Clause | insomniacslk/dhcp |
| [pierrec/lz4](https://github.com/pierrec/lz4) | v4.1.14 | BSD-3-Clause | u-root/uio |
| [mdlayher/vsock](https://github.com/mdlayher/vsock), [mdlayher/socket](https://github.com/mdlayher/socket) | v1.3.0, v0.6.0 | MIT | gvisor-tap-vsock's transports (linked, unused) |
| [Microsoft/go-winio](https://github.com/Microsoft/go-winio) | v0.6.2 | MIT | gvisor-tap-vsock on Windows (linked there, unused) |
| [linuxkit/virtsock](https://github.com/linuxkit/virtsock) | `v0.0.0-20220523201153-1a23e78aa7a2` | Apache-2.0 | gvisor-tap-vsock on Windows (linked there, unused) |
| [pkg/errors](https://github.com/pkg/errors) | v0.9.1 | BSD-2-Clause | go-winio (Windows) |
| `golang.org/x/crypto`, `net`, `sync`, `sys`, `time`, `exp` | v0.57.0, v0.59.0, v0.23.0, v0.48.0, v0.12.0, `v0.0.0-20231110203233-9a3e6036ecaa` | BSD-3-Clause | Go's extended libraries |
| Go's standard library and runtime | the toolchain `go.mod` pins (go1.27.3) | BSD-3-Clause | |

All are permissive. None of the Apache-2.0 modules has a `NOTICE` file. A binary release carries these licenses'
texts beside the binaries (BSD, MIT, ISC and Apache-2.0 all ask for that in a binary distribution): `LICENSES.txt`,
which `licenses.sh` writes for `build.sh` from the same `go list -deps` and each module's own `LICENSE*`, `COPYING*`,
`NOTICE*` and `PATENTS` files (golang.org/x's and Go's have a `PATENTS` grant), Go's own first. It fails when a linked
module has none, so a new dependency can't slip into a release without its text.

**The fork:** `replace` points the module path `github.com/containers/gvisor-tap-vsock` at evidolob's fork, at one
commit; its code and license are upstream's (Apache-2.0, © the gvisor-tap-vsock authors), with #745's changes. When
#745 lands in a release, `go.mod` requires that release and this row names it.

## Test-only

`helper_test.go` uses gVisor's netstack as the guest and gopacket and insomniacslk/dhcp to build and read frames (the
same modules as above); the module graph has more (ginkgo, gomega, testify and others from gvisor-tap-vsock's own
tests), none of which is built into the helper.
