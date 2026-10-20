package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/proxy/policy"
)

// Reaches is a project's policy as the settings page reviews it: every
// host and route, how it is reached and its rules, {repo} filled in.
func TestReaches(t *testing.T) {
	pp, _ := mustLoad(t, good).Project("orbit")
	rs := pp.Reaches()
	if !slices.IsSortedFunc(rs, func(a, b Reach) int { return strings.Compare(a.Target, b.Target) }) {
		t.Fatal("not sorted")
	}
	at := func(target string) Reach {
		i := slices.IndexFunc(rs, func(r Reach) bool { return r.Target == target })
		if i < 0 {
			t.Fatalf("no %s in %+v", target, rs)
		}
		return rs[i]
	}
	if r := at("registry.npmjs.org"); r.Bundle != "" || !strings.Contains(r.How, "tunnel") || len(r.Rules) != 0 {
		t.Errorf("tunnel %+v", r)
	}
	gh := at("github.com")
	if gh.Bundle != "github" || !strings.Contains(gh.How, "github_app") {
		t.Errorf("github.com %+v", gh)
	}
	if !slices.Contains(gh.Rules, "POST /zaneGittins/orbit.git/git-receive-pack: auto, pushing refs/heads/agent/*") {
		t.Errorf("rules %q", gh.Rules)
	}
	if got := describeRules([]policy.Rule{{Match: "GET /repos/{repo}/actions/**", Action: policy.Auto, FollowRedirects: []string{"*.blob.core.windows.net"}}}, map[string]string{"repo": "o/r"}); len(got) != 1 ||
		got[0] != "GET /repos/o/r/actions/**: auto, following redirects without credentials to *.blob.core.windows.net" {
		t.Errorf("followed redirects described as %q", got)
	}
	if r := at("proxy.golang.org"); !slices.Contains(r.Rules, "GET,HEAD /**: auto") {
		t.Errorf("methods shorthand %q", r.Rules)
	}
	if r := at("/k8s/prod"); !strings.Contains(r.How, "abc.k8s.ondigitalocean.com") || !strings.Contains(r.How, "k8s_prod") || len(r.Rules) == 0 {
		t.Errorf("route %+v", r)
	}
	if r := at("/orbit"); !strings.Contains(r.How, "100.101.1.2/32") {
		t.Errorf("private route %+v", r)
	}
}

func TestDiffReaches(t *testing.T) {
	a := []Reach{{Target: "a", How: "tunnel"}, {Target: "b", How: "tunnel"}}
	b := []Reach{{Target: "b", How: "tunnel"}, {Target: "b", How: "inspected"}, {Target: "c", How: "tunnel"}}
	added, removed := DiffReaches(a, b)
	if len(added) != 2 || added[0].How != "inspected" || added[1].Target != "c" || len(removed) != 1 || removed[0].Target != "a" {
		t.Fatalf("added %+v removed %+v", added, removed)
	}
}
