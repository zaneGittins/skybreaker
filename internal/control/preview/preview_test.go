package preview

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/wsock"
)

const origin = "https://skybreaker.example.ts.net:8401"

// upstream is a dev server that records what it saw and answers with
// whatever headers the test sets.
type upstream struct {
	*httptest.Server
	got  chan *http.Request
	hdrs http.Header
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{got: make(chan *http.Request, 8), hdrs: http.Header{}}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.got <- r.Clone(r.Context())
		for k, v := range u.hdrs {
			w.Header()[k] = v
		}
		io.WriteString(w, "dev page")
	}))
	t.Cleanup(u.Close)
	return u
}

func handlerFor(u *upstream) *Handler {
	ap := netip.MustParseAddrPort(strings.TrimPrefix(u.URL, "http://"))
	return &Handler{Project: "orbit", Origin: origin, Target: func() (netip.AddrPort, bool) { return ap, true }}
}

func serve(h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, origin+path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOnlyItsOwnOriginMayRead(t *testing.T) {
	u := newUpstream(t)
	h := handlerFor(u)
	for name, c := range map[string]struct {
		method string
		hdr    map[string]string
		ok     bool
	}{
		"typed URL":                   {"GET", map[string]string{"Sec-Fetch-Site": "none", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}, true},
		"link from the app":           {"GET", map[string]string{"Sec-Fetch-Site": "same-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}, true},
		"its own scripts":             {"GET", map[string]string{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"}, true},
		"its own HMR socket":          {"GET", map[string]string{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "websocket"}, true},
		"curl, no browser headers":    {"GET", nil, true},
		"another preview's fetch":     {"GET", map[string]string{"Sec-Fetch-Site": "same-site", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty", "Origin": "https://skybreaker.example.ts.net:8402"}, false},
		"another site's fetch":        {"GET", map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "script"}, false},
		"framed by another preview":   {"GET", map[string]string{"Sec-Fetch-Site": "same-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe"}, false},
		"form posted from elsewhere":  {"POST", map[string]string{"Sec-Fetch-Site": "same-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}, false},
		"another preview's socket":    {"GET", map[string]string{"Sec-Fetch-Site": "same-site", "Sec-Fetch-Mode": "websocket", "Origin": "https://skybreaker.example.ts.net:8402"}, false},
		"old browser, foreign Origin": {"GET", map[string]string{"Origin": "https://evil.example"}, false},
		"old browser, own Origin":     {"POST", map[string]string{"Origin": origin}, true},
	} {
		w := serve(h, c.method, "/", c.hdr)
		if ok := w.Code == 200; ok != c.ok {
			t.Errorf("%s: %d", name, w.Code)
		}
		if !c.ok {
			select {
			case <-u.got:
				t.Errorf("%s reached the dev server", name)
			default:
			}
		} else {
			<-u.got
		}
	}
}

func TestUpstreamSeesLocalhost(t *testing.T) {
	// Dev servers (Vite, Next) refuse hosts they don't know and check a
	// socket's Origin; the proxy has already checked both.
	u := newUpstream(t)
	port := u.URL[strings.LastIndex(u.URL, ":")+1:]
	serve(handlerFor(u), "GET", "/src/main.ts?t=1", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": origin, "Referer": origin + "/about"})
	r := <-u.got
	if r.Host != "localhost:"+port || r.Header.Get("Origin") != "http://localhost:"+port || r.Header.Get("Referer") != "http://localhost:"+port+"/about" {
		t.Fatalf("host %q origin %q referer %q", r.Host, r.Header.Get("Origin"), r.Header.Get("Referer"))
	}
	if r.URL.RequestURI() != "/src/main.ts?t=1" {
		t.Fatalf("path %q", r.URL.RequestURI())
	}
	if r.Header.Get("X-Forwarded-For") != "" {
		t.Fatal("client address forwarded")
	}
}

func TestCookiesAreThisProjectsOnly(t *testing.T) {
	// Browsers scope cookies by host, not port: every preview, and the app,
	// get each other's. The proxy names this project's and drops the rest.
	u := newUpstream(t)
	u.hdrs["Set-Cookie"] = []string{"session=abc; Path=/; Domain=skybreaker.example.ts.net", "__Host-csrf=x; Path=/; Secure", "bad cookie; Path=/"}
	w := serve(handlerFor(u), "GET", "/", map[string]string{"Sec-Fetch-Site": "same-origin", "Cookie": "sbp_orbit_session=mine; sbp_comet_session=theirs; other=app"})
	r := <-u.got
	if got := r.Header.Get("Cookie"); got != "session=mine" {
		t.Fatalf("dev server got cookies %q", got)
	}
	set := w.Result().Header.Values("Set-Cookie")
	if len(set) != 2 {
		t.Fatalf("set-cookie %q", set)
	}
	for _, c := range set {
		if !strings.HasPrefix(c, "sbp_orbit_") || !strings.Contains(c, "HttpOnly") || strings.Contains(strings.ToLower(c), "domain=") {
			t.Errorf("cookie %q", c)
		}
	}
}

func TestResponseHeaders(t *testing.T) {
	u := newUpstream(t)
	port := u.URL[strings.LastIndex(u.URL, ":")+1:]
	u.hdrs["Access-Control-Allow-Origin"] = []string{"*"}
	u.hdrs["Access-Control-Allow-Credentials"] = []string{"true"}
	u.hdrs["Location"] = []string{"http://localhost:" + port + "/login?next=/"}
	w := serve(handlerFor(u), "GET", "/", map[string]string{"Sec-Fetch-Site": "same-origin"})
	h := w.Result().Header
	if h.Get("Access-Control-Allow-Origin") != "" || h.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("CORS headers passed through")
	}
	if !slices.ContainsFunc(h.Values("Content-Security-Policy"), func(v string) bool { return strings.Contains(v, "frame-ancestors 'self'") }) {
		t.Fatalf("csp %q", h.Values("Content-Security-Policy"))
	}
	if h.Get("Cross-Origin-Opener-Policy") != "same-origin" || h.Get("Referrer-Policy") == "" {
		t.Fatalf("headers %v", h)
	}
	if h.Get("Location") != origin+"/login?next=/" {
		t.Fatalf("location %q", h.Get("Location"))
	}
}

func TestNothingListening(t *testing.T) {
	h := &Handler{Project: "orbit", Origin: origin, Target: func() (netip.AddrPort, bool) { return netip.MustParseAddrPort("127.0.0.1:1"), true }}
	w := serve(h, "GET", "/", map[string]string{"Sec-Fetch-Site": "none"})
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "0.0.0.0") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	none := &Handler{Project: "orbit", Origin: origin, Target: func() (netip.AddrPort, bool) { return netip.AddrPort{}, false }}
	if w := serve(none, "GET", "/", nil); w.Code != http.StatusNotFound {
		t.Fatalf("no port chosen: %d", w.Code)
	}
}

func TestParseListening(t *testing.T) {
	tcp := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:223E 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1
   1: 00000000:223D 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2
   2: 0200590A:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 3
   3: 0200590A:D2A4 0100590A:1F90 01 00000000:00000000 00:00000000 00000000     0        0 4
   garbage line with ZZZZ:XYZW
   4: 00000000:0000 00000000:0000 0A
`
	tcp6 := `  sl  local_address                         remote_address                        st
   0: 00000000000000000000000001000000:0BB8 00000000000000000000000000000000:0000 0A
   1: 00000000000000000000000000000000:1435 00000000000000000000000000000000:0000 0A
   2: 0000000000000000FFFF00000100007F:223D 00000000000000000000000000000000:0000 0A
`
	got := ParseListening([]byte(tcp + tcp6))
	want := []Listening{{Port: 3000, Public: false}, {Port: 5173, Public: true}, {Port: 8080, Public: true}, {Port: 8765, Public: true}, {Port: 8766, Public: false}}
	if !slices.Equal(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	// A sandbox that lists thousands of ports gets a bounded answer.
	var b strings.Builder
	for i := 1; i < 5000; i++ {
		b.WriteString("0: 00000000:")
		b.WriteString(strings.ToUpper(strings.TrimLeft(hex4(i), "")))
		b.WriteString(" 00000000:0000 0A\n")
	}
	if n := len(ParseListening([]byte(b.String()))); n != maxListening {
		t.Fatalf("%d ports", n)
	}
}

func hex4(i int) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{digits[i>>12&15], digits[i>>8&15], digits[i>>4&15], digits[i&15]})
}

func TestHotReloadSocket(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "http://"+r.Host {
			http.Error(w, "origin", http.StatusForbidden) // as Vite checks
			return
		}
		c, err := wsock.Accept(w, r)
		if err != nil {
			return
		}
		defer c.Close(1000, "")
		bin, b, err := c.Read(context.Background())
		if err == nil {
			c.Write(context.Background(), bin, b)
		}
	}))
	defer up.Close()
	ap := netip.MustParseAddrPort(strings.TrimPrefix(up.URL, "http://"))
	front := httptest.NewServer(&Handler{Project: "orbit", Origin: origin, Target: func() (netip.AddrPort, bool) { return ap, true }})
	defer front.Close()
	u := "ws" + strings.TrimPrefix(front.URL, "http") + "/@vite/hmr"
	c, err := wsock.Dial(context.Background(), u, http.Header{"Origin": {origin}, "Sec-Fetch-Site": {"same-origin"}, "Sec-Fetch-Mode": {"websocket"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(1000, "")
	c.Write(context.Background(), false, []byte(`{"type":"ping"}`))
	if _, b, err := c.Read(context.Background()); err != nil || string(b) != `{"type":"ping"}` {
		t.Fatalf("echo %q %v", b, err)
	}
	if _, err := wsock.Dial(context.Background(), u, http.Header{"Origin": {"https://skybreaker.example.ts.net:8402"}, "Sec-Fetch-Site": {"same-site"}, "Sec-Fetch-Mode": {"websocket"}}, nil); err == nil {
		t.Fatal("another preview opened this one's socket")
	}
}
