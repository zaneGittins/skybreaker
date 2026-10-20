package server

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/proxy/config"
)

func TestTunnel(t *testing.T) {
	w := newWorld(t)
	code, body, _ := do(t, w.orbit(), "GET", "https://tunnel.test/anything", "", nil)
	if code != 200 || body != "hello from tunnel.test" {
		t.Fatalf("tunnel: %d %q", code, body)
	}
	// The tunnel is opaque: the client's TLS session reached the upstream
	// directly, with nothing injected.
	h := w.hitsFor("tunnel.test")
	if len(h) != 1 || h[0].Header.Get("Authorization") != "" {
		t.Fatalf("hits = %+v", h)
	}
}

func TestUnlistedHostDenied(t *testing.T) {
	w := newWorld(t)
	for _, u := range []string{"https://example.com/", "https://evil.test/", "https://github.test.evil.com/"} {
		if code, body, _ := do(t, w.orbit(), "GET", u, "", nil); code != 0 && code != http.StatusForbidden {
			t.Errorf("%s: %d %q", u, code, body)
		}
	}
	if !strings.Contains(w.auditBuf.String(), `"decision":"deny"`) {
		t.Error("denial not audited")
	}
}

func TestTunnelWrongPort(t *testing.T) {
	w := newWorld(t)
	if code, _, _ := do(t, w.orbit(), "GET", "https://tunnel.test:8443/", "", nil); code != 0 && code != http.StatusForbidden {
		t.Fatalf("port 8443 allowed: %d", code)
	}
	if len(w.hitsFor("tunnel.test")) != 0 {
		t.Fatal("upstream reached on an unlisted port")
	}
}

// rawConnect speaks CONNECT by hand to check status codes the http.Client
// hides behind a generic error.
func rawConnect(t *testing.T, w *world, target, auth string) int {
	t.Helper()
	c, err := net.Dial("tcp", w.proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if auth != "" {
		fmt.Fprintf(c, "Proxy-Authorization: Basic %s\r\n", base64.StdEncoding.EncodeToString([]byte(auth)))
	}
	fmt.Fprint(c, "\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

func TestProxyAuth(t *testing.T) {
	w := newWorld(t)
	otherTok, _, _ := w.sessions.Mint("bifract", time.Hour)
	cases := []struct {
		name, from, auth string
		want             int
	}{
		{"none", "orbit", "", http.StatusProxyAuthRequired},
		{"bad token", "orbit", "orbit:nope", http.StatusProxyAuthRequired},
		{"token of another project", "orbit", "orbit:" + otherTok, http.StatusProxyAuthRequired},
		{"unknown project", "orbit", "ghost:" + w.token, http.StatusProxyAuthRequired},
		{"good", "orbit", "orbit:" + w.token, http.StatusOK},
		{"bifract cannot reach orbit's hosts", "bifract", "bifract:" + otherTok, http.StatusForbidden},
	}
	for _, c := range cases {
		w.setFrom(c.from)
		if got := rawConnect(t, w, "tunnel.test:443", c.auth); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
	w.setFrom("orbit")
	// A revoked token stops working at once.
	info, _ := w.sessions.Lookup(w.token)
	w.sessions.Revoke(info.ID)
	if got := rawConnect(t, w, "tunnel.test:443", "orbit:"+w.token); got != http.StatusProxyAuthRequired {
		t.Errorf("revoked token: %d", got)
	}
}

func TestSenderMustBeSandbox(t *testing.T) {
	// A stolen session token used from anywhere but a sandbox network fails,
	// and so does fetching the CA.
	w := newWorld(t)
	w.setFrom("")
	if got := rawConnect(t, w, "tunnel.test:443", "orbit:"+w.token); got != http.StatusForbidden {
		t.Fatalf("outside sender: %d, want 403", got)
	}
	if code, _, _ := do(t, http.DefaultClient, "GET", w.proxy.URL+"/ca.crt", "", nil); code != http.StatusForbidden {
		t.Fatalf("outside sender fetched the CA: %d", code)
	}
	if !strings.Contains(w.auditBuf.String(), "sender is not a sandbox") {
		t.Error("refusal not audited")
	}
}

func TestTokenWorksOnlyFromItsOwnSandbox(t *testing.T) {
	// bifract's sandbox holding orbit's token (leaked through a shared
	// repo, say) gets nothing from it, on the proxy or on a route.
	w := newWorld(t)
	w.setFrom("bifract")
	if got := rawConnect(t, w, "tunnel.test:443", "orbit:"+w.token); got != http.StatusProxyAuthRequired {
		t.Errorf("orbit's token from bifract's network: %d", got)
	}
	if code, _ := routeReq(t, w, "GET", "/k8s/prod/api/v1/namespaces/apps/pods", "", w.token); code != http.StatusUnauthorized {
		t.Errorf("orbit's token on a route from bifract's network: %d", code)
	}
	w.setFrom("orbit")
	if got := rawConnect(t, w, "tunnel.test:443", "orbit:"+w.token); got != http.StatusOK {
		t.Errorf("own network: %d", got)
	}
}

func TestMITMInjectsCredential(t *testing.T) {
	w := newWorld(t)
	code, body, _ := do(t, w.orbit(), "GET", "https://github.test/zaneGittins/orbit.git/info/refs?service=git-upload-pack", "", nil)
	if code != 200 {
		t.Fatalf("fetch: %d %s", code, body)
	}
	h := w.hitsFor("github.test")
	if len(h) != 1 {
		t.Fatalf("hits %d", len(h))
	}
	user, pass, ok := (&http.Request{Header: h[0].Header}).BasicAuth()
	if !ok || user != "x-access-token" || pass != "ghs_SECRET" {
		t.Errorf("upstream auth = %q %q %v", user, pass, ok)
	}
	if h[0].Path != "/zaneGittins/orbit.git/info/refs?service=git-upload-pack" {
		t.Errorf("upstream path %q", h[0].Path)
	}
	if h[0].Header.Get("Proxy-Authorization") != "" {
		t.Error("session token leaked upstream")
	}
	if strings.Contains(w.auditBuf.String(), "ghs_SECRET") || strings.Contains(w.auditBuf.String(), w.token) {
		t.Error("secret or token in the audit log")
	}
}

func TestMITMRules(t *testing.T) {
	w := newWorld(t)
	c := w.orbit()
	cases := []struct {
		method, url string
		want        int
	}{
		{"GET", "https://github.test/zaneGittins/bifract.git/info/refs", 403},            // another repo
		{"GET", "https://github.test/zaneGittins/orbit.git/HEAD", 403},                   // no rule
		{"DELETE", "https://api.github.test/repos/zaneGittins/orbit", 403},               // deny rule
		{"GET", "https://api.github.test/repos/zaneGittins/orbit/pulls", 200},            // auto
		{"GET", "https://api.github.test/repos/zaneGittins/orbit/../bifract/pulls", 400}, // traversal
		{"GET", "https://api.github.test/repos/zaneGittins%2Forbit/pulls", 400},          // encoded slash
	}
	for _, cs := range cases {
		code, body, _ := do(t, c, cs.method, cs.url, "", nil)
		if code != cs.want {
			t.Errorf("%s %s: %d, want %d (%s)", cs.method, cs.url, code, cs.want, body)
		}
	}
	if n := len(w.hitsFor("github.test")); n != 0 {
		t.Errorf("denied requests reached github.test: %d", n)
	}
}

func TestForeignCredentialRejected(t *testing.T) {
	w := newWorld(t)
	c := w.orbit()
	// An attacker's own key on an allowed LLM host is the documented
	// exfiltration route; it must never be forwarded.
	code, _, _ := do(t, c, "POST", "https://llm.test/v1/messages", "{}", map[string]string{"x-api-key": "sk-ant-ATTACKER"})
	if code != http.StatusForbidden {
		t.Fatalf("attacker key: %d, want 403", code)
	}
	code, _, _ = do(t, c, "GET", "https://open.test/x", "", map[string]string{"Authorization": "Bearer mine"})
	if code != http.StatusForbidden {
		t.Fatalf("own bearer on a mitm host: %d, want 403", code)
	}
	if len(w.hitsFor("llm.test"))+len(w.hitsFor("open.test")) != 0 {
		t.Fatal("request with a foreign credential reached upstream")
	}
	// The placeholder is replaced by the real key.
	code, _, _ = do(t, c, "POST", "https://llm.test/v1/messages", "{}", map[string]string{"x-api-key": "skybreaker-injected"})
	if code != 200 {
		t.Fatalf("placeholder: %d", code)
	}
	if got := w.hitsFor("llm.test")[0].Header.Get("x-api-key"); got != "sk-ant-SECRET" {
		t.Fatalf("injected key = %q", got)
	}
	code, _, _ = do(t, c, "GET", "https://api.github.test/repos/zaneGittins/orbit", "", map[string]string{"Authorization": "token skybreaker-injected"})
	if code != 200 || w.hitsFor("api.github.test")[0].Header.Get("Authorization") != "Bearer ghs_SECRET" {
		t.Fatalf("gh placeholder: %d %v", code, w.hitsFor("api.github.test"))
	}
}

func TestUnboundSecretFailsClosed(t *testing.T) {
	w := newWorld(t)
	code, _, _ := do(t, w.orbit(), "GET", "https://unbound.test/echo", "", nil)
	if code != http.StatusBadGateway && code != http.StatusForbidden {
		t.Fatalf("unbound secret: %d", code)
	}
	if len(w.hitsFor("unbound.test")) != 0 {
		t.Fatal("request sent upstream without its credential")
	}
}

func TestBlockedAddress(t *testing.T) {
	w := newWorld(t)
	code, _, _ := do(t, w.orbit(), "GET", "https://internal.test/", "", nil)
	if code != http.StatusForbidden && code != http.StatusBadGateway {
		t.Fatalf("internal.test (10.0.0.5): %d", code)
	}
}

func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

func receivePack(ref string) string {
	zero := strings.Repeat("0", 40)
	sha := strings.Repeat("a", 40)
	return pkt(zero+" "+sha+" "+ref+"\x00report-status side-band-64k\n") + "0000" + "PACKDATA"
}

func TestRefPolicy(t *testing.T) {
	w := newWorld(t)
	c := w.orbit()
	u := "https://github.test/zaneGittins/orbit.git/git-receive-pack"
	hdr := map[string]string{"Content-Type": "application/x-git-receive-pack-request"}
	if code, _, _ := do(t, c, "POST", u, receivePack("refs/heads/main"), hdr); code != http.StatusForbidden {
		t.Fatalf("push to main: %d", code)
	}
	if code, _, _ := do(t, c, "POST", u, receivePack("refs/tags/v1"), hdr); code != http.StatusForbidden {
		t.Fatalf("push tag: %d", code)
	}
	body := receivePack("refs/heads/agent/ORB-1")
	if code, b, _ := do(t, c, "POST", u, body, hdr); code != 200 {
		t.Fatalf("push agent branch: %d %s", code, b)
	}
	h := w.hitsFor("github.test")
	if len(h) != 1 || string(h[0].Body) != body {
		t.Fatalf("upstream body altered or missing: %+v", h)
	}
	gz := map[string]string{"Content-Type": "application/x-git-receive-pack-request", "Content-Encoding": "gzip"}
	if code, _, _ := do(t, c, "POST", u, body, gz); code != http.StatusForbidden && code != http.StatusBadRequest {
		t.Fatalf("gzip receive-pack: %d", code)
	}
}

func TestAskFlow(t *testing.T) {
	w := newWorld(t)
	c := w.orbit()
	u := "https://api.github.test/repos/zaneGittins/orbit/pulls/7/merge"
	code, body, hdr := do(t, c, "PUT", u, `{"merge_method":"squash"}`, nil)
	if code != http.StatusForbidden {
		t.Fatalf("first ask: %d %s", code, body)
	}
	id := hdr.Get("X-Skybreaker-Approval")
	if id == "" || !strings.Contains(body, id) {
		t.Fatalf("no approval id: %q %s", id, body)
	}
	a, ok := w.approvals.Get(id)
	if !ok || a.Method != "PUT" || a.Host != "api.github.test" || a.Project != "orbit" || a.View.Title == "" {
		t.Fatalf("approval = %+v", a)
	}
	// A different body is a different request.
	if code, _, h2 := do(t, c, "PUT", u, `{"merge_method":"merge"}`, nil); code != 403 || h2.Get("X-Skybreaker-Approval") == id {
		t.Fatalf("changed body reused approval: %d", code)
	}
	if _, err := w.approvals.Decide(id, true, "zane@example.com"); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := do(t, c, "PUT", u, `{"merge_method":"squash"}`, nil); code != 200 {
		t.Fatalf("approved retry: %d", code)
	}
	if code, _, _ := do(t, c, "PUT", u, `{"merge_method":"squash"}`, nil); code != 403 {
		t.Fatalf("approval replayed: %d", code)
	}
	if n := len(w.hitsFor("api.github.test")); n != 1 {
		t.Fatalf("upstream saw %d merges, want 1", n)
	}
}

func TestAskApprovedWhileHeld(t *testing.T) {
	w := newWorld(t)
	w.srv.hold = 2 * time.Second
	sub, cancel := w.approvals.Subscribe()
	defer cancel()
	go func() {
		a := <-sub
		w.approvals.Decide(a.ID, true, "zane@example.com")
	}()
	code, _, _ := do(t, w.orbit(), "PUT", "https://api.github.test/repos/zaneGittins/orbit/pulls/1/merge", "{}", nil)
	if code != 200 {
		t.Fatalf("held request approved in time: %d", code)
	}
}

func routeReq(t *testing.T, w *world, method, path, body, token string) (int, string) {
	t.Helper()
	hdr := map[string]string{"Content-Type": "application/json"}
	if token != "" {
		hdr["Authorization"] = "Bearer " + token
	}
	code, b, _ := do(t, http.DefaultClient, method, w.proxy.URL+path, body, hdr)
	return code, b
}

func TestK8sRoute(t *testing.T) {
	w := newWorld(t)
	if code, _ := routeReq(t, w, "GET", "/k8s/prod/api/v1/namespaces/apps/pods", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, b := routeReq(t, w, "GET", "/k8s/prod/api/v1/namespaces/apps/pods", "", w.token); code != 200 {
		t.Fatalf("list pods: %d %s", code, b)
	}
	h := w.hitsFor("kube.test")
	if len(h) != 1 || h[0].Path != "/api/v1/namespaces/apps/pods" || h[0].Header.Get("Authorization") != "Bearer kube-SECRET" {
		t.Fatalf("upstream = %+v", h)
	}
	for _, p := range []string{
		"/k8s/prod/api/v1/namespaces/apps/secrets",
		"/k8s/prod/api/v1/namespaces/kube-system/pods",
		"/k8s/prod/api/v1/namespaces/apps/pods/web/exec?command=sh",
	} {
		if code, _ := routeReq(t, w, "GET", p, "", w.token); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", p, code)
		}
	}
	if code, _ := routeReq(t, w, "DELETE", "/k8s/prod/api/v1/namespaces/apps/pods/web", "", w.token); code != http.StatusForbidden {
		t.Errorf("delete (ask, no approval): %d", code)
	}
	// bifract has no k8s capability.
	other, _, _ := w.sessions.Mint("bifract", time.Hour)
	w.setFrom("bifract")
	if code, _ := routeReq(t, w, "GET", "/k8s/prod/api/v1/namespaces/apps/pods", "", other); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("bifract on k8s route: %d", code)
	}
}

func TestCACert(t *testing.T) {
	w := newWorld(t)
	code, body, _ := do(t, http.DefaultClient, "GET", w.proxy.URL+"/ca.crt", "", nil)
	if code != 200 || body != string(w.brokerCA.CertPEM()) {
		t.Fatalf("ca.crt: %d", code)
	}
}

func TestPlainHTTP(t *testing.T) {
	w := newWorld(t)
	code, body, _ := do(t, w.orbit(), "GET", "http://plain.test/x", "", nil)
	if code != 200 || body != "hello from plain.test" {
		t.Fatalf("plain http: %d %q", code, body)
	}
	if h := w.hitsFor("plain.test"); len(h) != 1 || h[0].Header.Get("Proxy-Authorization") != "" {
		t.Fatalf("hits = %+v", h)
	}
	// tunnel.test only lists 443.
	if code, _, _ := do(t, w.orbit(), "GET", "http://tunnel.test/x", "", nil); code != http.StatusForbidden {
		t.Fatalf("plain http to a 443-only host: %d", code)
	}
}

func TestMITMHostHeaderMismatch(t *testing.T) {
	w := newWorld(t)
	// CONNECT open.test, then send a request whose Host names another host.
	c, err := net.Dial("tcp", w.proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	auth := base64.StdEncoding.EncodeToString([]byte("orbit:" + w.token))
	fmt.Fprintf(c, "CONNECT open.test:443 HTTP/1.1\r\nHost: open.test:443\r\nProxy-Authorization: Basic %s\r\n\r\n", auth)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("connect: %v %v", resp, err)
	}
	tc := tls.Client(c, &tls.Config{ServerName: "open.test", RootCAs: w.brokerCA.Pool()})
	fmt.Fprint(tc, "GET / HTTP/1.1\r\nHost: github.test\r\nConnection: close\r\n\r\n")
	resp, err = http.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusMisdirectedRequest && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("host mismatch: %d", resp.StatusCode)
	}
	if len(w.hitsFor("github.test")) != 0 {
		t.Fatal("mismatched Host reached github.test")
	}
}

func TestSNIMismatchRefused(t *testing.T) {
	w := newWorld(t)
	c, _ := net.Dial("tcp", w.proxy.Listener.Addr().String())
	defer c.Close()
	auth := base64.StdEncoding.EncodeToString([]byte("orbit:" + w.token))
	fmt.Fprintf(c, "CONNECT open.test:443 HTTP/1.1\r\nHost: open.test:443\r\nProxy-Authorization: Basic %s\r\n\r\n", auth)
	br := bufio.NewReader(c)
	http.ReadResponse(br, nil)
	tc := tls.Client(c, &tls.Config{ServerName: "github.test", RootCAs: w.brokerCA.Pool()})
	if err := tc.Handshake(); err == nil {
		t.Fatal("TLS handshake with a mismatched SNI succeeded")
	}
}

func TestReceivePackProbe(t *testing.T) {
	w := newWorld(t)
	u := "https://github.test/zaneGittins/orbit.git/git-receive-pack"
	code, b, _ := do(t, w.orbit(), "POST", u, "0000", map[string]string{"Content-Type": "application/x-git-receive-pack-request"})
	if code != 200 {
		t.Fatalf("probe: %d %s", code, b)
	}
}

func TestPlainHTTPToMITMHost(t *testing.T) {
	// http:// through the proxy carries Proxy-Authorization, which is the
	// proxy's own credential, not a foreign one.
	w := newWorld(t)
	code, body, _ := do(t, w.orbit(), "GET", "http://open.test/x", "", nil)
	if code != 200 || body != "hello from open.test" {
		t.Fatalf("http:// to a mitm host: %d %q", code, body)
	}
	if h := w.hitsFor("open.test"); len(h) != 1 || h[0].Header.Get("Proxy-Authorization") != "" {
		t.Fatalf("hits = %+v", h)
	}
}

func TestRouteProxyBasicAuth(t *testing.T) {
	w := newWorld(t)
	auth := base64.StdEncoding.EncodeToString([]byte("orbit:" + w.token))
	code, b, _ := do(t, http.DefaultClient, "GET", w.proxy.URL+"/k8s/prod/api/v1/namespaces/apps/pods", "", map[string]string{"Proxy-Authorization": "Basic " + auth})
	if code != 200 {
		t.Fatalf("route with proxy Basic auth: %d %s", code, b)
	}
}

func TestEscapedRoutePrefix(t *testing.T) {
	// /k8s/pro%64 cleans to /k8s/prod; the policy must see the same path
	// that goes upstream, so a mismatched escape is refused.
	w := newWorld(t)
	code, _ := routeReq(t, w, "GET", "/k8s/pro%64/api/v1/namespaces/kube-system/secrets", "", w.token)
	if code == 200 {
		t.Fatal("escaped route prefix accepted")
	}
	if len(w.hitsFor("kube.test")) != 0 {
		t.Fatal("request reached the cluster")
	}
}

func TestApprovalShowsWholeBodyOrSaysSo(t *testing.T) {
	w := newWorld(t)
	big := `{"pad":"` + strings.Repeat("x", 8000) + `","merge_method":"rebase"}`
	_, _, hdr := do(t, w.orbit(), "PUT", "https://api.github.test/repos/zaneGittins/orbit/pulls/3/merge", big, map[string]string{"Content-Type": "application/json"})
	a, ok := w.approvals.Get(hdr.Get("X-Skybreaker-Approval"))
	if !ok {
		t.Fatal("no approval")
	}
	if !strings.Contains(a.View.Body, `"merge_method":"rebase"`) {
		t.Fatalf("approval card hides the end of an 8 KB body")
	}
}

func TestApprovalBoundToContentType(t *testing.T) {
	w := newWorld(t)
	u := "https://api.github.test/repos/zaneGittins/orbit/pulls/4/merge"
	_, _, hdr := do(t, w.orbit(), "PUT", u, "{}", map[string]string{"Content-Type": "application/json"})
	id := hdr.Get("X-Skybreaker-Approval")
	w.approvals.Decide(id, true, "zane@example.com")
	if code, _, _ := do(t, w.orbit(), "PUT", u, "{}", map[string]string{"Content-Type": "application/merge-patch+json"}); code == 200 {
		t.Fatal("approval reused for a different content type")
	}
	if code, _, _ := do(t, w.orbit(), "PUT", u, "{}", map[string]string{"Content-Type": "application/json"}); code != 200 {
		t.Fatalf("identical retry: %d", code)
	}
}

func TestCredentialSideChannelsRefused(t *testing.T) {
	w := newWorld(t)
	c := w.orbit()
	cases := []struct {
		name, url string
		hdr       map[string]string
	}{
		{"query key", "https://llm.test/v1/messages?key=ATTACKER", nil},
		{"query access_token", "https://llm.test/v1/messages?access_token=ATTACKER", nil},
		{"query api_key mixed case", "https://llm.test/v1/messages?Api_Key=ATTACKER", nil},
		// Servers that map headers to CGI-style names treat _ as -.
		{"underscore header", "https://llm.test/v1/messages", map[string]string{"X_Api_Key": "sk-ATTACKER"}},
		{"underscore authorization", "https://llm.test/v1/messages", map[string]string{"Http_Authorization": "Bearer x"}},
	}
	for _, cs := range cases {
		if code, _, _ := do(t, c, "POST", cs.url, "{}", cs.hdr); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", cs.name, code)
		}
	}
	if n := len(w.hitsFor("llm.test")); n != 0 {
		t.Fatalf("%d requests with smuggled credentials reached upstream", n)
	}
	// Cookies never reach a credentialed upstream.
	if code, _, _ := do(t, c, "POST", "https://llm.test/v1/messages", "{}", map[string]string{"Cookie": "session=ATTACKER"}); code != 200 {
		t.Fatalf("request with a cookie: %d", code)
	}
	if got := w.hitsFor("llm.test")[0].Header.Get("Cookie"); got != "" {
		t.Fatalf("cookie forwarded: %q", got)
	}
	// Ordinary query parameters still pass.
	if code, _, _ := do(t, c, "GET", "https://api.github.test/repos/zaneGittins/orbit/pulls?state=open&per_page=50", "", nil); code != 200 {
		t.Fatalf("ordinary query: %d", code)
	}
}

func TestRefusedRequestNeverReachesApprover(t *testing.T) {
	w := newWorld(t)
	code, _, hdr := do(t, w.orbit(), "PUT", "https://api.github.test/repos/zaneGittins/orbit/pulls/9/merge", "{}", map[string]string{"Authorization": "Bearer mine"})
	if code != http.StatusForbidden || hdr.Get("X-Skybreaker-Approval") != "" {
		t.Fatalf("own credential on an ask rule: %d approval %q", code, hdr.Get("X-Skybreaker-Approval"))
	}
	if n := len(w.approvals.List()); n != 0 {
		t.Fatalf("%d approval cards for a request that was refused anyway", n)
	}
}

func TestAskFloodIsThrottled(t *testing.T) {
	w := newWorld(t)
	c := w.orbit()
	var last int
	for i := range 21 {
		last, _, _ = do(t, c, "PUT", fmt.Sprintf("https://api.github.test/repos/zaneGittins/orbit/pulls/%d/merge", i), "{}", nil)
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("21st distinct ask: %d, want 429", last)
	}
	if n := len(w.approvals.List()); n != 20 {
		t.Fatalf("%d approval cards, want the cap of 20", n)
	}
}

func TestApprovalBindingCoversRouting(t *testing.T) {
	// Two routes to one upstream host can inject different credentials; an
	// approval granted for one must not be spendable on the other.
	base := target{mode: "route", host: "kube.test", port: 443, routePrefix: "/k8s/prod-ro", inject: &config.Inject{Bearer: "ro"}}
	other := base
	other.routePrefix, other.inject = "/k8s/prod-admin", &config.Inject{Bearer: "admin"}
	if approvalBinding(base) == approvalBinding(other) {
		t.Fatal("different routes share an approval binding")
	}
	port := base
	port.port = 6443
	if approvalBinding(base) == approvalBinding(port) {
		t.Fatal("different ports share an approval binding")
	}
}

func TestRevokeCutsOpenMITMConnection(t *testing.T) {
	// The kill switch must stop a kept-alive connection, not only new ones.
	w := newWorld(t)
	c := w.orbit()
	u := "https://api.github.test/repos/zaneGittins/orbit/pulls"
	if code, _, _ := do(t, c, "GET", u, "", nil); code != 200 {
		t.Fatalf("before revoke: %d", code)
	}
	w.sessions.RevokeAll()
	if code, _, _ := do(t, c, "GET", u, "", nil); code == 200 {
		t.Fatal("request on an open connection succeeded after revoke")
	}
	if n := len(w.hitsFor("api.github.test")); n != 1 {
		t.Fatalf("upstream saw %d requests, want 1", n)
	}
}

func TestRevokeClosesOpenTunnel(t *testing.T) {
	w := newWorld(t)
	c, err := net.Dial("tcp", w.proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	auth := base64.StdEncoding.EncodeToString([]byte("orbit:" + w.token))
	fmt.Fprintf(c, "CONNECT tunnel.test:443 HTTP/1.1\r\nHost: tunnel.test:443\r\nProxy-Authorization: Basic %s\r\n\r\n", auth)
	br := bufio.NewReader(c)
	if resp, err := http.ReadResponse(br, nil); err != nil || resp.StatusCode != 200 {
		t.Fatalf("connect: %v %v", resp, err)
	}
	w.sessions.RevokeAll()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := br.ReadByte(); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("tunnel still open after revoke: %v", err)
	}
}

func TestUpgradeRequestsRefusedPlainly(t *testing.T) {
	// kubectl exec/attach/port-forward and websockets switch protocols,
	// which the broker can't inspect. They're refused with a clear status,
	// before any approval card, rather than forwarded without the upgrade.
	w := newWorld(t)
	upgrade := func(proto string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + w.token, "Connection": "Upgrade", "Upgrade": proto}
	}
	for _, c := range []struct{ method, path, proto string }{
		{"GET", "/k8s/prod/api/v1/namespaces/apps/pods", "SPDY/3.1"},         // auto
		{"DELETE", "/k8s/prod/api/v1/namespaces/apps/pods/web", "websocket"}, // ask
	} {
		code, b, _ := do(t, http.DefaultClient, c.method, w.proxy.URL+c.path, "", upgrade(c.proto))
		if code != http.StatusNotImplemented || !strings.Contains(b, "upgrade") {
			t.Errorf("%s %s with Upgrade: %d %q, want 501", c.method, c.path, code, b)
		}
	}
	code, _, _ := do(t, w.orbit(), "GET", "https://api.github.test/repos/zaneGittins/orbit", "", map[string]string{"Connection": "Upgrade", "Upgrade": "websocket"})
	if code != http.StatusNotImplemented {
		t.Errorf("websocket upgrade on a mitm host: %d, want 501", code)
	}
	if len(w.hitsFor("kube.test")) != 0 || len(w.hitsFor("api.github.test")) != 0 {
		t.Error("an upgrade request reached the upstream")
	}
	if n := len(w.approvals.List()); n != 0 {
		t.Errorf("%d approval cards for upgrade requests", n)
	}
}

// A tunnel carries TLS to the host it was opened for: one opened for
// tunnel.test can't present another name to the same address (domain
// fronting reaches every site behind a CDN), or carry anything but TLS.
func TestTunnelChecksServerName(t *testing.T) {
	w := newWorld(t)
	open := func() net.Conn {
		c, err := net.Dial("tcp", w.proxy.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		auth := base64.StdEncoding.EncodeToString([]byte("orbit:" + w.token))
		fmt.Fprintf(c, "CONNECT tunnel.test:443 HTTP/1.1\r\nHost: tunnel.test:443\r\nProxy-Authorization: Basic %s\r\n\r\n", auth)
		br := bufio.NewReader(c)
		if resp, err := http.ReadResponse(br, nil); err != nil || resp.StatusCode != 200 {
			t.Fatalf("connect: %v %v", resp, err)
		}
		return c
	}
	pool := w.brokerCA.Pool()
	pool.AppendCertsFromPEM(w.publicCA.CertPEM())
	for _, name := range []string{"api.github.test", ""} {
		c := open()
		tc := tls.Client(c, &tls.Config{ServerName: name, RootCAs: pool, InsecureSkipVerify: name == ""})
		c.SetDeadline(time.Now().Add(3 * time.Second))
		if err := tc.Handshake(); err == nil {
			t.Errorf("a tunnel for tunnel.test reached %q", name)
		}
		c.Close()
	}
	c := open()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: api.github.test\r\n\r\n")
	if b, _ := io.ReadAll(c); len(b) != 0 {
		t.Errorf("plain text through a tunnel: %q", b)
	}
	c.Close()
	if n := len(w.hitsFor("api.github.test")); n != 0 {
		t.Fatalf("api.github.test saw %d requests through tunnel.test", n)
	}
	if !strings.Contains(w.auditBuf.String(), "server name") {
		t.Error("refusal not audited")
	}
}

// A plain-HTTP request to a tunnel host goes to that host by name: a Host
// header naming another site at the same address is not passed on.
func TestPlainHTTPHostHeaderIsTheURLs(t *testing.T) {
	w := newWorld(t)
	c, err := net.Dial("tcp", w.proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	auth := base64.StdEncoding.EncodeToString([]byte("orbit:" + w.token))
	fmt.Fprintf(c, "GET http://plain.test/x HTTP/1.1\r\nHost: api.github.test\r\nProxy-Authorization: Basic %s\r\nConnection: close\r\n\r\n", auth)
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("plain: %v %v", resp, err)
	}
	if n := len(w.hitsFor("api.github.test")); n != 0 {
		t.Fatal("the upstream was told it served api.github.test")
	}
	if n := len(w.hitsFor("plain.test")); n != 1 {
		t.Fatalf("plain.test saw %d requests", n)
	}
}
