package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/insomniacslk/dhcp/dhcpv4"
)

// maxKept bounds the kept leases: each is reserved at the next start, and the /24 has 252 addresses for guests.
const maxKept = 200

// leaseBook keeps the guests' IPv4 addresses (MAC to address) in a file, so a restarted helper gives each guest the
// address it had. gvisor-tap-vsock's pool is in memory and hands out addresses in order, ignoring the one a guest asks
// for: after a restart under two running guests (a page's reconnect doesn't flap the link) the first to renew would
// take the other's address. The file's lines are "<mac> <address>", the least recently acknowledged first.
type leaseBook struct {
	path  string
	mu    sync.Mutex
	order []string          // MACs, the least recently acknowledged first
	byMAC map[string]string // MAC to address
}

// openLeases reads the kept leases at path (none if it doesn't exist yet); a nil book with path "".
func openLeases(path string) (*leaseBook, error) {
	if path == "" {
		return nil, nil
	}
	b := &leaseBook{path: path, byMAC: map[string]string{}}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return b, nil
	} else if err != nil {
		return nil, fmt.Errorf("the kept leases: %w", err)
	}
	defer f.Close()
	_, subnet, _ := net.ParseCIDR(Subnet)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		mac, addr, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		hw, err := net.ParseMAC(mac)
		ip := net.ParseIP(addr).To4()
		if !ok || err != nil || len(hw) != 6 || ip == nil || !subnet.Contains(ip) || ip[3] == 0 || ip[3] == 255 ||
			ip.String() == GatewayIP || ip.String() == HostIP {
			continue // not ours, or damaged: that guest gets a fresh address
		}
		b.set(hw.String(), ip.String()) // a later line (a more recent acknowledgment) takes an address from an earlier
	}
	return b, sc.Err()
}

// static is the kept leases as gvisor-tap-vsock's DHCPStaticLeases (address to MAC), reserved at the network's start.
func (b *leaseBook) static() map[string]string {
	m := map[string]string{}
	if b == nil {
		return m
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for mac, ip := range b.byMAC {
		m[ip] = mac
	}
	return m
}

// set records mac's address as the most recent, dropping another MAC that had that address (the pool gave it to mac
// since) and the least recent past maxKept; whether anything changed.
func (b *leaseBook) set(mac, ip string) bool {
	if b.byMAC[mac] == ip && len(b.order) > 0 && b.order[len(b.order)-1] == mac {
		return false
	}
	order := b.order[:0]
	for _, m := range b.order {
		if m == mac || b.byMAC[m] == ip {
			if m != mac {
				delete(b.byMAC, m)
			}
			continue
		}
		order = append(order, m)
	}
	b.order = order
	b.order = append(b.order, mac)
	b.byMAC[mac] = ip
	for len(b.order) > maxKept {
		delete(b.byMAC, b.order[0])
		b.order = b.order[1:]
	}
	return true
}

// ack records the address in f when f (an Ethernet frame to a guest) is the helper's DHCP acknowledgment.
func (b *leaseBook) ack(f []byte) {
	if b == nil || len(f) < 14+20+8 || binary.BigEndian.Uint16(f[12:]) != 0x0800 || f[14+9] != 17 {
		return
	}
	udp := 14 + int(f[14]&15)*4
	if len(f) < udp+8 || binary.BigEndian.Uint16(f[udp:]) != 67 || binary.BigEndian.Uint16(f[udp+2:]) != 68 {
		return
	}
	m, err := dhcpv4.FromBytes(f[udp+8:])
	if err != nil || m.MessageType() != dhcpv4.MessageTypeAck || len(m.ClientHWAddr) != 6 || m.YourIPAddr.To4() == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.set(m.ClientHWAddr.String(), m.YourIPAddr.String()) {
		return
	}
	if err := b.save(); err != nil {
		log.Printf("couldn't keep the guests' addresses (a restart may give them others): %v", err)
	}
}

// save writes the book whole, through a temporary file renamed into place.
func (b *leaseBook) save() error {
	var buf bytes.Buffer
	for _, mac := range b.order {
		fmt.Fprintf(&buf, "%s %s\n", mac, b.byMAC[mac])
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return err
	}
	tmp := b.path + ".new"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, b.path)
}
