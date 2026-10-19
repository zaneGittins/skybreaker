// Package preview shows you a web server running in a project's sandbox
// (a dev server, say) on a preview origin of the project's own: a port of
// Skybreaker's tailnet name that no other project ever gets.
//
// What the page runs is the sandbox's to choose, so it must gain nothing
// from your browser's trust. The origin isolates it from the app; on top of
// that, Handler answers only its own origin's requests and navigations (so
// another project's page can't read it, whatever CORS headers the dev
// server sends), keeps cookies to this project (browsers share cookies
// across ports), refuses to be framed and leaves the app no opener.
package preview

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Handler serves one project's preview origin.
type Handler struct {
	Project string
	Origin  string // e.g. https://skybreaker.tail1234.ts.net:8401
	// Target is the sandbox address being previewed now; false when no port
	// has been chosen.
	Target func() (netip.AddrPort, bool)

	once  sync.Once
	proxy *httputil.ReverseProxy
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.allowed(r) {
		http.Error(w, "this preview answers only its own pages", http.StatusForbidden)
		return
	}
	ap, ok := h.Target()
	if !ok {
		page(w, http.StatusNotFound, "No preview chosen",
			"Pick a port for "+h.Project+" in Skybreaker, on the project's page under Preview.")
		return
	}
	h.once.Do(h.init)
	r = r.WithContext(context.WithValue(r.Context(), targetKey{}, ap))
	h.proxy.ServeHTTP(w, r)
}

type targetKey struct{}

// allowed admits the origin's own requests, top-level navigations to it
// (a link from the app, a typed URL) and clients that aren't browsers.
// Fetches, sockets, frames and form posts from any other origin are
// refused, so another project's page can't read this one with your
// identity.
func (h *Handler) allowed(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
		// A browser without Fetch Metadata: go by Origin, which it sends on
		// every cross-origin request that could change or read anything.
		o := r.Header.Get("Origin")
		return o == "" || o == h.Origin
	}
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document"
}

func (h *Handler) init() {
	cookiePrefix := "sbp_" + h.Project + "_"
	h.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			ap := pr.In.Context().Value(targetKey{}).(netip.AddrPort)
			local := "http://localhost:" + strconv.Itoa(int(ap.Port()))
			pr.SetURL(&url.URL{Scheme: "http", Host: ap.String()})
			// The dev server sees itself addressed as localhost: Vite and
			// Next refuse unknown hosts and check a socket's Origin, and the
			// checks above have already been made.
			pr.Out.Host = strings.TrimPrefix(local, "http://")
			for _, k := range []string{"Origin", "Referer"} {
				if v := pr.Out.Header.Get(k); strings.HasPrefix(v, h.Origin) {
					pr.Out.Header.Set(k, local+strings.TrimPrefix(v, h.Origin))
				}
			}
			var mine []string
			for _, c := range pr.In.Cookies() {
				if name, ok := strings.CutPrefix(c.Name, cookiePrefix); ok && name != "" {
					mine = append(mine, (&http.Cookie{Name: name, Value: c.Value}).String())
				}
			}
			pr.Out.Header.Del("Cookie")
			if len(mine) > 0 {
				pr.Out.Header.Set("Cookie", strings.Join(mine, "; "))
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			hd := resp.Header
			for k := range hd {
				if strings.HasPrefix(k, "Access-Control-") {
					hd.Del(k)
				}
			}
			var cookies []string
			for _, v := range hd.Values("Set-Cookie") {
				c, err := http.ParseSetCookie(v)
				if err != nil {
					continue
				}
				c.Name, c.Domain, c.HttpOnly = cookiePrefix+c.Name, "", true
				if s := c.String(); s != "" {
					cookies = append(cookies, s)
				}
			}
			hd.Del("Set-Cookie")
			for _, c := range cookies {
				hd.Add("Set-Cookie", c)
			}
			if loc := hd.Get("Location"); loc != "" {
				ap := resp.Request.Context().Value(targetKey{}).(netip.AddrPort)
				hd.Set("Location", h.localToOrigin(loc, ap.Port()))
			}
			hd.Add("Content-Security-Policy", "frame-ancestors 'self'")
			hd.Set("Cross-Origin-Opener-Policy", "same-origin")
			hd.Set("Referrer-Policy", "same-origin")
			return nil
		},
		Transport: &http.Transport{
			Proxy:                 nil, // straight to the sandbox, never through a proxy
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			ResponseHeaderTimeout: 2 * time.Minute, // a dev server's first compile
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			ap := r.Context().Value(targetKey{}).(netip.AddrPort)
			page(w, http.StatusBadGateway, "Nothing answered on port "+strconv.Itoa(int(ap.Port())),
				"Start the dev server in "+h.Project+"'s sandbox listening on 0.0.0.0, not localhost: "+
					"vite --host 0.0.0.0, next dev -H 0.0.0.0, python -m http.server --bind 0.0.0.0. Then reload.")
		},
	}
}

// localToOrigin rewrites a redirect to the dev server's own address so it
// stays on the preview.
func (h *Handler) localToOrigin(loc string, port uint16) string {
	for _, host := range []string{"localhost", "127.0.0.1", "0.0.0.0", "[::1]"} {
		for _, scheme := range []string{"http://", "https://"} {
			if rest, ok := strings.CutPrefix(loc, scheme+host+":"+strconv.Itoa(int(port))); ok && (rest == "" || strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, "?")) {
				return h.Origin + rest
			}
		}
	}
	return loc
}

// page answers with a small page of Skybreaker's own.
func page(w http.ResponseWriter, status int, title, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'self'")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>%s</title>`+
		`<style>body{font:16px/1.5 system-ui,sans-serif;background:#141412;color:#F1EEE7;margin:0;padding:15vh 24px}main{max-width:36em;margin:auto}h1{font-size:22px}p{color:#C2BDB2}</style>`+
		`<main><h1>%s</h1><p>%s</p></main>`, html.EscapeString(title), html.EscapeString(title), html.EscapeString(text))
}

// Listening is a TCP port something in the sandbox listens on. Public is
// false when it listens on loopback only, where the preview can't reach.
type Listening struct {
	Port   uint16 `json:"port"`
	Public bool   `json:"public"`
}

const maxListening = 64

// ParseListening reads /proc/net/tcp and /proc/net/tcp6 (concatenated) for
// listening sockets. The sandbox wrote them, so anything odd is skipped and
// the answer is bounded.
func ParseListening(b []byte) []Listening {
	ports := map[uint16]bool{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 4096)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[3] != "0A" { // 0A: LISTEN
			continue
		}
		addr, portHex, ok := strings.Cut(f[1], ":")
		if !ok || len(portHex) != 4 || (len(addr) != 8 && len(addr) != 32) {
			continue
		}
		p, err := strconv.ParseUint(portHex, 16, 16)
		raw, herr := hex.DecodeString(addr)
		if err != nil || herr != nil || p == 0 {
			continue
		}
		if _, seen := ports[uint16(p)]; !seen && len(ports) >= maxListening {
			continue
		}
		ports[uint16(p)] = ports[uint16(p)] || !loopback(raw)
	}
	out := make([]Listening, 0, len(ports))
	for p, pub := range ports {
		out = append(out, Listening{Port: p, Public: pub})
	}
	slices.SortFunc(out, func(a, b Listening) int { return int(a.Port) - int(b.Port) })
	return out
}

// loopback reads an address as /proc/net prints it: 32-bit words in host
// (little-endian) order.
func loopback(raw []byte) bool {
	if len(raw) == 4 {
		return raw[3] == 127
	}
	var ip [16]byte
	for i := 0; i < 16; i += 4 {
		ip[i], ip[i+1], ip[i+2], ip[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	return netip.AddrFrom16(ip).Unmap().IsLoopback()
}

// Registry holds each project's preview origin and the sandbox port it
// shows now.
type Registry struct {
	mu      sync.Mutex
	origins map[string]string
	targets map[string]uint16
}

func NewRegistry() *Registry {
	return &Registry{origins: map[string]string{}, targets: map[string]uint16{}}
}

func (r *Registry) SetOrigin(project, origin string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.origins[project] = origin
}

// Origin is the project's preview origin, or "" when it has none.
func (r *Registry) Origin(project string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.origins[project]
}

var ErrPort = errors.New("preview port must be between 1 and 65535")

func (r *Registry) SetTarget(project string, port int) error {
	if port < 1 || port > 65535 {
		return ErrPort
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.origins[project]; !ok {
		return fmt.Errorf("no preview for %q", project)
	}
	r.targets[project] = uint16(port)
	return nil
}

func (r *Registry) Target(project string) (uint16, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.targets[project]
	return p, ok
}
