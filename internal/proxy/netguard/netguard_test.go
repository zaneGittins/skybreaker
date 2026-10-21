package netguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestAllowedDefaults(t *testing.T) {
	g, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"8.8.8.8":                true,
		"140.82.112.3":           true,
		"2606:4700:4700::1111":   true,
		"::ffff:8.8.8.8":         true,
		"127.0.0.1":              false,
		"127.8.9.10":             false,
		"0.0.0.0":                false,
		"10.1.2.3":               false,
		"172.16.0.1":             false,
		"172.31.255.255":         false,
		"192.168.77.241":         false,
		"169.254.169.254":        false, // cloud metadata
		"100.64.0.1":             false, // tailnet CGNAT
		"100.100.100.100":        false, // Tailscale DNS
		"100.127.255.255":        false,
		"198.18.0.1":             false,
		"224.0.0.1":              false,
		"255.255.255.255":        false,
		"::1":                    false,
		"::":                     false,
		"::ffff:127.0.0.1":       false, // 4in6-mapped loopback
		"::ffff:169.254.169.254": false,
		"::ffff:10.0.0.1":        false,
		"fe80::1":                false,
		"fe80::1%eth0":           false, // zone ID
		"fd00::1":                false, // ULA
		"fc00::1":                false,
		"fd7a:115c:a1e0::53":     false, // Tailscale ULA
		"ff02::1":                false,
		"64:ff9b::7f00:1":        false, // NAT64 of 127.0.0.1
		"2001:db8::1":            false,
		"172.15.255.255":         true,
		"172.32.0.0":             true,
		"100.63.255.255":         true,
		"100.128.0.0":            true,
		"fec0::1":                true, // deprecated site-local, globally routed range is outside fc00::/7
		"2001:4860:4860::8888":   true,
		"::ffff:140.82.112.3":    true,
		"64:ff9b:1::1":           false,
		"::7f00:1":               false, // IPv4-compatible 127.0.0.1
		"::a00:1":                false, // IPv4-compatible 10.0.0.1
		"::ffff:0:a00:1":         false, // SIIT IPv4-translated 10.0.0.1
		"2001:0:4136:e378::1":    false, // Teredo: tunnels to an IPv4 server and client
		"2002:7f00:1::1":         false, // 6to4 of 127.0.0.1
		"2002:a00:1::1":          false, // 6to4 of 10.0.0.1
		"2002:808:808::1":        false, // 6to4 is deprecated (RFC 7526); none of it is reachable
		"2001:200::1":            true,  // just past Teredo's /32
	}
	for s, want := range cases {
		ip := netip.MustParseAddr(s)
		if got := g.Allowed(ip); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", s, got, want)
		}
	}
	if g.Allowed(netip.Addr{}) {
		t.Error("the zero Addr is allowed")
	}
}

func TestExtraBlockedAndAllow(t *testing.T) {
	g, err := New([]string{"8.8.4.0/24", "2001:4860::/32"}, []string{"100.101.102.103/32", "10.9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	for s, want := range map[string]bool{
		"8.8.4.4":                false,
		"8.8.8.8":                true,
		"2001:4860:4860::8888":   false,
		"100.101.102.103":        true, // allow wins over the CGNAT block
		"100.101.102.104":        false,
		"10.9.9.9":               true, // bare IP means /32
		"10.9.9.10":              false,
		"::ffff:100.101.102.103": true,
	} {
		if got := g.Allowed(netip.MustParseAddr(s)); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", s, got, want)
		}
	}
	for _, bad := range []string{"nope", "10.0.0.0/33", "", "1.2.3.4/"} {
		if _, err := New([]string{bad}, nil); err == nil {
			t.Errorf("New accepted blocked %q", bad)
		}
		if _, err := New(nil, []string{bad}); err == nil {
			t.Errorf("New accepted allow %q", bad)
		}
	}
}

func TestWithCopies(t *testing.T) {
	g, _ := New(nil, nil)
	g2 := g.With(netip.MustParsePrefix("127.0.0.0/8"))
	if !g2.Allowed(netip.MustParseAddr("127.0.0.1")) {
		t.Error("With did not allow the prefix")
	}
	if g.Allowed(netip.MustParseAddr("127.0.0.1")) {
		t.Error("With changed the original guard")
	}
}

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	addrs, ok := f[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	var out []netip.Addr
	for _, a := range addrs {
		if network == "ip4" && !a.Unmap().Is4() || network == "ip6" && a.Unmap().Is4() {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func listen(t *testing.T) (port string) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, port, _ = net.SplitHostPort(ln.Addr().String())
	return port
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestDialBlocksLoopbackByDefault(t *testing.T) {
	port := listen(t)
	g, _ := New(nil, nil)
	g = g.WithResolver(fakeResolver{"localhost": {netip.MustParseAddr("127.0.0.1")}})
	for _, addr := range []string{"127.0.0.1:" + port, "localhost:" + port, "[::ffff:127.0.0.1]:" + port} {
		c, err := g.DialContext(ctx(t), "tcp", addr)
		if err == nil {
			c.Close()
			t.Errorf("dialed %s", addr)
		} else if !errors.Is(err, ErrBlocked) {
			t.Errorf("dial %s: err = %v, want ErrBlocked", addr, err)
		}
	}
}

func TestDialAllowedByException(t *testing.T) {
	port := listen(t)
	g, _ := New(nil, []string{"127.0.0.1/32"})
	g = g.WithResolver(fakeResolver{"svc.test": {netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("127.0.0.1")}})
	for _, addr := range []string{"127.0.0.1:" + port, "svc.test:" + port} {
		c, err := g.DialContext(ctx(t), "tcp", addr)
		if err != nil {
			t.Errorf("dial %s: %v", addr, err)
			continue
		}
		if got := c.RemoteAddr().(*net.TCPAddr).IP.String(); got != "127.0.0.1" {
			t.Errorf("dial %s connected to %s", addr, got)
		}
		c.Close()
	}
}

func TestDialAllAddressesBlocked(t *testing.T) {
	g, _ := New(nil, nil)
	g = g.WithResolver(fakeResolver{"rebind.test": {netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("169.254.169.254"), netip.MustParseAddr("fd00::1")}})
	_, err := g.DialContext(ctx(t), "tcp", "rebind.test:443")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
}

func TestDialZoneLiteral(t *testing.T) {
	g, _ := New(nil, nil)
	if _, err := g.DialContext(ctx(t), "tcp", "[fe80::1%lo]:443"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
}

func TestDialErrors(t *testing.T) {
	g, _ := New(nil, nil)
	g = g.WithResolver(fakeResolver{})
	if _, err := g.DialContext(ctx(t), "tcp", "no-port"); err == nil {
		t.Error("address without port accepted")
	}
	if _, err := g.DialContext(ctx(t), "udp", "8.8.8.8:53"); err == nil {
		t.Error("udp accepted")
	}
	if _, err := g.DialContext(ctx(t), "tcp", "missing.test:443"); err == nil || errors.Is(err, ErrBlocked) {
		t.Errorf("unknown host: err = %v", err)
	}
}

func TestDialFamilyFilter(t *testing.T) {
	port := listen(t)
	g, _ := New(nil, []string{"127.0.0.1/32"})
	g = g.WithResolver(fakeResolver{"svc.test": {netip.MustParseAddr("127.0.0.1")}})
	if _, err := g.DialContext(ctx(t), "tcp6", "svc.test:"+port); err == nil {
		t.Error("tcp6 dial reached an IPv4-only host")
	}
	c, err := g.DialContext(ctx(t), "tcp4", "svc.test:"+port)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

// The connect-time check is what defeats DNS rebinding: whatever the lookup
// said, the address the socket actually connects to is checked again.
func TestControlRejectsBlockedPeer(t *testing.T) {
	g, _ := New(nil, nil)
	for _, addr := range []string{"127.0.0.1:80", "[::1]:80", "10.0.0.1:443", "[::ffff:127.0.0.1]:80", "[fe80::1%eth0]:80"} {
		if err := g.control("tcp", addr, nil); !errors.Is(err, ErrBlocked) {
			t.Errorf("control(%s) = %v, want ErrBlocked", addr, err)
		}
	}
	if err := g.control("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("control(8.8.8.8) = %v", err)
	}
	if err := g.control("tcp", "garbage", nil); err == nil {
		t.Error("control accepted an unparsable address")
	}
}

func TestRebindingCaughtAtConnect(t *testing.T) {
	port := listen(t)
	g, _ := New(nil, nil)
	// Simulate a lookup that passed the pre-check but connects to loopback:
	// with the pre-check skipped, only the Control hook stands in the way.
	g = g.WithResolver(fakeResolver{"rebind.test": {netip.MustParseAddr("127.0.0.1")}})
	g.skipPrecheck = true
	c, err := g.DialContext(ctx(t), "tcp", "rebind.test:"+port)
	if err == nil {
		c.Close()
		t.Fatal("connected to loopback through a rebinding name")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
}

// fakeTailnet knows one name and records what it dials.
type fakeTailnet struct {
	names  map[string]string
	dialed []string
}

func (f *fakeTailnet) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if ip, ok := f.names[host]; ok {
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
	}
	return nil, ErrNotTailnet
}

func (f *fakeTailnet) DialContext(_ context.Context, _, addr string) (net.Conn, error) {
	f.dialed = append(f.dialed, addr)
	c, _ := net.Pipe()
	return c, nil
}

func TestTailnetNamesDialThroughTheTailnet(t *testing.T) {
	tn := &fakeTailnet{names: map[string]string{"orbit.example.ts.net": "100.101.1.2"}}
	g, _ := New(nil, nil)
	g = g.WithTailnet(tn)
	// Tailnet addresses are blocked unless a route allows them.
	if _, err := g.DialContext(context.Background(), "tcp", "orbit.example.ts.net:443"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("unlisted tailnet peer: %v", err)
	}
	c, err := g.With(netip.MustParsePrefix("100.101.1.2/32")).DialContext(context.Background(), "tcp", "orbit.example.ts.net:443")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if len(tn.dialed) != 1 || tn.dialed[0] != "100.101.1.2:443" {
		t.Fatalf("dialed %v, want the checked address through the tailnet", tn.dialed)
	}
	// A tailnet address given directly goes the same way.
	if c, err := g.With(netip.MustParsePrefix("100.101.1.2/32")).DialContext(context.Background(), "tcp", "100.101.1.2:443"); err != nil {
		t.Fatal(err)
	} else {
		c.Close()
	}
	if len(tn.dialed) != 2 {
		t.Fatalf("tailnet literal not dialed through the tailnet: %v", tn.dialed)
	}
	// Other names use the ordinary resolver and the host network.
	g = g.WithResolver(fakeResolver{"public.example": {netip.MustParseAddr("127.0.0.1")}})
	if _, err := g.DialContext(context.Background(), "tcp", "public.example:443"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("public name: %v", err)
	}
	if len(tn.dialed) != 2 {
		t.Fatalf("public name went through the tailnet: %v", tn.dialed)
	}
}
