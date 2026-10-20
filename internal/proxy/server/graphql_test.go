package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func gql(query string) string {
	b, _ := json.Marshal(map[string]string{"query": query})
	return string(b)
}

const gqlURL = "https://api.github.test/graphql"

var jsonCT = map[string]string{"Content-Type": "application/json"}

func TestGraphQLQueriesAndListedMutationsRun(t *testing.T) {
	w := newWorld(t)
	for _, q := range []string{
		`query { repository(owner: "zaneGittins", name: "orbit") { pullRequests(first: 5) { nodes { url } } } }`,
		`mutation { createPullRequest(input: {repositoryId: "R", baseRefName: "main", headRefName: "agent/x", title: "t"}) { pullRequest { url } } }`,
		`mutation { c: addComment(input: {subjectId: "PR", body: "done"}) { clientMutationId } }`,
	} {
		if code, b, _ := do(t, w.orbit(), "POST", gqlURL, gql(q), jsonCT); code != 200 {
			t.Errorf("%s: %d %s", q, code, b)
		}
	}
	if n := len(w.hitsFor("api.github.test")); n != 3 {
		t.Fatalf("upstream saw %d requests, want 3", n)
	}
}

func TestGraphQLMergeNeedsApproval(t *testing.T) {
	w := newWorld(t)
	merge := gql(`mutation { mergePullRequest(input: {pullRequestId: "PR"}) { clientMutationId } }`)
	code, _, hdr := do(t, w.orbit(), "POST", gqlURL, merge, jsonCT)
	id := hdr.Get("X-Skybreaker-Approval")
	if code != http.StatusForbidden || id == "" {
		t.Fatalf("merge: %d, approval %q", code, id)
	}
	a, _ := w.approvals.Get(id)
	shown := ""
	for _, f := range a.View.Fields {
		shown += f.Label + "=" + f.Value + "\n"
	}
	if !strings.Contains(shown, "GraphQL=mutation mergePullRequest") {
		t.Fatalf("card doesn't show the broker's parse:\n%s", shown)
	}
	w.approvals.Decide(id, true, "zane@example.com")
	// The approval is for that merge, not for another mutation in its place.
	other := gql(`mutation { deleteRef(input: {refId: "main"}) { clientMutationId } }`)
	if code, _, _ := do(t, w.orbit(), "POST", gqlURL, other, jsonCT); code == 200 {
		t.Fatal("approval spent on a different mutation")
	}
	if code, _, _ := do(t, w.orbit(), "POST", gqlURL, merge, jsonCT); code != 200 {
		t.Fatalf("approved merge: %d", code)
	}
	if n := len(w.hitsFor("api.github.test")); n != 1 {
		t.Fatalf("upstream saw %d requests, want the one approved merge", n)
	}
}

func TestGraphQLDisguisesAreHeld(t *testing.T) {
	w := newWorld(t)
	for name, q := range map[string]string{
		"alias":           `mutation { createPullRequest: mergePullRequest(input: {pullRequestId: "PR"}) { clientMutationId } }`,
		"fragment":        `mutation { ...F } fragment F on Mutation { mergePullRequest(input: {pullRequestId: "PR"}) { clientMutationId } }`,
		"inline fragment": `mutation { ... on Mutation { deleteRef(input: {refId: "R"}) { clientMutationId } } }`,
		"listed + other":  `mutation { addComment(input: {subjectId: "S", body: "x"}) { clientMutationId } updateRef(input: {refId: "R", oid: "0"}) { clientMutationId } }`,
		"subscription":    `subscription { x { id } }`,
	} {
		code, _, hdr := do(t, w.orbit(), "POST", gqlURL, gql(q), jsonCT)
		if code != http.StatusForbidden || hdr.Get("X-Skybreaker-Approval") == "" {
			t.Errorf("%s: %d, approval %q", name, code, hdr.Get("X-Skybreaker-Approval"))
		}
	}
	for name, c := range map[string]struct{ body, ct string }{
		"unparseable":        {`{"query": "mutation { mergePullRequest(input: {) }"}`, "application/json"},
		"duplicate query":    {`{"query": "query { viewer { id } }", "query": "mutation { mergePullRequest(input: {}) { x } }"}`, "application/json"},
		"form encoded":       {`query=mutation+%7B+mergePullRequest+%7D`, "application/x-www-form-urlencoded"},
		"no content type":    {gql(`query { viewer { id } }`), ""},
		"two ops, no choice": {gql(`query A { viewer { id } } mutation B { mergePullRequest(input: {}) { x } }`), "application/json"},
	} {
		code, _, hdr := do(t, w.orbit(), "POST", gqlURL, c.body, map[string]string{"Content-Type": c.ct})
		if code == 200 || hdr.Get("X-Skybreaker-Approval") != "" {
			t.Errorf("%s: %d, approval %q", name, code, hdr.Get("X-Skybreaker-Approval"))
		}
	}
	if n := len(w.hitsFor("api.github.test")); n != 0 {
		t.Fatalf("%d disguised or unreadable requests reached GitHub", n)
	}
}

// The operation is judged from the body alone, so nothing else may carry
// one: a server that reads query or operationName from the URL would run
// what the URL says while the broker judged the body.
func TestGraphQLQueryStringRefused(t *testing.T) {
	w := newWorld(t)
	body := gql(`query A { viewer { login } } mutation B { mergePullRequest(input: {pullRequestId: "PR"}) { clientMutationId } }`)
	body = strings.Replace(body, `}"}`, `}","operationName":"A"}`, 1)
	if code, b, _ := do(t, w.orbit(), "POST", gqlURL+"?operationName=B", body, jsonCT); code != http.StatusBadRequest {
		t.Fatalf("query string with a GraphQL request: %d %s", code, b)
	}
	if n := len(w.hitsFor("api.github.test")); n != 0 {
		t.Fatalf("upstream saw %d requests", n)
	}
}
