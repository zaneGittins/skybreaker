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
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
	d, err := b.Download(ctx, ref(t, p, "Get the podcast"))
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
