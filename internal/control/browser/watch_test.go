package browser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// texts is the JSON messages a fake view got, decoded.
func (f *fakeWS) texts() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, s := range f.text {
		var m map[string]any
		json.Unmarshal([]byte(s), &m)
		out = append(out, m)
	}
	return out
}

// waitFor waits until a message matching ok has come.
func (f *fakeWS) waitFor(t *testing.T, what string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range f.texts() {
			if ok(m) {
				return m
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no %s in %v", what, f.texts())
	return nil
}

func (f *fakeWS) waitFrames(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for f.frames() < n && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if f.frames() < n {
		t.Fatalf("%d frames, want %d", f.frames(), n)
	}
}

func cue(act string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["t"] == "cue" && m["act"] == act }
}

// Watching shows the page and where the agent acts on it, without pausing
// the agent the way a take-over does.
func TestWatchShowsTheAgent(t *testing.T) {
	ts := site(t)
	b := start(t, Options{})
	ctx := ctxT(t)
	b.Open(ctx, ts.URL+"/")
	ws := &fakeWS{in: make(chan []byte)}
	wctx, stop := context.WithCancel(ctx)
	ended := make(chan struct{})
	go func() { LiveWatch(wctx, ws, func() (*Browser, bool) { return b, true }); close(ended) }()
	ws.waitFrames(t, 1)
	ws.waitFor(t, "running state", func(m map[string]any) bool {
		return m["t"] == "state" && m["running"] == true && m["taken"] == false && m["site"] == "127.0.0.1"
	})
	p, err := b.Read(ctx)
	if err != nil {
		t.Fatalf("agent paused by a watcher: %v", err)
	}
	if err := b.Click(ctx, ref(t, p, "Next page")); err != nil {
		t.Fatal(err)
	}
	c := ws.waitFor(t, "click cue", cue("click"))
	if x, _ := c["x"].(float64); x <= 0 {
		t.Errorf("click cue without a place: %v", c)
	}
	ws.waitFor(t, "page cue", func(m map[string]any) bool { return cue("page")(m) && m["site"] == "127.0.0.1" })
	if err := b.Scroll(ctx, true); err != nil {
		t.Fatal(err)
	}
	ws.waitFor(t, "scroll cue", cue("scroll"))

	b.Take(ctx, "zane@example.com")
	ws.waitFor(t, "taken cue", cue("taken"))
	b.Release(ctx)
	ws.waitFor(t, "free cue", cue("free"))

	stop()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("watch didn't end with its context")
	}
	deadline := time.Now().Add(5 * time.Second)
	for b.watching() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := b.watching(); n != 0 {
		t.Fatalf("%d watchers left after the view went", n)
	}
}

// Several can watch at once, through a take-over and after it.
func TestWatchTwoAtOnce(t *testing.T) {
	ts := site(t)
	b := start(t, Options{})
	ctx := ctxT(t)
	b.Open(ctx, ts.URL+"/still")
	f1, _, err := b.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f2, _, err := b.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := func(name string, ch <-chan Frame) {
		t.Helper()
		select {
		case f, ok := <-ch:
			if !ok || len(f.JPEG) == 0 {
				t.Fatalf("%s: frames ended", name)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: no frame", name)
		}
	}
	next("first", f1)
	next("second", f2)
	if _, err := b.Take(ctx, "zane@example.com"); err != nil {
		t.Fatal(err)
	}
	b.Release(ctx)
	drain := func(ch <-chan Frame) {
		for len(ch) > 0 {
			<-ch
		}
	}
	drain(f1)
	drain(f2)
	b.Open(ctx, ts.URL+"/next")
	next("first after a take-over", f1)
	next("second after a take-over", f2)
}

// A watch waits for the browser to start, and again once it has closed.
func TestWatchWaitsForABrowser(t *testing.T) {
	old := watchPoll
	watchPoll = 20 * time.Millisecond
	t.Cleanup(func() { watchPoll = old })
	b := start(t, Options{})
	ctx := ctxT(t)
	ws := &fakeWS{in: make(chan []byte)}
	found := make(chan struct{})
	tries := 0
	find := func() (*Browser, bool) {
		select {
		case <-found:
			return b, !strings.Contains(statusOf(b), "closed")
		default:
		}
		tries++
		if tries == 3 {
			close(found)
		}
		return nil, false
	}
	wctx, stop := context.WithCancel(ctx)
	ended := make(chan struct{})
	go func() { LiveWatch(wctx, ws, find); close(ended) }()
	t.Cleanup(func() { stop(); <-ended }) // before watchPoll is put back
	ws.waitFor(t, "not running", func(m map[string]any) bool { return m["t"] == "state" && m["running"] == false })
	ws.waitFrames(t, 1)
	n := len(ws.texts())
	b.Close()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ts := ws.texts()
		if len(ts) > n && ts[len(ts)-1]["t"] == "state" && ts[len(ts)-1]["running"] == false {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("a closed browser wasn't said: %v", ws.texts())
}

func statusOf(b *Browser) string {
	select {
	case <-b.Done():
		return "closed"
	default:
		return "open"
	}
}
