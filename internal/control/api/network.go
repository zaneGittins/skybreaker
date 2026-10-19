package api

import (
	"net/http"
	"slices"

	"github.com/zaneGittins/skybreaker/internal/proxy/approval"
	"github.com/zaneGittins/skybreaker/internal/proxy/audit"
	"github.com/zaneGittins/skybreaker/internal/tsauth"
	"github.com/zaneGittins/skybreaker/internal/webutil"
)

// pendingRequests lists the broker's requests waiting on the user, for one
// project or ("") all.
func (s *server) pendingRequests(project string) []approval.Approval {
	var out []approval.Approval
	for _, a := range s.Network.Approvals() {
		if a.State == approval.StatePending && (project == "" || a.Project == project) {
			out = append(out, a)
		}
	}
	return out
}

// requests lists pending and recently decided requests. Each card carries
// the broker's own parse of the request (view), never the agent's words.
func (s *server) requests(w http.ResponseWriter, r *http.Request) {
	webutil.WriteJSON(w, http.StatusOK, nonNil(s.Network.Approvals()))
}

func (s *server) decideRequest(w http.ResponseWriter, r *http.Request) {
	var approve bool
	switch r.PathValue("verb") {
	case "approve":
		approve = true
	case "deny":
	default:
		webutil.Error(w, http.StatusNotFound, "approve or deny")
		return
	}
	p, _ := tsauth.FromContext(r.Context())
	a, err := s.Network.Decide(r.PathValue("id"), approve, p.Name())
	if err != nil {
		fail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, a)
}

// activity is the security page: the broker's recent decisions, newest
// first, and the sandboxes' live tokens.
func (s *server) activity(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	entries, err := s.Network.Audit(1000)
	if err != nil {
		fail(w, err)
		return
	}
	log := []audit.Entry{}
	for _, e := range slices.Backward(entries) {
		if project == "" || e.Project == project {
			log = append(log, e)
		}
		if len(log) == 300 {
			break
		}
	}
	webutil.WriteJSONGzip(w, r, http.StatusOK, map[string]any{"log": log, "tokens": nonNil(s.Network.Tokens())})
}

func (s *server) revokeToken(w http.ResponseWriter, r *http.Request) {
	p, _ := tsauth.FromContext(r.Context())
	if err := s.Network.RevokeToken(r.PathValue("id"), p.Name()); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// kill revokes every sandbox token at once. It asks for a typed phrase so a
// stray tap can't fire it.
func (s *server) kill(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirm string `json:"confirm"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Confirm != "revoke all" {
		webutil.Error(w, http.StatusBadRequest, `send {"confirm": "revoke all"}`)
		return
	}
	p, _ := tsauth.FromContext(r.Context())
	n, err := s.Network.RevokeAll(p.Name())
	if err != nil {
		fail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]int{"revoked": n})
}
