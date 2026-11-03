package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
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
	b, err := openLeases(file)
	if err != nil {
		t.Fatal(err)
	}
	got := b.static()
	want := map[string]string{"192.168.127.2": "52:54:00:00:00:06", "192.168.127.7": "52:54:00:00:00:07"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	for i := range maxKept + 5 {
		b.set(fmt.Sprintf("52:54:00:01:%02x:%02x", i>>8, i&255), fmt.Sprintf("10.0.%d.%d", i>>8, i&255)) // set doesn't check addresses
	}
	if err := b.save(); err != nil {
		t.Fatal(err)
	}
	if len(b.order) != maxKept || b.byMAC["52:54:00:00:00:06"] != "" || b.byMAC["52:54:00:01:00:04"] != "" || b.byMAC["52:54:00:01:00:05"] == "" {
		t.Fatalf("%d kept; the oldest should have gone first", len(b.order))
	}
	b.set("52:54:00:00:00:09", "192.168.127.9")
	b.set("52:54:00:00:00:0a", "192.168.127.9") // the pool gave .9 to another MAC since
	if err := b.save(); err != nil {
		t.Fatal(err)
	}
	again, err := openLeases(file) // the 10.0/16 lines aren't the network's: only .9's
	if err != nil || fmt.Sprint(again.static()) != fmt.Sprint(map[string]string{"192.168.127.9": "52:54:00:00:00:0a"}) {
		t.Fatalf("read back: %v, %v", again.static(), err)
	}
	if b, err := openLeases(""); b != nil || err != nil || len(b.static()) != 0 {
		t.Fatal("no file, no book")
	}
}
