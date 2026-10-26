package webutil

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestHardenHeadersAndCSRF(t *testing.T) {
	h := HardenWith(AppCSP, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	ts := httptest.NewServer(h)
	defer ts.Close()

	resp, _ := http.Get(ts.URL)
	resp.Body.Close()
	for k, want := range map[string]string{"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY"} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s = %q", k, got)
		}
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Errorf("CSP %q", resp.Header.Get("Content-Security-Policy"))
	}

	post := func(ct string, hdr map[string]string) int {
		r, _ := http.NewRequest("POST", ts.URL, strings.NewReader("{}"))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post("application/json", nil); c != 200 {
		t.Errorf("json post %d", c)
	}
	if c := post("application/json; charset=utf-8", nil); c != 200 {
		t.Errorf("json+charset post %d", c)
	}
	if c := post("application/gzip", nil); c != 200 {
		t.Errorf("archive post %d", c)
	}
	if c := post("application/gzip", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.test"}); c != http.StatusForbidden {
		t.Errorf("cross-site archive post %d", c)
	}
	if c := post("text/plain", nil); c != http.StatusUnsupportedMediaType {
		t.Errorf("text post %d", c)
	}
	if c := post("", nil); c != http.StatusUnsupportedMediaType {
		t.Errorf("no content type %d", c)
	}
	if c := post("application/json", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.test"}); c != http.StatusForbidden {
		t.Errorf("cross-site json post %d", c)
	}
}

func TestStatic(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html>home")},
		"app.js":     {Data: []byte("console.log(1)")},
	}
	ts := httptest.NewServer(Static(fsys))
	defer ts.Close()
	get := func(p string) (int, string, http.Header) {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	if c, b, h := get("/app.js"); c != 200 || b != "console.log(1)" || !strings.HasPrefix(h.Get("Content-Type"), "text/javascript") || h.Get("ETag") == "" {
		t.Errorf("app.js: %d %q %v", c, b, h)
	}
	// Client-side routes fall back to the page.
	if c, b, _ := get("/p/orbit"); c != 200 || !strings.Contains(b, "home") {
		t.Errorf("spa fallback: %d %q", c, b)
	}
	if c, _, _ := get("/missing.js"); c != 404 {
		t.Errorf("missing asset: %d", c)
	}
	// Paths are cleaned inside the embedded FS; at worst the page comes back.
	if _, b, _ := get("/../etc/passwd"); strings.Contains(b, "root:") || !strings.Contains(b, "home") {
		t.Errorf("traversal: %q", b)
	}
}

func TestStaticCompressesAndVersions(t *testing.T) {
	js := strings.Repeat("console.log('skybreaker');\n", 200)
	fsys := fstest.MapFS{
		"index.html":      {Data: []byte(`<!doctype html><link rel="stylesheet" href="/app.css"><script src="/app.js"></script><a href="/p/x">p</a>`)},
		"app.js":          {Data: []byte(js)},
		"app.css":         {Data: []byte("body{}")},
		"fonts/x.woff2":   {Data: []byte(strings.Repeat("w", 4000))},
		"vendor/xterm.js": {Data: []byte(js)},
	}
	ts := httptest.NewServer(Static(fsys))
	defer ts.Close()
	// The transport would decompress for us; ask by hand to see the bytes.
	get := func(p string, hdr map[string]string) (*http.Response, []byte) {
		req, _ := http.NewRequest("GET", ts.URL+p, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}
	gz := map[string]string{"Accept-Encoding": "gzip, br"}

	resp, b := get("/app.js", gz)
	if resp.Header.Get("Content-Encoding") != "gzip" || !strings.Contains(resp.Header.Get("Vary"), "Accept-Encoding") || len(b) >= len(js) {
		t.Fatalf("gzip app.js: %v, %d bytes", resp.Header, len(b))
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := io.ReadAll(zr); string(plain) != js {
		t.Error("gzip body doesn't decompress to the file")
	}
	gzTag := resp.Header.Get("ETag")
	plainResp, plain := get("/app.js", map[string]string{"Accept-Encoding": "identity"})
	if plainResp.Header.Get("Content-Encoding") != "" || string(plain) != js || plainResp.Header.Get("ETag") == gzTag {
		t.Errorf("identity app.js: %v", plainResp.Header)
	}
	if r, _ := get("/app.js", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": gzTag}); r.StatusCode != 304 {
		t.Errorf("revalidating the gzip copy: %d", r.StatusCode)
	}
	if r, _ := get("/fonts/x.woff2", gz); r.Header.Get("Content-Encoding") != "" {
		t.Error("woff2 is already compressed and was gzipped")
	}

	// The page names each asset by content hash; that URL is cached for good.
	_, page := get("/p/x", nil)
	m := regexp.MustCompile(`src="/app\.js\?v=([0-9a-f]+)"`).FindSubmatch(page)
	if m == nil || !regexp.MustCompile(`href="/app\.css\?v=[0-9a-f]+"`).Match(page) || !strings.Contains(string(page), `href="/p/x"`) {
		t.Fatalf("page not versioned: %s", page)
	}
	if r, _ := get("/app.js?v="+string(m[1]), nil); !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("versioned asset: %v", r.Header.Get("Cache-Control"))
	}
	if r, _ := get("/app.js?v=stale", nil); r.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("stale version cached: %v", r.Header.Get("Cache-Control"))
	}
	if r, _ := get("/", nil); r.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("page cached: %v", r.Header.Get("Cache-Control"))
	}
}

func TestWriteJSONGzip(t *testing.T) {
	big := map[string]string{"text": strings.Repeat("row\n", 2000)}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { WriteJSONGzip(w, r, 200, big) })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	h(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Body.Len() > 2000 {
		t.Fatalf("big reply: %v, %d bytes", rec.Header(), rec.Body.Len())
	}
	zr, _ := gzip.NewReader(rec.Body)
	var got map[string]string
	if err := json.NewDecoder(zr).Decode(&got); err != nil || got["text"] != big["text"] {
		t.Errorf("decoded: %v", err)
	}
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("gzipped for a client that didn't ask")
	}
}

func TestSSE(t *testing.T) {
	events := make(chan any, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeSSE(w, r, events, time.Hour)
	}))
	defer ts.Close()
	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events <- map[string]string{"kind": "x"}
	sc := bufio.NewScanner(resp.Body)
	done := make(chan string, 1)
	go func() {
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: ") {
				done <- sc.Text()
				return
			}
		}
	}()
	select {
	case l := <-done:
		if l != `data: {"kind":"x"}` {
			t.Fatalf("line %q", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
}

func TestSSEStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { ServeSSE(w, r, make(chan any), time.Hour); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ServeSSE did not return on cancel")
	}
}

func TestNewServerLimits(t *testing.T) {
	// Sandboxes can reach the hook and proxy listeners: an idle or
	// oversized connection must not be held open for ever.
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "v")
	s := NewServer(ctx, http.NotFoundHandler())
	if s.ReadHeaderTimeout <= 0 || s.ReadHeaderTimeout > 30*time.Second {
		t.Errorf("ReadHeaderTimeout = %v", s.ReadHeaderTimeout)
	}
	if s.IdleTimeout <= 0 || s.IdleTimeout > 5*time.Minute {
		t.Errorf("IdleTimeout = %v", s.IdleTimeout)
	}
	if s.MaxHeaderBytes <= 0 || s.MaxHeaderBytes > 1<<20 {
		t.Errorf("MaxHeaderBytes = %d", s.MaxHeaderBytes)
	}
	if s.WriteTimeout != 0 || s.ReadTimeout != 0 {
		t.Error("held approvals, tunnels and SSE need no whole-request timeouts")
	}
	if s.BaseContext == nil || s.BaseContext(nil).Value(key{}) != "v" {
		t.Error("requests don't inherit the daemon's context")
	}
}

// A preview page sets cookies on Skybreaker's host, which the browser then
// sends to the app too. Even a flood of them must not lock you out.
func TestServerTakesABrowsersWorthOfCookies(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	srv.Config = NewServer(context.Background(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Start()
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Cookie", "c="+strings.Repeat("x", 200<<10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("200 KiB of cookies: %v %v", resp, err)
	}
}

func TestHardenSetsOpenerPolicy(t *testing.T) {
	w := httptest.NewRecorder()
	HardenWith(AppCSP, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Header().Get("Cross-Origin-Opener-Policy") != "same-origin" {
		t.Fatal("no opener policy: a page the app opened could steer it")
	}
}

// A DELETE with no body has no content type to check (MCP clients end a
// session so); a form can't send one, and cross-origin protection still
// applies. With a body, it must be JSON like any write.
func TestBodylessDelete(t *testing.T) {
	ts := httptest.NewServer(HardenWith(AppCSP, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	defer ts.Close()
	r, _ := http.NewRequest("DELETE", ts.URL, nil)
	if resp, err := http.DefaultClient.Do(r); err != nil || resp.StatusCode != 200 {
		t.Fatalf("bodyless DELETE: %v %v", resp, err)
	}
	r, _ = http.NewRequest("DELETE", ts.URL, strings.NewReader("x"))
	r.Header.Set("Content-Type", "text/plain")
	if resp, err := http.DefaultClient.Do(r); err != nil || resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("DELETE with a text body: %v %v", resp, err)
	}
}
