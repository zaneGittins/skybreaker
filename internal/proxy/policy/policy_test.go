package policy

import (
	"reflect"
	"strings"
	"testing"
)

func TestActionValid(t *testing.T) {
	for _, a := range []Action{Auto, Ask, Deny} {
		if !a.Valid() {
			t.Errorf("%q should be valid", a)
		}
	}
	for _, a := range []Action{"", "allow", "AUTO"} {
		if a.Valid() {
			t.Errorf("%q should be invalid", a)
		}
	}
}

func TestCompileErrors(t *testing.T) {
	vars := map[string]string{"repo": "zaneGittins/orbit"}
	cases := []struct {
		name string
		rule Rule
		want string
	}{
		{"no space", Rule{Match: "GET/x", Action: Auto}, "METHODS PATTERN"},
		{"empty", Rule{Match: "", Action: Auto}, "METHODS PATTERN"},
		{"bad action", Rule{Match: "GET /x", Action: "allow"}, "action"},
		{"lower method", Rule{Match: "get /x", Action: Auto}, "method"},
		{"empty method in list", Rule{Match: "GET,,HEAD /x", Action: Auto}, "method"},
		{"relative pattern", Rule{Match: "GET x/y", Action: Auto}, "start with /"},
		{"unknown var", Rule{Match: "GET /{owner}/x", Action: Auto}, "owner"},
		{"unclosed var", Rule{Match: "GET /{repo/x", Action: Auto}, "{"},
		{"bad glob", Rule{Match: "GET /[a", Action: Auto}, "pattern"},
		{"extra field", Rule{Match: "GET /x y", Action: Auto}, "METHODS PATTERN"},
		{"mixed star", Rule{Match: "GET /a**b", Action: Auto}, "**"},
		{"refs outside heads", Rule{Match: "POST /x", Action: Auto, Refs: []string{"refs/heads/agent/*", "*"}}, "refs/heads/"},
		{"empty refs entry", Rule{Match: "POST /x", Action: Auto, Refs: []string{""}}, "refs/heads/"},
		{"refs tags", Rule{Match: "POST /x", Action: Auto, Refs: []string{"refs/tags/*"}}, "refs/heads/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Compile([]Rule{c.rule}, vars)
			if err == nil {
				t.Fatalf("Compile(%q) succeeded, want error", c.rule.Match)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should mention %q", err, c.want)
			}
		})
	}
}

func TestDecide(t *testing.T) {
	rules := []Rule{
		{Match: "GET /{repo}.git/*", Action: Auto},
		{Match: "GET /{repo}.git/info/refs", Action: Deny}, // shadowed by the rule above
		{Match: "POST /{repo}.git/git-upload-pack", Action: Auto},
		{Match: "POST /{repo}.git/git-receive-pack", Action: Auto, Refs: []string{"refs/heads/agent/*"}},
		{Match: "GET,HEAD /repos/{repo}/**", Action: Auto},
		{Match: "PUT /repos/{repo}/pulls/*/merge", Action: Ask},
		{Match: "DELETE /**", Action: Deny},
		{Match: "* /v2/**", Action: Ask},
		{Match: "GET /", Action: Auto},
	}
	s, err := Compile(rules, map[string]string{"repo": "zaneGittins/orbit"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method, path string
		want         Action
		rule         int // index into rules, -1 for unmatched
	}{
		{"GET", "/zaneGittins/orbit.git/info", Auto, 0},
		{"GET", "/zaneGittins/orbit.git/info/refs", Deny, -1}, // * is one segment; the explicit deny rule matches
		{"GET", "/ZANEGITTINS/Orbit.GIT/info", Auto, 0},       // case-insensitive
		{"POST", "/zaneGittins/orbit.git/git-upload-pack", Auto, 2},
		{"POST", "/zaneGittins/orbit.git/git-receive-pack", Auto, 3},
		{"POST", "/zaneGittins/other.git/git-receive-pack", Deny, -1},
		{"POST", "/zaneGittins/orbit/git-receive-pack", Deny, -1},
		{"GET", "/repos/zaneGittins/orbit", Auto, 4}, // ** matches zero segments
		{"HEAD", "/repos/zaneGittins/orbit/pulls/3", Auto, 4},
		{"GET", "/repos/zaneGittins/orbitx/pulls", Deny, -1},
		{"GET", "/repos/zaneGittins", Deny, -1},
		{"PUT", "/repos/zaneGittins/orbit/pulls/12/merge", Ask, 5},
		{"PUT", "/repos/zaneGittins/orbit/pulls/12/13/merge", Deny, -1},
		{"DELETE", "/repos/zaneGittins/orbit", Deny, 6},
		{"PATCH", "/v2/droplets/1", Ask, 7},
		{"PATCH", "/v2", Ask, 7},
		{"GET", "/", Auto, 8},
		{"GET", "/x", Deny, -1},
		{"get", "/", Deny, -1}, // methods are exact
	}
	for _, c := range cases {
		d := s.Decide(c.method, c.path)
		if d.Action != c.want {
			t.Errorf("%s %s: action %q, want %q (reason %q)", c.method, c.path, d.Action, c.want, d.Reason)
		}
		switch {
		case c.rule < 0 && c.want == Deny && d.Rule != nil && d.Rule.Action != Deny:
			t.Errorf("%s %s: matched %+v, want a deny", c.method, c.path, d.Rule)
		case c.rule >= 0 && (d.Rule == nil || !reflect.DeepEqual(*d.Rule, rules[c.rule])):
			t.Errorf("%s %s: matched %+v, want %+v", c.method, c.path, d.Rule, rules[c.rule])
		}
	}
	if d := s.Decide("GET", "/nothing/here"); d.Rule != nil || d.Reason != "no rule matched" {
		t.Errorf("unmatched decision = %+v", d)
	}
}

func TestDecideEmptySet(t *testing.T) {
	s, err := Compile(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := s.Decide("GET", "/"); d.Action != Deny {
		t.Errorf("empty set allowed a request: %+v", d)
	}
	var nilSet *Set
	if d := nilSet.Decide("GET", "/"); d.Action != Deny {
		t.Errorf("nil set allowed a request: %+v", d)
	}
}

func TestPatternSemantics(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/", "/", true},
		{"/", "/a", false},
		{"/**", "/", true},
		{"/**", "/a/b/c", true},
		{"/a/**/z", "/a/z", true},
		{"/a/**/z", "/a/b/c/z", true},
		{"/a/**/z", "/a/b/c", false},
		{"/a/*", "/a/", true}, // empty last segment is a segment
		{"/a/*", "/a", false},
		{"/a", "/a/", false}, // trailing slash is significant
		{"/a/", "/a/", true},
		{"/*.git/x", "/repo.git/x", true},
		{"/*.git/x", "/repo/x", false},
		{"/a/**", "/a", true},
		{"/**/b/**", "/x/b/y", true},
		{"/**/b/**", "/x/y", false},
	}
	for _, c := range cases {
		s, err := Compile([]Rule{{Match: "GET " + c.pattern, Action: Auto}}, nil)
		if err != nil {
			t.Fatalf("Compile %q: %v", c.pattern, err)
		}
		got := s.Decide("GET", c.path).Action == Auto
		if got != c.want {
			t.Errorf("pattern %q path %q: matched %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// A var value is literal text even when it contains glob metacharacters.
func TestVarsAreLiteral(t *testing.T) {
	s, err := Compile([]Rule{{Match: "GET /repos/{repo}", Action: Auto}}, map[string]string{"repo": "a/*"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Decide("GET", "/repos/a/b").Action != Deny {
		t.Error("a * inside a var value acted as a wildcard")
	}
	if s.Decide("GET", "/repos/a/*").Action != Auto {
		t.Error("literal var value did not match")
	}
}

func TestCleanPath(t *testing.T) {
	ok := []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"/a/b", "/a/b"},
		{"/a/b/", "/a/b/"},
		{"/a%20b", "/a b"},
		{"/repos/o/r/contents/file%2Ename", "/repos/o/r/contents/file.name"},
		{"/a/.git", "/a/.git"},
		{"/a/..b", "/a/..b"},
	}
	for _, c := range ok {
		got, valid := CleanPath(c.in)
		if !valid || got != c.want {
			t.Errorf("CleanPath(%q) = %q, %v; want %q, true", c.in, got, valid, c.want)
		}
	}
	bad := []string{
		"/a/../b",
		"/..",
		"/a/..",
		"/a/./b",
		"/.",
		"//a",
		"/a//b",
		"/a%2fb",
		"/a%2Fb",
		"/a%5cb",
		"/a%5Cb",
		"/a%00b",
		"/a\\b",
		"/a/%2E%2E/b",
		"/a/%2e/b",
		"/a%zz",
		"/a%",
		"a/b", // not absolute
		"/a%252fb/../c",
		"/a\x00b",
		"/a%0ab", // control characters
	}
	for _, in := range bad {
		if got, valid := CleanPath(in); valid {
			t.Errorf("CleanPath(%q) = %q, accepted; want rejection", in, got)
		}
	}
}

// Double-encoding stays encoded after one decode, so it can't sneak a slash
// into a single segment the upstream would later split.
func TestCleanPathDoubleEncoding(t *testing.T) {
	got, ok := CleanPath("/a%252fb")
	if !ok || got != "/a%2fb" {
		t.Fatalf("CleanPath double-encoded = %q, %v", got, ok)
	}
	s, _ := Compile([]Rule{{Match: "GET /a/b", Action: Auto}}, nil)
	if s.Decide("GET", got).Action == Auto {
		t.Error("double-encoded slash matched a two-segment rule")
	}
}

func FuzzCleanPath(f *testing.F) {
	for _, s := range []string{"/", "/a/b", "/a%2fb", "/a/../b", "/%2e%2e/x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		p, ok := CleanPath(in)
		if !ok {
			return
		}
		for _, seg := range strings.Split(p, "/")[1:] {
			if seg == "." || seg == ".." {
				t.Fatalf("CleanPath(%q) = %q kept a dot segment", in, p)
			}
		}
		if strings.Contains(p, "//") || strings.ContainsAny(p, "\\\x00") {
			t.Fatalf("CleanPath(%q) = %q", in, p)
		}
	})
}

func TestLowercaseMethodIsDenied(t *testing.T) {
	// Go's server accepts "delete"; rule methods are uppercase. Without a
	// check, "delete" would skip "DELETE /** deny" and fall to "* /** ask".
	s, err := Compile([]Rule{{Match: "DELETE /**", Action: Deny}, {Match: "* /**", Action: Ask}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"delete", "Delete", "dElEtE", "get", ""} {
		if d := s.Decide(m, "/v2/droplets/1"); d.Action != Deny {
			t.Errorf("method %q: %s, want deny", m, d.Action)
		}
	}
	if d := s.Decide("POST", "/v2/droplets"); d.Action != Ask {
		t.Errorf("POST: %s, want ask", d.Action)
	}
}

func TestGraphQLRule(t *testing.T) {
	ok := []Rule{{Match: "POST /graphql", Action: Auto, GraphQL: &GraphQL{Mutations: []string{"createPullRequest", "addComment"}, Other: Ask}}}
	if _, err := Compile(ok, nil); err != nil {
		t.Fatal(err)
	}
	for name, g := range map[string]*GraphQL{
		"other auto defeats the check": {Mutations: []string{"addComment"}, Other: Auto},
		"bad other":                    {Other: "maybe"},
		"bad mutation name":            {Mutations: []string{"merge PullRequest"}},
		"empty mutation name":          {Mutations: []string{""}},
	} {
		if _, err := Compile([]Rule{{Match: "POST /graphql", Action: Auto, GraphQL: g}}, nil); err == nil {
			t.Errorf("%s: compiled", name)
		}
	}
	// Unset, other is deny.
	if (&GraphQL{}).OtherAction() != Deny {
		t.Error("other defaults to something other than deny")
	}
}
