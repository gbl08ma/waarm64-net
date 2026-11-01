package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
)

// Subprotocol names the page's and the helper's framing, so either refuses the other clearly when they differ.
const Subprotocol = "webpc-net.v1"

// The guest's network: Podman machine's IPv4 subnet, and a ULA /64 of ours (fd77:6562:7063: "web" "pc").
const (
	Subnet      = "192.168.127.0/24"
	GatewayIP   = "192.168.127.1"
	HostIP      = "192.168.127.254"
	SubnetIPv6  = "fd77:6562:7063::/64"
	GatewayIPv6 = "fd77:6562:7063::1"
	GatewayMAC  = "5a:94:ef:e4:0c:dd"
)

// Close codes the page shows as plain messages (4000–4999 are the application's).
const (
	CloseVersion = 4001 // the page and the helper speak different versions
	CloseOrigin  = 4003 // the page's origin isn't one the helper accepts
	CloseToken   = 4004 // the link's token isn't this helper's
)

type Options struct {
	// HostLoopback lets the guest reach this computer's 127.0.0.1 at HostIP.
	HostLoopback bool
	// IPv6 gives the guest IPv6 (always on-link; a default route only while the host reaches the internet over IPv6).
	IPv6 bool
	MTU  int
	// Reach6 overrides how the host's IPv6 reach is checked (tests).
	Reach6 *Reach6Options
}

// Helper is one network (one switch) and the WebSockets attached to it: every page connected shares a LAN.
type Helper struct {
	vn      *virtualnetwork.VirtualNetwork
	token   string
	allowed []string
	ipv6    bool
	reach   *Reach6

	mu    sync.Mutex
	links map[*link]struct{}
}

func config(o Options) *types.Configuration {
	c := &types.Configuration{
		MTU:               o.MTU,
		Subnet:            Subnet,
		GatewayIP:         GatewayIP,
		GatewayMacAddress: GatewayMAC,
		DHCPStaticLeases:  map[string]string{},
		Forwards:          map[string]string{},
		NAT:               map[string]string{},
		GatewayVirtualIPs: []string{},
		Protocol:          types.QemuProtocol,
	}
	if o.HostLoopback {
		c.NAT[HostIP] = "127.0.0.1"
		c.GatewayVirtualIPs = []string{HostIP}
	}
	if o.IPv6 {
		c.EnableIPv6 = true
		c.SubnetIPv6 = SubnetIPv6
		c.GatewayIPv6 = GatewayIPv6
	}
	return c
}

// NewHelper makes the network and, with IPv6, starts checking the host's IPv6 reach until ctx ends.
func NewHelper(ctx context.Context, token string, allowed []string, o Options) (*Helper, error) {
	if o.MTU == 0 {
		o.MTU = 1500
	}
	vn, err := virtualnetwork.New(config(o))
	if err != nil {
		return nil, fmt.Errorf("the network: %w", err)
	}
	h := &Helper{vn: vn, token: token, allowed: allowed, ipv6: o.IPv6, links: map[*link]struct{}{}}
	if o.IPv6 {
		ro := Reach6Options{}
		if o.Reach6 != nil {
			ro = *o.Reach6
		}
		h.reach = StartReach6(ctx, ro, func(reached bool) {
			log.Printf("IPv6 to the internet %s: the guest's IPv6 default route %s", map[bool]string{true: "reached", false: "out"}[reached], map[bool]string{true: "is on", false: "is off"}[reached])
			h.readvertise()
		})
	}
	return h, nil
}

// reached tells the links whether to advertise an IPv6 default route.
func (h *Helper) reached() bool { return h.reach != nil && h.reach.Reached() }

// readvertise has every guest's router advertisement sent again (its IPv6 default route follows the reach at once).
func (h *Helper) readvertise() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for l := range h.links {
		l.solicit()
	}
}

func (h *Helper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	// The handshake completes before a refusal, so the page can tell the user why (a browser sees no HTTP status).
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}, InsecureSkipVerify: true})
	if err != nil {
		return // Accept answered the request
	}
	switch {
	case !h.originAllowed(origin):
		log.Printf("refused %s: origin %q isn't accepted (--origin adds one)", r.RemoteAddr, origin)
		_ = c.Close(CloseOrigin, "origin not accepted: run the helper with --origin "+origin)
	case subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(r.URL.Path, "/")), []byte(h.token)) != 1:
		log.Printf("refused %s from %s: wrong token", r.RemoteAddr, origin)
		_ = c.Close(CloseToken, "wrong token: pair again with the link the helper prints")
	case c.Subprotocol() != Subprotocol:
		log.Printf("refused %s from %s: the page doesn't speak %s", r.RemoteAddr, origin, Subprotocol)
		_ = c.Close(CloseVersion, "the page and the helper are different versions: this helper speaks "+Subprotocol)
	default:
		h.serve(r.Context(), c, origin)
	}
}

func (h *Helper) serve(ctx context.Context, c *websocket.Conn, origin string) {
	start := time.Now()
	l := newLink(ctx, c, h)
	h.mu.Lock()
	h.links[l] = struct{}{}
	h.mu.Unlock()
	log.Printf("connected: a page from %s", origin)
	err := h.vn.AcceptQemu(ctx, l)
	h.mu.Lock()
	delete(h.links, l)
	h.mu.Unlock()
	_ = l.Close()
	log.Printf("disconnected: the page from %s, after %s (%v)", origin, time.Since(start).Round(time.Second), err)
}

func (h *Helper) originAllowed(origin string) bool {
	norm, err := normOrigin(origin)
	if err != nil {
		return false
	}
	for _, a := range h.allowed {
		if a == norm {
			return true
		}
	}
	return false
}

// normOrigin is an origin as browsers send it: scheme://host[:port], lower case, without a default port or a path.
func normOrigin(s string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		return "", fmt.Errorf("%q isn't an origin (like https://vm.tny.im or http://localhost:8080)", s)
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return u.Scheme + "://" + host, nil
}
