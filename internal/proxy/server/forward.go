package server

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zaneGittins/skybreaker/internal/proxy/approval"
	"github.com/zaneGittins/skybreaker/internal/proxy/audit"
	"github.com/zaneGittins/skybreaker/internal/proxy/config"
	"github.com/zaneGittins/skybreaker/internal/proxy/gitproto"
	"github.com/zaneGittins/skybreaker/internal/proxy/graphql"
	"github.com/zaneGittins/skybreaker/internal/proxy/k8s"
	"github.com/zaneGittins/skybreaker/internal/proxy/netguard"
	"github.com/zaneGittins/skybreaker/internal/proxy/policy"
	"github.com/zaneGittins/skybreaker/internal/proxy/secrets"
	"github.com/zaneGittins/skybreaker/internal/webutil"
)

const maxAskBody = 4 << 20 // held in memory while waiting, and hashed into the approval

// target is everything forward needs to know about where a request goes and
// which policy applies.
type target struct {
	peer        sender
	token       string // the session token that authorised the request
	pp          *config.ProjectPolicy
	mode        string
	scheme      string
	host        string
	port        int
	escapedPath string // upstream path, route prefix already removed
	routePrefix string // the route this came in on, for routes
	rules       *policy.Set
	k8s         *k8s.Policy
	inject      *config.Inject
	guard       *netguard.Guard
	roots       *x509.CertPool // nil: the server's default roots
	transport   string         // transport cache key: guard and roots differ per route
}

// credentialHeaders are headers that carry a credential. A request may only
// carry the placeholder in them; the proxy is the only source of real ones.
var credentialHeaders = []string{"Authorization", "X-Api-Key", "Api-Key", "X-Goog-Api-Key", "Private-Token", "X-Auth-Token"}

// credentialParams are query parameters that APIs accept as credentials
// (Google's ?key=, OAuth's ?access_token=). Matched case-insensitively.
var credentialParams = map[string]bool{"key": true, "api_key": true, "apikey": true, "access_token": true, "private_token": true, "client_secret": true}

// foreignCredential reports a credential the agent brought itself. Header
// names with "_" are refused outright: servers that map headers to CGI-style
// names read X_Api_Key as X-Api-Key, and real clients don't send them.
func foreignCredential(r *http.Request) string {
	for name := range r.Header {
		if strings.Contains(name, "_") {
			return "header name with an underscore: " + name
		}
	}
	for _, h := range credentialHeaders {
		for _, v := range r.Header.Values(h) {
			if !isPlaceholder(v) {
				return "request carries its own credential in " + h
			}
		}
	}
	// Split on ';' as well as '&': url.Query drops pairs with a ';', and
	// some servers split on it.
	for _, pair := range strings.FieldsFunc(r.URL.RawQuery, func(c rune) bool { return c == '&' || c == ';' }) {
		name, _, _ := strings.Cut(pair, "=")
		if n, err := url.QueryUnescape(name); err == nil {
			name = n
		}
		if credentialParams[strings.ToLower(name)] {
			return "request carries a credential in the query parameter " + name
		}
	}
	return ""
}

const upgradeRefused = "protocol upgrades (kubectl exec, attach and port-forward, websockets) are not proxied"

// isUpgrade reports a request to switch protocols. What follows the switch
// can't be inspected, and stripping the upgrade would only turn it into a
// different request, so these are refused outright.
func isUpgrade(r *http.Request) bool {
	if r.Header.Get("Upgrade") != "" {
		return true
	}
	for _, f := range r.Header.Values("Connection") {
		for _, name := range strings.Split(f, ",") {
			if strings.EqualFold(textproto.TrimString(name), "upgrade") {
				return true
			}
		}
	}
	return false
}

var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, t target) {
	// Revoking the token cancels the request wherever it is: held for an
	// approval, or streaming a response.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer s.trackRequest(t.token, cancel)()
	r = r.WithContext(ctx)
	e := audit.Entry{Sender: t.peer.Name(), Project: t.pp.Name, Mode: t.mode, Host: t.host, Method: r.Method, Path: t.escapedPath}
	path, ok := policy.CleanPath(t.escapedPath)
	if !ok {
		s.deny(w, e, http.StatusBadRequest, "path is not canonical (dot segments, empty segments or encoded separators)")
		return
	}
	// Refuse agent-brought credentials before anything else, so such a
	// request never reaches an approver either. Proxy-Authorization carried
	// the session token and is ours.
	r.Header.Del("Proxy-Authorization")
	if reason := foreignCredential(r); reason != "" {
		s.deny(w, e, http.StatusForbidden, reason)
		return
	}
	if reason := impersonation(r); reason != "" {
		s.deny(w, e, http.StatusForbidden, reason)
		return
	}
	// The card shows one content type; an upstream that read another would
	// act on something the approver never saw.
	if len(r.Header.Values("Content-Type")) > 1 {
		s.deny(w, e, http.StatusBadRequest, "more than one Content-Type")
		return
	}
	if isUpgrade(r) {
		s.deny(w, e, http.StatusNotImplemented, upgradeRefused)
		return
	}

	// 1. Decide.
	var (
		action policy.Action
		reason string
		rule   *policy.Rule
		view   []approval.Field
		body   []byte
		read   bool // body has been read into body
	)
	switch {
	case t.k8s != nil:
		attrs, err := k8s.Parse(r.Method, path, r.URL.Query())
		if err != nil {
			s.deny(w, e, http.StatusBadRequest, "kubernetes request: "+err.Error())
			return
		}
		action, reason = t.k8s.Decide(attrs)
		view = append(view, approval.Field{Label: "Kubernetes", Value: fmt.Sprintf("%s %s/%s ns=%s name=%s", attrs.Verb, attrs.Resource, attrs.Subresource, attrs.Namespace, attrs.Name)})
	case t.rules != nil:
		d := t.rules.Decide(r.Method, path)
		action, reason, rule = d.Action, d.Reason, d.Rule
	default:
		action, reason = policy.Deny, "no policy"
	}
	if action != policy.Auto && action != policy.Ask {
		s.deny(w, e, http.StatusForbidden, reason)
		return
	}

	// 2a. GraphQL: one endpoint does everything, so the rule looks at the
	// operation. Queries and the rule's listed mutations keep its action;
	// any other operation gets the rule's graphql.other; a body that can't
	// be read unambiguously is refused.
	if rule != nil && rule.GraphQL != nil {
		// The operation is judged from the body alone; a server that also
		// reads query or operationName from the URL would run something
		// else, so nothing may ride there.
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			s.deny(w, e, http.StatusBadRequest, "GraphQL requests carry their operation in the body, not the URL")
			return
		}
		if r.Header.Get("Content-Encoding") != "" {
			s.deny(w, e, http.StatusBadRequest, "compressed GraphQL bodies are not inspected")
			return
		}
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			s.deny(w, e, http.StatusUnsupportedMediaType, "GraphQL requests must be application/json")
			return
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, maxAskBody+1))
		if err != nil || len(b) > maxAskBody {
			s.deny(w, e, http.StatusRequestEntityTooLarge, "GraphQL request too large to inspect")
			return
		}
		body, read = b, true
		q, err := graphql.Parse(body)
		if err != nil {
			s.deny(w, e, http.StatusBadRequest, err.Error())
			return
		}
		what := q.Op + " " + strings.Join(q.Fields, ", ")
		if q.Indirect {
			what += " (and fragments)"
		}
		view = append(view, approval.Field{Label: "GraphQL", Value: strings.TrimSpace(what)})
		listed := q.Op == "query" || q.Op == "mutation" && !q.Indirect && len(q.Fields) > 0 &&
			!slices.ContainsFunc(q.Fields, func(f string) bool { return !slices.Contains(rule.GraphQL.Mutations, f) })
		if !listed {
			action, reason = rule.GraphQL.OtherAction(), "GraphQL "+strings.TrimSpace(what)+" is not on the rule's list"
			if action != policy.Ask {
				s.deny(w, e, http.StatusForbidden, reason)
				return
			}
		}
	}

	// 2. Ref policy for git pushes, read from the start of the pack stream.
	if rule != nil && len(rule.Refs) > 0 {
		if r.Header.Get("Content-Encoding") != "" {
			s.deny(w, e, http.StatusBadRequest, "compressed receive-pack bodies are not inspected")
			return
		}
		src := r.Body
		if read {
			src = io.NopCloser(bytes.NewReader(body))
		}
		refs, rest, err := gitproto.Peek(src)
		if err == nil && len(refs) > 0 { // no refs: git's probe request
			err = gitproto.Check(refs, rule.Refs)
		}
		if err != nil {
			s.deny(w, e, http.StatusForbidden, err.Error())
			return
		}
		r.Body = rest
		for _, u := range refs {
			view = append(view, approval.Field{Label: "Ref", Value: fmt.Sprintf("%s %s → %s", u.Ref, short(u.Old), short(u.New))})
		}
	}

	// What goes upstream: the request's own headers, less hop-by-hop ones,
	// the placeholders and cookies. These are what an approval binds.
	out := r.Header.Clone()
	stripHop(out)
	for _, h := range credentialHeaders {
		out.Del(h)
	}
	out.Del("Cookie")

	// 3. Ask: hold the request until approved, or answer 403 with an ID an
	// identical retry can use once approved.
	if action == policy.Ask {
		if !read {
			b, err := io.ReadAll(io.LimitReader(r.Body, maxAskBody+1))
			if err != nil || len(b) > maxAskBody {
				s.deny(w, e, http.StatusRequestEntityTooLarge, "request body too large to approve")
				return
			}
			body, read = b, true
		}
		target := t.escapedPath
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		// Headers change what a request means (a k8s merge patch versus an
		// apply, an If-Match, an API version), and the route, port and
		// secret decide where it lands and with whose authority, so all of
		// them are bound into the approval along with the body.
		ct := r.Header.Get("Content-Type")
		view = append(view, approval.Field{Label: "Content-Type", Value: ct})
		for _, name := range slices.Sorted(maps.Keys(out)) {
			if !plainHeaders[name] {
				view = append(view, approval.Field{Label: "Header", Value: name + ": " + strings.Join(out[name], ", ")})
			}
		}
		if t.routePrefix != "" {
			view = append(view, approval.Field{Label: "Route", Value: t.routePrefix})
		}
		if t.port != 443 {
			view = append(view, approval.Field{Label: "Port", Value: strconv.Itoa(t.port)})
		}
		bound := approvalBinding(t) + "headers: " + headerBinding(out) + "\n"
		req := approval.Request{
			Project: t.pp.Name, Method: r.Method, Host: t.host, Path: target, Sender: t.peer.Name(),
			Body: append([]byte(bound), body...),
			View: s.view(r.Method, t, target, reason, view, body),
		}
		a, outcome := s.approvals.Request(r.Context(), req, s.hold)
		e.ApprovalID = a.ID
		switch outcome {
		case approval.Allowed:
			e.By = a.DecidedBy
		case approval.Denied:
			s.deny(w, e, http.StatusForbidden, "denied by "+a.DecidedBy)
			return
		case approval.Throttled:
			s.deny(w, e, http.StatusTooManyRequests, "too many approvals waiting for this project")
			return
		default:
			e.Decision, e.Reason, e.Status = "ask", reason, http.StatusForbidden
			s.log(e)
			w.Header().Set("X-Skybreaker-Approval", a.ID)
			webutil.WriteJSON(w, http.StatusForbidden, map[string]string{
				"error":       "approval required; the user approves it in Skybreaker, then retry the identical request",
				"approval_id": a.ID,
				"approve_at":  s.uiURL,
			})
			return
		}
	}

	// The session may have ended while the request was held: a revoked token
	// gets no credential, whatever was approved.
	if !s.live(t.token, t.pp.Name) {
		s.deny(w, e, http.StatusProxyAuthRequired, "session revoked or expired")
		return
	}

	// 4. Credentials: the placeholders and cookies are gone; add ours.
	if t.inject != nil {
		secret, err := s.secrets.Resolve(r.Context(), t.inject.SecretName(), t.host, secrets.Scope{Repo: t.pp.Repo})
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, secrets.ErrUnbound) {
				status = http.StatusForbidden
			}
			s.deny(w, e, status, "credential unavailable: "+err.Error())
			return
		}
		inject(out, t.inject, secret)
	}

	// 5. Forward.
	u := &url.URL{Scheme: t.scheme, Host: hostPort(t.host, t.port, t.scheme), RawQuery: r.URL.RawQuery}
	u.Path, u.RawPath = path, t.escapedPath
	var reqBody io.Reader = r.Body
	length := r.ContentLength
	if read {
		reqBody, length = bytes.NewReader(body), int64(len(body))
	}
	if length == 0 {
		reqBody = nil
	}
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), reqBody)
	if err != nil {
		s.deny(w, e, http.StatusBadRequest, "bad upstream request")
		return
	}
	upReq.Header = out
	upReq.ContentLength = length
	// Resolving the credential may have taken a round trip (an App token);
	// the session may have ended meanwhile.
	if !s.live(t.token, t.pp.Name) {
		s.deny(w, e, http.StatusProxyAuthRequired, "session revoked or expired")
		return
	}
	resp, err := s.transport(t.guard, t.roots, t.transport).RoundTrip(upReq)
	if err != nil {
		status, reason := dialStatus(err)
		if status == http.StatusBadGateway {
			reason = "upstream: " + err.Error()
		}
		s.deny(w, e, status, reason)
		return
	}
	e.Reason = reason
	s.relay(w, resp, e)
}

// relay sends an upstream response back to the client, logged as allowed.
func (s *Server) relay(w http.ResponseWriter, resp *http.Response, e audit.Entry) {
	defer resp.Body.Close()
	stripHop(resp.Header)
	maps.Copy(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	e.Decision, e.Status = "allow", resp.StatusCode
	s.log(e)
	copyFlush(w, resp.Body)
}

// approvalBinding is the part of where a request goes that the method,
// host and path don't already say.
// plainHeaders are the ones every client sends; the approval binds them
// like the rest, but the card doesn't list them.
var plainHeaders = map[string]bool{"Accept": true, "Accept-Encoding": true, "Accept-Language": true, "User-Agent": true, "Content-Type": true, "Content-Length": true}

// headerBinding encodes headers unambiguously: canonical names, sorted,
// each with its values in order.
func headerBinding(h http.Header) string {
	merged := map[string][]string{}
	for k, v := range h {
		ck := http.CanonicalHeaderKey(k)
		merged[ck] = append(merged[ck], v...)
	}
	list := [][]string{}
	for _, k := range slices.Sorted(maps.Keys(merged)) {
		list = append(list, append([]string{k}, merged[k]...))
	}
	b, _ := json.Marshal(list)
	return string(b)
}

// impersonation refuses headers that change who or what a request is:
// Kubernetes impersonation (the injected credential may hold impersonate
// rights, and a sandbox must never borrow another identity), and method
// overrides (rules judge the method on the wire; an upstream that took a
// POST for a DELETE would act on a method no rule was asked about).
func impersonation(r *http.Request) string {
	for k := range r.Header {
		switch k = http.CanonicalHeaderKey(k); {
		case strings.HasPrefix(k, "Impersonate-"):
			return "impersonation headers are refused"
		case k == "X-Http-Method-Override", k == "X-Http-Method", k == "X-Method-Override":
			return "method override headers are refused"
		}
	}
	return ""
}

func approvalBinding(t target) string {
	return fmt.Sprintf("mode=%s route=%s port=%d secret=%s\n", t.mode, t.routePrefix, t.port, t.inject.SecretName())
}

// copyFlush streams the response, flushing as data arrives so watches and
// server-sent events reach the client live.
func copyFlush(w http.ResponseWriter, r io.Reader) {
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			rc.Flush()
		}
		if err != nil {
			return
		}
	}
}

func hostPort(host string, port int, scheme string) string {
	if (scheme == "https" && port == 443) || (scheme == "http" && port == 80) || port == 0 {
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func stripHop(h http.Header) {
	for _, f := range h.Values("Connection") {
		for _, name := range strings.Split(f, ",") {
			h.Del(textproto.TrimString(name))
		}
	}
	for _, name := range hopHeaders {
		h.Del(name)
	}
}

// isPlaceholder accepts "skybreaker-injected" as a raw value, the last word of a
// scheme value ("Bearer skybreaker-injected", "token skybreaker-injected"), or either
// half of a Basic credential.
func isPlaceholder(v string) bool {
	f := strings.Fields(v)
	if len(f) == 0 {
		return true
	}
	if f[len(f)-1] == Placeholder {
		return true
	}
	if len(f) == 2 && strings.EqualFold(f[0], "Basic") {
		if b, err := base64.StdEncoding.DecodeString(f[1]); err == nil {
			user, pass, _ := strings.Cut(string(b), ":")
			return pass == Placeholder || (user == Placeholder && pass == "")
		}
	}
	return false
}

func inject(h http.Header, in *config.Inject, secret string) {
	switch {
	case in.Bearer != "":
		h.Set("Authorization", "Bearer "+secret)
	case in.BasicAuth != nil:
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(in.BasicAuth.User+":"+secret)))
	case in.Header != nil:
		h.Set(in.Header.Name, in.Header.Prefix+secret)
	}
}

// transport returns a pooled transport whose dials go through g and whose
// TLS trusts roots (nil: the server's default). Routes get their own, keyed
// by project and prefix, because their guard and CA can differ.
func (s *Server) transport(g *netguard.Guard, roots *x509.CertPool, key string) *http.Transport {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.transports[key]; ok {
		return t
	}
	t := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, portStr, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			port, _ := strconv.Atoi(portStr)
			return s.dial(ctx, g, host, port)
		},
		TLSClientConfig:       &tls.Config{RootCAs: cmp.Or(roots, s.roots), MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	s.transports[key] = t
	return t
}

func short(oid string) string {
	if len(oid) > 12 {
		return oid[:12]
	}
	return oid
}

// view renders the approval card from the proxy's own parse of the request,
// never from anything the agent says about it.
func (s *Server) view(method string, t target, path, reason string, extra []approval.Field, body []byte) approval.View {
	fields := []approval.Field{
		{Label: "Project", Value: t.pp.Name},
		{Label: "Sender", Value: t.peer.Name()},
		{Label: "Rule", Value: reason},
	}
	if t.pp.Repo != "" {
		fields = append(fields, approval.Field{Label: "Repo", Value: t.pp.Repo})
	}
	fields = append(fields, extra...)
	return approval.View{
		Title:  fmt.Sprintf("%s %s%s", method, t.host, s.secrets.Redact(path)),
		Fields: fields,
		Body:   s.secrets.Redact(preview(body)),
	}
}

// preview shows up to 64 KiB of a printable body, or a size for binary
// ones. A longer body is marked as cut, so the approver knows they haven't
// seen all of it.
func preview(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	const max = 64 << 10
	p := b
	if len(p) > max {
		p = p[:max]
	}
	for _, c := range p {
		if c < 0x09 || (c > 0x0d && c < 0x20) || c == 0x7f {
			return fmt.Sprintf("(%d bytes of binary data)", len(b))
		}
	}
	if len(b) > max {
		return string(p) + fmt.Sprintf("\n… CUT: only the first %d of %d bytes are shown", max, len(b))
	}
	return string(p)
}

// plain handles absolute-form requests (http://host/path through the
// proxy). Tunnel hosts are forwarded as they are; mitm hosts are upgraded to
// HTTPS upstream and go through the rules.
func (s *Server) plain(w http.ResponseWriter, r *http.Request, peer sender) {
	e := audit.Entry{Sender: peer.Name(), Host: r.URL.Host, Method: r.Method, Path: r.URL.EscapedPath()}
	pp, token, ok := s.proxyAuth(r)
	if !ok {
		s.needAuth(w, e)
		return
	}
	e.Project = pp.Name
	if r.URL.Scheme != "http" {
		s.deny(w, e, http.StatusBadRequest, "only http:// URLs can be proxied without CONNECT")
		return
	}
	host := strings.ToLower(strings.TrimSuffix(r.URL.Hostname(), "."))
	port := 80
	if p := r.URL.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	e.Host = host
	rule := pp.Host(host)
	if rule == nil {
		s.deny(w, e, http.StatusForbidden, "host is not on the project's allow list")
		return
	}
	e.Mode = rule.Mode
	if rule.Mode == config.ModeMITM {
		if !rule.AllowsPort(443) {
			s.deny(w, e, http.StatusForbidden, "port 443 is not allowed for "+host)
			return
		}
		s.forward(w, r, target{peer: peer, token: token, pp: pp, mode: config.ModeMITM, scheme: "https", host: host, port: 443,
			escapedPath: r.URL.EscapedPath(), rules: rule.Rules, inject: rule.Inject, guard: s.guard})
		return
	}
	if !rule.AllowsPort(port) {
		s.deny(w, e, http.StatusForbidden, fmt.Sprintf("port %d is not allowed for %s", port, host))
		return
	}
	if isUpgrade(r) {
		s.deny(w, e, http.StatusNotImplemented, upgradeRefused)
		return
	}
	// A tunnel host over plain HTTP: no rules and no injection, like a tunnel.
	// Revoking the token ends it, as it ends a tunnel's connection.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer s.trackRequest(token, cancel)()
	out := r.Clone(ctx)
	out.RequestURI = ""
	out.URL.Host = hostPort(host, port, "http")
	stripHop(out.Header)
	resp, err := s.transport(s.guard, nil, "").RoundTrip(out)
	if err != nil {
		status, reason := dialStatus(err)
		s.deny(w, e, status, reason)
		return
	}
	s.relay(w, resp, e)
}

// route serves the broker's own reverse-proxy paths (/k8s/<cluster>,
// ...). The session token comes as a Bearer token or proxy Basic
// credential and is removed before forwarding.
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	peer := senderOf(r)
	e := audit.Entry{Sender: peer.Name(), Mode: config.ModeRoute, Method: r.Method, Path: r.URL.EscapedPath()}
	var (
		pp    *config.ProjectPolicy
		token string
		ok    bool
	)
	if tok := bearer(r.Header.Get("Authorization")); tok != "" {
		if info, live := s.sessions.Lookup(tok); live {
			pp, ok = s.projectFor(r, info.Project, tok)
		}
		token = tok
		r.Header.Del("Authorization")
	} else {
		pp, token, ok = s.proxyAuth(r)
	}
	if !ok {
		e.Decision, e.Reason, e.Status = "deny", "missing or invalid session token", http.StatusUnauthorized
		s.log(e)
		webutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "a session token is required"})
		return
	}
	e.Project = pp.Name
	clean, okPath := policy.CleanPath(r.URL.EscapedPath())
	if !okPath {
		s.deny(w, e, http.StatusBadRequest, "path is not canonical")
		return
	}
	rt := pp.Route(clean)
	if rt == nil {
		s.deny(w, e, http.StatusNotFound, "no route for this path in the project's capabilities")
		return
	}
	// The policy sees the cleaned path; refuse if the escaped path that
	// goes upstream doesn't start with the same prefix (/k8s/pro%64/...).
	if escaped := r.URL.EscapedPath(); escaped != rt.Prefix && !strings.HasPrefix(escaped, rt.Prefix+"/") {
		s.deny(w, e, http.StatusBadRequest, "route prefix must not be escaped")
		return
	}
	g := s.guard
	if len(rt.AllowPrivate) > 0 {
		g = s.guard.With(rt.AllowPrivate...)
	}
	scheme, port := rt.Upstream.Scheme, 443
	if scheme == "http" {
		port = 80
	}
	if p := rt.Upstream.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	s.forward(w, r, target{
		peer: peer, token: token, pp: pp, mode: config.ModeRoute, scheme: scheme, host: rt.Upstream.Hostname(), port: port,
		escapedPath: rt.Strip(r.URL.EscapedPath()), routePrefix: rt.Prefix, rules: rt.Rules, k8s: rt.K8s,
		inject: rt.Inject, guard: g, roots: rt.RootCAs, transport: "route:" + pp.Name + ":" + rt.Prefix,
	})
}
