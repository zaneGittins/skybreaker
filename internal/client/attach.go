package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/zaneGittins/skybreaker/internal/wsock"
)

// AttachIO is a terminal: keystrokes in, screen out, and its size.
type AttachIO struct {
	In     <-chan []byte
	Out    io.Writer
	Size   func() (rows, cols int)
	Resize <-chan struct{} // the size changed
}

// escaper watches what you type for ~. at the start of a line, ssh's way
// out: tmux's detach key belongs to the sandbox, which can rebind it, so
// this one is kept on your side. ~~ sends a single ~.
type escaper struct{ midLine, tilde bool }

func (e *escaper) feed(in []byte) (out []byte, detach bool) {
	for _, b := range in {
		if e.tilde {
			e.tilde = false
			switch b {
			case '.':
				return out, true
			case '~':
				out = append(out, '~')
				e.midLine = true
				continue
			}
			out = append(out, '~')
		}
		if b == '~' && !e.midLine {
			e.tilde = true
			continue
		}
		out = append(out, b)
		e.midLine = b != '\r' && b != '\n'
	}
	return out, false
}

// pingEvery keeps the server's idle timer from dropping a quiet viewer.
var pingEvery = 25 * time.Second

// Attach relays a session's terminal until it ends, you detach, or ctx is
// done. Everything the sandbox sends passes through a Filter first.
func (c *Client) Attach(ctx context.Context, id string, t AttachIO) error {
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	ws, err := wsock.Dial(dctx, c.wsEndpoint("api", "sessions", id, "attach"), http.Header{"Origin": {c.origin()}}, c.TLS)
	cancel()
	if err != nil {
		return err
	}
	defer ws.Close(1000, "")
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	resize := func() error {
		rows, cols := t.Size()
		rows, cols = min(max(rows, 1), 1000), min(max(cols, 1), 1000)
		return ws.Write(ctx, false, fmt.Appendf(nil, `{"type":"resize","rows":%d,"cols":%d}`, rows, cols))
	}
	if err := resize(); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		var f Filter
		for {
			binary, b, err := ws.Read(ctx)
			if err != nil {
				done <- err
				return
			}
			if !binary {
				continue
			}
			if out := f.Write(nil, b); len(out) > 0 {
				if _, err := t.Out.Write(out); err != nil {
					done <- err
					return
				}
			}
		}
	}()

	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	var way escaper
	for {
		select {
		case err := <-done:
			if errors.Is(err, io.EOF) {
				return nil // the session ended or you detached
			}
			return err
		case b, ok := <-t.In:
			if !ok {
				return nil
			}
			b, leave := way.feed(b)
			if len(b) > 0 {
				if err := ws.Write(ctx, true, b); err != nil {
					return err
				}
			}
			if leave {
				return nil
			}
		case <-t.Resize:
			if err := resize(); err != nil {
				return err
			}
		case <-ping.C:
			if err := ws.Write(ctx, false, []byte(`{"type":"ping"}`)); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
