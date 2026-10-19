package server

import (
	"encoding/base64"
	"net/http"
	"testing"
)

func TestIsPlaceholder(t *testing.T) {
	basic := func(s string) string { return "Basic " + base64.StdEncoding.EncodeToString([]byte(s)) }
	for v, want := range map[string]bool{
		"":                           true,
		"skybreaker-injected":        true,
		"Bearer skybreaker-injected": true,
		"token skybreaker-injected":  true,
		basic("x-access-token:skybreaker-injected"): true, // git over HTTPS
		basic("skybreaker-injected:"):               true,
		basic("skybreaker-injected"):                true, // a bare user: nothing secret
		"Bearer ghp_real":                           false,
		basic("x-access-token:ghp_real"):            false,
		basic("skybreaker-injected:ghp_real"):       false, // the password is what counts
		"Basic !!!not-base64":                       false,
		"Basic " + base64.StdEncoding.EncodeToString([]byte("a:skybreaker-injected")) + " extra": false,
		"skybreaker-injected-not": false,
	} {
		if got := isPlaceholder(v); got != want {
			t.Errorf("isPlaceholder(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestStripHopHonoursConnection(t *testing.T) {
	// Headers named in Connection are hop-by-hop too (RFC 9110 7.6.1), so a
	// client can't smuggle one past the broker by listing it.
	h := http.Header{
		"Connection":    {"keep-alive, X-Smuggled", " x-other "},
		"X-Smuggled":    {"1"},
		"X-Other":       {"2"},
		"Keep-Alive":    {"timeout=5"},
		"Upgrade":       {"h2c"},
		"Te":            {"trailers"},
		"Content-Type":  {"application/json"},
		"Authorization": {"Bearer x"},
	}
	stripHop(h)
	for _, gone := range []string{"Connection", "X-Smuggled", "X-Other", "Keep-Alive", "Upgrade", "Te"} {
		if _, ok := h[gone]; ok {
			t.Errorf("%s kept", gone)
		}
	}
	if h.Get("Content-Type") == "" || h.Get("Authorization") == "" {
		t.Errorf("end-to-end headers dropped: %v", h)
	}
}

func TestIsUpgrade(t *testing.T) {
	for _, c := range []struct {
		h    http.Header
		want bool
	}{
		{http.Header{}, false},
		{http.Header{"Connection": {"keep-alive"}}, false},
		{http.Header{"Connection": {"keep-alive, Upgrade"}}, true}, // no Upgrade header still asks to switch
		{http.Header{"Connection": {"close", "UPGRADE"}}, true},
		{http.Header{"Upgrade": {"websocket"}}, true},
		{http.Header{"Connection": {"upgrade-insecure"}}, false},
	} {
		r := &http.Request{Header: c.h}
		if got := isUpgrade(r); got != c.want {
			t.Errorf("isUpgrade(%v) = %v, want %v", c.h, got, c.want)
		}
	}
}
