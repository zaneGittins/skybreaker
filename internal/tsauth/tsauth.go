// Package tsauth authorises HTTP requests by the Tailscale identity of the
// connection they arrived on. Both UIs and the proxy call WhoIs on every
// request and WebSocket upgrade instead of trusting `tailscale serve`
// headers, which are empty for tagged callers.
package tsauth

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Peer is who is on the other end of a tailnet connection.
type Peer struct {
	Node  string   // machine name
	Login string   // user login; meaningless for tagged nodes
	Tags  []string // ACL tags; a tagged node is never a user
}

// Name is the label used in audit logs: the login for people, the machine
// name for tagged nodes.
func (p Peer) Name() string {
	if len(p.Tags) == 0 && p.Login != "" {
		return p.Login
	}
	return p.Node
}

// Identifier resolves the peer behind a connection's remote address.
type Identifier interface {
	WhoIs(ctx context.Context, remoteAddr string) (Peer, error)
}

// Static is an Identifier that answers the same peer for every address. It
// exists for local development without a tailnet and must never front a
// real listener.
type Static Peer

func (s Static) WhoIs(context.Context, string) (Peer, error) { return Peer(s), nil }

type ctxKey struct{}

// WithPeer stores p in ctx.
func WithPeer(ctx context.Context, p Peer) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the peer stored by the middleware.
func FromContext(ctx context.Context) (Peer, bool) {
	p, ok := ctx.Value(ctxKey{}).(Peer)
	return p, ok
}

// RequireUsers admits only untagged peers whose login is listed. An empty
// list admits nobody: access has to be granted by name.
func RequireUsers(id Identifier, logins []string) func(http.Handler) http.Handler {
	listed := func(p Peer) bool {
		if len(p.Tags) > 0 || p.Login == "" {
			return false
		}
		return slices.ContainsFunc(logins, func(l string) bool { return strings.EqualFold(l, p.Login) })
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := id.WhoIs(r.Context(), r.RemoteAddr)
			if err != nil || !listed(p) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPeer(r.Context(), p)))
		})
	}
}

// SameOrigin reports whether the request's Origin names the host it was sent
// to. WebSocket upgrades are GETs, which cross-origin protection lets
// through, so they must check this to stop other tailnet pages hijacking a
// terminal.
func SameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
