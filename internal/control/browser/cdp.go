// Package browser gives an assistant a web browser whose logins it never
// holds. Chromium runs in a container of its own, speaking the DevTools
// protocol only over its stdio pipe (--remote-debugging-pipe), so nothing
// listens: the agent's sandbox has no socket to reach it by. Skybreaker
// drives it here and offers the agent a few high-level actions; never
// script, cookies or the protocol itself.
package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// ErrClosed is a call to a browser that has gone.
var ErrClosed = errors.New("the browser has closed")

// Event is a DevTools event, in the target session it came from.
type Event struct {
	Method  string
	Session string
	Params  json.RawMessage
}

// Conn is one DevTools pipe: NUL-terminated JSON messages each way.
type Conn struct {
	w io.WriteCloser

	mu      sync.Mutex
	next    int64
	waiting map[int64]chan reply
	closed  bool
	wmu     sync.Mutex

	events chan Event
	done   chan struct{}
}

type reply struct {
	result json.RawMessage
	err    error
}

// maxMessage bounds one message from the browser; a screenshot frame is
// the largest.
const maxMessage = 32 << 20

// NewConn reads from r and writes to w until either ends.
func NewConn(r io.Reader, w io.WriteCloser) *Conn {
	c := &Conn{w: w, waiting: map[int64]chan reply{}, events: make(chan Event, 256), done: make(chan struct{})}
	go c.read(r)
	return c
}

// Events are the browser's events. A slow reader loses them rather than
// stalling the pipe.
func (c *Conn) Events() <-chan Event { return c.events }

// Done is closed once the browser has gone.
func (c *Conn) Done() <-chan struct{} { return c.done }

func (c *Conn) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	defer c.Close()
	for {
		b, err := readMessage(br)
		if err != nil {
			return
		}
		var m struct {
			ID      int64           `json:"id"`
			Method  string          `json:"method"`
			Session string          `json:"sessionId"`
			Params  json.RawMessage `json:"params"`
			Result  json.RawMessage `json:"result"`
			Error   *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if m.Method != "" {
			e := Event{Method: m.Method, Session: m.Session, Params: m.Params}
			// A held request or target waits on its answer: never lose
			// one. Anything else may go when the reader is behind.
			if strings.HasPrefix(m.Method, "Fetch.") || strings.HasPrefix(m.Method, "Target.") {
				select {
				case c.events <- e:
				case <-c.done:
					return
				}
				continue
			}
			select {
			case c.events <- e:
			default:
			}
			continue
		}
		c.mu.Lock()
		ch := c.waiting[m.ID]
		delete(c.waiting, m.ID)
		c.mu.Unlock()
		if ch == nil {
			continue
		}
		if m.Error != nil {
			ch <- reply{err: fmt.Errorf("browser: %s", m.Error.Message)}
		} else {
			ch <- reply{result: m.Result}
		}
	}
}

func readMessage(br *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		chunk, err := br.ReadSlice(0)
		out = append(out, chunk...)
		if len(out) > maxMessage {
			return nil, errors.New("browser: message too large")
		}
		switch {
		case err == nil:
			return out[:len(out)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return nil, err
		}
	}
}

// Call sends method to a target session ("" for the browser itself) and
// waits for its result.
func (c *Conn) Call(ctx context.Context, session, method string, params any) (json.RawMessage, error) {
	msg := map[string]any{"method": method}
	if params != nil {
		msg["params"] = params
	}
	if session != "" {
		msg["sessionId"] = session
	}
	ch := make(chan reply, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	c.next++
	id := c.next
	msg["id"] = id
	c.waiting[id] = ch
	c.mu.Unlock()
	b, err := json.Marshal(msg)
	if err != nil {
		c.forget(id)
		return nil, err
	}
	c.wmu.Lock()
	_, err = c.w.Write(append(b, 0))
	c.wmu.Unlock()
	if err != nil {
		c.forget(id)
		return nil, ErrClosed
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		c.forget(id)
		return nil, ctx.Err()
	case <-c.done:
		return nil, ErrClosed
	}
}

func (c *Conn) forget(id int64) {
	c.mu.Lock()
	delete(c.waiting, id)
	c.mu.Unlock()
}

// Close ends the connection; calls waiting on it fail.
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	close(c.done)
	return c.w.Close()
}
