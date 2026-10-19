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
			if err := ws.Write(ctx, true, b); err != nil {
				return err
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
