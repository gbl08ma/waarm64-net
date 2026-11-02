package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/insomniacslk/dhcp/dhcpv4"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/link/ethernet"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

const testOrigin = "http://page.test"

var guestMAC = [6]byte{0x52, 0x54, 0x00, 0x12, 0x34, 0x56}

// helperServer serves a helper on loopback; the test's context ends it.
// destroyStack stops the network's gVisor stack, which VirtualNetwork.Close leaves running and has no way to reach.
// The race detector's build of an idle stack keeps every core busy in gVisor's sleeper (0 CPU without -race), so stacks
// left behind by the tests before starve a later test's transfer into its deadline.
func destroyStack(h *Helper) {
	f := reflect.ValueOf(h.vn).Elem().FieldByName("stack")
	(*stack.Stack)(f.UnsafePointer()).Destroy()
}

func helperServer(t *testing.T, o Options) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, err := NewHelper(ctx, "tok", []string{DefaultOrigin, testOrigin}, o)
	if err != nil {
		t.Fatal(err)
	}
	var serving sync.WaitGroup
	t.Cleanup(func() { // last: after the test's connections and the server have closed
		done := make(chan struct{})
		go func() { serving.Wait(); close(done) }()
		select {
		case <-done:
			destroyStack(h)
		case <-time.After(5 * time.Second): // a link still on the switch would deliver into a destroyed stack
		}
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serving.Add(1)
		defer serving.Done()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func dial(t *testing.T, host, path, origin string, protos ...string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://"+host+path, &websocket.DialOptions{Subprotocols: protos, HTTPHeader: http.Header{"Origin": {origin}}})
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadLimit(MaxMessage)
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func TestRefusals(t *testing.T) {
	host := helperServer(t, Options{})
	for _, tc := range []struct {
		name, path, origin string
		protos             []string
		code               websocket.StatusCode
	}{
		{"origin", "/tok", "https://evil.test", []string{Subprotocol}, CloseOrigin},
		{"no origin", "/tok", "", []string{Subprotocol}, CloseOrigin},
		{"origin's scheme", "/tok", "http://vm.tny.im", []string{Subprotocol}, CloseOrigin},
		{"token", "/tak", testOrigin, []string{Subprotocol}, CloseToken},
		{"no token", "/", testOrigin, []string{Subprotocol}, CloseToken},
		{"version", "/tok", testOrigin, []string{"webpc-net.v0"}, CloseVersion},
		{"no version", "/tok", testOrigin, nil, CloseVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := dial(t, host, tc.path, tc.origin, tc.protos...)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _, err := c.Read(ctx)
			if got := websocket.CloseStatus(err); got != tc.code {
				t.Fatalf("closed with %v (%v), want %v", got, err, tc.code)
			}
		})
	}
}

func TestNormOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://vm.tny.im":      "https://vm.tny.im",
		"https://VM.tny.im:443/": "https://vm.tny.im",
		"http://localhost:8080":  "http://localhost:8080",
		"http://[::1]:80":        "http://[::1]",
	} {
		if got, err := normOrigin(in); err != nil || got != want {
			t.Errorf("normOrigin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"vm.tny.im", "ws://vm.tny.im", "https://vm.tny.im/x", "https://", "file:///"} {
		if _, err := normOrigin(bad); err == nil {
			t.Errorf("normOrigin(%q) accepted", bad)
		}
	}
}

// frames sends frames as one message, each after its length.
func send(t *testing.T, c *websocket.Conn, frames ...[]byte) {
	t.Helper()
	var msg []byte
	for _, f := range frames {
		msg = binary.BigEndian.AppendUint32(msg, uint32(len(f)))
		msg = append(msg, f...)
	}
	if err := c.Write(context.Background(), websocket.MessageBinary, msg); err != nil {
		t.Fatal(err)
	}
}

// await reads frames until one satisfies ok, or fails after a while.
func await(t *testing.T, c *websocket.Conn, what string, ok func(gopacket.Packet) bool) gopacket.Packet {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		for len(msg) >= 4 {
			n := int(binary.BigEndian.Uint32(msg))
			if n > len(msg)-4 {
				t.Fatalf("a message that isn't whole frames")
			}
			p := gopacket.NewPacket(msg[4:4+n], layers.LayerTypeEthernet, gopacket.Default)
			if ok(p) {
				return p
			}
			msg = msg[4+n:]
		}
	}
}

// discovery is a guest's DHCP discover, as a frame.
func discovery(t *testing.T) []byte {
	t.Helper()
	d, err := dhcpv4.NewDiscovery(net.HardwareAddr(guestMAC[:]))
	if err != nil {
		t.Fatal(err)
	}
	buf := gopacket.NewSerializeBuffer()
	ip := &layers.IPv4{Version: 4, TTL: 64, Protocol: layers.IPProtocolUDP, SrcIP: net.IPv4zero, DstIP: net.IPv4bcast}
	udp := &layers.UDP{SrcPort: 68, DstPort: 67}
	_ = udp.SetNetworkLayerForChecksum(ip)
	if err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true},
		&layers.Ethernet{SrcMAC: guestMAC[:], DstMAC: layers.EthernetBroadcast, EthernetType: layers.EthernetTypeIPv4}, ip, udp, gopacket.Payload(d.ToBytes())); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestMessageAtTheLimit: a message of exactly MaxMessage bytes, of the largest frames a page sends (web/local-net.ts
// batches up to it), leaves the link up. Past the limit the WebSocket closes, and the guest's link goes with it.
func TestMessageAtTheLimit(t *testing.T) {
	c := dial(t, helperServer(t, Options{}), "/tok", testOrigin, Subprotocol)
	const largest = 65553 // the page's MAX_PORT_FRAME
	frame := func(n int) []byte {
		f := make([]byte, n)
		copy(f, []byte{0x52, 0x54, 0x00, 0x99, 0x99, 0x99}) // to a MAC not on the switch: it goes nowhere
		copy(f[6:], guestMAC[:])
		return f
	}
	var frames [][]byte
	left := MaxMessage
	for left >= 4+largest {
		frames = append(frames, frame(largest))
		left -= 4 + largest
	}
	frames = append(frames, frame(left-4))
	send(t, c, frames...)
	send(t, c, discovery(t))
	await(t, c, "a DHCP offer after the message", func(p gopacket.Packet) bool {
		u, _ := p.Layer(layers.LayerTypeUDP).(*layers.UDP)
		return u != nil && u.DstPort == 68
	})
}

func TestDHCP(t *testing.T) {
	c := dial(t, helperServer(t, Options{}), "/tok", testOrigin, Subprotocol)
	var err error
	send(t, c, discovery(t))
	var offer *dhcpv4.DHCPv4
	await(t, c, "a DHCP offer", func(p gopacket.Packet) bool {
		u, _ := p.Layer(layers.LayerTypeUDP).(*layers.UDP)
		if u == nil || u.DstPort != 68 {
			return false
		}
		offer, err = dhcpv4.FromBytes(u.Payload)
		return err == nil && offer.MessageType() == dhcpv4.MessageTypeOffer
	})
	if !net.ParseIP(GatewayIP).Equal(offer.Router()[0]) || !offer.YourIPAddr.Mask(net.CIDRMask(24, 32)).Equal(net.ParseIP("192.168.127.0")) {
		t.Fatalf("offer %v, router %v", offer.YourIPAddr, offer.Router())
	}
	if dns := offer.DNS(); len(dns) == 0 || !dns[0].Equal(net.ParseIP(GatewayIP)) {
		t.Fatalf("offer's resolvers %v", dns)
	}
}

// resolver answers DNS queries (any, with QR set) while on, as a probe target on IPv4 loopback.
func resolver(t *testing.T, on *atomic.Bool) string {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		b := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(b)
			if err != nil {
				return
			}
			if on.Load() && n >= 12 {
				b[2] |= 0x80
				_, _ = pc.WriteTo(b[:n], from)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func lifetime(p gopacket.Packet) (uint16, bool) {
	ra, _ := p.Layer(layers.LayerTypeICMPv6RouterAdvertisement).(*layers.ICMPv6RouterAdvertisement)
	if ra == nil {
		return 0, false
	}
	return ra.RouterLifetime, true
}

// TestIPv6Reach: the guest's IPv6 default route (a router advertisement's lifetime) follows the host's IPv6 reach,
// both at a solicitation and unsolicited when the reach changes; the advertisements stay valid (checksums).
func TestIPv6Reach(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	to := resolver(t, &on)
	c := dial(t, helperServer(t, Options{IPv6: true, Reach6: &Reach6Options{To: []string{to}, Network: "udp4", Every: 100 * time.Millisecond, Timeout: 200 * time.Millisecond, Misses: 2}}), "/tok", testOrigin, Subprotocol)
	send(t, c, routerSolicitation(guestMAC))
	await(t, c, "a router advertisement with a default route", func(p gopacket.Packet) bool {
		l, ok := lifetime(p)
		return ok && l > 0
	})
	on.Store(false)
	p := await(t, c, "a router advertisement without a default route (unsolicited)", func(p gopacket.Packet) bool {
		l, ok := lifetime(p)
		return ok && l == 0
	})
	checkICMPv6(t, p)
	on.Store(true)
	await(t, c, "the default route again (unsolicited)", func(p gopacket.Packet) bool {
		l, ok := lifetime(p)
		return ok && l > 0
	})
}

// checkICMPv6 fails unless p's ICMPv6 checksum is right.
func checkICMPv6(t *testing.T, p gopacket.Packet) {
	t.Helper()
	raw := p.Data()
	ip := header.IPv6(raw[ethLen:])
	icmp := header.ICMPv6(append([]byte(nil), raw[icmpAt:]...))
	got := icmp.Checksum()
	icmp.SetChecksum(0)
	if want := header.ICMPv6Checksum(header.ICMPv6ChecksumParams{Header: icmp, Src: ip.SourceAddress(), Dst: ip.DestinationAddress()}); got != want {
		t.Fatalf("ICMPv6 checksum %#04x, want %#04x", got, want)
	}
}

// neighborSolicitation asks, from a guest's link-local address, for the gateway's fe80::1 by unicast or multicast, as
// Linux checks its router is still there.
func neighborSolicitation(mac [6]byte) []byte {
	target := tcpip.AddrFrom16([16]byte{0xfe, 0x80, 15: 1})
	src := tcpip.AddrFrom16([16]byte{0xfe, 0x80, 8: 0x50, 9: 0x54, 15: 0x56})
	dst := header.SolicitedNodeAddr(target)
	f := make([]byte, icmpAt+header.ICMPv6NeighborSolicitMinimumSize+8)
	header.Ethernet(f).Encode(&header.EthernetFields{SrcAddr: tcpip.LinkAddress(mac[:]), DstAddr: header.EthernetAddressFromMulticastIPv6Address(dst), Type: header.IPv6ProtocolNumber})
	icmp := header.ICMPv6(f[icmpAt:])
	header.IPv6(f[ethLen:]).Encode(&header.IPv6Fields{PayloadLength: uint16(len(icmp)), TransportProtocol: header.ICMPv6ProtocolNumber, HopLimit: header.NDPHopLimit, SrcAddr: src, DstAddr: dst})
	icmp.SetType(header.ICMPv6NeighborSolicit)
	ns := header.NDPNeighborSolicit(icmp.MessageBody())
	ns.SetTargetAddress(target)
	ns.Options().Serialize(header.NDPOptionsSerializer{header.NDPSourceLinkLayerAddressOption(mac[:])})
	icmp.SetChecksum(header.ICMPv6Checksum(header.ICMPv6ChecksumParams{Header: icmp, Src: src, Dst: dst}))
	return f
}

// TestGatewayIsARouter: the gateway's neighbor advertisements say it's a router. Without the flag Linux takes it for
// a host that stopped routing and drops the default route through it, seconds after the guest's traffic pauses.
func TestGatewayIsARouter(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	h := helperServer(t, Options{IPv6: true, Reach6: &Reach6Options{To: []string{resolver(t, &on)}, Network: "udp4", Every: time.Hour}})
	c := dial(t, h, "/tok", testOrigin, Subprotocol)
	send(t, c, neighborSolicitation(guestMAC))
	p := await(t, c, "a neighbor advertisement", func(p gopacket.Packet) bool {
		return p.Layer(layers.LayerTypeICMPv6NeighborAdvertisement) != nil
	})
	if na := p.Layer(layers.LayerTypeICMPv6NeighborAdvertisement).(*layers.ICMPv6NeighborAdvertisement); !na.Router() {
		t.Fatalf("the gateway's neighbor advertisement doesn't say it's a router: flags %#x", na.Flags)
	}
	checkICMPv6(t, p)
}

// TestNoIPv6: --no-ipv6 answers no router solicitation.
func TestNoIPv6(t *testing.T) {
	c := dial(t, helperServer(t, Options{}), "/tok", testOrigin, Subprotocol)
	send(t, c, routerSolicitation(guestMAC))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			return // nothing came
		}
		if p := gopacket.NewPacket(msg[4:], layers.LayerTypeEthernet, gopacket.Default); p.Layer(layers.LayerTypeICMPv6RouterAdvertisement) != nil {
			t.Fatal("a router advertisement without IPv6")
		}
	}
}

// guestStack is a guest on gVisor's netstack at 192.168.127.2 whose frames cross c.
func guestStack(t *testing.T, c *websocket.Conn) *stack.Stack {
	t.Helper()
	ep := channel.New(256, 1500, tcpip.LinkAddress(guestMAC[:]))
	s := stack.New(stack.Options{NetworkProtocols: []stack.NetworkProtocolFactory{ipv4.NewProtocol, arp.NewProtocol}, TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol}})
	t.Cleanup(s.Destroy)
	if err := s.CreateNIC(1, ethernet.New(ep)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProtocolAddress(1, tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: tcpip.AddrFrom4([4]byte{192, 168, 127, 2}).WithPrefix()}, stack.AddressProperties{}); err != nil {
		t.Fatal(err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, Gateway: tcpip.AddrFrom4([4]byte{192, 168, 127, 1}), NIC: 1}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { // guest → helper
		for {
			pkt := ep.ReadContext(ctx)
			if pkt == nil {
				return
			}
			f := pkt.ToView().AsSlice()
			msg := binary.BigEndian.AppendUint32(nil, uint32(len(f)))
			pkt.DecRef()
			if c.Write(ctx, websocket.MessageBinary, append(msg, f...)) != nil {
				return
			}
		}
	}()
	go func() { // helper → guest
		for {
			_, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			for len(msg) >= 4 {
				n := int(binary.BigEndian.Uint32(msg))
				pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(append([]byte(nil), msg[4:4+n]...))})
				ep.InjectInbound(0, pkt)
				pkt.DecRef()
				msg = msg[4+n:]
			}
		}
	}()
	return s
}

// TestTCPToHost: the guest's TCP reaches a server on this computer's loopback through --host-loopback, both ways,
// a megabyte each.
func TestTCPToHost(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	const size = 1 << 20
	go func() {
		s, err := ln.Accept()
		if err != nil {
			return
		}
		defer s.Close()
		b, _ := io.ReadAll(io.LimitReader(s, size))
		_, _ = s.Write(b)
	}()
	s := guestStack(t, dial(t, helperServer(t, Options{HostLoopback: true}), "/tok", testOrigin, Subprotocol))
	port := ln.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := gonet.DialContextTCP(ctx, s, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4([4]byte{192, 168, 127, 254}), Port: uint16(port)}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	want := make([]byte, size)
	for i := range want {
		want[i] = byte(i * 7)
	}
	go func() { _, _ = conn.Write(want) }()
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	got := make([]byte, size)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("byte %d differs", i)
		}
	}
}

// TestNoHostLoopback: without --host-loopback the host's address on the network leads nowhere.
func TestNoHostLoopback(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	s := guestStack(t, dial(t, helperServer(t, Options{}), "/tok", testOrigin, Subprotocol))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if conn, err := gonet.DialContextTCP(ctx, s, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4([4]byte{192, 168, 127, 254}), Port: uint16(ln.Addr().(*net.TCPAddr).Port)}, ipv4.ProtocolNumber); err == nil {
		conn.Close()
		t.Fatal("reached the host's loopback")
	}
}

func TestRewriteRA(t *testing.T) {
	if _, ok := rewriteRA(routerSolicitation(guestMAC)); ok {
		t.Fatal("rewrote a solicitation")
	}
	if _, ok := rewriteRA(make([]byte, 20)); ok {
		t.Fatal("rewrote a runt")
	}
	p := gopacket.NewPacket(routerSolicitation(guestMAC), layers.LayerTypeEthernet, gopacket.Default)
	if p.Layer(layers.LayerTypeICMPv6RouterSolicitation) == nil || p.ErrorLayer() != nil {
		t.Fatal("the solicitation doesn't parse")
	}
	checkICMPv6(t, p)
}
