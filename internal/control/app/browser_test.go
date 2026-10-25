package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
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

	"github.com/zaneGittins/skybreaker/internal/control/browser"
)

// withChromium has the fixture's assistants run a local Chromium, from
// $SKYBREAKER_CHROMIUM, in place of one in a container.
func withChromium(t *testing.T, f *fixture) {
	t.Helper()
	bin := os.Getenv("SKYBREAKER_CHROMIUM")
	if bin == "" {
		t.Skip("no Chromium: set SKYBREAKER_CHROMIUM")
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	f.broker.ca = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	dir := t.TempDir()
	f.app.browserOpts = func(o *browser.Options) {
		o.Proxy, o.CASPKI = "", ""
		o.Profile, o.Downloads = filepath.Join(dir, "profile"), filepath.Join(dir, "downloads")
		o.Launch = func(ctx context.Context, args []string) (io.ReadCloser, io.WriteCloser, func(), error) {
			inR, inW, _ := os.Pipe()
			outR, outW, _ := os.Pipe()
			cmd := exec.Command(bin, args...)
			cmd.ExtraFiles = []*os.File{inR, outW}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				return nil, nil, nil, err
			}
			inR.Close()
			outW.Close()
			return outR, inW, func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Wait() }, nil
		}
	}
	t.Cleanup(func() { f.app.StopBrowser("pip") })
}

// browserCalls are the docker calls on pip's browser container, as
// "run" and "rm".
func browserCalls(f *fixture) []string {
	f.docker.mu.Lock()
	defer f.docker.mu.Unlock()
	var out []string
	for _, c := range f.docker.calls {
		if (c[1] == "run" || c[1] == "rm") && slices.Contains(c, "skybreaker-pip-browser") {
			out = append(out, c[1])
		}
	}
	return out
}

// A browser stuck on a page that never answers is replaced on its next
// use, and the old one's cleanup leaves the new one's container be.
func TestStuckBrowserIsReplaced(t *testing.T) {
	f := newAssistantFixture(t)
	withChromium(t, f)
	old := browserAnswer
	browserAnswer = 2 * time.Second
	t.Cleanup(func() { browserAnswer = old })
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Spin</title><script>setTimeout(() => { for (;;); }, 200)</script>`)
	}))
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	b1, err := f.app.Browser(ctx, "pip")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := f.app.Browser(ctx, "pip"); again != b1 {
		t.Fatal("a working browser was replaced")
	}
	b1.Open(ctx, ts.URL+"/")
	time.Sleep(500 * time.Millisecond)
	b2, err := f.app.Browser(ctx, "pip")
	if err != nil {
		t.Fatal(err)
	}
	if b2 == b1 {
		t.Fatal("a stuck browser was handed out again")
	}
	select {
	case <-b1.Done():
	default:
		t.Fatal("the stuck browser still runs")
	}
	if err := b2.Alive(ctx); err != nil {
		t.Fatalf("the new browser: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if calls := browserCalls(f); calls[len(calls)-1] != "run" {
		t.Fatalf("the new browser's container was removed: %v", calls)
	}
}

// Stopping a browser, as restarting one does, waits until it has gone.
func TestStopBrowserWaits(t *testing.T) {
	f := newAssistantFixture(t)
	withChromium(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	b1, err := f.app.Browser(ctx, "pip")
	if err != nil {
		t.Fatal(err)
	}
	f.app.StopBrowser("pip")
	select {
	case <-b1.Done():
	default:
		t.Fatal("stop left the browser running")
	}
	if _, ok := f.app.BrowserStatus(ctx, "pip"); ok {
		t.Fatal("a stopped browser is still listed")
	}
	if calls := browserCalls(f); !strings.HasSuffix(strings.Join(calls, " "), "rm") {
		t.Fatalf("stop left the container: %v", calls)
	}
}
