package main

import (
	"encoding/binary"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
)

// Ethernet + IPv6 + ICMPv6: where a router advertisement's fields are in a frame.
const (
	ethLen  = header.EthernetMinimumSize
	ip6Len  = header.IPv6MinimumSize
	icmpAt  = ethLen + ip6Len
	raFixed = header.ICMPv6HeaderSize + header.NDPRAMinimumSize
)

// rewriteRA is frame with its router lifetime 0, when frame is a router advertisement (ok); otherwise ok is false. A
// router advertisement with lifetime 0 keeps the prefix on the link (the guest's address, the gateway's DNS) and takes
// the default route away, so the guest's IPv6 to the internet fails at once ("network unreachable") and its programs
// use IPv4, rather than waiting on a host that can't reach out over IPv6.
func rewriteRA(frame []byte) ([]byte, bool) {
	if len(frame) < icmpAt+raFixed || header.Ethernet(frame).Type() != header.IPv6ProtocolNumber {
		return nil, false
	}
	ip := header.IPv6(frame[ethLen:])
	if ip.TransportProtocol() != header.ICMPv6ProtocolNumber || int(ip.PayloadLength()) != len(frame)-icmpAt {
		return nil, false
	}
	if header.ICMPv6(frame[icmpAt:]).Type() != header.ICMPv6RouterAdvert {
		return nil, false
	}
	out := append([]byte(nil), frame...)
	icmp := header.ICMPv6(out[icmpAt:])
	binary.BigEndian.PutUint16(icmp.MessageBody()[2:], 0) // Router Lifetime
	icmp.SetChecksum(0)
	icmp.SetChecksum(header.ICMPv6Checksum(header.ICMPv6ChecksumParams{Header: icmp, Src: ip.SourceAddress(), Dst: ip.DestinationAddress()}))
	return out, true
}

// routerSolicitation is a router solicitation from mac (from the unspecified address, without options, as RFC 4861
// allows before an address is configured) to all routers.
func routerSolicitation(mac [6]byte) []byte {
	f := make([]byte, icmpAt+header.ICMPv6HeaderSize+header.NDPRSMinimumSize)
	header.Ethernet(f).Encode(&header.EthernetFields{SrcAddr: tcpip.LinkAddress(mac[:]), DstAddr: header.EthernetAddressFromMulticastIPv6Address(header.IPv6AllRoutersLinkLocalMulticastAddress), Type: header.IPv6ProtocolNumber})
	icmp := header.ICMPv6(f[icmpAt:])
	header.IPv6(f[ethLen:]).Encode(&header.IPv6Fields{
		PayloadLength:     uint16(len(icmp)),
		TransportProtocol: header.ICMPv6ProtocolNumber,
		HopLimit:          header.NDPHopLimit,
		SrcAddr:           header.IPv6Any,
		DstAddr:           header.IPv6AllRoutersLinkLocalMulticastAddress,
	})
	icmp.SetType(header.ICMPv6RouterSolicit)
	icmp.SetChecksum(header.ICMPv6Checksum(header.ICMPv6ChecksumParams{Header: icmp, Src: header.IPv6Any, Dst: header.IPv6AllRoutersLinkLocalMulticastAddress}))
	return f
}
