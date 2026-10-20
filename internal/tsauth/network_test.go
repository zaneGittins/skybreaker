package tsauth

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDevNetwork(t *testing.T) {
	n := Dev(Peer{Node: "dev", Login: "zane@example.com"})
	ln, err := n.ListenTLS("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, err := n.ListenTLS("0.0.0.0:0"); err == nil {
		t.Fatal("dev network listened on a non-loopback address")
	}
	if _, err := n.ListenTLS("192.168.1.10:443"); err == nil {
		t.Fatal("dev network listened on a LAN address")
	}
	p, err := n.WhoIs(context.Background(), "127.0.0.1:1234")
	if err != nil || p.Login != "zane@example.com" {
		t.Fatalf("WhoIs = %+v %v", p, err)
	}
	if _, err := n.WhoIs(context.Background(), "100.64.0.9:1234"); err == nil {
		t.Fatal("dev network vouched for a non-loopback caller")
	}
	var _ net.Listener = ln
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLoopbackHost(t *testing.T) {
	// Dev mode trusts every loopback caller, so a page on evil.example
	// that rebinds its name to 127.0.0.1 would be same-origin with itself
	// and in. Only loopback names are served.
	h := LoopbackHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	for host, want := range map[string]int{
		"127.0.0.1:7700":        200,
		"127.0.0.1":             200,
		"localhost:7700":        200,
		"LOCALHOST:7700":        200,
		"[::1]:7700":            200,
		"evil.example:7700":     http.StatusMisdirectedRequest,
		"127.0.0.1.nip.io:7700": http.StatusMisdirectedRequest,
		"localhost.evil:7700":   http.StatusMisdirectedRequest,
		"10.0.0.5:7700":         http.StatusMisdirectedRequest,
		"":                      http.StatusMisdirectedRequest,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %q: %d, want %d", host, w.Code, want)
		}
	}
}
