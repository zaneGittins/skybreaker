package tsauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeID maps remote addresses to peers; anything else fails WhoIs.
type fakeID map[string]Peer

func (f fakeID) WhoIs(_ context.Context, addr string) (Peer, error) {
	if p, ok := f[addr]; ok {
		return p, nil
	}
	return Peer{}, errors.New("unknown peer")
}

var (
	agentHost = Peer{Node: "agent-vm", Tags: []string{"tag:agent-host"}}
	you       = Peer{Node: "phone", Login: "Zane@example.com"}
	stranger  = Peer{Node: "laptop", Login: "eve@example.com"}
)

func ids() fakeID {
	return fakeID{"100.64.0.2:5000": agentHost, "100.64.0.9:5000": you, "100.64.0.7:5000": stranger}
}

func serve(t *testing.T, h http.Handler, remote string) int {
	t.Helper()
	r := httptest.NewRequest("GET", "http://skybreaker.example.ts.net/x", nil)
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func capture(p *Peer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*p, _ = FromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
}

func TestRequireUsers(t *testing.T) {
	var got Peer
	h := RequireUsers(ids(), []string{"zane@example.com"})(capture(&got))
	for remote, want := range map[string]int{
		"100.64.0.9:5000": http.StatusNoContent, // login matches case-insensitively
		"100.64.0.7:5000": http.StatusForbidden,
		"100.64.0.2:5000": http.StatusForbidden, // tagged devices are never users
		"bogus":           http.StatusForbidden,
	} {
		if code := serve(t, h, remote); code != want {
			t.Errorf("%s: status %d, want %d", remote, code, want)
		}
	}
}

func TestRequireUsersEmptyListDeniesAll(t *testing.T) {
	var got Peer
	h := RequireUsers(ids(), nil)(capture(&got))
	if code := serve(t, h, "100.64.0.9:5000"); code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", code)
	}
}

func TestTaggedLoginIgnored(t *testing.T) {
	// A tagged node reports the "tagged-devices" profile; even if it somehow
	// carried a listed login it must not pass as a user.
	id := fakeID{"1.2.3.4:1": {Node: "x", Login: "zane@example.com", Tags: []string{"tag:agent-ui"}}}
	var got Peer
	h := RequireUsers(id, []string{"zane@example.com"})(capture(&got))
	if code := serve(t, h, "1.2.3.4:1"); code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", code)
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{"match", "https://skybreaker.example.ts.net", "skybreaker.example.ts.net", true},
		{"match with port", "https://skybreaker.example.ts.net:8443", "skybreaker.example.ts.net:8443", true},
		{"other site", "https://evil.example.ts.net", "skybreaker.example.ts.net", false},
		{"missing", "", "skybreaker.example.ts.net", false},
		{"null", "null", "skybreaker.example.ts.net", false},
		{"suffix trick", "https://skybreaker.example.ts.net.evil.com", "skybreaker.example.ts.net", false},
		{"case", "https://SKYBREAKER.example.ts.net", "skybreaker.example.ts.net", true},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "http://"+c.host+"/ws", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := SameOrigin(r); got != c.want {
			t.Errorf("%s: SameOrigin = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestStatic(t *testing.T) {
	p, err := Static(you).WhoIs(context.Background(), "anything:1")
	if err != nil || p.Login != you.Login {
		t.Fatalf("Static.WhoIs = %+v, %v", p, err)
	}
}

func TestName(t *testing.T) {
	if agentHost.Name() != "agent-vm" || you.Name() != "Zane@example.com" {
		t.Errorf("Name: %q %q", agentHost.Name(), you.Name())
	}
}
