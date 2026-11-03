package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"maps"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/containers/gvisor-tap-vsock/pkg/tap"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
)

const (
	// maxKept bounds the kept leases: each is reserved at the next start, and at least 150 of the /24's addresses stay
	// for MACs the helper hasn't seen.
	maxKept = 100
	// maxSeen bounds the MACs seen as a source on a link in one run (16 per link, links come and go).
	maxSeen = 1024
	// saveAfter gathers the acknowledgments of a burst (several guests booting) into one write.
	saveAfter = 500 * time.Millisecond
)

// leaseBook keeps the guests' IPv4 addresses (MAC to address) in a file, so a restarted helper gives each guest the
// address it had. gvisor-tap-vsock's pool is in memory and hands out addresses in order, ignoring the one a guest asks
// for: after a restart under two running guests (a page's reconnect doesn't flap the link) the first to renew would
// take the other's address. The file's lines are "<mac> <address>", the least recent first; it is best effort: one
// that can't be read is ignored, and one that can't be written is said in the log.
//
// What's kept comes from the pool itself, never from frames (a guest can forge an acknowledgment), and only for MACs
// seen as a frame's source on a link: a frame that looks like the gateway's acknowledgment only schedules a save,
// which a writer of its own does off the switch's path.
type leaseBook struct {
	path  string
	mu    sync.Mutex
	order []string          // MACs, the least recent first
	byMAC map[string]string // MAC to address
	seen  map[string]uint64 // MACs seen as a source on a link, to when (a count: the latest connection's is highest)
	count uint64
	dirty chan struct{}
	done  chan struct{} // closed once run has made its last save
}

// openLeases reads the kept leases at path (none if it doesn't exist or can't be read); a nil book with path "".
func openLeases(path string) *leaseBook {
	if path == "" {
		return nil
	}
	b := &leaseBook{path: path, byMAC: map[string]string{}, seen: map[string]uint64{}, dirty: make(chan struct{}, 1), done: make(chan struct{})}
	if stale, _ := filepath.Glob(path + ".new-*"); len(stale) > 0 { // a run that stopped mid-save
		for _, s := range stale {
			_ = os.Remove(s)
		}
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return b
	} else if err != nil { // not overwritten either: the guests' addresses aren't kept this run
		log.Printf("can't read the kept leases, so they aren't kept this run (the guests may get other addresses): %v", err)
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		mac, addr, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		hw, err := net.ParseMAC(mac)
		if ok && err == nil && len(hw) == 6 && guestAddress(addr) {
			b.set(hw.String(), addr) // a later line (more recent) takes an address from an earlier one
		}
	}
	if err := sc.Err(); errors.Is(err, bufio.ErrTooLong) {
		log.Printf("ignored the rest of the kept leases (%s), which the next save drops: %v", path, err)
	} else if err != nil {
		log.Printf("can't read the kept leases, so they aren't kept this run (the guests may get other addresses): %v", err)
		return nil
	}
	return b
}

// guestAddress: an address of the network's that the pool gives guests (not the network's, broadcast, gateway's or
// host's).
func guestAddress(s string) bool {
	ip := net.ParseIP(s).To4()
	_, subnet, _ := net.ParseCIDR(Subnet)
	return ip != nil && ip.String() == s && subnet.Contains(ip) && ip[3] != 0 && ip[3] != 255 && s != GatewayIP && s != HostIP
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

// set records mac's address as the most recent, dropping another MAC that had that address (a later line of the file
// is a later lease) and the least recent past maxKept; whether anything changed. For reading the file.
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
	b.order = append(order, mac)
	b.byMAC[mac] = ip
	for len(b.order) > maxKept {
		delete(b.byMAC, b.order[0])
		b.order = b.order[1:]
	}
	return true
}

// saw notes mac as a frame's source on a link, now (only such MACs' leases are kept, the most recently seen last);
// past maxSeen the least recently seen is forgotten.
func (b *leaseBook) saw(mac [6]byte) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.count++
	b.seen[net.HardwareAddr(mac[:]).String()] = b.count
	if len(b.seen) > maxSeen {
		oldest, at := "", b.count
		for m, n := range b.seen {
			if n < at {
				oldest, at = m, n
			}
		}
		delete(b.seen, oldest)
	}
}

// ack schedules a save when f (an Ethernet frame to a guest) looks like the gateway's DHCP reply. Called on the
// switch's path: it only signals.
func (b *leaseBook) ack(f []byte) {
	if b == nil || len(f) < 14+20+8 || binary.BigEndian.Uint16(f[12:]) != 0x0800 || f[14+9] != 17 {
		return
	}
	if net.HardwareAddr(f[6:12]).String() != GatewayMAC || !net.IP(f[14+12:14+16]).Equal(net.ParseIP(GatewayIP)) {
		return
	}
	udp := 14 + int(f[14]&15)*4
	if len(f) < udp+8 || binary.BigEndian.Uint16(f[udp:]) != 67 || binary.BigEndian.Uint16(f[udp+2:]) != 68 {
		return
	}
	select {
	case b.dirty <- struct{}{}:
	default: // a save is pending already
	}
}

// run saves the book after each burst of acknowledgments, from the pool's leases (`pool`: address to MAC) and the MACs
// on a link now (`live`), until ctx ends, and once more then; done is closed after that.
func (b *leaseBook) run(ctx context.Context, pool func() map[string]string, live func() map[string]bool) {
	if b == nil {
		return
	}
	defer close(b.done)
	for {
		select {
		case <-b.dirty:
		case <-ctx.Done():
			b.update(pool(), live())
			return
		}
		select {
		case <-time.After(saveAfter):
		case <-ctx.Done():
		}
		b.update(pool(), live())
	}
}

// Done is closed once the book's last save is made (at once without a book).
func (b *leaseBook) Done() <-chan struct{} {
	if b == nil {
		c := make(chan struct{})
		close(c)
		return c
	}
	return b.done
}

// update makes the book this run's leases (the pool's, of MACs seen on a link, in the order they were last seen, those
// on a link now last of all) after those kept from before that this run hasn't taken over, the last maxKept of them, and
// writes it when it changed. A guest connected now is never the one trimmed while fewer than maxKept are. The same pool
// and the same sightings give the same book: nothing churns.
func (b *leaseBook) update(leases map[string]string, live map[string]bool) {
	type lease struct {
		mac, ip string
		live    bool
		at      uint64
	}
	b.mu.Lock()
	var now []lease
	macs, ips := map[string]bool{}, map[string]bool{}
	for ip, mac := range leases {
		if at, ok := b.seen[mac]; ok && guestAddress(ip) {
			now = append(now, lease{mac, ip, live[mac], at})
			macs[mac], ips[ip] = true, true
		}
	}
	slices.SortFunc(now, func(x, y lease) int {
		if x.live != y.live {
			return map[bool]int{true: 1, false: -1}[x.live]
		}
		return cmp.Compare(x.at, y.at)
	})
	var order []string
	byMAC := map[string]string{}
	for _, m := range b.order {
		if ip := b.byMAC[m]; !macs[m] && !ips[ip] {
			order = append(order, m)
			byMAC[m] = ip
		}
	}
	for _, l := range now {
		order = append(order, l.mac)
		byMAC[l.mac] = l.ip
	}
	for len(order) > maxKept {
		delete(byMAC, order[0])
		order = order[1:]
	}
	if slices.Equal(order, b.order) && maps.Equal(byMAC, b.byMAC) {
		b.mu.Unlock()
		return
	}
	b.order, b.byMAC = order, byMAC
	data := b.lines()
	b.mu.Unlock()
	if err := b.save(data); err != nil { // outside the lock: links note their sources meanwhile
		log.Printf("couldn't keep the guests' addresses (a restart may give them others): %v", err)
	}
}

// lines is the book as its file. Under b.mu.
func (b *leaseBook) lines() []byte {
	var buf bytes.Buffer
	for _, mac := range b.order {
		fmt.Fprintf(&buf, "%s %s\n", mac, b.byMAC[mac])
	}
	return buf.Bytes()
}

// save writes data as the book's file, through a temporary file of this process's, synced and renamed into place (run
// is its only writer; another helper on the same address starts only once this one's last save is made, main.go).
func (b *leaseBook) save(data []byte) error {
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.new-%d", b.path, os.Getpid())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, b.path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// poolLeases is the network's DHCP pool (address to MAC). VirtualNetwork keeps it in an unexported field and serves
// it only through its services endpoint, whose middleware logs every request at Info; the pool's own Leases is
// exported. Pinned with the fork's commit; TestLeasesKept fails if the field moves.
func poolLeases(vn *virtualnetwork.VirtualNetwork) map[string]string {
	f := reflect.ValueOf(vn).Elem().FieldByName("ipPool")
	if !f.IsValid() || f.Type() != reflect.TypeFor[*tap.IPPool]() || f.IsNil() {
		return map[string]string{}
	}
	return (*tap.IPPool)(f.UnsafePointer()).Leases()
}
