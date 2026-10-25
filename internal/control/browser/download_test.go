package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const episodeSize = 1 << 20

// episodes is a site whose episode comes slowly: a first part, then the
// rest once release is closed. The popup link opens it in a new tab.
func episodes(t *testing.T, release chan struct{}) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<!doctype html><title>Studio</title>
<a href="/episode" download>Download episode</a>
<a href="#" onclick="window.open('/quick'); return false">Download in a tab</a>`)
		case "/quick":
			w.Header().Set("Content-Disposition", `attachment; filename="quick.wav"`)
			w.Write([]byte("RIFF quick"))
		case "/episode":
			w.Header().Set("Content-Disposition", `attachment; filename="episode.wav"`)
			w.Header().Set("Content-Length", fmt.Sprint(episodeSize))
			w.Write(make([]byte, 64<<10))
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Write(make([]byte, episodeSize-64<<10))
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// waitDownload waits until the download is no longer in progress.
func waitDownload(t *testing.T, b *Browser, guid string) Downloaded {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, d := range b.Downloads() {
			if d.GUID == guid && d.State != "inProgress" {
				return d
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("download %s still going: %+v", guid, b.Downloads())
	return Downloaded{}
}

func TestSlowDownloadCarriesOn(t *testing.T) {
	release := make(chan struct{})
	ts := episodes(t, release)
	b := start(t, Options{})
	ctx := ctxT(t)
	b.Open(ctx, ts.URL+"/")
	p, _ := b.Read(ctx)
	link := ref(t, p, "Download episode")
	d, err := b.Download(ctx, link, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != "inProgress" || d.Name != "episode.wav" || d.Received == 0 || d.Total != episodeSize {
		t.Fatalf("a slow download came back as %+v", d)
	}
	// Asking again while it goes would start a second copy of it.
	if _, err := b.Download(ctx, link, time.Second); !errors.Is(err, ErrDownloading) {
		t.Fatalf("second download: %v", err)
	}
	close(release)
	got := waitDownload(t, b, d.GUID)
	if got.State != "completed" {
		t.Fatalf("download ended %+v", got)
	}
	if fi, err := os.Stat(got.Path); err != nil || fi.Size() != episodeSize {
		t.Fatalf("file %v %v", fi, err)
	}
	b.Forget(got.GUID)
	if n := len(b.Downloads()); n != 0 {
		t.Fatalf("%d downloads after forgetting", n)
	}
}

func TestStalledDownloadIsCanceled(t *testing.T) {
	old := downloadStall
	downloadStall = time.Second
	t.Cleanup(func() { downloadStall = old })
	ts := episodes(t, make(chan struct{}))
	b := start(t, Options{})
	ctx := ctxT(t)
	b.Open(ctx, ts.URL+"/")
	p, _ := b.Read(ctx)
	d, err := b.Download(ctx, ref(t, p, "Download episode"), 500*time.Millisecond)
	if err != nil || d.State != "inProgress" {
		t.Fatalf("download %+v %v", d, err)
	}
	got := waitDownload(t, b, d.GUID)
	if got.State != "canceled" {
		t.Fatalf("stalled download ended %+v", got)
	}
	for i := 0; i < 50 && fileExists(got.Path); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(got.Path); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(b.opts.Downloads)
		t.Fatalf("a canceled download left its file: %v %v", err, entries)
	}
	// A new one may start once the stalled one is gone.
	if _, err := b.Download(ctx, ref(t, p, "Download episode"), 500*time.Millisecond); errors.Is(err, ErrDownloading) {
		t.Fatal("a canceled download still holds the next one up")
	}
}

// A site that opens its file in a new tab still downloads it.
func TestDownloadInANewTab(t *testing.T) {
	ts := episodes(t, make(chan struct{}))
	b := start(t, Options{})
	ctx := ctxT(t)
	b.Open(ctx, ts.URL+"/")
	p, _ := b.Read(ctx)
	d, err := b.Download(ctx, ref(t, p, "Download in a tab"), 10*time.Second)
	if err != nil || d.State != "completed" || d.Name != "quick.wav" {
		t.Fatalf("download %+v %v", d, err)
	}
	if got, _ := os.ReadFile(d.Path); string(got) != "RIFF quick" {
		t.Fatalf("file %q", got)
	}
}

func TestCrashedPageClosesTheBrowser(t *testing.T) {
	b := start(t, Options{})
	ctx := ctxT(t)
	// The renderer dies without answering.
	go b.call(ctx, b.page, "Page.crash", nil, nil)
	select {
	case <-b.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("the browser outlived its crashed page")
	}
}

func TestHungPageIsNotAlive(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Spin</title><script>setTimeout(() => { for (;;); }, 200)</script>`)
	}))
	t.Cleanup(ts.Close)
	b := start(t, Options{})
	ctx := ctxT(t)
	if err := b.Alive(ctx); err != nil {
		t.Fatalf("a blank page isn't alive: %v", err)
	}
	b.Open(ctx, ts.URL+"/")
	time.Sleep(500 * time.Millisecond)
	actx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := b.Alive(actx); err == nil {
		t.Fatal("a page stuck in a loop is alive")
	} else if !strings.Contains(err.Error(), "answer") {
		t.Fatalf("error %v", err)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
