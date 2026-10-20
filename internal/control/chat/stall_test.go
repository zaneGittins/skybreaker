package chat

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// A harness that never reads its input but floods requests the driver
// answers must not hold its run's lock forever: Reap would then hold the
// manager's, and every other chat would stall behind it.
func TestHarnessThatWontReadCantStallOthers(t *testing.T) {
	st, _ := store.Open("")
	log, _ := OpenLog("")
	start := func(ctx context.Context, s store.Session, argv []string, env map[string]string) (*Proc, error) {
		_, inW := io.Pipe() // nobody reads stdin
		outR, outW := io.Pipe()
		go func() {
			for {
				if _, err := outW.Write([]byte(`{"type":"control_request","request_id":"x","request":{"subtype":"nope"}}` + "\n")); err != nil {
					return
				}
			}
		}()
		return &Proc{Stdin: inW, Stdout: outR, Wait: func() error { return nil }, Kill: func() { outW.Close() }}, nil
	}
	m := NewManager(Config{Log: log, Sessions: st, Start: start})
	for _, id := range []string{"shostile", "sother"} {
		st.CreateSession(store.Session{ID: id, Project: "p", Harness: "claude", Mode: store.ModeChat, Status: store.StatusRunning, Created: time.Now()})
	}
	if err := m.Send(context.Background(), "shostile", "hi"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second) // let r.in (4096) fill
	go m.Reap(time.Now().Add(time.Hour))
	time.Sleep(200 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		m.Note("sother", "hello") // another session entirely
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("another session's call blocks")
	}
}
