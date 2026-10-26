// Package webutil holds the HTTP plumbing both UIs share: hardening headers,
// JSON-only cross-origin-protected writes, embedded static files and
// server-sent events.
package webutil

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// AppCSP is the app's: no inline script and no third-party origin (fonts
// and xterm.js are embedded). It allows inline styles, which xterm.js's DOM
// renderer injects for its theme, and a form posted to GitHub, which is how
// a GitHub App is created from a manifest.
const AppCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self' https://github.com; frame-ancestors 'none'"

// NewServer is an http.Server for a daemon listener, whose requests inherit
// ctx. Sandboxes reach the proxy and hook listeners, so idle and oversized
// connections are cut. There is no whole-request timeout: held approvals,
// tunnels, watches and event streams are long.
func NewServer(ctx context.Context, h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// Above the most cookies a browser keeps for a host: a preview page
		// can set them on Skybreaker's name, and the app must still answer.
		MaxHeaderBytes: 1 << 20,
		BaseContext:    func(net.Listener) context.Context { return ctx },
	}
}

// HardenWith adds security headers, csp among them, and refuses writes that
// aren't JSON (or a gzipped archive) or that come from another site.
// Neither content type can be sent by a plain HTML form, and cross-origin
// protection stops fetches from other origins.
func HardenWith(csp string, next http.Handler) http.Handler {
	csrf := http.NewCrossOriginProtection()
	inner := csrf.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		case http.MethodDelete:
			// Without a body there's nothing to check, and no form sends a
			// DELETE.
			if r.ContentLength != 0 || r.Header.Get("Content-Type") != "" {
				if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
					WriteJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "writes must be application/json"})
					return
				}
			}
		default:
			if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" && mt != "application/gzip" {
				WriteJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "writes must be application/json"})
				return
			}
		}
		next.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// No page the app opens (a preview, a sign-in) can steer it back.
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		inner.ServeHTTP(w, r)
	})
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func Error(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// WriteJSONGzip is WriteJSON, gzipped for a client that accepts it when the
// reply is big enough to be worth it.
func WriteJSONGzip(w http.ResponseWriter, r *http.Request, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	b = append(b, '\n')
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Add("Vary", "Accept-Encoding")
	if len(b) >= 1024 && AcceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		b = gzipped(b)
	}
	w.WriteHeader(status)
	w.Write(b)
}

func AcceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(name, "gzip") && strings.ReplaceAll(params, " ", "") != "q=0" {
			return true
		}
	}
	return false
}

func gzipped(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

type asset struct {
	body, gz           []byte
	etag, gzTag, ctype string
	version            string // content hash, the ?v= the pages name it by
}

// assetRef is a same-origin src or href in a page.
var assetRef = regexp.MustCompile(`((?:src|href)=")/([^"?#]+)"`)

// Static serves an embedded single-page app: files by name with ETags, and
// index.html for any extensionless path so client-side routes reload.
// Pages name each asset by content hash (/app.js?v=...), and that URL is
// cached for good; the pages themselves are revalidated every time. Text
// files go out gzipped to clients that accept it, compressed once here.
func Static(fsys fs.FS) http.Handler {
	assets := map[string]*asset{}
	var pages []string
	fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil
		}
		assets[p] = &asset{body: b}
		if path.Ext(p) == ".html" {
			pages = append(pages, p)
		}
		return nil
	})
	finish := func(name string, a *asset) {
		sum := sha256.Sum256(a.body)
		a.version = hex.EncodeToString(sum[:8])
		a.etag = `"` + a.version + `"`
		a.ctype = mime.TypeByExtension(path.Ext(name))
		switch {
		case path.Ext(name) == ".webmanifest":
			a.ctype = "application/manifest+json"
		case a.ctype == "":
			a.ctype = "application/octet-stream"
		}
		if compressible(a.ctype) {
			if gz := gzipped(a.body); len(gz) < len(a.body)*9/10 {
				a.gz, a.gzTag = gz, `"`+a.version+`-gz"`
			}
		}
	}
	for name, a := range assets {
		if path.Ext(name) != ".html" {
			finish(name, a)
		}
	}
	for _, name := range pages {
		a := assets[name]
		a.body = assetRef.ReplaceAllFunc(a.body, func(m []byte) []byte {
			sm := assetRef.FindSubmatch(m)
			if ref, ok := assets[string(sm[2])]; ok && ref.version != "" {
				return []byte(string(sm[1]) + "/" + string(sm[2]) + "?v=" + ref.version + `"`)
			}
			return m
		})
		finish(name, a)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" || (path.Ext(name) == "" && assets[name] == nil) {
			name = "index.html"
		}
		a, ok := assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", a.ctype)
		if path.Ext(name) != ".html" && r.URL.Query().Get("v") == a.version {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		body, tag := a.body, a.etag
		if a.gz != nil {
			h.Add("Vary", "Accept-Encoding")
			if AcceptsGzip(r) {
				body, tag = a.gz, a.gzTag
				h.Set("Content-Encoding", "gzip")
			}
		}
		h.Set("ETag", tag)
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Write(body)
	})
}

func compressible(ctype string) bool {
	return strings.HasPrefix(ctype, "text/") || strings.Contains(ctype, "javascript") ||
		strings.Contains(ctype, "json") || strings.Contains(ctype, "svg")
}

// ServeSSE streams each value from events as a JSON data line until the
// client goes away, with a comment ping every keepalive.
func ServeSSE[T any](w http.ResponseWriter, r *http.Request, events <-chan T, keepalive time.Duration) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	rc := http.NewResponseController(w)
	fmt.Fprint(w, ": hello\n\n")
	rc.Flush()
	tick := time.NewTicker(keepalive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
		case ev, ok := <-events:
			if !ok {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		if rc.Flush() != nil {
			return
		}
	}
}
