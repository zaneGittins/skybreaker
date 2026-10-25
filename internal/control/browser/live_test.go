package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	done := make(chan error, 1)
	go func() { done <- Live(ctx, ws, b, "zane@example.com") }()
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
	case err := <-done:
		if err != nil {
			t.Fatal(err)
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
	if err := Live(ctx, ws, b, "zane@example.com"); !errors.Is(err, ErrTaken) {
		t.Fatalf("second live view: %v", err)
	}
}
