package wsock

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// client is a minimal, test-only WebSocket client.
type client struct {
	c  net.Conn
	br *bufio.Reader
}

func dial(t *testing.T, url string, hdr map[string]string) (*client, *http.Response) {
	t.Helper()
	addr := strings.TrimPrefix(url, "http://")
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 16)
	rand.Read(key)
	h := map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Sec-WebSocket-Version": "13", "Sec-WebSocket-Key": base64.StdEncoding.EncodeToString(key)}
	for k, v := range hdr {
		h[k] = v // overrides, so a test can break one header
	}
	req := fmt.Sprintf("GET /ws HTTP/1.1\r\nHost: %s\r\n", addr)
	for k, v := range h {
		req += k + ": " + v + "\r\n"
	}
	c.Write([]byte(req + "\r\n"))
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		if resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(base64.StdEncoding.EncodeToString(key)) {
			t.Fatal("bad accept key")
		}
	}
	return &client{c, br}, resp
}

// frame writes one masked client frame.
func (cl *client) frame(fin bool, op byte, payload []byte, mask bool) {
	var b bytes.Buffer
	first := op
	if fin {
		first |= 0x80
	}
	b.WriteByte(first)
	mbit := byte(0)
	if mask {
		mbit = 0x80
	}
	switch n := len(payload); {
	case n < 126:
		b.WriteByte(mbit | byte(n))
	case n < 65536:
		b.WriteByte(mbit | 126)
		binary.Write(&b, binary.BigEndian, uint16(n))
	default:
		b.WriteByte(mbit | 127)
		binary.Write(&b, binary.BigEndian, uint64(n))
	}
	p := append([]byte(nil), payload...)
	if mask {
		k := []byte{1, 2, 3, 4}
		b.Write(k)
		for i := range p {
			p[i] ^= k[i%4]
		}
	}
	b.Write(p)
	cl.c.Write(b.Bytes())
}

// read returns the next server frame.
func (cl *client) read(t *testing.T) (byte, []byte) {
	t.Helper()
	cl.c.SetReadDeadline(time.Now().Add(2 * time.Second))
	h := make([]byte, 2)
	if _, err := io.ReadFull(cl.br, h); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if h[1]&0x80 != 0 {
		t.Fatal("server frames must not be masked")
	}
	n := int(h[1] & 0x7f)
	switch n {
	case 126:
		var l uint16
		binary.Read(cl.br, binary.BigEndian, &l)
		n = int(l)
	case 127:
		var l uint64
		binary.Read(cl.br, binary.BigEndian, &l)
		n = int(l)
	}
	p := make([]byte, n)
	io.ReadFull(cl.br, p)
	return h[0] & 0x0f, p
}

// echo serves an echo endpoint and reports the server-side error.
func echo(t *testing.T) (*httptest.Server, chan error) {
	t.Helper()
	errs := make(chan error, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r)
		if err != nil {
			return
		}
		ctx := context.Background()
		for {
			bin, data, err := c.Read(ctx)
			if err != nil {
				errs <- err
				return
			}
			c.Write(ctx, bin, data)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, errs
}

func TestEcho(t *testing.T) {
	ts, _ := echo(t)
	cl, resp := dial(t, ts.URL, nil)
	if resp.StatusCode != 101 {
		t.Fatalf("handshake %d", resp.StatusCode)
	}
	cl.frame(true, opBinary, []byte("hello"), true)
	if op, p := cl.read(t); op != opBinary || string(p) != "hello" {
		t.Fatalf("got %d %q", op, p)
	}
	cl.frame(true, opText, []byte(`{"type":"resize"}`), true)
	if op, p := cl.read(t); op != opText || string(p) != `{"type":"resize"}` {
		t.Fatalf("got %d %q", op, p)
	}
	big := bytes.Repeat([]byte("x"), 70000)
	cl.frame(true, opBinary, big, true)
	if _, p := cl.read(t); !bytes.Equal(p, big) {
		t.Fatalf("64-bit length frame: %d bytes", len(p))
	}
}

func TestFragmentsAndPing(t *testing.T) {
	ts, _ := echo(t)
	cl, _ := dial(t, ts.URL, nil)
	cl.frame(false, opBinary, []byte("hel"), true)
	cl.frame(true, opPing, []byte("p"), true) // control frames may interleave
	if op, p := cl.read(t); op != opPong || string(p) != "p" {
		t.Fatalf("pong: %d %q", op, p)
	}
	cl.frame(true, opContinuation, []byte("lo"), true)
	if _, p := cl.read(t); string(p) != "hello" {
		t.Fatalf("reassembled %q", p)
	}
}

func TestClose(t *testing.T) {
	ts, errs := echo(t)
	cl, _ := dial(t, ts.URL, nil)
	cl.frame(true, opClose, []byte{0x03, 0xe8}, true)
	if op, _ := cl.read(t); op != opClose {
		t.Fatalf("close not echoed: %d", op)
	}
	select {
	case err := <-errs:
		if err != io.EOF {
			t.Fatalf("Read after close = %v, want io.EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server Read did not return")
	}
}

func TestProtocolViolations(t *testing.T) {
	cases := map[string]func(*client){
		"unmasked frame":     func(c *client) { c.frame(true, opBinary, []byte("x"), false) },
		"reserved opcode":    func(c *client) { c.frame(true, 0x3, []byte("x"), true) },
		"fragmented control": func(c *client) { c.frame(false, opPing, []byte("x"), true) },
		"stray continuation": func(c *client) { c.frame(true, opContinuation, []byte("x"), true) },
		"oversized frame":    func(c *client) { c.frame(true, opBinary, make([]byte, MaxMessage+1), true) },
		"rsv bits": func(c *client) {
			c.c.Write([]byte{0x80 | 0x40 | opBinary, 0x80 | 1, 1, 2, 3, 4, 'x'})
		},
	}
	for name, send := range cases {
		ts, errs := echo(t)
		cl, _ := dial(t, ts.URL, nil)
		send(cl)
		select {
		case err := <-errs:
			if err == nil || err == io.EOF {
				t.Errorf("%s: err = %v, want a protocol error", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestHandshakeRejects(t *testing.T) {
	ts, _ := echo(t)
	cases := map[string]map[string]string{
		"no upgrade":  {"Upgrade": "h2c"},
		"bad version": {"Sec-WebSocket-Version": "8"},
	}
	for name, hdr := range cases {
		_, resp := dial(t, ts.URL, hdr)
		if resp.StatusCode == 101 {
			t.Errorf("%s: upgraded", name)
		}
	}
	// A plain GET is not a WebSocket.
	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 101 {
		t.Error("plain GET upgraded")
	}
}

func TestContextCancelUnblocksRead(t *testing.T) {
	done := make(chan error, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, _, err = c.Read(ctx)
		done <- err
	}))
	defer ts.Close()
	dial(t, ts.URL, nil)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Read returned nil after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read ignored the context")
	}
}

func TestPeerThatNeverReadsCantHangRead(t *testing.T) {
	// A viewer that floods pings and never reads fills the socket, so the
	// pong write blocks. Cancelling Read's context must still unblock it.
	done := make(chan time.Duration, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r)
		if err != nil {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(time.Second, cancel)
		start := time.Now()
		c.Read(ctx)
		done <- time.Since(start)
	}))
	defer ts.Close()
	cl, _ := dial(t, ts.URL, nil)
	if tc, ok := cl.c.(*net.TCPConn); ok {
		tc.SetReadBuffer(4096)
	}
	go func() {
		ping := bytes.Repeat([]byte("p"), 125)
		for range 100000 {
			cl.frame(true, opPing, ping, true)
		}
	}()
	select {
	case d := <-done:
		if d > 3*time.Second {
			t.Fatalf("Read took %v after its context was cancelled at 1s", d)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Read stayed blocked writing a pong after its context was cancelled")
	}
	cl.c.Close()
}
