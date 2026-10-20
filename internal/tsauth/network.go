package tsauth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ErrNotTailnet is what a tailnet lookup returns for a name outside the
// tailnet, so the caller can ask ordinary DNS instead.
var ErrNotTailnet = errors.New("not a tailnet name")

// Network is where a daemon listens and how it learns who connected: a
// tsnet node in production, loopback in development.
type Network interface {
	Identifier
	// ListenTLS opens an HTTPS listener with the node's tailnet certificate.
	ListenTLS(addr string) (net.Listener, error)
	Close() error
}

// Dev is a Network for local development without a tailnet. It listens on
// loopback only and reports every loopback caller as peer, so it can never
// vouch for anyone across a real network.
func Dev(peer Peer) Network { return devNet{peer} }

type devNet struct{ peer Peer }

func (d devNet) listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("dev mode listens on loopback only, not %q", addr)
	}
	return net.Listen("tcp", addr)
}

// ListenTLS is plain HTTP in development.
func (d devNet) ListenTLS(addr string) (net.Listener, error) { return d.listen(addr) }

func (d devNet) WhoIs(_ context.Context, remote string) (Peer, error) {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil || !ap.Addr().Unmap().IsLoopback() {
		return Peer{}, errors.New("dev network: caller is not on loopback")
	}
	return d.peer, nil
}

func (devNet) Close() error { return nil }

// LoopbackHost serves only requests addressed to a loopback name. Wrap dev
// mode's UIs in it: they trust every loopback caller, and without it a page
// whose name is rebound to 127.0.0.1 would be same-origin with itself.
func LoopbackHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		ip, err := netip.ParseAddr(host)
		if !strings.EqualFold(host, "localhost") && (err != nil || !ip.IsLoopback()) {
			http.Error(w, "dev mode answers only to localhost and loopback addresses", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
