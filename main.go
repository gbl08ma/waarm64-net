// webpc-net puts a browser VM's network on this computer's sockets. The page (webpc, https://vm.tny.im)
// connects over a WebSocket on loopback and sends the guest's Ethernet frames; gvisor-tap-vsock's userspace network
// (Podman machine's, Lima's) gives the guest DHCP, DNS and a gateway whose TCP, UDP and ICMP go out through this
// program's own sockets. README.md says what is ours and what is imported.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"
)

// DefaultPort is where the helper listens unless --listen says otherwise (provisional).
const DefaultPort = 8740

// DefaultOrigin is the page's own origin, the one a WebSocket is accepted from without --origin.
const DefaultOrigin = "https://vm.tny.im"

type origins []string

func (o *origins) String() string { return strings.Join(*o, ",") }
func (o *origins) Set(s string) error {
	norm, err := normOrigin(s)
	if err != nil {
		return err
	}
	*o = append(*o, norm)
	return nil
}

func main() {
	log.SetFlags(log.LstdFlags)
	var (
		listen       = flag.String("listen", fmt.Sprintf("127.0.0.1:%d", DefaultPort), "the loopback address and port to listen on")
		allowed      origins
		newToken     = flag.Bool("new-token", false, "replace the kept token, so links paired before stop working")
		hostLoopback = flag.Bool("host-loopback", false, "let the guest reach this computer's own loopback at "+HostIP)
		noIPv6       = flag.Bool("no-ipv6", false, "give the guest IPv4 only")
		mtu          = flag.Int("mtu", 1500, "the guest link's MTU")
		version      = flag.Bool("version", false, "print the version and exit")
	)
	flag.Var(&allowed, "origin", "a page origin to accept besides "+DefaultOrigin+" (repeatable; e.g. http://localhost:8080)")
	flag.Parse()
	if *version {
		fmt.Println(Version())
		return
	}
	if err := run(*listen, append(origins{DefaultOrigin}, allowed...), *newToken, Options{HostLoopback: *hostLoopback, IPv6: !*noIPv6, MTU: *mtu}); err != nil {
		log.Fatal(err)
	}
}

func run(listen string, allowed []string, newToken bool, o Options) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("--listen: %w", err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--listen: %q isn't a loopback address: the helper only listens on this computer", host)
	}
	token, err := loadToken(newToken)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	h, err := NewHelper(ctx, token, allowed, o)
	if err != nil {
		return err
	}
	ws := "ws://" + net.JoinHostPort(host, port) + "/" + token
	log.Printf("%s listening on %s (IPv6 %s); pages accepted from %s", Version(), ln.Addr(), onOff(o.IPv6), strings.Join(allowed, ", "))
	fmt.Printf("\nPair the page with this link:\n\n    %s/#net=%s\n\n", allowed[0], ws)
	for _, a := range allowed[1:] {
		fmt.Printf("or, on %s:\n\n    %s/#net=%s\n\n", a, a, ws)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
