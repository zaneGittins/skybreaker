// Package mcp is Skybreaker's own MCP server: the board's tools, and any
// other tools Skybreaker gives agents, over Streamable HTTP. It speaks the
// small subset tool servers need (initialize, ping, tools/list, tools/call),
// answers every request with plain JSON and never opens a stream.
//
// Who is calling is decided by the mount, not the message: sandboxes reach it
// on the hook listener, where the path's project and token place them; you
// reach it on the UI, as your tailnet login. Tools see that as a Caller.
package mcp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Caller is who a request comes from.
type Caller struct {
	Project string // the sandbox's project; "" on the UI mount
	Session string // the Skybreaker chat the agent runs in; "" or "-" outside a chat
	User    string // the tailnet login, on the UI mount only
	Client  string // the MCP client's own name, such as "claude-code"
	MCP     string // the MCP session ID
}

// Tool is one tool. Call returns the text shown to the model; an error is
// returned to it as a failed call, so it can correct itself.
type Tool struct {
	Name        string
	Description string
	Input       json.RawMessage // JSON Schema of the arguments
	ReadOnly    bool
	Call        func(ctx context.Context, c Caller, args json.RawMessage) (string, error)
}

type Server struct {
	Name, Version, Instructions string
	// Tools are the tool sets; each says which tools a caller gets.
	Tools []func(Caller) []Tool

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	caller Caller // the mount's caller when the session began
	seen   time.Time
}

// sessionTTL ends sessions from clients that went away without saying so.
const sessionTTL = 24 * time.Hour

const maxBody = 1 << 20

var versions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Handler serves the server at one mount. resolve decides who the request
// is from, or refuses it.
func (s *Server) Handler(resolve func(*http.Request) (Caller, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, err := resolve(r)
		if err != nil {
			// Not 403: Claude Code reads a 403 as "needs sign-in" and stops
			// trying the server for minutes.
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPost:
			s.post(w, r, caller)
		case http.MethodDelete:
			s.mu.Lock()
			if sess, ok := s.sessions[r.Header.Get("Mcp-Session-Id")]; ok && sameMount(sess.caller, caller) {
				delete(s.sessions, r.Header.Get("Mcp-Session-Id"))
			}
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			// No server-initiated stream: tools only answer.
			w.Header().Set("Allow", "POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// sameMount reports whether a session is used from where it began, so a
// session ID can't carry one project's caller into another's.
func sameMount(a, b Caller) bool {
	return a.Project == b.Project && a.Session == b.Session && a.User == b.User
}

func (s *Server) post(w http.ResponseWriter, r *http.Request, caller Caller) {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		http.Error(w, "send application/json", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	var req request
	dec := json.NewDecoder(strings.NewReader(string(body)))
	if err := dec.Decode(&req); err != nil || req.JSONRPC != "2.0" || req.Method == "" || dec.More() {
		// Batches went away in 2025-06-18 and are refused here too.
		writeJSON(w, http.StatusBadRequest, response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32600, "send one JSON-RPC 2.0 request"}})
		return
	}

	if req.Method == "initialize" {
		s.initialize(w, req, caller)
		return
	}
	id := r.Header.Get("Mcp-Session-Id")
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if ok && (!sameMount(sess.caller, caller) || time.Since(sess.seen) > sessionTTL) {
		ok = false
	}
	if ok {
		sess.seen = time.Now()
		caller.Client = sess.caller.Client
	}
	s.mu.Unlock()
	if !ok {
		// The client starts a new session on 404.
		http.Error(w, "unknown or expired session", http.StatusNotFound)
		return
	}
	caller.MCP = id

	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted) // a notification
		return
	}
	var result any
	var rerr *rpcError
	switch req.Method {
	case "ping":
		result = struct{}{}
	case "tools/list":
		result = map[string]any{"tools": s.list(caller)}
	case "tools/call":
		result, rerr = s.call(r.Context(), caller, req.Params)
	default:
		rerr = &rpcError{-32601, "method not found: " + req.Method}
	}
	writeJSON(w, http.StatusOK, response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rerr})
}

func (s *Server) initialize(w http.ResponseWriter, req request, caller Caller) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	json.Unmarshal(req.Params, &p)
	version := versions[0]
	if slices.Contains(versions, p.ProtocolVersion) {
		version = p.ProtocolVersion
	}
	caller.Client = p.ClientInfo.Name
	b := make([]byte, 18)
	rand.Read(b)
	id := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = map[string]*session{}
	}
	now := time.Now()
	for k, x := range s.sessions {
		if now.Sub(x.seen) > sessionTTL {
			delete(s.sessions, k)
		}
	}
	s.sessions[id] = &session{caller: caller, seen: now}
	s.mu.Unlock()
	w.Header().Set("Mcp-Session-Id", id)
	writeJSON(w, http.StatusOK, response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]string{"name": s.Name, "version": s.Version},
		"instructions":    s.Instructions,
	}})
}

func (s *Server) tools(c Caller) []Tool {
	var out []Tool
	for _, set := range s.Tools {
		out = append(out, set(c)...)
	}
	return out
}

func (s *Server) list(c Caller) []map[string]any {
	out := []map[string]any{}
	for _, t := range s.tools(c) {
		m := map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Input}
		if t.ReadOnly {
			m["annotations"] = map[string]bool{"readOnlyHint": true}
		}
		out = append(out, m)
	}
	return out
}

func (s *Server) call(ctx context.Context, c Caller, raw json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Name == "" {
		return nil, &rpcError{-32602, "tools/call needs a name"}
	}
	i := slices.IndexFunc(s.tools(c), func(t Tool) bool { return t.Name == p.Name })
	if i < 0 {
		return nil, &rpcError{-32602, "unknown tool: " + p.Name}
	}
	if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
		p.Arguments = json.RawMessage("{}")
	}
	text, err := s.tools(c)[i].Call(ctx, c, p.Arguments)
	if err != nil {
		return map[string]any{"content": []map[string]string{{"type": "text", "text": err.Error()}}, "isError": true}, nil
	}
	return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}, nil
}

// Args decodes a tool's arguments strictly: an argument the tool doesn't
// know is a mistake worth telling the model about.
func Args(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("bad arguments: " + err.Error())
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
