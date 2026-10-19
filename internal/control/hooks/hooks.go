// Package hooks receives Claude Code hook calls from inside the sandboxes and
// turns PermissionRequest into an approval card in the UI. These are harness
// approvals: steering, not containment. The sandbox and the broker are the
// boundary, so a timeout simply hands the prompt back to the TUI.
package hooks

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/webutil"
)

type Kind string

const (
	KindPermission   Kind = "permission"
	KindDecided      Kind = "decided"
	KindNotification Kind = "notification"
	KindStop         Kind = "stop"
)

// Event is what the UI and notifications hear about.
type Event struct {
	Kind       Kind            `json:"kind"`
	Project    string          `json:"project"`
	Session    string          `json:"session"`
	Tool       string          `json:"tool,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Message    string          `json:"message,omitempty"`
	ApprovalID string          `json:"approval_id,omitempty"`
	Allowed    bool            `json:"allowed,omitempty"`
	Time       time.Time       `json:"time"`
}

type Pending struct {
	ID      string          `json:"id"`
	Project string          `json:"project"`
	Session string          `json:"session"`
	Tool    string          `json:"tool"`
	Input   json.RawMessage `json:"input"`
	Created time.Time       `json:"created"`
}

type Decision struct {
	Allow   bool
	Message string
	By      string
}

var ErrNotFound = errors.New("approval not found or already decided")

type waiter struct {
	Pending
	decided chan Decision
}

type Handler struct {
	// Timeout is how long a permission request waits for a tap before the
	// TUI's own prompt takes over. It stays under Claude's 600s hook limit.
	Timeout time.Duration
	// AllowSource, when set, must accept the caller's address for the
	// project: a sandbox can only call in from its own network.
	AllowSource func(project string, remote netip.Addr) bool
	// MaxPending caps held permission requests per project; past it they
	// fall back to the TUI at once, so a sandbox can't pile them up.
	MaxPending int
	// Direct, when set, names sessions that answer their own prompts: chat
	// sessions, whose harness asks over its structured protocol once the
	// hook returns no decision. Their hooks raise nothing here. The session
	// header is the sandbox's word, but claiming one only hands the prompt
	// back to the harness.
	Direct func(project, session string) bool

	auth    func(project, token string) bool
	onEvent func(Event)
	mux     *http.ServeMux

	mu      sync.Mutex
	pending map[string]*waiter
}

// New returns a handler for POST /hooks/claude/{project}/{token}. auth checks
// the per-project hook token; onEvent may be nil.
func New(auth func(project, token string) bool, onEvent func(Event)) *Handler {
	if onEvent == nil {
		onEvent = func(Event) {}
	}
	h := &Handler{Timeout: 590 * time.Second, MaxPending: 16, auth: auth, onEvent: onEvent, mux: http.NewServeMux(), pending: map[string]*waiter{}}
	h.mux.HandleFunc("POST /hooks/claude/{project}/{token}", h.claude)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

var sessionRe = regexp.MustCompile(`^[A-Za-z0-9_-]{0,64}$`)

// hookInput is the subset of Claude Code's hook payload we use.
type hookInput struct {
	Event     string          `json:"hook_event_name"`
	SessionID string          `json:"session_id"`
	Tool      string          `json:"tool_name"`
	Input     json.RawMessage `json:"tool_input"`
	Message   string          `json:"message"`
}

func (h *Handler) claude(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	if h.AllowSource != nil {
		ap, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil || !h.AllowSource(project, ap.Addr().Unmap()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	if !h.auth(project, r.PathValue("token")) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	session := r.Header.Get("X-Skybreaker-Session")
	if !sessionRe.MatchString(session) {
		http.Error(w, "bad session", http.StatusBadRequest)
		return
	}
	var in hookInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&in); err != nil {
		http.Error(w, "bad hook payload", http.StatusBadRequest)
		return
	}
	if h.Direct != nil && session != "" && h.Direct(project, session) {
		webutil.WriteJSON(w, http.StatusOK, struct{}{})
		return
	}
	ev := Event{Project: project, Session: session, Tool: in.Tool, Input: in.Input, Message: in.Message, Time: time.Now()}
	switch in.Event {
	case "PermissionRequest":
		h.permission(w, r, ev)
		return
	case "Notification":
		ev.Kind = KindNotification
		h.onEvent(ev)
	case "Stop":
		ev.Kind = KindStop
		h.onEvent(ev)
	}
	webutil.WriteJSON(w, http.StatusOK, struct{}{})
}

func (h *Handler) permission(w http.ResponseWriter, r *http.Request, ev Event) {
	p, room := h.registerIfRoom(Pending{Project: ev.Project, Session: ev.Session, Tool: ev.Tool, Input: ev.Input, Created: ev.Time})
	if !room {
		webutil.WriteJSON(w, http.StatusOK, struct{}{})
		return
	}
	ev.Kind, ev.ApprovalID = KindPermission, p.ID
	h.onEvent(ev)

	timer := time.NewTimer(h.Timeout)
	defer timer.Stop()
	var (
		d  Decision
		ok bool
	)
	select {
	case d = <-p.decided:
		ok = true
	case <-timer.C:
		d, ok = h.expire(p)
	case <-r.Context().Done():
		d, ok = h.expire(p)
	}
	if !ok {
		// Nobody answered: return no decision, so Claude shows its own
		// prompt in the terminal.
		h.onEvent(Event{Kind: KindDecided, Project: ev.Project, Session: ev.Session, ApprovalID: p.ID, Message: "expired", Time: time.Now()})
		webutil.WriteJSON(w, http.StatusOK, struct{}{})
		return
	}
	decision := map[string]string{"behavior": "deny"}
	if d.Allow {
		decision["behavior"] = "allow"
	} else if d.Message != "" {
		decision["message"] = d.Message
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": decision}})
}

// registerIfRoom holds a new request unless its project already has
// MaxPending waiting. Counting and inserting share one critical section.
func (h *Handler) registerIfRoom(p Pending) (*waiter, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, w := range h.pending {
		if w.Project == p.Project {
			n++
		}
	}
	if n >= h.MaxPending {
		return nil, false
	}
	p.ID = newID()
	wt := &waiter{Pending: p, decided: make(chan Decision, 1)}
	h.pending[p.ID] = wt
	return wt, true
}

// expire withdraws a request whose wait ended. If Decide already took it,
// its decision is on the way and wins.
func (h *Handler) expire(p *waiter) (Decision, bool) {
	h.mu.Lock()
	_, still := h.pending[p.ID]
	delete(h.pending, p.ID)
	h.mu.Unlock()
	if still {
		return Decision{}, false
	}
	return <-p.decided, true
}

// Decide answers a pending permission request.
func (h *Handler) Decide(id string, d Decision) error {
	h.mu.Lock()
	p, ok := h.pending[id]
	delete(h.pending, id)
	h.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	p.decided <- d
	h.onEvent(Event{Kind: KindDecided, Project: p.Project, Session: p.Session, ApprovalID: id, Allowed: d.Allow, Message: d.By, Time: time.Now()})
	return nil
}

// Pending lists waiting requests for a project ("" for all), oldest first.
func (h *Handler) Pending(project string) []Pending {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Pending
	for _, p := range h.pending {
		if project == "" || p.Project == project {
			out = append(out, p.Pending)
		}
	}
	slices.SortFunc(out, func(a, b Pending) int { return a.Created.Compare(b.Created) })
	return out
}

// ClaudeSettings renders /etc/claude-code/managed-settings.json for a
// sandbox. Managed settings outrank user and project settings, and the file
// is mounted read-only, so an agent can't remove its own hooks. The hooks are
// commands rather than HTTP hooks so they can carry $SKYBREAKER_SESSION, which
// tmux sets per session.
func ClaudeSettings(hookURL string) ([]byte, error) {
	if strings.ContainsAny(hookURL, "'\\\n\r\x00 ") || !strings.HasPrefix(hookURL, "http://") {
		return nil, fmt.Errorf("hooks: unsafe hook URL %q", hookURL)
	}
	cmd := func(maxTime int) string {
		// --fail turns a refusal into a non-zero exit, which Claude treats
		// as a non-blocking hook error.
		return fmt.Sprintf(`curl -sS --fail --noproxy '*' --max-time %d -X POST -H 'Content-Type: application/json' -H "X-Skybreaker-Session: ${SKYBREAKER_SESSION:-}" --data-binary @- '%s'`, maxTime, hookURL)
	}
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type group struct {
		Matcher string `json:"matcher,omitempty"`
		Hooks   []hook `json:"hooks"`
	}
	settings := map[string]any{"hooks": map[string][]group{
		"PermissionRequest": {{Matcher: "*", Hooks: []hook{{"command", cmd(595), 600}}}},
		"Notification":      {{Hooks: []hook{{"command", cmd(10), 15}}}},
		"Stop":              {{Hooks: []hook{{"command", cmd(10), 15}}}},
	}}
	return json.MarshalIndent(settings, "", "  ")
}

func newID() string {
	b := make([]byte, 10)
	rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}
