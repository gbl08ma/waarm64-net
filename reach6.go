package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Whether this computer reaches the internet over IPv6, checked as the page checks a tunnel's far end (webpc's
// web/reach6.ts, docs/network-adapters.md §4 "IPv6 out"): a DNS query for the root's NS records to three operators'
// resolvers at once, any answer counting; at the start and every minute; three lost in a row (3 s each) make IPv6
// out, one answer brings it back, and until the first answer it is out. While it's out the guest's router
// advertisements carry no default route (rewriteRA).

// ProbeTargets are Cloudflare's, Google's and Quad9's resolvers: one blocked where the traffic leaves mustn't make
// IPv6 as a whole count as out.
var ProbeTargets = []string{"2606:4700:4700::1111", "2001:4860:4860::8888", "2620:fe::fe"}

type Reach6Options struct {
	To      []string      // "[addr]:port" or addresses (port 53); default ProbeTargets
	Every   time.Duration // default a minute
	Timeout time.Duration // default 3 s
	Misses  int           // default 3
	Network string        // default "udp6" ("udp4" in tests: this one may have no IPv6 at all)
}

type Reach6 struct {
	reached atomic.Bool
}

func (r *Reach6) Reached() bool { return r.reached.Load() }

// StartReach6 probes until ctx ends, calling onChange (from its own goroutine) when the answer changes.
func StartReach6(ctx context.Context, o Reach6Options, onChange func(reached bool)) *Reach6 {
	if o.To == nil {
		o.To = ProbeTargets
	}
	if o.Every == 0 {
		o.Every = time.Minute
	}
	if o.Timeout == 0 {
		o.Timeout = 3 * time.Second
	}
	if o.Misses == 0 {
		o.Misses = 3
	}
	if o.Network == "" {
		o.Network = "udp6"
	}
	r := &Reach6{}
	go func() {
		lost := 0
		for {
			if probe6(ctx, o.Network, o.To, o.Timeout) {
				lost = 0
				if !r.reached.Swap(true) {
					onChange(true)
				}
			} else if lost++; lost >= o.Misses && r.reached.Swap(false) {
				onChange(false)
			}
			wait := o.Every
			if lost > 0 && lost < o.Misses {
				wait = 0 // misses one after another
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
		}
	}()
	return r
}

// probe6 asks every target at once; true when any answers within timeout.
func probe6(ctx context.Context, network string, to []string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var wg sync.WaitGroup
	var answered atomic.Bool
	for _, t := range to {
		addr := t
		if _, _, err := net.SplitHostPort(t); err != nil {
			addr = net.JoinHostPort(t, "53")
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ask(ctx, network, addr) {
				answered.Store(true)
				cancel()
			}
		}()
	}
	wg.Wait()
	return answered.Load()
}

// ask sends one query for the root's NS records over UDP and IPv6, and reports whether its answer came.
func ask(ctx context.Context, network, addr string) bool {
	var d net.Dialer
	c, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return false // no IPv6 route, at once
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Now()) })
	defer stop()
	var id [2]byte
	_, _ = rand.Read(id[:])
	q := append(id[:], 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0, 0 /* the root */, 0, 2 /* NS */, 0, 1 /* IN */)
	if _, err := c.Write(q); err != nil {
		return false
	}
	buf := make([]byte, 1500)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return false
		}
		if n >= 12 && binary.BigEndian.Uint16(buf) == binary.BigEndian.Uint16(id[:]) && buf[2]&0x80 != 0 {
			return true
		}
	}
}
