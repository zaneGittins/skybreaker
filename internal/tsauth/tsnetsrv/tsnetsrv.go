//go:build !nodeps

// Package tsnetsrv runs Skybreaker as its own node on the tailnet with
// tsnet: it answers WhoIs for every connection it accepts, and reaches
// tailnet peers for the broker's routes. The first start needs TS_AUTHKEY;
// after that the state directory holds the node's identity.
package tsnetsrv

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"strings"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"

	"github.com/zaneGittins/skybreaker/internal/tsauth"
)

type localClient interface {
	WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error)
	Status(ctx context.Context) (*ipnstate.Status, error)
	CertPair(ctx context.Context, domain string) (certPEM, keyPEM []byte, err error)
}

type node struct {
	srv    *tsnet.Server
	lc     localClient
	domain string
}

// Start brings the node up and waits until it is connected.
func Start(ctx context.Context, hostname, stateDir string) (tsauth.Network, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	s := &tsnet.Server{
		Hostname: hostname,
		Dir:      stateDir,
		AuthKey:  os.Getenv("TS_AUTHKEY"),
		Logf:     func(string, ...any) {},
		UserLogf: log.Printf,
	}
	if _, err := s.Up(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("tsnet: %w", err)
	}
	lc, err := s.LocalClient()
	if err != nil {
		s.Close()
		return nil, err
	}
	n := &node{srv: s, lc: lc}
	if d := s.CertDomains(); len(d) > 0 {
		n.domain = d[0]
		go warm(ctx, lc, n.domain)
	}
	return n, nil
}

// Domain is the node's name on the tailnet, the one its certificate is for.
func (n *node) Domain() string { return n.domain }

// warm fetches the node's certificate now, which a TLS listener would
// otherwise do on its first visitor's handshake: that takes long enough
// for the browser to give up.
func warm(ctx context.Context, lc localClient, domain string) {
	if _, _, err := lc.CertPair(ctx, domain); err != nil && ctx.Err() == nil {
		log.Printf("skybreaker: no HTTPS certificate for %s yet: %v", domain, err)
	}
}

func (n *node) ListenTLS(addr string) (net.Listener, error) { return n.srv.ListenTLS("tcp", addr) }
func (n *node) Close() error                                { return n.srv.Close() }

// DialContext connects to a tailnet address from this node.
func (n *node) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return n.srv.Dial(ctx, network, addr)
}

// LookupNetIP resolves a MagicDNS name from the node's own peer list.
func (n *node) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	st, err := n.lc.Status(ctx)
	if err != nil {
		return nil, err
	}
	return lookupPeer(st, network, host)
}

// lookupPeer finds host among the tailnet's peers. A name outside the
// tailnet's MagicDNS domain is tsauth.ErrNotTailnet; one inside it that no
// peer has is not found.
func lookupPeer(st *ipnstate.Status, network, host string) ([]netip.Addr, error) {
	name := strings.TrimSuffix(strings.ToLower(host), ".")
	suffix := ""
	if st.CurrentTailnet != nil {
		suffix = strings.Trim(strings.ToLower(st.CurrentTailnet.MagicDNSSuffix), ".")
	}
	if suffix == "" || !strings.HasSuffix(name, "."+suffix) {
		return nil, tsauth.ErrNotTailnet
	}
	for _, p := range st.Peer {
		if strings.TrimSuffix(strings.ToLower(p.DNSName), ".") != name {
			continue
		}
		var out []netip.Addr
		for _, ip := range p.TailscaleIPs {
			if network == "ip" || (network == "ip4") == ip.Is4() {
				out = append(out, ip)
			}
		}
		return out, nil
	}
	return nil, &net.DNSError{Err: "no such tailnet peer", Name: host, IsNotFound: true}
}

func (n *node) WhoIs(ctx context.Context, remoteAddr string) (tsauth.Peer, error) {
	w, err := n.lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return tsauth.Peer{}, err
	}
	return peerFrom(w), nil
}

// peerFrom keeps the login only for untagged nodes: a tagged node reports
// the "tagged-devices" profile, which is nobody.
func peerFrom(w *apitype.WhoIsResponse) tsauth.Peer {
	var p tsauth.Peer
	if w == nil || w.Node == nil {
		return p
	}
	p.Node = w.Node.ComputedName
	if p.Node == "" {
		p.Node = strings.TrimSuffix(w.Node.Name, ".")
	}
	p.Tags = append([]string(nil), w.Node.Tags...)
	if len(p.Tags) == 0 && w.UserProfile != nil {
		p.Login = w.UserProfile.LoginName
	}
	return p
}
