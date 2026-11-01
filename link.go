package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// A page's link to the switch: the net.Conn gvisor-tap-vsock's switch reads the guest's frames from and writes the
// network's to, in QEMU's stream framing (each frame after its 4-byte big-endian length). On the WebSocket each
// binary message is one or more whole frames in that framing, both ways: the frames the switch writes while one
// message is on its way go out together as the next (as the page joins its own).
type link struct {
	ctx    context.Context
	cancel context.CancelFunc
	c      *websocket.Conn
	h      *Helper

	in      chan []byte // messages from the page (and solicitations of ours), whole frames
	rest    []byte      // what Read hasn't handed the switch yet
	outMu   sync.Mutex
	out     []byte // frames for the page, waiting for the writer
	outWake chan struct{}

	macMu sync.Mutex
	macs  map[[6]byte]struct{} // the guests' MACs on this link (for router solicitations on their behalf)
}

// MaxMessage bounds a message from the page; MaxQueued bounds what waits for the page (frames past it are dropped, as
// a full NIC queue drops them, rather than stalling the switch, which writes to every link under one lock).
const (
	MaxMessage = 1 << 20
	MaxQueued  = 4 << 20
	maxMACs    = 16
)

func newLink(ctx context.Context, c *websocket.Conn, h *Helper) *link {
	ctx, cancel := context.WithCancel(ctx)
	c.SetReadLimit(MaxMessage)
	l := &link{ctx: ctx, cancel: cancel, c: c, h: h, in: make(chan []byte, 16), outWake: make(chan struct{}, 1), macs: map[[6]byte]struct{}{}}
	go l.reader()
	go l.writer()
	if h.ipv6 {
		go l.refresh()
	}
	return l
}

// errFraming closes a link whose messages don't hold whole frames.
var errFraming = errors.New("a message that isn't whole frames")

func (l *link) reader() {
	defer l.cancel()
	for {
		typ, msg, err := l.c.Read(l.ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			_ = l.c.Close(websocket.StatusUnsupportedData, "binary messages only")
			return
		}
		fresh, err := l.learn(msg)
		if err != nil {
			_ = l.c.Close(websocket.StatusProtocolError, err.Error())
			return
		}
		select {
		case l.in <- msg:
		case <-l.ctx.Done():
			return
		}
		if fresh && l.h.ipv6 {
			// A guest new to the link (the page reconnected, or the helper restarted under a running guest): its router
			// advertisement now, after its frames, so the switch knows where the MAC is.
			l.solicit()
		}
	}
}

// learn checks that msg is whole frames and notes their sources (the guests on this link): fresh when one is new.
func (l *link) learn(msg []byte) (fresh bool, err error) {
	for len(msg) > 0 {
		if len(msg) < 4 {
			return false, errFraming
		}
		n := int(binary.BigEndian.Uint32(msg))
		if n < 14 || n > len(msg)-4 {
			return false, errFraming
		}
		var mac [6]byte
		copy(mac[:], msg[4+6:4+12])
		if mac[0]&1 == 0 { // unicast sources only
			l.macMu.Lock()
			if _, ok := l.macs[mac]; !ok && len(l.macs) < maxMACs {
				l.macs[mac] = struct{}{}
				fresh = true
			}
			l.macMu.Unlock()
		}
		msg = msg[4+n:]
	}
	return fresh, nil
}

func (l *link) Read(b []byte) (int, error) {
	for len(l.rest) == 0 {
		select {
		case m := <-l.in:
			l.rest = m
		case <-l.ctx.Done():
			return 0, io.EOF
		}
	}
	n := copy(b, l.rest)
	l.rest = l.rest[n:]
	return n, nil
}

// Write takes one frame, length first, as the switch writes them; a router advertisement is fixed on the way
// (rewriteRA). It never blocks on the page.
func (l *link) Write(b []byte) (int, error) {
	if l.ctx.Err() != nil {
		return 0, net.ErrClosed
	}
	if len(b) >= 4 && int(binary.BigEndian.Uint32(b)) == len(b)-4 {
		if l.h.ipv6 && !l.h.reached() {
			if f, ok := rewriteRA(b[4:]); ok {
				b = append(b[:4:4], f...)
			}
		}
	}
	l.outMu.Lock()
	if len(l.out)+len(b) <= MaxQueued {
		l.out = append(l.out, b...)
	}
	l.outMu.Unlock()
	select {
	case l.outWake <- struct{}{}:
	default:
	}
	return len(b), nil
}

func (l *link) writer() {
	defer l.cancel()
	var spare []byte
	for {
		select {
		case <-l.outWake:
		case <-l.ctx.Done():
			return
		}
		l.outMu.Lock()
		msg := l.out
		if len(msg) == 0 { // a wake for frames an earlier message took
			l.outMu.Unlock()
			continue
		}
		l.out = spare[:0] // the buffer of the message before, written
		l.outMu.Unlock()
		if err := l.c.Write(l.ctx, websocket.MessageBinary, msg); err != nil {
			return
		}
		spare = msg
	}
}

// solicit sends a router solicitation into the switch for each guest on the link, so the switch sends each its router
// advertisement now (it answers solicitations only, and the guest sends them only when its link comes up).
func (l *link) solicit() {
	l.macMu.Lock()
	var frames []byte
	for mac := range l.macs {
		f := routerSolicitation(mac)
		frames = binary.BigEndian.AppendUint32(frames, uint32(len(f)))
		frames = append(frames, f...)
	}
	l.macMu.Unlock()
	if len(frames) == 0 {
		return
	}
	select {
	case l.in <- frames:
	default: // the switch is behind: the next refresh sends it
	}
}

// RefreshEvery is how often each guest's router advertisement is sent again: the switch's carry a router lifetime of
// 1000 s, and nothing else would renew it.
var RefreshEvery = 5 * time.Minute

func (l *link) refresh() {
	t := time.NewTicker(RefreshEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			l.solicit()
		case <-l.ctx.Done():
			return
		}
	}
}

func (l *link) Close() error {
	l.cancel()
	return l.c.CloseNow()
}

func (l *link) LocalAddr() net.Addr                { return addr("webpc-net") }
func (l *link) RemoteAddr() net.Addr               { return addr("page") }
func (l *link) SetDeadline(t time.Time) error      { return nil }
func (l *link) SetReadDeadline(t time.Time) error  { return nil }
func (l *link) SetWriteDeadline(t time.Time) error { return nil }

type addr string

func (a addr) Network() string { return "websocket" }
func (a addr) String() string  { return string(a) }
