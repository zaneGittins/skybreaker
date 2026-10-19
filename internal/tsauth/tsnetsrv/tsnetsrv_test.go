//go:build !nodeps

package tsnetsrv

import (
	"errors"
	"net/netip"
	"testing"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"

	"github.com/zaneGittins/skybreaker/internal/tsauth"
)

func TestPeerFrom(t *testing.T) {
	user := &apitype.WhoIsResponse{
		Node:        &tailcfg.Node{ComputedName: "phone", Addresses: []netip.Prefix{netip.MustParsePrefix("100.64.0.9/32")}},
		UserProfile: &tailcfg.UserProfile{LoginName: "zane@example.com"},
	}
	if p := peerFrom(user); p.Node != "phone" || p.Login != "zane@example.com" || len(p.Tags) != 0 || p.Addr != "100.64.0.9" {
		t.Fatalf("user peer = %+v", p)
	}
	tagged := &apitype.WhoIsResponse{
		Node:        &tailcfg.Node{Name: "agent-vm.example.ts.net.", Tags: []string{"tag:agent-host"}},
		UserProfile: &tailcfg.UserProfile{LoginName: "tagged-devices"},
	}
	if p := peerFrom(tagged); p.Node != "agent-vm.example.ts.net" || p.Login != "" || !p.HasTag("tag:agent-host") {
		t.Fatalf("tagged peer = %+v", p)
	}
	if p := peerFrom(nil); p.Node != "" {
		t.Fatalf("nil = %+v", p)
	}
}

func TestLookupPeer(t *testing.T) {
	st := &ipnstate.Status{
		CurrentTailnet: &ipnstate.TailnetStatus{MagicDNSSuffix: "example.ts.net"},
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			key.NewNode().Public(): {DNSName: "orbit.example.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.101.1.2"), netip.MustParseAddr("fd7a:115c:a1e0::2")}},
		},
	}
	if got, err := lookupPeer(st, "ip4", "Orbit.example.ts.net."); err != nil || len(got) != 1 || got[0].String() != "100.101.1.2" {
		t.Fatalf("ip4 = %v %v", got, err)
	}
	if got, err := lookupPeer(st, "ip", "orbit.example.ts.net"); err != nil || len(got) != 2 {
		t.Fatalf("ip = %v %v", got, err)
	}
	if _, err := lookupPeer(st, "ip", "api.github.com"); !errors.Is(err, tsauth.ErrNotTailnet) {
		t.Fatalf("public name: %v", err)
	}
	// A name in the tailnet's domain that no peer has is an answer, not a
	// reason to ask public DNS.
	if _, err := lookupPeer(st, "ip", "ghost.example.ts.net"); err == nil || errors.Is(err, tsauth.ErrNotTailnet) {
		t.Fatalf("unknown tailnet name: %v", err)
	}
}
