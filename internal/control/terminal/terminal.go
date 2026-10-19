// Package terminal relays a browser terminal (xterm.js over a WebSocket) to
// a PTY running `tmux attach` inside a sandbox. Every viewer gets its own
// PTY; tmux handles several viewers, redraws on reconnect and scrollback.
//
// Wire protocol: binary frames are terminal bytes in both directions. Text
// frames from the browser are JSON control messages; the first must be
// {"type":"resize","rows":R,"cols":C} so the PTY starts at the right size.
package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Conn is a WebSocket connection; an adapter wraps the real library.
type Conn interface {
	Read(ctx context.Context) (binary bool, data []byte, err error)
	Write(ctx context.Context, binary bool, data []byte) error
	Close(code int, reason string) error
}

type PTY interface {
	io.ReadWriteCloser
	Resize(rows, cols uint16) error
}

// Starter runs argv under a new PTY of the given size.
type Starter func(ctx context.Context, argv []string, rows, cols uint16) (PTY, error)

const (
	CloseNormal = 1000
	CloseError  = 1011
)

// idleTimeout drops a viewer that has sent nothing, not even the UI's
// 25-second ping, so a phone that vanished doesn't hold a PTY, a docker
// exec and a tmux client forever.
var idleTimeout = 90 * time.Second

type control struct {
	Type string `json:"type"`
	Rows int    `json:"rows"`
	Cols int    `json:"cols"`
}

func parseControl(b []byte) (control, error) {
	var c control
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("terminal: bad control message: %w", err)
	}
	if c.Type == "resize" && (c.Rows < 1 || c.Rows > 1000 || c.Cols < 1 || c.Cols > 1000) {
		return c, fmt.Errorf("terminal: size %dx%d out of range", c.Rows, c.Cols)
	}
	return c, nil
}

// Bridge runs until the browser or the PTY goes away.
func Bridge(ctx context.Context, conn Conn, start Starter, argv []string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	first, cerr := func() (control, error) {
		rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
		defer rcancel()
		binary, b, err := conn.Read(rctx)
		if err != nil {
			return control{}, err
		}
		if binary {
			return control{}, errors.New("terminal: first message must be a resize")
		}
		c, err := parseControl(b)
		if err == nil && c.Type != "resize" {
			err = errors.New("terminal: first message must be a resize")
		}
		return c, err
	}()
	if cerr != nil {
		conn.Close(CloseError, "expected the terminal size first")
		return cerr
	}
	pty, err := start(ctx, argv, uint16(first.Rows), uint16(first.Cols))
	if err != nil {
		conn.Close(CloseError, "could not attach")
		return err
	}
	defer pty.Close()

	// PTY → browser.
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		buf := make([]byte, 32<<10)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, true, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Browser → PTY.
	inDone := make(chan error, 1)
	go func() {
		for {
			rctx, rcancel := context.WithTimeout(ctx, idleTimeout)
			binary, b, err := conn.Read(rctx)
			rcancel()
			if err != nil {
				inDone <- err
				return
			}
			if binary {
				if _, err := pty.Write(b); err != nil {
					inDone <- err
					return
				}
				continue
			}
			c, err := parseControl(b)
			if err != nil {
				continue
			}
			if c.Type == "resize" {
				pty.Resize(uint16(c.Rows), uint16(c.Cols))
			}
		}
	}()

	select {
	case <-outDone:
		// tmux detached or the session ended.
		conn.Close(CloseNormal, "session ended")
		return nil
	case err := <-inDone:
		// The viewer left or went quiet: closing the PTY ends `docker
		// exec`, which only detaches this tmux client.
		pty.Close()
		conn.Close(CloseNormal, "viewer gone")
		<-outDone
		if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
}
