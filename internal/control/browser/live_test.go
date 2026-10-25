package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeWS is the user's end of the live view.
type fakeWS struct {
	in   chan []byte // what the user sends
	mu   sync.Mutex
	bins [][]byte
	text []string
}

func (f *fakeWS) Read(ctx context.Context) (bool, []byte, error) {
	select {
	case b, ok := <-f.in:
		if !ok {
			return false, nil, errors.New("closed")
		}
		return false, b, nil
	case <-ctx.Done():
		return false, nil, ctx.Err()
	}
}

func (f *fakeWS) Write(_ context.Context, binary bool, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if binary {
		f.bins = append(f.bins, data)
	} else {
		f.text = append(f.text, string(data))
	}
	return nil
}

func (f *fakeWS) Close(int, string) error { return nil }

func (f *fakeWS) frames() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bins)
}

func TestLive(t *testing.T) {
	ts := site(t)
	b := start(t, Options{})
	ctx := ctxT(t)
	b.Open(ctx, ts.URL+"/")
	ws := &fakeWS{in: make(chan []byte, 8)}
	type ended struct {
		done bool
		err  error
	}
	done := make(chan ended, 1)
	go func() { d, err := Live(ctx, ws, b, "zane@example.com"); done <- ended{d, err} }()
	deadline := time.Now().Add(10 * time.Second)
	for ws.frames() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if ws.frames() == 0 || !bytes.HasPrefix(ws.bins[0], []byte{0xff, 0xd8}) {
		t.Fatal("no JPEG frames")
	}
	if _, err := b.Read(ctx); !errors.Is(err, ErrPaused) {
		t.Errorf("agent not paused: %v", err)
	}
	msg := func(v any) { m, _ := json.Marshal(v); ws.in <- m }
	msg(map[string]any{"t": "fit", "w": 390, "h": 700})
	sized := func() bool {
		ws.mu.Lock()
		defer ws.mu.Unlock()
		for _, m := range ws.text {
			if m == `{"h":700,"t":"size","w":390}` {
				return true
			}
		}
		return false
	}
	for !sized() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !sized() {
		t.Error("fitting to a phone didn't resize the frames")
	}
	msg(map[string]any{"t": "go", "url": ts.URL + "/next"})
	msg(map[string]any{"t": "mouse", "type": "mouseMoved", "x": 10, "y": 10})
	msg(map[string]any{"t": "nonsense"})
	msg(map[string]any{"t": "done"})
	select {
	case e := <-done:
		if e.err != nil || !e.done {
			t.Fatalf("done %v, %v", e.done, e.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("live view didn't end on done")
	}
	if s, _ := b.Status(ctx); s.Title != "Next" || s.TakenBy != "" {
		t.Fatalf("after: %+v", s)
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	var sawState, sawErr bool
	for _, m := range ws.text {
		sawState = sawState || bytes.Contains([]byte(m), []byte(`"t":"state"`))
		sawErr = sawErr || bytes.Contains([]byte(m), []byte(`"t":"error"`))
	}
	if !sawState || !sawErr {
		t.Errorf("text messages %v", ws.text)
	}
}

// One person at a time: a second live view is refused, not merged.
func TestLiveIsOneAtATime(t *testing.T) {
	b := start(t, Options{})
	ctx := ctxT(t)
	b.Take(ctx, "kim@example.com")
	ws := &fakeWS{in: make(chan []byte)}
	if _, err := Live(ctx, ws, b, "zane@example.com"); !errors.Is(err, ErrTaken) {
		t.Fatalf("second live view: %v", err)
	}
}

// Going away isn't being done: a take-over that ends some other way says so,
// so what asked for it can wait on.
func TestLiveGoneIsNotDone(t *testing.T) {
	b := start(t, Options{})
	ctx := ctxT(t)
	ws := &fakeWS{in: make(chan []byte)}
	close(ws.in)
	if done, err := Live(ctx, ws, b, "zane@example.com"); done || err != nil {
		t.Fatalf("done %v, %v", done, err)
	}
}

// You again, from a reload or another device, take the browser over from
// your own old view, which ends without handing it back.
func TestLiveYouAgain(t *testing.T) {
	b := start(t, Options{})
	ctx := ctxT(t)
	old := &fakeWS{in: make(chan []byte)}
	first := make(chan bool, 1)
	go func() { done, _ := Live(ctx, old, b, "zane@example.com"); first <- done }()
	deadline := time.Now().Add(10 * time.Second)
	for s, _ := b.Status(ctx); s.TakenBy == "" && time.Now().Before(deadline); s, _ = b.Status(ctx) {
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := Live(ctx, &fakeWS{in: make(chan []byte)}, b, "kim@example.com"); !errors.Is(err, ErrTaken) {
		t.Fatalf("someone else: %v", err)
	}
	now := &fakeWS{in: make(chan []byte, 1)}
	second := make(chan error, 1)
	go func() { _, err := Live(ctx, now, b, "zane@example.com"); second <- err }()
	select {
	case done := <-first:
		if done {
			t.Error("the old view said done")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the old view held on")
	}
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-second:
		t.Fatalf("the new view ended: %v", err)
	default:
	}
	if s, _ := b.Status(ctx); s.TakenBy != "zane@example.com" {
		t.Fatalf("the old view's end handed it back: %+v", s)
	}
	m, _ := json.Marshal(map[string]any{"t": "done"})
	now.in <- m
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

// Taking over a page that's sitting still shows it at once: a sign-in form
// waiting for you doesn't repaint on its own.
func TestTakeOverAStillPage(t *testing.T) {
	ts := site(t)
	b := start(t, Options{})
	ctx := ctxT(t)
	b.Open(ctx, ts.URL+"/still")
	for i := range 3 {
		time.Sleep(time.Second)
		frames, err := b.Take(ctx, "zane@example.com")
		if err != nil {
			t.Fatal(err)
		}
		select {
		case f := <-frames:
			if f.Width == 0 || len(f.JPEG) == 0 {
				t.Fatalf("take-over %d: empty frame", i)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("take-over %d: no frame of a still page", i)
		}
		b.Release(ctx)
	}
}

// After you say done, the next take-over shows the page as the first did.
func TestTakeOverAfterDone(t *testing.T) {
	ts := site(t)
	b := start(t, Options{})
	ctx := ctxT(t)
	b.Open(ctx, ts.URL+"/next")
	for i := range 2 {
		ws := &fakeWS{in: make(chan []byte, 1)}
		ended := make(chan bool, 1)
		go func() { done, _ := Live(ctx, ws, b, "zane@example.com"); ended <- done }()
		deadline := time.Now().Add(5 * time.Second)
		for ws.frames() == 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if ws.frames() == 0 {
			t.Fatalf("take-over %d: no frame", i)
		}
		m, _ := json.Marshal(map[string]any{"t": "done"})
		ws.in <- m
		if !<-ended {
			t.Fatalf("take-over %d didn't end done", i)
		}
	}
}

// A view that falls behind skips old frames, never the newest: the page as
// it is now always arrives, though some on the way may not.
func TestOfferKeepsTheNewest(t *testing.T) {
	ch := make(chan Frame, 2)
	for w := 1; w <= 5; w++ {
		offer(ch, Frame{Width: w})
	}
	var got []int
	for len(ch) > 0 {
		got = append(got, (<-ch).Width)
	}
	if !slices.Equal(got, []int{4, 5}) {
		t.Fatalf("got %v", got)
	}
}
