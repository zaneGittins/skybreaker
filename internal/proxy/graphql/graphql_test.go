package graphql

import (
	"encoding/json"
	"slices"
	"testing"
)

func body(query string, rest ...string) []byte {
	m := map[string]any{"query": query}
	for i := 0; i+1 < len(rest); i += 2 {
		m[rest[i]] = rest[i+1]
	}
	b, _ := json.Marshal(m)
	return b
}

func TestParse(t *testing.T) {
	cases := []struct {
		name     string
		in       []byte
		op       string
		fields   []string
		indirect bool
	}{
		{"shorthand query", body(`{ viewer { login } }`), "query", []string{"viewer"}, false},
		{"named query", body(`query Repo($o: String!) { repository(owner: $o, name: "x") { id } }`), "query", []string{"repository"}, false},
		{"mutation", body(`mutation { createPullRequest(input: {repositoryId: "R", baseRefName: "main", headRefName: "f", title: "t"}) { pullRequest { url } } }`), "mutation", []string{"createPullRequest"}, false},
		// The alias is what the response calls it; the field is what runs.
		{"alias disguise", body(`mutation { createPullRequest: mergePullRequest(input: {pullRequestId: "P"}) { clientMutationId } }`), "mutation", []string{"mergePullRequest"}, false},
		{"two fields", body(`mutation { a: addComment(input: {subjectId: "S", body: "x"}) { clientMutationId } deleteRef(input: {refId: "R"}) { clientMutationId } }`), "mutation", []string{"addComment", "deleteRef"}, false},
		// Only top-level fields are mutations; names further down are data.
		{"nested names", body(`mutation { createPullRequest(input: {title: "t"}) { pullRequest { mergePullRequest: id deleteRef: url } } }`), "mutation", []string{"createPullRequest"}, false},
		{"fragment spread", body(`mutation { ...M } fragment M on Mutation { mergePullRequest(input: {pullRequestId: "P"}) { clientMutationId } }`), "mutation", nil, true},
		{"inline fragment", body(`mutation { ... on Mutation { mergePullRequest(input: {pullRequestId: "P"}) { clientMutationId } } }`), "mutation", nil, true},
		{"strings hide nothing", body(`mutation { addComment(input: {subjectId: "S", body: "} mergePullRequest(input: {}) { x } #"}) { clientMutationId } }`), "mutation", []string{"addComment"}, false},
		{"block string", body("mutation { addComment(input: {subjectId: \"S\", body: \"\"\"a \\\"\"\" } mergePullRequest { x } \"\"\"}) { clientMutationId } }"), "mutation", []string{"addComment"}, false},
		{"comments", body("# mutation { mergePullRequest { x } }\nquery { viewer { login } } # } mutation"), "query", []string{"viewer"}, false},
		{"escapes", body(`mutation { addComment(input: {subjectId: "S", body: "\" } mergePullRequest { x } \u0022"}) { clientMutationId } }`), "mutation", []string{"addComment"}, false},
		{"variables, directives, defaults", body(`mutation M($id: ID! = "x", $n: [Int!]! @deprecated) @a(b: [1, 2.5e3, {c: null}]) { addComment(input: {subjectId: $id, body: "x"}) @skip(if: false) { clientMutationId } }`), "mutation", []string{"addComment"}, false},
		{"commas and BOM", body("\ufeff,mutation,{,addComment(input:{subjectId:\"S\",body:\"x\"}),{,clientMutationId,},}"), "mutation", []string{"addComment"}, false},
		{"operationName picks", body(`query Q { viewer { login } } mutation M { mergePullRequest(input: {pullRequestId: "P"}) { clientMutationId } }`, "operationName", "M"), "mutation", []string{"mergePullRequest"}, false},
		{"operationName picks the query", body(`query Q { viewer { login } } mutation M { mergePullRequest(input: {}) { x } }`, "operationName", "Q"), "query", []string{"viewer"}, false},
		{"subscription", body(`subscription { issueUpdated { id } }`), "subscription", []string{"issueUpdated"}, false},
		{"keywords as names", body(`query { query: mutation { fragment on } }`), "query", []string{"mutation"}, false},
	}
	for _, c := range cases {
		r, err := Parse(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if r.Op != c.op || !slices.Equal(r.Fields, c.fields) || r.Indirect != c.indirect {
			t.Errorf("%s: %+v, want %s %v indirect=%v", c.name, r, c.op, c.fields, c.indirect)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for name, in := range map[string][]byte{
		"two operations, no name":   body(`query A { viewer { login } } mutation B { mergePullRequest(input: {}) { x } }`),
		"unknown operationName":     body(`mutation M { addComment(input: {}) { x } }`, "operationName", "N"),
		"duplicate operation names": body(`query M { viewer { id } } mutation M { mergePullRequest(input: {}) { x } }`, "operationName", "M"),
		"only fragments":            body(`fragment F on Query { viewer { id } }`),
		"empty":                     body(``),
		"unbalanced":                body(`mutation { addComment(input: {subjectId: "S"} { x }`),
		"unterminated string":       body(`mutation { addComment(input: {body: "x}) { x } }`),
		"unterminated block string": body(`mutation { addComment(input: {body: """x}) { x } }`),
		"trailing text":             body(`query { viewer { id } } }`),
		"bad character":             body("query { viewer \x00 { id } }"),
		"duplicate query keys":      []byte(`{"query": "query { viewer { id } }", "query": "mutation { mergePullRequest(input: {}) { x } }"}`),
		"duplicate operationName":   []byte(`{"query": "query A { a } mutation B { b }", "operationName": "A", "operationName": "B"}`),
		"batch":                     []byte(`[{"query": "query { viewer { id } }"}]`),
		"query not a string":        []byte(`{"query": {"mutation": true}}`),
		"no query":                  []byte(`{"variables": {}}`),
		"not JSON":                  []byte(`query { viewer { id } }`),
		"trailing JSON":             []byte(`{"query": "query { a }"} {"query": "mutation { b }"}`),
		"deep nesting":              body(`query { a ` + repeat("{ a ", 600) + repeat("}", 600) + ` }`),
	} {
		if r, err := Parse(in); err == nil {
			t.Errorf("%s: accepted as %+v", name, r)
		}
	}
}

func repeat(s string, n int) string {
	out := ""
	for range n {
		out += s
	}
	return out
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`{ viewer { login } }`,
		`mutation { a: mergePullRequest(input: {x: [1, "}", """b"""]}) { c } }`,
		`query Q { a } mutation M { ...F } fragment F on Mutation { b }`,
		"# c\nsubscription { s @d(e: $f) }",
	} {
		f.Add(body(s), "")
		f.Add(body(s), "Q")
	}
	f.Fuzz(func(t *testing.T, b []byte, _ string) {
		r, err := Parse(b)
		if err != nil {
			return
		}
		if r.Op != "query" && r.Op != "mutation" && r.Op != "subscription" {
			t.Fatalf("op %q", r.Op)
		}
		if !r.Indirect && len(r.Fields) == 0 {
			t.Fatalf("an operation with no fields: %+v", r)
		}
	})
}
