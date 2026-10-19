package terminal

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"
)

type msg struct {
	binary bool
	data   []byte
}

// fakeConn is one end of a WebSocket: the test pushes client messages into
// in and reads what the bridge wrote from out.
type fakeConn struct {
	in     chan msg
	out    chan msg
	closed chan string
	once   sync.Once
}

func newConn() *fakeConn {
	return &fakeConn{in: make(chan msg, 16), out: make(chan msg, 64), closed: make(chan string, 1)}
}

func (c *fakeConn) Read(ctx context.Context) (bool, []byte, error) {
	select {
	case m, ok := <-c.in:
		if !ok {
			return false, nil, io.EOF
		}
		return m.binary, m.data, nil
	case <-ctx.Done():
		return false, nil, ctx.Err()
	}
}

func (c *fakeConn) Write(ctx context.Context, binary bool, data []byte) error {
	c.out <- msg{binary, append([]byte(nil), data...)}
	return nil
}

func (c *fakeConn) Close(code int, reason string) error {
	c.once.Do(func() { c.closed <- reason })
	return nil
}

// fakePTY echoes nothing by itself: the test writes "screen output" into
// screen and reads keystrokes from keys.
type fakePTY struct {
	screenR *io.PipeReader
	screenW *io.PipeWriter
	keysR   *io.PipeReader
	keysW   *io.PipeWriter
	mu      sync.Mutex
	sizes   [][2]uint16
	done    chan struct{}
	once    sync.Once
}

func newPTY() *fakePTY {
	p := &fakePTY{done: make(chan struct{})}
	p.screenR, p.screenW = io.Pipe()
	p.keysR, p.keysW = io.Pipe()
	return p
}

func (p *fakePTY) Read(b []byte) (int, error)  { return p.screenR.Read(b) }
func (p *fakePTY) Write(b []byte) (int, error) { return p.keysW.Write(b) }
func (p *fakePTY) Close() error {
	p.once.Do(func() { close(p.done); p.screenW.Close(); p.keysW.Close() })
	return nil
}
func (p *fakePTY) Resize(rows, cols uint16) error {
	p.mu.Lock()
	p.sizes = append(p.sizes, [2]uint16{rows, cols})
	p.mu.Unlock()
	return nil
}

func TestBridge(t *testing.T) {
	conn, pty := newConn(), newPTY()
	var gotArgv []string
	var gotSize [2]uint16
	start := func(_ context.Context, argv []string, rows, cols uint16) (PTY, error) {
		gotArgv, gotSize = argv, [2]uint16{rows, cols}
		return pty, nil
	}
	done := make(chan error, 1)
	go func() {
		done <- Bridge(context.Background(), conn, start, []string{"docker", "exec", "-it", "x", "tmux", "attach"})
	}()

	conn.in <- msg{false, []byte(`{"type":"resize","rows":40,"cols":120}`)}
	// Screen output reaches the browser as binary frames.
	go pty.screenW.Write([]byte("hello"))
	select {
	case m := <-conn.out:
		if !m.binary || string(m.data) != "hello" {
			t.Fatalf("out = %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no output")
	}
	if gotSize != [2]uint16{40, 120} || !slices.Equal(gotArgv, []string{"docker", "exec", "-it", "x", "tmux", "attach"}) {
		t.Fatalf("started with %v %v", gotArgv, gotSize)
	}
	// Keystrokes reach the PTY.
	conn.in <- msg{true, []byte("ls\r")}
	buf := make([]byte, 16)
	n, _ := pty.keysR.Read(buf)
	if string(buf[:n]) != "ls\r" {
		t.Fatalf("keys = %q", buf[:n])
	}
	conn.in <- msg{false, []byte(`{"type":"resize","rows":20,"cols":60}`)}
	time.Sleep(20 * time.Millisecond)
	pty.mu.Lock()
	if len(pty.sizes) == 0 || pty.sizes[len(pty.sizes)-1] != [2]uint16{20, 60} {
		t.Errorf("resize not applied: %v", pty.sizes)
	}
	pty.mu.Unlock()

	// The browser leaving closes the PTY (detaching tmux, not killing it).
	close(conn.in)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("bridge did not end when the client left")
	}
	select {
	case <-pty.done:
	default:
		t.Fatal("PTY left open")
	}
}

func TestPTYExitClosesConn(t *testing.T) {
	conn, pty := newConn(), newPTY()
	start := func(context.Context, []string, uint16, uint16) (PTY, error) { return pty, nil }
	done := make(chan error, 1)
	go func() { done <- Bridge(context.Background(), conn, start, []string{"x"}) }()
	conn.in <- msg{false, []byte(`{"type":"resize","rows":24,"cols":80}`)}
	time.Sleep(10 * time.Millisecond)
	pty.Close()
	select {
	case reason := <-conn.closed:
		if reason == "" {
			t.Error("empty close reason")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("conn not closed after the PTY exited")
	}
	<-done
}

func TestFirstMessageMustBeSize(t *testing.T) {
	conn := newConn()
	started := false
	start := func(context.Context, []string, uint16, uint16) (PTY, error) { started = true; return newPTY(), nil }
	conn.in <- msg{true, []byte("rm -rf /\r")}
	err := Bridge(context.Background(), conn, start, []string{"x"})
	if err == nil || started {
		t.Fatalf("err=%v started=%v", err, started)
	}
}

func TestBadSizes(t *testing.T) {
	for _, m := range []string{`{"type":"resize","rows":0,"cols":80}`, `{"type":"resize","rows":24,"cols":99999}`, `{nope`} {
		conn := newConn()
		conn.in <- msg{false, []byte(m)}
		err := Bridge(context.Background(), conn, func(context.Context, []string, uint16, uint16) (PTY, error) {
			return nil, errors.New("must not start")
		}, []string{"x"})
		if err == nil {
			t.Errorf("%s accepted", m)
		}
	}
}

func TestClientLeavingClosesConn(t *testing.T) {
	conn, pty := newConn(), newPTY()
	done := make(chan error, 1)
	go func() {
		done <- Bridge(context.Background(), conn, func(context.Context, []string, uint16, uint16) (PTY, error) { return pty, nil }, []string{"x"})
	}()
	conn.in <- msg{false, []byte(`{"type":"resize","rows":24,"cols":80}`)}
	time.Sleep(10 * time.Millisecond)
	close(conn.in) // the phone dropped off
	<-done
	select {
	case <-conn.closed:
	case <-time.After(time.Second):
		t.Fatal("connection left open after the client went away")
	}
}

func TestIdleViewerIsDropped(t *testing.T) {
	old := idleTimeout
	idleTimeout = 50 * time.Millisecond
	defer func() { idleTimeout = old }()
	conn, pty := newConn(), newPTY()
	done := make(chan error, 1)
	go func() {
		done <- Bridge(context.Background(), conn, func(context.Context, []string, uint16, uint16) (PTY, error) { return pty, nil }, []string{"x"})
	}()
	conn.in <- msg{false, []byte(`{"type":"resize","rows":24,"cols":80}`)}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a silent viewer kept its PTY forever")
	}
	select {
	case <-pty.done:
	default:
		t.Fatal("PTY left open")
	}
}

func TestPingKeepsViewerAlive(t *testing.T) {
	old := idleTimeout
	idleTimeout = 80 * time.Millisecond
	defer func() { idleTimeout = old }()
	conn, pty := newConn(), newPTY()
	done := make(chan error, 1)
	go func() {
		done <- Bridge(context.Background(), conn, func(context.Context, []string, uint16, uint16) (PTY, error) { return pty, nil }, []string{"x"})
	}()
	conn.in <- msg{false, []byte(`{"type":"resize","rows":24,"cols":80}`)}
	for range 5 {
		time.Sleep(40 * time.Millisecond)
		conn.in <- msg{false, []byte(`{"type":"ping"}`)}
	}
	select {
	case <-done:
		t.Fatal("viewer sending pings was dropped")
	default:
	}
	close(conn.in)
	<-done
}
