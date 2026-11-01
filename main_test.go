package main

import (
	"net"
	"strings"
	"testing"
)

func TestCheckMTU(t *testing.T) {
	for _, c := range []struct {
		mtu  int
		ipv6 bool
		ok   bool
	}{
		{1500, true, true}, {1280, true, true}, {1279, true, false}, {576, false, true}, {575, false, false},
		{65520, true, true}, {65521, true, false}, {0, false, false},
	} {
		if err := checkMTU(c.mtu, c.ipv6); (err == nil) != c.ok {
			t.Errorf("checkMTU(%d, ipv6 %v): %v, want ok %v", c.mtu, c.ipv6, err, c.ok)
		}
	}
}

// A port another program has: the message says what to do, not just the socket's error.
func TestPortTaken(t *testing.T) {
	t.Setenv("WEBPC_NET_TOKEN", newToken())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	err = run(ln.Addr().String(), []string{DefaultOrigin}, false, Options{MTU: 1500})
	if err == nil || !strings.Contains(err.Error(), "is taken") || !strings.Contains(err.Error(), "--listen") {
		t.Fatalf("run on a taken port: %v", err)
	}
}

func TestListenLoopbackOnly(t *testing.T) {
	t.Setenv("WEBPC_NET_TOKEN", newToken())
	for _, l := range []string{"0.0.0.0:8740", "192.168.1.2:8740", "localhost:8740", ":8740"} {
		if err := run(l, []string{DefaultOrigin}, false, Options{MTU: 1500}); err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("run(%q): %v, want a refusal", l, err)
		}
	}
}
