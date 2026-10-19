package preview

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/netip"
	"net/textproto"
	"strings"
	"testing"
)

// The page's script runs in your browser, which isn't behind the broker:
// the policy keeps it talking to its own server and nothing else.
func TestPolicyConfinesThePage(t *testing.T) {
	u := newUpstream(t)
	u.hdrs["Content-Security-Policy"] = []string{"default-src *; report-uri https://evil.example/r"}
	u.hdrs["Content-Security-Policy-Report-Only"] = []string{"default-src 'none'; report-to x"}
	for _, k := range []string{"Report-To", "Reporting-Endpoints", "Nel", "Alt-Svc", "Clear-Site-Data", "Strict-Transport-Security", "Refresh", "Server", "X-Powered-By", "Link"} {
		u.hdrs[k] = []string{"x"}
	}
	u.hdrs["Etag"] = []string{`"v1"`}
	u.hdrs["Cache-Control"] = []string{"no-cache"}
	h := serve(handlerFor(u), "GET", "/", map[string]string{"Sec-Fetch-Site": "same-origin"}).Result().Header
	csp := h.Values("Content-Security-Policy")
	if len(csp) != 1 {
		t.Fatalf("csp %q", csp)
	}
	for _, want := range []string{"default-src 'self'", "connect-src 'self'", "form-action 'self'", "frame-ancestors 'self'", "base-uri 'self'", "object-src 'none'"} {
		if !strings.Contains(csp[0], want) {
			t.Errorf("csp lacks %q: %s", want, csp[0])
		}
	}
	for _, k := range []string{"Content-Security-Policy-Report-Only", "Report-To", "Reporting-Endpoints", "Nel", "Alt-Svc", "Clear-Site-Data", "Strict-Transport-Security", "Refresh", "Server", "X-Powered-By", "Link"} {
		if h.Get(k) != "" {
			t.Errorf("%s passed through", k)
		}
	}
	if h.Get("Etag") != `"v1"` || h.Get("Cache-Control") != "no-cache" || h.Get("X-Dns-Prefetch-Control") != "off" {
		t.Fatalf("headers %v", h)
	}
}

func TestCookiesAreSignedAndThisProjectsOnly(t *testing.T) {
	u := newUpstream(t)
	u.hdrs["Set-Cookie"] = []string{"session=abc; Path=/; Domain=skybreaker.example.ts.net", "__Host-csrf=x; Path=/; Secure", "bad cookie; Path=/"}
	h := handlerFor(u)
	w := serve(h, "GET", "/", map[string]string{"Sec-Fetch-Site": "same-origin"})
	<-u.got
	set := w.Result().Cookies()
	if len(set) != 2 {
		t.Fatalf("set-cookie %v", w.Result().Header.Values("Set-Cookie"))
	}
	for _, c := range set {
		if !strings.HasPrefix(c.Name, "sbp_orbit_") || !c.HttpOnly || c.Domain != "" {
			t.Errorf("cookie %+v", c)
		}
	}
	// What the browser sends back reaches the dev server as it set it;
	// anything planted by script on another port (unsigned, or signed for
	// another value) is dropped, as are other projects' and the app's.
	delete(u.hdrs, "Set-Cookie")
	real := set[0].Name + "=" + set[0].Value
	planted := "sbp_orbit_session=planted; sbp_orbit_session=" + strings.Replace(set[0].Value, "abc", "evil", 1)
	serve(h, "GET", "/", map[string]string{"Sec-Fetch-Site": "same-origin", "Cookie": planted + "; " + real + "; sbp_comet_session=theirs; other=app"})
	if got := (<-u.got).Header.Get("Cookie"); got != "session=abc" {
		t.Fatalf("dev server got cookies %q", got)
	}
}

func TestCookieBombCapped(t *testing.T) {
	// Cookies set on a preview port go to the app too; a flood of them
	// would lock you out of approvals and the kill switch.
	u := newUpstream(t)
	for i := range 40 {
		u.hdrs["Set-Cookie"] = append(u.hdrs["Set-Cookie"], fmt.Sprintf("c%d=v; Path=/", i))
	}
	u.hdrs["Set-Cookie"] = append(u.hdrs["Set-Cookie"], "big="+strings.Repeat("x", 4000))
	set := serve(handlerFor(u), "GET", "/", map[string]string{"Sec-Fetch-Site": "same-origin"}).Result().Header.Values("Set-Cookie")
	if len(set) > maxCookies {
		t.Fatalf("%d cookies passed", len(set))
	}
	for _, c := range set {
		if len(c) > maxCookieBytes {
			t.Fatalf("cookie of %d bytes passed", len(c))
		}
	}
}

func TestEarlyHintsAndTrailersDropped(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "raw=1; Domain=example.ts.net; Path=/")
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Del("Set-Cookie")
		w.Header().Set("Trailer", "Set-Cookie")
		w.WriteHeader(200)
		io.WriteString(w, "body")
		w.Header().Set("Set-Cookie", "trailer=1; Domain=example.ts.net")
	}))
	defer up.Close()
	ap := netip.MustParseAddrPort(strings.TrimPrefix(up.URL, "http://"))
	front := httptest.NewUnstartedServer(nil)
	front.Config.Handler = &Handler{Project: "orbit", Origin: "http://" + front.Listener.Addr().String(), Target: func() (netip.AddrPort, bool) { return ap, true }}
	front.Start()
	defer front.Close()
	var early []int
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{Got1xxResponse: func(code int, _ textproto.MIMEHeader) error { early = append(early, code); return nil }})
	req, _ := http.NewRequestWithContext(ctx, "GET", front.URL+"/", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(early) != 0 {
		t.Fatalf("informational responses reached the browser: %v", early)
	}
	if len(resp.Trailer) != 0 || resp.Header.Get("Set-Cookie") != "" {
		t.Fatalf("trailer %v, header %v", resp.Trailer, resp.Header)
	}
}

func TestOnlyItsOwnHost(t *testing.T) {
	u := newUpstream(t)
	r := httptest.NewRequest("GET", "https://skybreaker.example.ts.net:8402/", nil)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	handlerFor(u).ServeHTTP(w, r)
	if w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("another port's host: %d", w.Code)
	}
	if w := serve(handlerFor(u), "GET", "/", map[string]string{"Sec-Fetch-Site": "same-origin"}); w.Code != 200 {
		t.Fatalf("own host: %d", w.Code)
	}
	<-u.got
}
