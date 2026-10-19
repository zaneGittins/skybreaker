package server

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
	"time"
)

// revokeThenApprove answers the next approval card after revoking orbit's
// token, as the kill switch would while a request is held.
func revokeThenApprove(t *testing.T, w *world) {
	t.Helper()
	sub, cancel := w.approvals.Subscribe()
	go func() {
		defer cancel()
		a := <-sub
		info, _ := w.sessions.Lookup(w.token)
		w.sessions.Revoke(info.ID)
		w.approvals.Decide(a.ID, true, "zane@example.com")
	}()
}

func TestRevokedWhileHeldOnARoute(t *testing.T) {
	w := newWorld(t)
	w.srv.hold = 2 * time.Second
	revokeThenApprove(t, w)
	code, _ := routeReq(t, w, "PATCH", "/k8s/prod/api/v1/namespaces/apps/pods/web", `{"metadata":{}}`, w.token)
	if code == 200 || len(w.hitsFor("kube.test")) != 0 {
		t.Fatalf("revoked during the hold, still forwarded: %d, %d hits", code, len(w.hitsFor("kube.test")))
	}
}

func TestRevokedWhileHeldInMITM(t *testing.T) {
	w := newWorld(t)
	w.srv.hold = 2 * time.Second
	revokeThenApprove(t, w)
	code, _, _ := do(t, w.orbit(), "PUT", "https://api.github.test/repos/zaneGittins/orbit/pulls/7/merge", "{}", nil)
	if code == 200 || len(w.hitsFor("api.github.test")) != 0 {
		t.Fatalf("revoked during the hold, still merged: %d, %d hits", code, len(w.hitsFor("api.github.test")))
	}
}

func TestRevokeCutsOpenRouteStream(t *testing.T) {
	// A watch through a route is one long request on the broker's own
	// listener, not a tracked CONNECT: revoking must end it too.
	w := newWorld(t)
	req, _ := http.NewRequest("GET", w.proxy.URL+"/k8s/prod/api/v1/namespaces/apps/pods?watch=stream", nil)
	req.Header.Set("Authorization", "Bearer "+w.token)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("watch: %v %v", resp, err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if line, err := br.ReadString('\n'); err != nil || !strings.HasPrefix(line, "event") {
		t.Fatalf("first event: %q %v", line, err)
	}
	w.sessions.RevokeAll()
	done := make(chan struct{})
	go func() {
		for {
			if _, err := br.ReadString('\n'); err != nil {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the watch kept streaming after its token was revoked")
	}
}

func TestRevokeCutsPlainHTTPTunnelStream(t *testing.T) {
	w := newWorld(t)
	c := w.orbit()
	c.Timeout = 15 * time.Second
	resp, err := c.Get("http://plain.test/x?watch=stream")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v %v", resp, err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if line, err := br.ReadString('\n'); err != nil || !strings.HasPrefix(line, "event") {
		t.Fatalf("first event: %q %v", line, err)
	}
	w.sessions.RevokeAll()
	done := make(chan struct{})
	go func() {
		for {
			if _, err := br.ReadString('\n'); err != nil {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the download kept flowing after its token was revoked")
	}
}
