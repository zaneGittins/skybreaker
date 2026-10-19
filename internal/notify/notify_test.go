package notify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type hit struct {
	header http.Header
	body   string
}

func ntfyServer(t *testing.T) (*httptest.Server, func() []hit) {
	t.Helper()
	var mu sync.Mutex
	var hits []hit
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits = append(hits, hit{r.Header.Clone(), string(b)})
		mu.Unlock()
	}))
	t.Cleanup(ts.Close)
	return ts, func() []hit { mu.Lock(); defer mu.Unlock(); return append([]hit(nil), hits...) }
}

func TestNtfySend(t *testing.T) {
	ts, hits := ntfyServer(t)
	n := &Ntfy{URL: ts.URL + "/skybreaker-topic", Token: "tk"}
	err := n.Notify(context.Background(), Message{ID: "a1", Title: "Approval waiting", Body: "Open Skybreaker to review", Click: "https://broker.ts.net/#a1", Priority: 4, Tags: []string{"lock"}})
	if err != nil {
		t.Fatal(err)
	}
	h := hits()
	if len(h) != 1 {
		t.Fatalf("got %d posts", len(h))
	}
	g := h[0]
	for k, want := range map[string]string{"Title": "Approval waiting", "Click": "https://broker.ts.net/#a1", "Priority": "4", "Tags": "lock", "Authorization": "Bearer tk"} {
		if got := g.header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if g.body != "Open Skybreaker to review" {
		t.Errorf("body %q", g.body)
	}
}

func TestNtfyError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer ts.Close()
	if err := (&Ntfy{URL: ts.URL}).Notify(context.Background(), Message{Title: "x"}); err == nil {
		t.Fatal("want error on 500")
	}
}

func TestDedup(t *testing.T) {
	ts, hits := ntfyServer(t)
	now := time.Unix(1000, 0)
	d := Dedup(&Ntfy{URL: ts.URL}, 10*time.Minute)
	d.now = func() time.Time { return now }
	ctx := context.Background()
	d.Notify(ctx, Message{ID: "x", Title: "t"})
	d.Notify(ctx, Message{ID: "x", Title: "t"}) // duplicate, dropped
	d.Notify(ctx, Message{ID: "y", Title: "t"})
	d.Notify(ctx, Message{Title: "no id always sent"})
	d.Notify(ctx, Message{Title: "no id always sent"})
	now = now.Add(11 * time.Minute)
	d.Notify(ctx, Message{ID: "x", Title: "t"}) // window passed
	if n := len(hits()); n != 5 {
		t.Fatalf("sent %d, want 5", n)
	}
}

func TestNop(t *testing.T) {
	if err := (Nop{}).Notify(context.Background(), Message{Title: "t"}); err != nil {
		t.Fatal(err)
	}
}

func TestFromConfig(t *testing.T) {
	n, err := FromConfig(Config{})
	if err != nil || n != (Nop{}) {
		t.Fatalf("no URL: %#v %v, want Nop", n, err)
	}
	if _, err := FromConfig(Config{URL: "https://ntfy.example/t", TokenFile: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("a missing token file was ignored")
	}

	ts, hits := ntfyServer(t)
	tok := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tok, []byte("tk_secret\n"), 0o600)
	n, err = FromConfig(Config{URL: ts.URL + "/topic", TokenFile: tok})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		n.Notify(context.Background(), Message{ID: "same", Title: "t"})
	}
	h := hits()
	if len(h) != 1 {
		t.Fatalf("%d pushes for one ID, want 1 (deduplicated)", len(h))
	}
	if got := h[0].header.Get("Authorization"); got != "Bearer tk_secret" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestMulti(t *testing.T) {
	var a, b []string
	m := Multi{notifierFunc(func(m Message) error { a = append(a, m.Title); return nil }), notifierFunc(func(m Message) error { b = append(b, m.Title); return errors.New("down") })}
	err := m.Notify(context.Background(), Message{Title: "hi"})
	if len(a) != 1 || len(b) != 1 || err == nil {
		t.Errorf("a %v b %v err %v", a, b, err)
	}
}

type notifierFunc func(Message) error

func (f notifierFunc) Notify(_ context.Context, m Message) error { return f(m) }
