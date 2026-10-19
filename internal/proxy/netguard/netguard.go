// Package netguard decides which IP addresses the broker may connect to on a
// sandbox's behalf. Names are resolved by the broker, and every address is
// checked twice: after lookup, and again on the socket at connect time, which
// defeats DNS rebinding and keeps agents off loopback, metadata, the LAN and
// the tailnet.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"

	"github.com/zaneGittins/skybreaker/internal/tsauth"
)

var ErrBlocked = errors.New("netguard: destination address is blocked")

// ErrNotTailnet is what a Tailnet's lookup returns for a name it doesn't
// own; the guard then asks the ordinary resolver.
var ErrNotTailnet = tsauth.ErrNotTailnet

// Tailnet reaches peers on the tailnet through Skybreaker's own node, so a
// route to a tailnet service needs no tailscaled on the host. Its
// addresses are still blocked unless the route allows them.
type Tailnet interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// tailnetRanges are Tailscale's IPv4 CGNAT range and IPv6 ULA.
var tailnetRanges = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fd7a:115c:a1e0::/48")}

func onTailnet(ip netip.Addr) bool {
	ip = ip.WithZone("").Unmap()
	for _, p := range tailnetRanges {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

var defaultBlocked = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
	"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/96", "::1/128", "::ffff:0:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
	"2001::/32", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8",
}

// DefaultBlocked returns loopback, link-local (cloud metadata), private,
// CGNAT (tailnet), documentation, multicast and reserved ranges, IPv4 and IPv6.
// The IPv6 forms that carry or tunnel to an IPv4 address (IPv4-compatible,
// SIIT, NAT64, Teredo, 6to4) are blocked whole rather than decoded: none is a
// legitimate upstream.
func DefaultBlocked() []netip.Prefix {
	out := make([]netip.Prefix, len(defaultBlocked))
	for i, s := range defaultBlocked {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type Guard struct {
	blocked  []netip.Prefix
	allow    []netip.Prefix
	resolver Resolver
	tailnet  Tailnet

	skipPrecheck bool // tests: prove the connect-time check stands alone
}

// New blocks DefaultBlocked plus extraBlocked. Entries in allow win over any
// block, for upstreams that really are private (a service on the tailnet). A bare IP
// means a single address.
func New(extraBlocked []string, allow []string) (*Guard, error) {
	g := &Guard{blocked: DefaultBlocked(), resolver: net.DefaultResolver}
	for _, s := range extraBlocked {
		p, err := parsePrefix(s)
		if err != nil {
			return nil, err
		}
		g.blocked = append(g.blocked, p)
	}
	for _, s := range allow {
		p, err := parsePrefix(s)
		if err != nil {
			return nil, err
		}
		g.allow = append(g.allow, p)
	}
	return g, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "/") {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("netguard: bad address %q: %w", s, err)
		}
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("netguard: bad prefix %q: %w", s, err)
	}
	return p.Masked(), nil
}

// With returns a copy that also allows the given prefixes.
func (g *Guard) With(allow ...netip.Prefix) *Guard {
	c := *g
	c.allow = append(append([]netip.Prefix(nil), g.allow...), allow...)
	return &c
}

// WithResolver returns a copy that resolves names with r.
func (g *Guard) WithResolver(r Resolver) *Guard {
	c := *g
	c.resolver = r
	return &c
}

// WithTailnet returns a copy that resolves tailnet names, and dials tailnet
// addresses, through t.
func (g *Guard) WithTailnet(t Tailnet) *Guard {
	c := *g
	c.tailnet = t
	return &c
}

// Allowed reports whether the broker may connect to ip. 4in6-mapped
// addresses are judged as IPv4 and zones are ignored.
func (g *Guard) Allowed(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.WithZone("").Unmap()
	for _, p := range g.allow {
		if p.Contains(ip) {
			return true
		}
	}
	for _, p := range g.blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// control runs on the socket just before connect, with the address actually
// being dialled.
func (g *Guard) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("netguard: %w", err)
	}
	if !g.Allowed(ap.Addr()) {
		return fmt.Errorf("%w: %s", ErrBlocked, ap.Addr())
	}
	return nil
}

// DialContext dials addr ("host:port") over TCP, trying only allowed
// addresses, in the resolver's order.
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var family string
	switch network {
	case "tcp":
		family = "ip"
	case "tcp4":
		family = "ip4"
	case "tcp6":
		family = "ip6"
	default:
		return nil, fmt.Errorf("netguard: unsupported network %q", network)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("netguard: %w", err)
	}

	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else if addrs, err = g.lookup(ctx, family, host); err != nil {
		return nil, fmt.Errorf("netguard: resolving %s: %w", host, err)
	}

	var candidates []netip.Addr
	for _, ip := range addrs {
		if (family == "ip4" && !ip.Unmap().Is4()) || (family == "ip6" && ip.Unmap().Is4()) {
			continue
		}
		if g.skipPrecheck || g.Allowed(ip) {
			candidates = append(candidates, ip)
		}
	}
	if len(candidates) == 0 {
		if len(addrs) == 0 {
			return nil, fmt.Errorf("netguard: no %s address for %s", family, host)
		}
		return nil, fmt.Errorf("%w: every address of %s", ErrBlocked, host)
	}

	d := net.Dialer{Control: g.control}
	var lastErr error
	for _, ip := range candidates {
		dial := d.DialContext
		if g.tailnet != nil && onTailnet(ip) {
			// The tailnet's dialer is userspace: there is no socket for
			// control to check, but the address was checked above and is
			// dialled as an address, so nothing re-resolves it.
			dial = g.tailnet.DialContext
		}
		c, err := dial(ctx, network, net.JoinHostPort(ip.Unmap().WithZone(ip.Zone()).String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("netguard: dialing %s: %w", addr, lastErr)
}

// lookup asks the tailnet first, when there is one, then the resolver.
func (g *Guard) lookup(ctx context.Context, family, host string) ([]netip.Addr, error) {
	if g.tailnet != nil {
		addrs, err := g.tailnet.LookupNetIP(ctx, family, host)
		if !errors.Is(err, ErrNotTailnet) {
			return addrs, err
		}
	}
	return g.resolver.LookupNetIP(ctx, family, host)
}
