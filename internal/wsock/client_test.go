package wsock

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// tlsEcho serves a TLS echo endpoint that also reports the Origin it saw.
func tlsEcho(t *testing.T) (*httptest.Server, *tls.Config, chan string) {
	t.Helper()
	origins := make(chan string, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins <- r.Header.Get("Origin")
		c, err := Accept(w, r)
		if err != nil {
			return
		}
		defer c.Close(1000, "")
		for {
			bin, b, err := c.Read(context.Background())
			if err != nil {
				return
			}
			if err := c.Write(context.Background(), bin, b); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, &tls.Config{RootCAs: pool}, origins
}

func TestDialRoundTrip(t *testing.T) {
	srv, tc, origins := tlsEcho(t)
	ctx := context.Background()
	u := "wss" + strings.TrimPrefix(srv.URL, "https") + "/ws"
	c, err := Dial(ctx, u, http.Header{"Origin": {srv.URL}}, tc)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(1000, "")
	if got := <-origins; got != srv.URL {
		t.Fatalf("server saw Origin %q", got)
	}
	big := strings.Repeat("x", 70000) // a 64-bit length, masked
	for _, m := range []struct {
		bin  bool
		data string
	}{{true, "keys"}, {false, `{"type":"resize","rows":24,"cols":80}`}, {true, big}} {
		if err := c.Write(ctx, m.bin, []byte(m.data)); err != nil {
			t.Fatal(err)
		}
		bin, b, err := c.Read(ctx)
		if err != nil || bin != m.bin || string(b) != m.data {
			t.Fatalf("echo: bin=%v len=%d err=%v", bin, len(b), err)
		}
	}
}

func TestDialRefusals(t *testing.T) {
	ctx := context.Background()
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "cross-origin terminal refused", http.StatusForbidden)
	}))
	defer forbidden.Close()
	if _, err := Dial(ctx, forbidden.URL, nil, nil); err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "cross-origin") {
		t.Fatalf("403: %v", err)
	}

	// A server that answers 101 with the wrong accept key isn't speaking
	// WebSocket to us.
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, _ := http.NewResponseController(w).Hijack()
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: bm9wZQ==\r\n\r\n")
		rw.Flush()
	}))
	defer wrong.Close()
	if _, err := Dial(ctx, wrong.URL, nil, nil); err == nil || !strings.Contains(err.Error(), "handshake") {
		t.Fatalf("wrong accept key: %v", err)
	}

	// Only ws, wss, http and https.
	if _, err := Dial(ctx, "ftp://example.com/", nil, nil); err == nil {
		t.Fatal("ftp scheme accepted")
	}
}

// rawServer completes a handshake by hand, then runs fn on the connection.
func rawServer(t *testing.T, fn func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+acceptKey(req.Header.Get("Sec-WebSocket-Key"))+"\r\n\r\n")
		fn(conn)
	}()
	return "ws://" + ln.Addr().String() + "/"
}

func TestClientRejectsMaskedServerFrames(t *testing.T) {
	u := rawServer(t, func(c net.Conn) {
		// FIN+binary, masked, "hi" under a zero mask.
		c.Write([]byte{0x82, 0x82, 0, 0, 0, 0, 'h', 'i'})
		time.Sleep(time.Second)
	})
	c, err := Dial(context.Background(), u, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Read(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("masked server frame: %v", err)
	}
}

func TestClientFramesAreMasked(t *testing.T) {
	got := make(chan byte, 1)
	u := rawServer(t, func(c net.Conn) {
		var h [2]byte
		io.ReadFull(c, h[:])
		got <- h[1]
	})
	c, err := Dial(context.Background(), u, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Write(context.Background(), true, []byte("k"))
	if b := <-got; b&0x80 == 0 {
		t.Fatal("client frame sent unmasked")
	}
}

func TestDialRequiresAProperUpgrade(t *testing.T) {
	// RFC 6455 4.1: a 101 without Connection: Upgrade, or with an extension
	// or subprotocol we never asked for, is a failed handshake.
	for name, extra := range map[string]string{
		"no Connection":       "Upgrade: websocket\r\n",
		"unasked extension":   "Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Extensions: permessage-deflate\r\n",
		"unasked subprotocol": "Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Protocol: chat\r\n",
	} {
		u := rawServerHandshake(t, extra)
		if c, err := Dial(context.Background(), u, nil, nil); err == nil {
			c.Close(1000, "")
			t.Errorf("%s: accepted", name)
		}
	}
}

// rawServerHandshake answers the handshake with a correct accept key and
// the given header lines.
func rawServerHandshake(t *testing.T, headers string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\n"+headers+"Sec-WebSocket-Accept: "+acceptKey(req.Header.Get("Sec-WebSocket-Key"))+"\r\n\r\n")
		time.Sleep(500 * time.Millisecond)
	}()
	return "ws://" + ln.Addr().String() + "/"
}
