package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeChrome answers CDP over a pipe the way Chromium's
// --remote-debugging-pipe does: NUL-terminated JSON each way.
type fakeChrome struct {
	in     *bufio.Reader // what the driver sent
	out    io.Writer     // what the driver reads
	closed chan struct{}
}

func newPair(t *testing.T) (*Conn, *fakeChrome) {
	t.Helper()
	toChrome, driverOut := io.Pipe()
	driverIn, fromChrome := io.Pipe()
	c := NewConn(driverIn, driverOut)
	t.Cleanup(func() { c.Close(); toChrome.Close(); fromChrome.Close() })
	return c, &fakeChrome{in: bufio.NewReader(toChrome), out: fromChrome}
}

// next is the driver's next message, or nil once the pipe is gone. Safe
// from any goroutine: tests check what it returns.
func (f *fakeChrome) next() map[string]any {
	b, err := f.in.ReadBytes(0)
	if err != nil {
		return nil
	}
	var m map[string]any
	json.Unmarshal(b[:len(b)-1], &m)
	return m
}

func (f *fakeChrome) send(v any) {
	b, _ := json.Marshal(v)
	f.out.Write(append(b, 0))
}

func TestCallAndEvents(t *testing.T) {
	c, chrome := newPair(t)
	events := c.Events()
	type res struct {
		v   json.RawMessage
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := c.Call(context.Background(), "s1", "Page.navigate", map[string]string{"url": "https://example.com"})
		done <- res{v, err}
	}()
	m := chrome.next()
	if m["method"] != "Page.navigate" || m["sessionId"] != "s1" || m["params"].(map[string]any)["url"] != "https://example.com" {
		t.Fatalf("sent %v", m)
	}
	// An event arrives before the answer.
	chrome.send(map[string]any{"method": "Page.loadEventFired", "sessionId": "s1", "params": map[string]any{"timestamp": 1}})
	chrome.send(map[string]any{"id": m["id"], "result": map[string]any{"frameId": "F1"}})
	r := <-done
	if r.err != nil || !strings.Contains(string(r.v), "F1") {
		t.Fatalf("result %s %v", r.v, r.err)
	}
	select {
	case e := <-events:
		if e.Method != "Page.loadEventFired" || e.Session != "s1" {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
}

func TestCallError(t *testing.T) {
	c, chrome := newPair(t)
	go func() {
		m := chrome.next()
		chrome.send(map[string]any{"id": m["id"], "error": map[string]any{"code": -32000, "message": "Cannot navigate to invalid URL"}})
	}()
	if _, err := c.Call(context.Background(), "", "Page.navigate", nil); err == nil || !strings.Contains(err.Error(), "invalid URL") {
		t.Fatalf("err %v", err)
	}
}

// A call waiting when Chromium goes away fails rather than hanging.
func TestCallEndsWhenTheBrowserDoes(t *testing.T) {
	c, chrome := newPair(t)
	go func() {
		chrome.next()
		chrome.out.(io.Closer).Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Call(ctx, "", "Browser.getVersion", nil); err == nil || ctx.Err() != nil {
		t.Fatalf("err %v ctx %v", err, ctx.Err())
	}
	if _, err := c.Call(ctx, "", "Browser.getVersion", nil); err == nil {
		t.Fatal("called a closed browser")
	}
}

func TestCallTimesOut(t *testing.T) {
	c, chrome := newPair(t)
	go chrome.next()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Call(ctx, "", "Page.navigate", nil); err == nil {
		t.Fatal("no timeout")
	}
}
