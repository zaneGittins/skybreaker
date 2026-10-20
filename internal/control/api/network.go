package api

import (
	"net/http"
	"slices"
	"strconv"

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
// activityPage is how many audit entries a page of the activity log holds.
const activityPage = 50

// activityScan bounds how far back the log is read for one page; older
// entries stay in the audit log file.
const activityScan = 20000

// activity is a page of the broker's audit log, newest first, filtered to a
// project or to what was blocked, and the sandboxes' tokens.
func (s *server) activity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	project, blocked := q.Get("project"), q.Get("blocked") == "1"
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	want := page*activityPage + 1 // one more says whether there's a next page
	var matched []audit.Entry
	for n := want; ; n = min(n*4, activityScan) {
		entries, err := s.Network.Audit(n)
		if err != nil {
			fail(w, err)
			return
		}
		matched = matched[:0]
		for _, e := range slices.Backward(entries) {
			if (project == "" || e.Project == project) && (!blocked || e.Decision == "deny") {
				matched = append(matched, e)
			}
		}
		if len(matched) >= want || len(entries) < n || n == activityScan {
			break
		}
	}
	log := []audit.Entry{}
	if lo := (page - 1) * activityPage; lo < len(matched) {
		log = matched[lo:min(lo+activityPage, len(matched))]
	}
	webutil.WriteJSONGzip(w, r, http.StatusOK, map[string]any{
		"log": log, "page": page, "more": len(matched) >= want, "tokens": nonNil(s.Network.Tokens()),
	})
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
