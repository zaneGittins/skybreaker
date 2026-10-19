package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"
)

// terminal is your terminal in raw mode. One goroutine reads keys for the
// whole run, so the picker and an attached session share them and none is
// lost between the two.
type terminal struct {
	fd     int
	state  *termState
	keys   chan []byte
	resize chan struct{}
	sig    chan os.Signal
	out    io.Writer
}

func openTerminal() (*terminal, error) {
	fd := int(os.Stdin.Fd())
	if !isTerminal(fd) || !isTerminal(int(os.Stdout.Fd())) {
		return nil, errors.New("this needs a terminal; `skybreaker ls` lists sessions without one")
	}
	st, err := makeRaw(fd)
	if err != nil {
		return nil, err
	}
	t := &terminal{fd: fd, state: st, keys: make(chan []byte, 64), resize: make(chan struct{}, 1), sig: make(chan os.Signal, 1), out: os.Stdout}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				t.keys <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				close(t.keys)
				return
			}
		}
	}()
	notifyResize(t.sig)
	go func() {
		for range t.sig {
			select {
			case t.resize <- struct{}{}:
			default:
			}
		}
	}()
	return t, nil
}

func (t *terminal) close() {
	signal.Stop(t.sig)
	restore(t.fd, t.state)
}

func (t *terminal) size() (int, int) { return termSize(int(os.Stdout.Fd())) }

// resetModes undoes what a session may have left on: mouse and focus
// reporting, bracketed paste, the kitty keyboard protocol, charsets,
// scrolling margins, keypad modes, colours and the alternate screen.
const resetModes = "\x1b[0m\x1b(B\x1b[r\x1b[?1l\x1b>\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l\x1b[?1015l" +
	"\x1b[?1004l\x1b[?2004l\x1b[<99u\x1b[>4;0m\x1b[?1049l\x1b[?25h"

// attach relays a session in the main screen, then puts your terminal back
// as it was, whatever the session switched on.
func (t *terminal) attach(ctx context.Context, c *Client, id string) error {
	fmt.Fprint(t.out, "\x1b[?25h\x1b[2J\x1b[H")
	err := c.Attach(ctx, id, AttachIO{In: t.keys, Out: t.out, Size: t.size, Resize: t.resize})
	fmt.Fprint(t.out, resetModes)
	return err
}

// drain drops keys typed as a session ended: they were meant for it, and
// mustn't pick something in the list.
func (t *terminal) drain(d time.Duration) {
	stop := time.After(d)
	for {
		select {
		case <-t.keys:
		case <-stop:
			return
		}
	}
}

// target is the session ID to attach to for an entry, starting a session
// or switching a chat to its terminal as needed.
func target(ctx context.Context, c *Client, e Entry) (string, error) {
	if s := e.Session; s != nil {
		if !s.Chat() {
			return s.ID, nil
		}
		t, err := c.ChatTerminal(ctx, s.ID)
		return t.ID, err
	}
	s, err := c.NewSession(ctx, e.Project, e.Harness)
	return s.ID, err
}

func label(e Entry) string {
	if s := e.Session; s != nil {
		return Visible(s.Project + "/" + s.Harness)
	}
	return Visible(e.Project + "/" + e.Harness)
}

// pick runs the picker until you quit, attaching to what you choose and
// coming back when you detach.
func pick(ctx context.Context, c *Client, t *terminal, user string) error {
	enter := func() { fmt.Fprint(t.out, "\x1b[?1049h\x1b[?25l") }
	leave := func() { fmt.Fprint(t.out, "\x1b[?25h\x1b[?1049l") }
	enter()
	defer leave()

	var entries []Entry
	sel, status := 0, ""
	load := func() {
		lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		in, err := c.Inbox(lctx)
		if err != nil {
			status = yellow + Visible(err.Error()) + reset
			return
		}
		// Keep the selection on the same line across a refresh.
		var was string
		if sel >= 0 && sel < len(entries) {
			was = label(entries[sel])
			if s := entries[sel].Session; s != nil {
				was = s.ID
			}
		}
		entries = Entries(in)
		// The selection stays on the same entry, or on none: never moved
		// onto another one a stray Enter would open.
		found := was == ""
		for i, e := range entries {
			if (e.Session != nil && e.Session.ID == was) || (e.Session == nil && label(e) == was) {
				sel, found = i, true
			}
		}
		if !found || sel >= len(entries) {
			sel = -1
		}
	}
	draw := func() {
		rows, cols := t.size()
		var b strings.Builder
		b.WriteString("\x1b[H\x1b[2J")
		fmt.Fprintf(&b, " %sskybreaker%s  %s%s · %s%s\r\n\r\n", bold, reset, dim, Visible(c.Host()), Visible(user), reset)
		room := max(rows-6, 1)
		start := 0
		if sel >= room {
			start = sel - room + 1
		}
		lines := Render(entries, sel, cols-2)
		if len(lines) == 0 {
			b.WriteString(" " + dim + "No projects." + reset + "\r\n")
		}
		for i := start; i < len(lines) && i < start+room; i++ {
			b.WriteString(" " + lines[i] + "\r\n")
		}
		fmt.Fprintf(&b, "\x1b[%d;1H %s\r\n %s↑↓ select · enter attach · r refresh · q quit · in a session, Ctrl-b d comes back (or ~. if it won't)%s", rows-1, status, dim, reset)
		io.WriteString(t.out, b.String())
	}
	load()
	draw()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			load()
		case <-t.resize:
		case b, ok := <-t.keys:
			if !ok {
				return nil
			}
			switch ParseKey(b) {
			case KeyUp:
				sel = max(sel-1, 0)
			case KeyDown:
				sel = min(sel+1, max(len(entries)-1, 0))
				if sel < 0 {
					sel = 0
				}
			case KeyRefresh:
				load()
			case KeyQuit:
				return nil
			case KeyEnter:
				if sel < 0 || sel >= len(entries) {
					break
				}
				e := entries[sel]
				id, err := target(ctx, c, e)
				if err != nil {
					status = yellow + Visible(err.Error()) + reset
					break
				}
				leave()
				err = t.attach(ctx, c, id)
				t.drain(400 * time.Millisecond)
				enter()
				if err != nil {
					status = yellow + Visible(err.Error()) + reset
				} else {
					status = "Back from " + label(e) + "."
				}
				load()
			}
		}
		draw()
	}
}
