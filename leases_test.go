package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestLeaseBook: damaged lines and addresses that aren't the guests' are skipped, the later of two MACs on one address
// (the more recent acknowledgment) has it, and past maxKept the least recently acknowledged go.
func TestLeaseBook(t *testing.T) {
	file := filepath.Join(t.TempDir(), "leases")
	lines := "52:54:00:00:00:01 192.168.127.2\n" +
		"junk\n" +
		"52:54:00:00:00:02 10.0.0.2\n" + // not the network's
		"52:54:00:00:00:03 192.168.127.1\n" + // the gateway's
		"52:54:00:00:00:04 192.168.127.254\n" + // the host's
		"52:54:00:00:00:05 192.168.127.255\n" + // the broadcast
		"52:54:00:00:00:06 192.168.127.2\n" + // another MAC on the first's address
		"52:54:00:00:00:07 192.168.127.7\n"
	if err := os.WriteFile(file, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	b := openLeases(file)
	got := b.static()
	want := map[string]string{"192.168.127.2": "52:54:00:00:00:06", "192.168.127.7": "52:54:00:00:00:07"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	for i := range maxKept + 5 {
		b.set(fmt.Sprintf("52:54:00:01:%02x:%02x", i>>8, i&255), fmt.Sprintf("10.0.%d.%d", i>>8, i&255)) // set doesn't check addresses
	}
	if err := b.save(b.lines()); err != nil {
		t.Fatal(err)
	}
	if len(b.order) != maxKept || b.byMAC["52:54:00:00:00:06"] != "" || b.byMAC["52:54:00:01:00:04"] != "" || b.byMAC["52:54:00:01:00:05"] == "" {
		t.Fatalf("%d kept; the oldest should have gone first", len(b.order))
	}
	b.set("52:54:00:00:00:09", "192.168.127.9")
	b.set("52:54:00:00:00:0a", "192.168.127.9") // the pool gave .9 to another MAC since
	if err := b.save(b.lines()); err != nil {
		t.Fatal(err)
	}
	again := openLeases(file) // the 10.0/16 lines aren't the network's: only .9's
	if fmt.Sprint(again.static()) != fmt.Sprint(map[string]string{"192.168.127.9": "52:54:00:00:00:0a"}) {
		t.Fatalf("read back: %v", again.static())
	}
	if b := openLeases(""); b != nil || len(b.static()) != 0 {
		t.Fatal("no file, no book")
	}
	// Best effort: a file that can't be read (a directory here) or a line past the scanner's limit starts it empty.
	if b := openLeases(t.TempDir()); b == nil || len(b.static()) != 0 {
		t.Fatal("a directory as the file")
	}
	long := filepath.Join(t.TempDir(), "leases")
	_ = os.WriteFile(long, append([]byte("52:54:00:00:00:01 192.168.127.2\n"), make([]byte, 1<<17)...), 0o600)
	if b := openLeases(long); len(b.static()) != 1 {
		t.Fatalf("a long line: %v", b.static())
	}
}

// TestLeaseUpdate: only the pool's leases of MACs seen on a link are kept, and only the network's guest addresses.
func TestLeaseUpdate(t *testing.T) {
	b := openLeases(filepath.Join(t.TempDir(), "leases"))
	b.saw([6]byte{0x52, 0x54, 0, 0, 0, 1})
	b.update(map[string]string{
		"192.168.127.2":   "52:54:00:00:00:01",
		"192.168.127.3":   "52:54:00:00:00:02", // never a source on a link
		"192.168.127.1":   "5a:94:ef:e4:0c:dd", // the gateway's reservation
		"192.168.127.254": "52:54:00:00:00:01",
	})
	if got := fmt.Sprint(openLeases(b.path).static()); got != fmt.Sprint(map[string]string{"192.168.127.2": "52:54:00:00:00:01"}) {
		t.Fatalf("kept %v", got)
	}
}

// TestLeaseChurn: past maxKept MACs in one run, the book keeps the most recently seen, and the same pool gives the same
// book (no rewrite on every reply); a MAC seen again moves up and the least recently seen goes.
func TestLeaseChurn(t *testing.T) {
	b := openLeases(filepath.Join(t.TempDir(), "leases"))
	pool := map[string]string{}
	mac := func(i int) [6]byte { return [6]byte{0x52, 0x54, 0, 2, byte(i >> 8), byte(i)} }
	str := func(i int) string { m := mac(i); return net.HardwareAddr(m[:]).String() }
	for i := range maxKept + 20 {
		m := mac(i)
		b.saw(m)
		pool[fmt.Sprintf("192.168.127.%d", 2+i)] = net.HardwareAddr(m[:]).String()
	}
	b.update(pool)
	first := slices.Clone(b.order)
	if len(first) != maxKept || first[0] != str(20) || first[maxKept-1] != str(maxKept+19) {
		t.Fatalf("kept %d, from %s to %s: want the last seen %d", len(first), first[0], first[len(first)-1], maxKept)
	}
	info, _ := os.Stat(b.path)
	time.Sleep(20 * time.Millisecond)
	b.update(pool)
	if again, _ := os.Stat(b.path); !slices.Equal(b.order, first) || !again.ModTime().Equal(info.ModTime()) {
		t.Fatal("the same pool changed the book")
	}
	b.saw(mac(0)) // the first guest is back (its page reconnected)
	b.update(pool)
	if b.order[maxKept-1] != str(0) || b.byMAC[str(20)] != "" {
		t.Fatalf("a MAC seen again: %v ... %v", b.order[:2], b.order[maxKept-2:])
	}
}
