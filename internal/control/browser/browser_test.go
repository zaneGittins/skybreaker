package browser

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// chromium is a Chromium to test with: $SKYBREAKER_CHROMIUM, or one on
// PATH. Tests that need one skip without it.
func chromium(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("SKYBREAKER_CHROMIUM"); p != "" {
		return p
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skip("no Chromium: set SKYBREAKER_CHROMIUM")
	return ""
}

// localLaunch runs Chromium on this machine with its pipe on fds 3 and 4,
// as the browser container does over docker exec.
func localLaunch(bin string) Launch {
	return func(ctx context.Context, args []string) (io.ReadCloser, io.WriteCloser, func(), error) {
		inR, inW, err := os.Pipe()
		if err != nil {
			return nil, nil, nil, err
		}
		outR, outW, err := os.Pipe()
		if err != nil {
			return nil, nil, nil, err
		}
		cmd := exec.Command(bin, args...)
		cmd.ExtraFiles = []*os.File{inR, outW}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return nil, nil, nil, err
		}
		inR.Close()
		outW.Close()
		// Chromium's helpers are in its process group: all of them go.
		return outR, inW, func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Wait() }, nil
	}
}

const home = `<!doctype html><title>Home</title>
<h1>Welcome home</h1>
<a href="/next">Next page</a>
<a href="/security">Account security</a>
<a href="/file" download>Get the podcast</a>
<form action="/search"><label for=q>Search</label><input id=q name=q value="prefilled-text">
<label for=pw>Password</label><input id=pw type=password value="hunter2">
<input autocomplete="one-time-code" aria-label="Code">
<button>Go</button></form>`

func site(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, home)
		case "/next":
			fmt.Fprint(w, `<!doctype html><title>Next</title><p>Second page</p>`)
		case "/search":
			fmt.Fprintf(w, `<!doctype html><title>Results</title><p>You searched for %s</p>`, r.URL.Query().Get("q"))
		case "/still":
			fmt.Fprint(w, `<!doctype html><title>Still</title><div style="height:1500px;background:linear-gradient(#fff,#cde)"></div>`)
		case "/drag":
			fmt.Fprint(w, `<!doctype html><title>Drag</title><body style="height:600px" onmousemove="document.title='buttons '+event.buttons">`)
		case "/alert":
			fmt.Fprint(w, `<!doctype html><title>Alert</title><button onclick="alert('Saved!');document.title='After'">Save</button>`)
		case "/security":
			fmt.Fprint(w, `<!doctype html><title>Security</title><p>Change password</p>`)
		case "/file":
			w.Header().Set("Content-Disposition", `attachment; filename="podcast.mp3"`)
			w.Write([]byte("ID3 fake audio"))
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func start(t *testing.T, o Options) *Browser {
	t.Helper()
	bin := chromium(t)
	dir := t.TempDir()
	o.Launch, o.Profile, o.Downloads = localLaunch(bin), filepath.Join(dir, "profile"), filepath.Join(dir, "downloads")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := Start(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func ref(t *testing.T, p Page, name string) int {
	t.Helper()
	for _, e := range p.Elements {
		if e.Name == name {
			return e.Ref
		}
	}
	t.Fatalf("no element %q in %+v", name, p.Elements)
	return 0
}

func TestReadTypeAndClick(t *testing.T) {
	ts := site(t)
	b := start(t, Options{})
	ctx := ctxT(t)
	if err := b.Open(ctx, ts.URL+"/"); err != nil {
		t.Fatal(err)
	}
	p, err := b.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Home" || !strings.Contains(p.Text, "Welcome home") {
		t.Fatalf("page %+v", p)
	}
	all := fmt.Sprintf("%+v", p)
	for _, value := range []string{"hunter2", "prefilled-text"} {
		if strings.Contains(all, value) {
			t.Errorf("read shows a field's contents %q", value)
		}
	}
	pw := ref(t, p, "Password")
	if e := p.Elements[pw-1]; !e.Secret {
		t.Errorf("password not marked secret: %+v", e)
	}
	for _, secret := range []int{pw, ref(t, p, "Code")} {
		if err := b.Type(ctx, secret, "guess", false); !errors.Is(err, ErrSecret) {
			t.Errorf("typed into a secret field: %v", err)
		}
	}
	if err := b.Type(ctx, ref(t, p, "Search"), " podcasts", true); err != nil {
		t.Fatal(err)
	}
	if p, _ := b.Read(ctx); !strings.Contains(p.Text, "You searched for prefilled-text podcasts") {
		t.Fatalf("after submit: %q", p.Text)
	}
	b.Open(ctx, ts.URL+"/")
	p, _ = b.Read(ctx)
	if err := b.Click(ctx, ref(t, p, "Next page")); err != nil {
		t.Fatal(err)
	}
	if s, _ := b.Status(ctx); s.Title != "Next" {
		t.Fatalf("after click: %+v", s)
	}
	if err := b.Back(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := b.Status(ctx); s.Title != "Home" {
		t.Fatalf("after back: %+v", s)
	}
	if err := b.Click(ctx, 999); err == nil {
		t.Error("clicked an element that isn't there")
	}
}

func TestDenyList(t *testing.T) {
	ts := site(t)
	host := strings.TrimPrefix(ts.URL, "http://")
	host, _, _ = strings.Cut(host, ":")
	b := start(t, Options{Deny: []string{host + "/security"}})
	ctx := ctxT(t)
	if err := b.Open(ctx, ts.URL+"/security"); !errors.Is(err, ErrDenied) {
		t.Fatalf("open: %v", err)
	}
	for _, u := range []string{"file:///etc/passwd", "chrome://settings", "javascript:alert(1)", "data:text/html,hi"} {
		if err := b.Open(ctx, u); err == nil {
			t.Errorf("opened %s", u)
		}
	}
	// A click is checked as well as an address.
	b.Open(ctx, ts.URL+"/")
	p, _ := b.Read(ctx)
	b.Click(ctx, ref(t, p, "Account security"))
	if s, _ := b.Status(ctx); s.Title == "Security" {
		t.Fatalf("a click reached a denied page: %+v", s)
	}
}

func TestDenied(t *testing.T) {
	deny := []string{"myaccount.google.com/security", "example.com"}
	for u, want := range map[string]bool{
		"https://myaccount.google.com/security":           true,
		"https://myaccount.google.com/security/2sv":       true,
		"https://myaccount.google.com/security-checkup":   false,
		"https://MyAccount.Google.com/./security":         true,
		"https://myaccount.google.com/%73ecurity":         true,
		"https://myaccount.google.com/SECURITY":           true,
		"https://evil.myaccount.google.com/security":      true,
		"https://myaccount.google.com.evil.com/security":  false,
		"https://example.com/anything":                    true,
		"https://notexample.com/":                         false,
		"https://myaccount.google.com/personal-info":      false,
		"https://myaccount.google.com/a/../security/keys": true,
	} {
		if got := denied(u, deny); got != want {
			t.Errorf("%s: denied=%v", u, got)
		}
	}
}

func TestTakeOver(t *testing.T) {
	ts := site(t)
	host := strings.TrimPrefix(ts.URL, "http://")
	host, _, _ = strings.Cut(host, ":")
	b := start(t, Options{Deny: []string{host + "/security"}})
	ctx := ctxT(t)
	b.Open(ctx, ts.URL+"/")
	frames, err := b.Take(ctx, "zane@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Take(ctx, "kim@example.com"); !errors.Is(err, ErrTaken) {
		t.Errorf("taken twice: %v", err)
	}
	if _, err := b.Read(ctx); !errors.Is(err, ErrPaused) {
		t.Errorf("agent read while taken: %v", err)
	}
	select {
	case f := <-frames:
		if !bytes.HasPrefix(f.JPEG, []byte{0xff, 0xd8}) || f.Width == 0 {
			t.Fatalf("frame %d bytes %dx%d", len(f.JPEG), f.Width, f.Height)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no frame")
	}
	// You may go where the agent may not, and type where it may not.
	if err := b.Go(ctx, ts.URL+"/security"); err != nil {
		t.Fatal(err)
	}
	if s, _ := b.Status(ctx); s.Title != "Security" || s.TakenBy != "zane@example.com" {
		t.Fatalf("status %+v", s)
	}
	b.Release(ctx)
	if _, ok := <-frames; ok {
		// drain one buffered frame; the channel must then close
		for range frames {
		}
	}
	if _, err := b.Read(ctx); err != nil {
		t.Errorf("agent after release: %v", err)
	}
	if err := b.Mouse(ctx, Mouse{Type: "mousePressed"}); err == nil {
		t.Error("input without taking the browser")
	}
}

func TestScreenshotAndDownload(t *testing.T) {
	ts := site(t)
	b := start(t, Options{})
	ctx := ctxT(t)
	b.Open(ctx, ts.URL+"/")
	png, err := b.Screenshot(ctx)
	if err != nil || !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatalf("screenshot %d bytes %v", len(png), err)
	}
	p, _ := b.Read(ctx)
	d, err := b.Download(ctx, ref(t, p, "Get the podcast"), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(d.Path)
	if d.Name != "podcast.mp3" || string(got) != "ID3 fake audio" {
		t.Fatalf("download %+v %q", d, got)
	}
}

// Through the broker: the browser answers its login challenge itself and
// trusts only the broker's CA, pinned, for the sites it intercepts.
func TestThroughTheBroker(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caT := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Broker CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caT, caT, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafT := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "example.test"}, DNSNames: []string{"example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, _ := x509.CreateCertificate(rand.Reader, leafT, ca, &leafKey.PublicKey, caKey)
	tlsSite := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Secure</title>over the broker`)
	}))
	tlsSite.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}}}
	tlsSite.StartTLS()
	defer tlsSite.Close()

	asked := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("pip.browser:tok")) {
			w.Header().Set("Proxy-Authenticate", `Basic realm="skybreaker"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		up, err := net.Dial("tcp", tlsSite.Listener.Addr().String())
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		c, _, _ := w.(http.Hijacker).Hijack()
		c.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
		go io.Copy(up, c)
		io.Copy(c, up)
	}))
	defer proxy.Close()
	sum := sha256.Sum256(ca.RawSubjectPublicKeyInfo)
	b := start(t, Options{Proxy: proxy.URL, CASPKI: base64.StdEncoding.EncodeToString(sum[:]),
		Credentials: func(context.Context) (string, string, error) { asked++; return "pip.browser", "tok", nil }})
	ctx := ctxT(t)
	if err := b.Open(ctx, "https://example.test/"); err != nil {
		t.Fatal(err)
	}
	if p, _ := b.Read(ctx); p.Title != "Secure" || asked == 0 {
		t.Fatalf("page %+v, credentials asked %d times", p, asked)
	}
}

// A phone that takes over sees the page laid out for its own screen, and
// the agent gets the desktop back after.
func TestFitForAPhone(t *testing.T) {
	ts := site(t)
	b := start(t, Options{})
	ctx := ctxT(t)
	b.Open(ctx, ts.URL+"/")
	if err := b.Fit(ctx, 390, 700); err == nil {
		t.Error("fit without taking the browser")
	}
	frames, err := b.Take(ctx, "zane@example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][2]int{{0, 700}, {390, 50}, {5000, 700}} {
		if err := b.Fit(ctx, bad[0], bad[1]); err == nil {
			t.Errorf("fit %v", bad)
		}
	}
	if err := b.Fit(ctx, 390, 700); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for f := range frames {
		if f.Width == 390 && f.Height == 700 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("still %dx%d", f.Width, f.Height)
		default:
		}
	}
	width := func() float64 {
		var out struct {
			Result struct{ Value float64 } `json:"result"`
		}
		if err := b.call(ctx, b.page, "Runtime.evaluate", map[string]any{"expression": "screen.width", "returnByValue": true}, &out); err != nil {
			t.Fatal(err)
		}
		return out.Result.Value
	}
	if w := width(); w != 390 {
		t.Errorf("page width %v while fitted", w)
	}
	b.Release(ctx)
	if w := width(); w == 390 {
		t.Error("the agent kept the phone's width")
	}
}

// A page's alert or confirm doesn't freeze the browser: it's answered, and
// the page carries on.
func TestDialogsDontHang(t *testing.T) {
	ts := site(t)
	b := start(t, Options{})
	ctx := ctxT(t)
	if err := b.Open(ctx, ts.URL+"/alert"); err != nil {
		t.Fatal(err)
	}
	p, err := b.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var save int
	for _, e := range p.Elements {
		if strings.Contains(e.Name, "Save") {
			save = e.Ref
		}
	}
	quick, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := b.Click(quick, save); err != nil {
		t.Fatal(err)
	}
	if s, err := b.Status(quick); err != nil || s.Title != "After" {
		t.Fatalf("after the alert: %+v %v", s, err)
	}
}

// A move with the button held is a drag, so a slider CAPTCHA or a
// selection follows you.
func TestDragHoldsTheButton(t *testing.T) {
	ts := site(t)
	b := start(t, Options{})
	ctx := ctxT(t)
	b.Open(ctx, ts.URL+"/drag")
	if _, err := b.Take(ctx, "zane@example.com"); err != nil {
		t.Fatal(err)
	}
	defer b.Release(ctx)
	title := func() string { s, _ := b.Status(ctx); return s.Title }
	b.Mouse(ctx, Mouse{Type: "mousePressed", X: 10, Y: 10, Button: "left", ClickCount: 1})
	b.Mouse(ctx, Mouse{Type: "mouseMoved", X: 40, Y: 10})
	if got := title(); got != "buttons 1" {
		t.Fatalf("while held: %q", got)
	}
	b.Mouse(ctx, Mouse{Type: "mouseReleased", X: 40, Y: 10, Button: "left", ClickCount: 1})
	b.Mouse(ctx, Mouse{Type: "mouseMoved", X: 60, Y: 10})
	if got := title(); got != "buttons 0" {
		t.Fatalf("after: %q", got)
	}
}

// What you see while you have the browser is sharp: frames carry twice
// the page's pixels, for a Retina screen or a phone; the agent's own
// screenshots stay at the page's size, so they cost it no more.
func TestSharpFramesPlainScreenshots(t *testing.T) {
	ts := site(t)
	b := start(t, Options{})
	ctx := ctxT(t)
	b.Open(ctx, ts.URL+"/")
	shot, err := b.Screenshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Result struct{ Value float64 } `json:"result"`
	}
	b.call(ctx, b.page, "Runtime.evaluate", map[string]any{"expression": "innerWidth", "returnByValue": true}, &out)
	if cfg, err := png.DecodeConfig(bytes.NewReader(shot)); err != nil || float64(cfg.Width) != out.Result.Value {
		t.Errorf("screenshot %d wide, page %v: %v", cfg.Width, out.Result.Value, err)
	}
	// And again after a take-over, a phone's included, is handed back.
	for i, fit := range []bool{false, true, false} {
		frames, err := b.Take(ctx, "zane@example.com")
		if err != nil {
			t.Fatal(err)
		}
		if fit {
			b.Fit(ctx, 390, 700)
		}
		b.Go(ctx, ts.URL+"/next")
		sharp := false
		deadline := time.After(10 * time.Second)
		for !sharp {
			select {
			case f := <-frames:
				cfg, err := jpeg.DecodeConfig(bytes.NewReader(f.JPEG))
				sharp = err == nil && cfg.Width == 2*f.Width && (!fit || f.Width == 390)
			case <-deadline:
				t.Fatalf("take-over %d: no frame at twice the page's pixels", i)
			}
		}
		b.Release(ctx)
	}
}

// Chromium keeps its frames out of /dev/shm, which a container gets 64 MB
// of: a few sharp frames would fill it and hang the browser.
func TestLaunchKeepsOutOfShm(t *testing.T) {
	bin := chromium(t)
	dir := t.TempDir()
	var got []string
	inner := localLaunch(bin)
	o := Options{Profile: filepath.Join(dir, "p"), Downloads: filepath.Join(dir, "d"), Launch: func(ctx context.Context, args []string) (io.ReadCloser, io.WriteCloser, func(), error) {
		got = args
		return inner(ctx, args)
	}}
	b, err := Start(ctxT(t), o)
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	for _, want := range []string{"--disable-dev-shm-usage", "--force-device-scale-factor=2"} {
		if !slices.Contains(got, want) {
			t.Errorf("launched without %s: %v", want, got)
		}
	}
}

// Sites (Google's sign-in first) refuse a browser that says it's headless
// or automated: it reads as an ordinary Chrome, in its pages and requests.
func TestLooksLikeChrome(t *testing.T) {
	var hdr http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			hdr = r.Header.Clone()
		}
		fmt.Fprint(w, `<!doctype html><title>Hello</title>`)
	}))
	defer ts.Close()
	b := start(t, Options{})
	ctx := ctxT(t)
	if err := b.Open(ctx, ts.URL+"/"); err != nil {
		t.Fatal(err)
	}
	js := func(e string) any {
		var out struct {
			Result struct{ Value any } `json:"result"`
		}
		b.call(ctx, b.page, "Runtime.evaluate", map[string]any{"expression": e, "returnByValue": true}, &out)
		return out.Result.Value
	}
	ua, _ := js("navigator.userAgent").(string)
	brands, _ := js("JSON.stringify(navigator.userAgentData.brands)").(string)
	for what, v := range map[string]string{"navigator.userAgent": ua, "User-Agent": hdr.Get("User-Agent"), "brands": brands, "Sec-CH-UA": hdr.Get("Sec-Ch-Ua")} {
		if strings.Contains(v, "Headless") || !strings.Contains(v, "Chrom") {
			t.Errorf("%s = %q", what, v)
		}
	}
	// A screen at least as big as the window, as on any real machine.
	if ok := js("screen.width >= outerWidth && screen.height >= outerHeight && screen.width >= 1280"); ok != true {
		t.Errorf("screen %v around a window of %v", js("screen.width+'x'+screen.height"), js("outerWidth+'x'+outerHeight"))
	}
	if w := js("navigator.webdriver"); w != false {
		t.Errorf("navigator.webdriver = %v", w)
	}
	if l := js("navigator.language"); l != "en-US" {
		t.Errorf("navigator.language = %v", l)
	}
	if l := hdr.Get("Accept-Language"); !strings.HasPrefix(l, "en-US") {
		t.Errorf("Accept-Language = %q", l)
	}
}
