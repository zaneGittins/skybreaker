package setup

import (
	"context"
	"net/netip"
	"slices"
	"testing"
)

func TestDefaultsFromTailscale(t *testing.T) {
	l := testLayout(t)
	f := newFakeRunner(l)
	f.out["tailscale status --json"] = `{"MagicDNSSuffix": "tail1234.ts.net.", "Self": {"UserID": 42},
		"User": {"42": {"LoginName": "you@github"}, "7": {"LoginName": "other@github"}}}`
	a := (&Host{Layout: l, Cmd: f}).Defaults(context.Background())
	if a.Tailnet != "tail1234.ts.net" || !slices.Equal(a.Users, []string{"you@github"}) {
		t.Errorf("tailnet %q, users %v", a.Tailnet, a.Users)
	}
	if a.Branch != "main" || a.Image != DefaultImage || len(a.Harnesses) != 2 {
		t.Errorf("defaults: %+v", a)
	}
	// A tagged node has no person to default to.
	if _, users := parseTailscaleStatus([]byte(`{"Self": {"UserID": 1}, "User": {"1": {"LoginName": "tagged-devices"}}}`)); users != nil {
		t.Errorf("users = %v", users)
	}
}

func TestLANPrefixes(t *testing.T) {
	p := netip.MustParsePrefix
	got := lanPrefixes([]iface{
		{Name: "lo", Up: true, Loop: true, Addrs: []netip.Prefix{p("127.0.0.1/8")}},
		{Name: "eth0", Up: true, Addrs: []netip.Prefix{p("192.168.77.156/24"), p("fe80::1/64")}},
		{Name: "eth1", Up: true, Addrs: []netip.Prefix{p("203.0.113.5/24")}},
		{Name: "eth2", Addrs: []netip.Prefix{p("10.0.0.5/8")}},
		{Name: "docker0", Up: true, Addrs: []netip.Prefix{p("172.17.0.1/16")}},
		{Name: "br-1a2b", Up: true, Addrs: []netip.Prefix{p("10.89.3.1/24")}},
		{Name: "tailscale0", Up: true, Addrs: []netip.Prefix{p("100.101.102.103/32")}},
		{Name: "wlan0", Up: true, Addrs: []netip.Prefix{p("192.168.77.157/24")}},
	})
	if !slices.Equal(got, []string{"192.168.77.0/24"}) {
		t.Errorf("got %v", got)
	}
}
