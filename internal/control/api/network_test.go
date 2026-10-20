package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/proxy/approval"
	"github.com/zaneGittins/skybreaker/internal/proxy/audit"
	"github.com/zaneGittins/skybreaker/internal/proxy/broker"
	"github.com/zaneGittins/skybreaker/internal/proxy/session"
)

type fakeNetwork struct {
	mu        sync.Mutex
	approvals []approval.Approval
	tokens    []session.Info
	decided   map[string]string // id → "approve by" / "deny by"
	revoked   []string
	killed    string
	log       []audit.Entry // oldest first, as the audit log holds them; nil for two
}

func newFakeNetwork() *fakeNetwork {
	now := time.Now()
	return &fakeNetwork{
		approvals: []approval.Approval{
			{ID: "n1", Project: "orbit", Method: "PUT", Host: "api.github.com", Path: "/repos/zaneGittins/orbit/pulls/7/merge", State: approval.StatePending, Created: now, Expires: now.Add(time.Minute),
				View: approval.View{Title: "Merge pull request #7", Fields: []approval.Field{{Label: "Repo", Value: "zaneGittins/orbit"}}}},
			{ID: "n0", Project: "orbit", Method: "PUT", Host: "api.github.com", Path: "/x", State: approval.StateDenied, Created: now.Add(-time.Minute), DecidedBy: "zane@example.com"},
		},
		tokens:  []session.Info{{ID: "s1", Project: "orbit", Created: now, Expires: now.Add(time.Hour)}},
		decided: map[string]string{},
	}
}

func (f *fakeNetwork) Approvals() []approval.Approval {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]approval.Approval(nil), f.approvals...)
}

func (f *fakeNetwork) Decide(id string, approve bool, by string) (approval.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.approvals {
		if a.ID != id {
			continue
		}
		if a.State != approval.StatePending {
			return a, approval.ErrNotPending
		}
		verb := "deny"
		f.approvals[i].State = approval.StateDenied
		if approve {
			verb = "approve"
			f.approvals[i].State = approval.StateApproved
		}
		f.decided[id] = verb + " " + by
		return f.approvals[i], nil
	}
	return approval.Approval{}, approval.ErrNotFound
}

func (f *fakeNetwork) Tokens() []session.Info { return f.tokens }

func (f *fakeNetwork) RevokeToken(id, by string) error {
	if id != "s1" {
		return broker.ErrNotFound
	}
	f.revoked = append(f.revoked, id+" "+by)
	return nil
}

func (f *fakeNetwork) RevokeAll(by string) (int, error) {
	f.killed = by
	return 1, nil
}

func (f *fakeNetwork) Audit(n int) ([]audit.Entry, error) {
	if f.log == nil {
		return []audit.Entry{
			{Decision: "allow", Project: "bifract", Host: "proxy.golang.org"},
			{Decision: "deny", Project: "orbit", Host: "evil.example", Reason: "host is not on the project's allow list"},
		}, nil
	}
	return f.log[max(0, len(f.log)-n):], nil
}

func TestNetworkRequests(t *testing.T) {
	f := newFixture(t, you)
	code, body := f.do(t, "GET", "/api/requests", "", nil)
	if code != 200 || !strings.Contains(body, "Merge pull request #7") {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, _ := f.do(t, "POST", "/api/requests/n1/approve", `{}`, nil); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	if f.network.decided["n1"] != "approve zane@example.com" {
		t.Fatalf("decision = %q", f.network.decided["n1"])
	}
	if code, _ := f.do(t, "POST", "/api/requests/n1/deny", `{}`, nil); code != 409 {
		t.Errorf("decided twice: %d", code)
	}
	if code, _ := f.do(t, "POST", "/api/requests/zz/approve", `{}`, nil); code != 404 {
		t.Errorf("unknown request: %d", code)
	}
	if code, _ := f.do(t, "POST", "/api/requests/n1/maybe", `{}`, nil); code != 404 && code != 405 {
		t.Errorf("unknown verb: %d", code)
	}
}

func TestNetworkInInboxAndProject(t *testing.T) {
	f := newFixture(t, you)
	var in struct {
		Requests []approval.Approval `json:"requests"`
		Projects []projectView       `json:"projects"`
	}
	_, body := f.do(t, "GET", "/api/inbox", "", nil)
	json.Unmarshal([]byte(body), &in)
	if len(in.Requests) != 1 || in.Requests[0].ID != "n1" {
		t.Fatalf("inbox requests: %s", body)
	}
	if len(in.Projects) != 1 || in.Projects[0].Pending != 2 {
		t.Errorf("pending count should include the network request: %s", body)
	}
	_, body = f.do(t, "GET", "/api/projects/orbit", "", nil)
	if !strings.Contains(body, `"requests":[{"id":"n1"`) {
		t.Errorf("project: %s", body)
	}
}

func TestActivity(t *testing.T) {
	f := newFixture(t, you)
	code, body := f.do(t, "GET", "/api/activity", "", nil)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var out struct {
		Log    []audit.Entry  `json:"log"`
		Tokens []session.Info `json:"tokens"`
	}
	json.Unmarshal([]byte(body), &out)
	// Newest first, as the page reads.
	if len(out.Log) != 2 || out.Log[0].Host != "evil.example" || len(out.Tokens) != 1 {
		t.Fatalf("activity = %s", body)
	}
	_, body = f.do(t, "GET", "/api/activity?project=bifract", "", nil)
	json.Unmarshal([]byte(body), &out)
	if len(out.Log) != 1 || out.Log[0].Project != "bifract" {
		t.Fatalf("filtered = %s", body)
	}
}

func TestTokensAndKill(t *testing.T) {
	f := newFixture(t, you)
	if code, _ := f.do(t, "POST", "/api/tokens/s1/revoke", `{}`, nil); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if code, _ := f.do(t, "POST", "/api/tokens/zz/revoke", `{}`, nil); code != 404 {
		t.Fatalf("revoke unknown: %d", code)
	}
	if code, _ := f.do(t, "POST", "/api/kill", `{"confirm":"yes"}`, nil); code != 400 {
		t.Fatalf("kill without the phrase: %d", code)
	}
	if f.network.killed != "" {
		t.Fatal("killed without the phrase")
	}
	code, body := f.do(t, "POST", "/api/kill", `{"confirm":"revoke all"}`, nil)
	if code != 200 || !strings.Contains(body, `"revoked":1`) || f.network.killed != "zane@example.com" {
		t.Fatalf("kill: %d %s by %q", code, body, f.network.killed)
	}
}

// The activity log comes a page at a time, newest first, filtered on the
// server so a page is always full.
func TestActivityPages(t *testing.T) {
	f := newFixture(t, you)
	for i := range 130 {
		e := audit.Entry{Decision: "allow", Project: "orbit", Host: "h.example", Reason: fmt.Sprint("#", i)}
		if i%2 == 1 {
			e.Project = "bifract"
		}
		if i%5 == 0 {
			e.Decision = "deny"
		}
		f.network.log = append(f.network.log, e)
	}
	page := func(q string) (log []audit.Entry, more bool, n int) {
		t.Helper()
		code, body := f.do(t, "GET", "/api/activity"+q, "", nil)
		var out struct {
			Log  []audit.Entry `json:"log"`
			Page int           `json:"page"`
			More bool          `json:"more"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil || code != 200 {
			t.Fatalf("%s: %d %s", q, code, body)
		}
		return out.Log, out.More, out.Page
	}
	if log, more, n := page(""); len(log) != 50 || log[0].Reason != "#129" || !more || n != 1 {
		t.Fatalf("first page: %d %v %d", len(log), more, n)
	}
	if log, more, _ := page("?page=3"); len(log) != 30 || log[0].Reason != "#29" || more {
		t.Fatalf("last page: %d %q %v", len(log), log[0].Reason, more)
	}
	if log, more, _ := page("?page=9"); len(log) != 0 || more {
		t.Fatalf("past the end: %d %v", len(log), more)
	}
	if _, _, n := page("?page=-2"); n != 1 {
		t.Fatalf("bad page: %d", n)
	}
	log, more, _ := page("?blocked=1")
	if len(log) != 26 || more || log[0].Reason != "#125" {
		t.Fatalf("blocked: %d %v", len(log), more)
	}
	for _, e := range log {
		if e.Decision != "deny" {
			t.Fatalf("blocked has %+v", e)
		}
	}
	if log, more, _ := page("?project=bifract&page=2"); len(log) != 15 || more || log[0].Project != "bifract" || log[0].Reason != "#29" {
		t.Fatalf("project page 2: %d %v %+v", len(log), more, log[0])
	}
}
