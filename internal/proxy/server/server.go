// Package server is the broker's request path: one listener that is an HTTP
// proxy for sandboxes (CONNECT tunnels, selective MITM, absolute-form plain
// HTTP) and a reverse proxy for routes such as /k8s/<cluster> and /grafana.
//
// Every request is checked in the same order: the sandbox network it came
// from, a session token minted for that same project, host or route, rules,
// approvals, credential injection, connect-time address check, audit.
// Anything not explicitly allowed is denied.
package server

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/proxy/approval"
	"github.com/zaneGittins/skybreaker/internal/proxy/audit"
	"github.com/zaneGittins/skybreaker/internal/proxy/ca"
	"github.com/zaneGittins/skybreaker/internal/proxy/config"
	"github.com/zaneGittins/skybreaker/internal/proxy/netguard"
	"github.com/zaneGittins/skybreaker/internal/proxy/secrets"
	"github.com/zaneGittins/skybreaker/internal/proxy/session"
)

// Placeholder is the credential value sandboxes carry where a tool insists
// on having one (GH_TOKEN, ANTHROPIC_API_KEY). The proxy swaps it for the
// real secret; any other credential is refused.
const Placeholder = "skybreaker-injected"

// Secrets resolves a named secret for one upstream host. The real store
// fails closed when the secret isn't bound to that host.
type Secrets interface {
	Resolve(ctx context.Context, name, host string, scope secrets.Scope) (string, error)
	Redact(text string) string
}

type Deps struct {
	Config *config.Config
	// Sandbox names the project whose sandbox network a connection comes
	// from. Nothing else may use the broker, and a token works only from
	// its own project's network.
	Sandbox       func(remoteAddr string) (project string, ok bool)
	Sessions      *session.Registry
	Secrets       Secrets
	CA            *ca.Authority
	Approvals     *approval.Store
	Audit         *audit.Logger
	Guard         *netguard.Guard
	UpstreamRoots *x509.CertPool // nil uses the system roots
	Hold          time.Duration  // how long an ask request is held, default 30s
	UIURL         string         // where approvals are answered, for the agent's error message

	portMap   map[int]int   // tests: dial a different local port per upstream port
	reapEvery time.Duration // tests: how often open connections are re-checked
}

type Server struct {
	cfg       *config.Config
	sandbox   func(string) (string, bool)
	sessions  *session.Registry
	secrets   Secrets
	ca        *ca.Authority
	approvals *approval.Store
	audit     *audit.Logger
	guard     *netguard.Guard
	roots     *x509.CertPool
	hold      time.Duration
	uiURL     string
	portMap   map[int]int
	mux       *http.ServeMux

	mu         sync.Mutex
	transports map[string]*http.Transport

	// Open tunnels and MITM connections, with the token that opened them,
	// so revocation and expiry cut them off too.
	connMu    sync.Mutex
	conns     map[net.Conn]string
	reapEvery time.Duration
	stop      context.CancelFunc
}

func New(d Deps) (*Server, error) {
	if d.Config == nil || d.Sandbox == nil || d.Sessions == nil || d.Secrets == nil || d.CA == nil ||
		d.Approvals == nil || d.Audit == nil || d.Guard == nil {
		return nil, errors.New("server: missing dependency")
	}
	s := &Server{
		cfg: d.Config, sandbox: d.Sandbox, sessions: d.Sessions, secrets: d.Secrets, ca: d.CA,
		approvals: d.Approvals, audit: d.Audit, guard: d.Guard,
		roots: d.UpstreamRoots, hold: d.Hold, uiURL: d.UIURL, portMap: d.portMap,
		mux: http.NewServeMux(), transports: map[string]*http.Transport{},
		conns: map[net.Conn]string{}, reapEvery: d.reapEvery,
	}
	if s.hold <= 0 {
		s.hold = 30 * time.Second
	}
	if s.reapEvery <= 0 {
		s.reapEvery = 5 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.stop = cancel
	go s.reap(ctx)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	s.mux.HandleFunc("GET /ca.crt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Write(s.ca.CertPEM())
	})
	s.mux.HandleFunc("/", s.route)
	return s, nil
}

// sender is the sandbox a request came from.
type sender struct{ project, addr string }

// Name is the label used in audit logs.
func (p sender) Name() string { return p.project + "@" + p.addr }

type senderKey struct{}

func senderOf(r *http.Request) sender {
	p, _ := r.Context().Value(senderKey{}).(sender)
	return p
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	addr, _, _ := net.SplitHostPort(r.RemoteAddr)
	project, ok := s.sandbox(r.RemoteAddr)
	if !ok || project == "" {
		s.log(audit.Entry{Sender: addr, Decision: "deny", Reason: "sender is not a sandbox", Host: r.Host, Method: r.Method})
		http.Error(w, "skybreaker: forbidden", http.StatusForbidden)
		return
	}
	peer := sender{project: project, addr: addr}
	r = r.WithContext(context.WithValue(r.Context(), senderKey{}, peer))
	switch {
	case r.Method == http.MethodConnect:
		s.connect(w, r, peer)
	case r.URL.IsAbs():
		s.plain(w, r, peer)
	default:
		s.mux.ServeHTTP(w, r)
	}
}

// Close stops the connection reaper.
func (s *Server) Close() { s.stop() }

// proxyAuth checks Proxy-Authorization: Basic project:token. The token must
// be live and minted for that very project, and the request must come from
// that project's sandbox.
func (s *Server) proxyAuth(r *http.Request) (*config.ProjectPolicy, string, bool) {
	project, token, ok := basicAuth(r.Header.Get("Proxy-Authorization"))
	if !ok {
		return nil, "", false
	}
	pp, ok := s.projectFor(r, project, token)
	return pp, token, ok
}

// track records an open connection until untrack is called.
func (s *Server) track(c net.Conn, token string) (untrack func()) {
	s.connMu.Lock()
	s.conns[c] = token
	s.connMu.Unlock()
	return func() {
		s.connMu.Lock()
		delete(s.conns, c)
		s.connMu.Unlock()
	}
}

// reap closes open connections whose session was revoked or has expired.
func (s *Server) reap(ctx context.Context) {
	t := time.NewTicker(s.reapEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.connMu.Lock()
		for c, token := range s.conns {
			if _, live := s.sessions.Lookup(token); !live {
				c.Close()
				delete(s.conns, c)
			}
		}
		s.connMu.Unlock()
	}
}

func (s *Server) projectFor(r *http.Request, project, token string) (*config.ProjectPolicy, bool) {
	info, ok := s.sessions.Lookup(token)
	if !ok || info.Project != project || senderOf(r).project != project {
		return nil, false
	}
	return s.cfg.Project(project)
}

func basicAuth(h string) (user, pass string, ok bool) {
	const prefix = "Basic "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", "", false
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	user, pass, ok = strings.Cut(string(b), ":")
	return user, pass, ok
}

func bearer(h string) string {
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

func (s *Server) log(e audit.Entry) {
	e.Path = s.secrets.Redact(e.Path)
	e.Reason = s.secrets.Redact(e.Reason)
	s.audit.Log(e)
}

// needAuth answers a proxy request that has no live session token.
func (s *Server) needAuth(w http.ResponseWriter, e audit.Entry) {
	e.Decision, e.Reason, e.Status = "deny", "missing or invalid session token", http.StatusProxyAuthRequired
	s.log(e)
	w.Header().Set("Proxy-Authenticate", `Basic realm="skybreaker"`)
	http.Error(w, "skybreaker: proxy authentication required", http.StatusProxyAuthRequired)
}

// Project returns a project's resolved policy.
func (s *Server) Project(name string) (*config.ProjectPolicy, bool) { return s.cfg.Project(name) }
