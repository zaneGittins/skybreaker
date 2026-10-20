package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/proxy/audit"
	"github.com/zaneGittins/skybreaker/internal/proxy/config"
	"github.com/zaneGittins/skybreaker/internal/proxy/netguard"
)

func (s *Server) connect(w http.ResponseWriter, r *http.Request, peer sender) {
	entry := audit.Entry{Sender: peer.Name(), Host: r.Host, Method: http.MethodConnect}
	pp, token, ok := s.proxyAuth(r)
	if !ok {
		s.needAuth(w, entry)
		return
	}
	entry.Project = pp.Name
	host, portStr, err := net.SplitHostPort(r.Host)
	port, perr := strconv.Atoi(portStr)
	if err != nil || perr != nil {
		s.deny(w, entry, http.StatusBadRequest, "CONNECT target must be host:port")
		return
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	entry.Host = host
	rule := pp.Host(host)
	if rule == nil {
		s.deny(w, entry, http.StatusForbidden, "host is not on the project's allow list")
		return
	}
	if !rule.AllowsPort(port) {
		s.deny(w, entry, http.StatusForbidden, fmt.Sprintf("port %d is not allowed for %s", port, host))
		return
	}
	entry.Mode = rule.Mode
	if rule.Mode == config.ModeTunnel {
		s.tunnel(w, r, entry, host, port, token)
		return
	}
	s.mitm(w, r, entry, pp, rule, port, token)
}

func (s *Server) deny(w http.ResponseWriter, e audit.Entry, status int, reason string) {
	e.Decision, e.Reason, e.Status = "deny", reason, status
	s.log(e)
	http.Error(w, "skybreaker: "+reason, status)
}

// dial connects to host:port through the address guard. Tests remap ports so
// every upstream lands on a local server.
func (s *Server) dial(ctx context.Context, g *netguard.Guard, host string, port int) (net.Conn, error) {
	if p, ok := s.portMap[port]; ok {
		port = p
	}
	return g.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}

func dialStatus(err error) (int, string) {
	if errors.Is(err, netguard.ErrBlocked) {
		return http.StatusForbidden, "destination address is blocked"
	}
	return http.StatusBadGateway, "upstream unreachable"
}

// hijack takes over the client connection and confirms the CONNECT. Bytes
// the client already sent (an eager TLS ClientHello) are kept.
func hijack(w http.ResponseWriter) (net.Conn, error) {
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		conn.Close()
		return nil, err
	}
	if rw.Reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: rw.Reader}, nil
	}
	return conn, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (s *Server) tunnel(w http.ResponseWriter, r *http.Request, e audit.Entry, host string, port int, token string) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	up, err := s.dial(ctx, s.guard, host, port)
	cancel()
	if err != nil {
		status, reason := dialStatus(err)
		s.deny(w, e, status, reason)
		return
	}
	client, err := hijack(w)
	if err != nil {
		up.Close()
		return
	}
	defer s.track(client, token)()
	// A tunnel is opened for a host, but the bytes inside could name any
	// site at that address (a CDN serves thousands): it carries TLS to the
	// host it was opened for, or nothing.
	client.SetReadDeadline(time.Now().Add(15 * time.Second))
	name, seen, err := serverName(client)
	client.SetReadDeadline(time.Time{})
	if err != nil || !strings.EqualFold(strings.TrimSuffix(name, "."), host) {
		client.Close()
		up.Close()
		reason := fmt.Sprintf("the tunnel's TLS server name %q isn't %s", name, host)
		if err != nil {
			reason = "a tunnel carries TLS with the host's server name, and this didn't"
		}
		e.Decision, e.Reason, e.Status = "deny", reason, http.StatusForbidden
		s.log(e)
		return
	}
	e.Decision, e.Status = "allow", http.StatusOK
	s.log(e)
	splice(&bufferedConn{Conn: client, r: bufio.NewReader(io.MultiReader(bytes.NewReader(seen), client))}, up)
}

// errHello stops a handshake once the ClientHello has been read.
var errHello = errors.New("read the ClientHello")

// serverName reads the TLS ClientHello that opens c and returns its server
// name, with every byte read so the hello can go on to the upstream.
func serverName(c net.Conn) (string, []byte, error) {
	rec := &recordConn{Conn: c}
	var name string
	got := false
	err := tls.Server(rec, &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		name, got = h.ServerName, true
		return nil, errHello
	}}).Handshake()
	if !got {
		if err == nil {
			err = errors.New("no ClientHello")
		}
		return "", rec.buf.Bytes(), err
	}
	return name, rec.buf.Bytes(), nil
}

// recordConn keeps what is read through it and writes nothing, so a TLS
// server can parse a ClientHello without answering it.
type recordConn struct {
	net.Conn
	buf bytes.Buffer
}

func (c *recordConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.buf.Write(p[:n])
	return n, err
}

func (c *recordConn) Write(p []byte) (int, error) { return len(p), nil }

// splice copies both ways until either side finishes.
func splice(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() { a.Close(); b.Close() }
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		once.Do(closeBoth)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}

// mitm terminates the client's TLS with a leaf from the broker CA and serves
// HTTP/1.1 on the inner connection, so every request goes through the rules.
func (s *Server) mitm(w http.ResponseWriter, r *http.Request, e audit.Entry, pp *config.ProjectPolicy, rule *config.HostRule, port int, token string) {
	host := rule.Host
	conn, err := hijack(w)
	if err != nil {
		return
	}
	tconn := tls.Server(conn, &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			// The SNI must name the host that was authorised at CONNECT.
			if sni := strings.ToLower(strings.TrimSuffix(hello.ServerName, ".")); sni != "" && sni != host {
				return nil, fmt.Errorf("SNI %q does not match CONNECT host %q", hello.ServerName, host)
			}
			return s.ca.Leaf(host)
		},
	})
	tconn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := tconn.Handshake(); err != nil {
		e.Decision, e.Reason = "deny", "TLS handshake failed: "+err.Error()
		s.log(e)
		tconn.Close()
		return
	}
	tconn.SetDeadline(time.Time{})
	defer s.track(tconn, token)()
	peer := senderOf(r)
	inner := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, ir *http.Request) {
			// Every request on the connection rechecks the session, so a
			// revoked or expired token stops at once.
			if info, live := s.sessions.Lookup(token); !live || info.Project != pp.Name {
				w.Header().Set("Connection", "close")
				s.deny(w, audit.Entry{Sender: peer.Name(), Project: pp.Name, Mode: rule.Mode, Host: host, Method: ir.Method, Path: ir.URL.Path},
					http.StatusProxyAuthRequired, "session revoked or expired")
				return
			}
			if !sameHost(ir.Host, host, port) {
				s.deny(w, audit.Entry{Sender: peer.Name(), Project: pp.Name, Mode: rule.Mode, Host: host, Method: ir.Method, Path: ir.URL.Path},
					http.StatusMisdirectedRequest, "Host header does not match the CONNECT host")
				return
			}
			s.forward(w, ir, target{
				peer: peer, token: token, pp: pp, mode: config.ModeMITM, scheme: "https", host: host, port: port,
				escapedPath: ir.URL.EscapedPath(), rules: rule.Rules, inject: rule.Inject, guard: s.guard,
			})
		}),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	inner.Serve(newOneConnListener(tconn))
}

func sameHost(hostHeader, host string, port int) bool {
	h, p, err := net.SplitHostPort(hostHeader)
	if err != nil {
		h, p = hostHeader, strconv.Itoa(port)
	}
	return strings.EqualFold(strings.TrimSuffix(h, "."), host) && p == strconv.Itoa(port)
}

// oneConnListener hands one connection to an http.Server and then blocks
// until that connection closes, which ends Serve.
type oneConnListener struct {
	conn   net.Conn
	once   sync.Once
	closed chan struct{}
}

func newOneConnListener(c net.Conn) *oneConnListener {
	l := &oneConnListener{closed: make(chan struct{})}
	l.conn = &notifyConn{Conn: c, closed: l.closed}
	return l
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = l.conn })
	if c != nil {
		return c, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

type notifyConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *notifyConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}
