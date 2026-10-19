// Package wsock is a small RFC 6455 WebSocket server: enough for the
// terminal relay, and nothing more. No extensions or subprotocols are
// negotiated; client frames must be masked; messages are capped at
// MaxMessage. Callers authenticate the request and check its Origin before
// calling Accept.
package wsock

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xa

	// MaxMessage bounds one message, reassembled. Keystrokes and resize
	// messages are tiny; a large paste fits comfortably.
	MaxMessage = 1 << 20
)

var ErrProtocol = errors.New("websocket: protocol error")

const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + guid))
	return base64.StdEncoding.EncodeToString(h[:])
}

func hasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

type Conn struct {
	conn net.Conn
	br   *bufio.Reader

	wmu       sync.Mutex
	closeOnce sync.Once

	fragOp byte
	frag   []byte
}

// Accept completes the opening handshake, or answers with an error status
// and returns an error.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	fail := func(status int, msg string) (*Conn, error) {
		if status == http.StatusUpgradeRequired {
			w.Header().Set("Sec-WebSocket-Version", "13")
		}
		http.Error(w, msg, status)
		return nil, errors.New("websocket: " + msg)
	}
	if r.Method != http.MethodGet || !hasToken(r.Header, "Upgrade", "websocket") || !hasToken(r.Header, "Connection", "upgrade") {
		return fail(http.StatusBadRequest, "not a websocket handshake")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return fail(http.StatusUpgradeRequired, "unsupported websocket version")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if k, err := base64.StdEncoding.DecodeString(key); err != nil || len(k) != 16 {
		return fail(http.StatusBadRequest, "bad Sec-WebSocket-Key")
	}
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return fail(http.StatusInternalServerError, "cannot take over the connection")
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, err
	}
	return &Conn{conn: conn, br: rw.Reader}, nil
}

// Read returns the next data message. Pings are answered and pongs dropped
// along the way; a close frame is echoed and reported as io.EOF.
func (c *Conn) Read(ctx context.Context) (isBinary bool, data []byte, err error) {
	// Cancelling ctx unblocks both directions: Read may be stuck writing a
	// pong to a peer that has stopped reading.
	stop := context.AfterFunc(ctx, func() {
		c.conn.SetReadDeadline(time.Unix(1, 0))
		c.conn.SetWriteDeadline(time.Unix(1, 0))
	})
	defer stop()
	for {
		// Frames may already be buffered, so the deadline alone doesn't end
		// the loop; and a pong would set a fresh write deadline.
		if err := ctx.Err(); err != nil {
			return false, nil, err
		}
		fin, op, payload, err := c.readFrame()
		if err != nil {
			if ctx.Err() != nil {
				return false, nil, ctx.Err()
			}
			if errors.Is(err, ErrProtocol) {
				c.Close(1002, "protocol error")
			}
			return false, nil, err
		}
		switch op {
		case opPing:
			c.writeFrame(opPong, payload, time.Now().Add(controlTimeout))
		case opPong:
		case opClose:
			code := 1000
			if len(payload) >= 2 {
				code = int(binary.BigEndian.Uint16(payload))
			}
			c.Close(code, "")
			return false, nil, io.EOF
		case opText, opBinary:
			if c.fragOp != 0 {
				return c.protocol("new message inside a fragmented one")
			}
			if fin {
				return op == opBinary, payload, nil
			}
			c.fragOp, c.frag = op, payload
		case opContinuation:
			if c.fragOp == 0 {
				return c.protocol("continuation without a message")
			}
			if len(c.frag)+len(payload) > MaxMessage {
				return c.protocol("message too large")
			}
			c.frag = append(c.frag, payload...)
			if fin {
				op, msg := c.fragOp, c.frag
				c.fragOp, c.frag = 0, nil
				return op == opBinary, msg, nil
			}
		default:
			return c.protocol(fmt.Sprintf("reserved opcode %#x", op))
		}
	}
}

func (c *Conn) protocol(msg string) (bool, []byte, error) {
	c.Close(1002, "protocol error")
	return false, nil, fmt.Errorf("%w: %s", ErrProtocol, msg)
}

func (c *Conn) readFrame() (fin bool, op byte, payload []byte, err error) {
	var h [2]byte
	if _, err := io.ReadFull(c.br, h[:]); err != nil {
		return false, 0, nil, err
	}
	fin, op = h[0]&0x80 != 0, h[0]&0x0f
	if h[0]&0x70 != 0 {
		return false, 0, nil, fmt.Errorf("%w: reserved bits set", ErrProtocol)
	}
	if h[1]&0x80 == 0 {
		return false, 0, nil, fmt.Errorf("%w: client frame not masked", ErrProtocol)
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var l [2]byte
		if _, err := io.ReadFull(c.br, l[:]); err != nil {
			return false, 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(l[:]))
	case 127:
		var l [8]byte
		if _, err := io.ReadFull(c.br, l[:]); err != nil {
			return false, 0, nil, err
		}
		n = binary.BigEndian.Uint64(l[:])
	}
	if op >= opClose && (!fin || n > 125) {
		return false, 0, nil, fmt.Errorf("%w: control frame fragmented or too long", ErrProtocol)
	}
	if n > MaxMessage {
		return false, 0, nil, fmt.Errorf("%w: frame too large", ErrProtocol)
	}
	var mask [4]byte
	if _, err := io.ReadFull(c.br, mask[:]); err != nil {
		return false, 0, nil, err
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return fin, op, payload, nil
}

// controlTimeout bounds writing a pong or close frame.
const controlTimeout = 5 * time.Second

// writeFrame sends one frame. The deadline is set under the write lock, so
// concurrent writers can't clear each other's.
func (c *Conn) writeFrame(op byte, payload []byte, deadline time.Time) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.conn.SetWriteDeadline(deadline)
	hdr := make([]byte, 0, 10)
	hdr = append(hdr, 0x80|op)
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n < 1<<16:
		hdr = append(hdr, 126)
		hdr = binary.BigEndian.AppendUint16(hdr, uint16(n))
	default:
		hdr = append(hdr, 127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	if _, err := c.conn.Write(hdr); err != nil {
		return err
	}
	_, err := c.conn.Write(payload)
	return err
}

// Write sends one unfragmented message.
func (c *Conn) Write(ctx context.Context, isBinary bool, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// A viewer that stops reading must not block the PTY pump forever.
	d, ok := ctx.Deadline()
	if !ok {
		d = time.Now().Add(15 * time.Second)
	}
	op := byte(opText)
	if isBinary {
		op = opBinary
	}
	return c.writeFrame(op, data, d)
}

// Close sends a close frame with code and reason and closes the connection.
func (c *Conn) Close(code int, reason string) error {
	var err error
	c.closeOnce.Do(func() {
		if len(reason) > 120 {
			reason = reason[:120]
		}
		p := binary.BigEndian.AppendUint16(nil, uint16(code))
		c.writeFrame(opClose, append(p, reason...), time.Now().Add(2*time.Second))
		err = c.conn.Close()
	})
	return err
}
