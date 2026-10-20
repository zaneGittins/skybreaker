package config

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/zaneGittins/skybreaker/internal/proxy/policy"
)

// Reach is one host or route a project's sandbox may reach, in words: what
// the settings page shows when a change is reviewed.
type Reach struct {
	Target string   `json:"target"` // a host, or a route's prefix
	How    string   `json:"how"`
	Rules  []string `json:"rules,omitempty"`
	Bundle string   `json:"bundle,omitempty"` // "" for the project's own list
}

func (r Reach) key() string {
	return r.Target + "\x00" + r.How + "\x00" + strings.Join(r.Rules, "\x00") + "\x00" + r.Bundle
}

// Reaches lists everything the project may reach, sorted by target.
func (p *ProjectPolicy) Reaches() []Reach {
	var out []Reach
	for _, h := range slices.Sorted(maps.Keys(p.hosts)) {
		out = append(out, p.hosts[h].reach)
	}
	for _, r := range p.routes {
		out = append(out, r.reach)
	}
	slices.SortStableFunc(out, func(a, b Reach) int { return strings.Compare(a.Target, b.Target) })
	return out
}

// DiffReaches is what b reaches that a doesn't, and the reverse.
func DiffReaches(a, b []Reach) (added, removed []Reach) {
	in := func(rs []Reach, r Reach) bool {
		return slices.ContainsFunc(rs, func(x Reach) bool { return x.key() == r.key() })
	}
	for _, r := range b {
		if !in(a, r) {
			added = append(added, r)
		}
	}
	for _, r := range a {
		if !in(b, r) {
			removed = append(removed, r)
		}
	}
	return added, removed
}

func describeHost(e *Entry, h *HostRule, vars map[string]string) Reach {
	r := Reach{Target: h.Host, Bundle: h.Capability}
	if h.Mode == ModeTunnel {
		r.How = "tunnel: passed through unread"
	} else {
		r.How = "inspected" + describeInject(e.Inject)
		rules, _ := entryRules(e)
		r.Rules = describeRules(rules, vars)
	}
	if !slices.Equal(h.Ports, []int{443}) {
		ps := make([]string, len(h.Ports))
		for i, p := range h.Ports {
			ps[i] = strconv.Itoa(p)
		}
		r.How += ", on port " + strings.Join(ps, ", ")
	}
	return r
}

func describeRoute(e *Entry, rt *RouteRule, vars map[string]string) Reach {
	r := Reach{Target: rt.Prefix, Bundle: rt.Capability, How: "route to " + rt.Upstream.String() + describeInject(e.Inject)}
	if len(e.AllowPrivate) > 0 {
		r.How += "; may reach private " + strings.Join(e.AllowPrivate, ", ")
	}
	if e.CAFile != "" {
		r.How += "; trusts the CA in " + e.CAFile
	}
	r.Rules = describeRules(e.Rules, vars)
	if k := e.K8s; k != nil {
		for _, x := range []struct {
			label string
			v     []string
		}{{"auto", k.Auto}, {"ask", k.Ask}, {"deny", k.Deny}, {"proxy", k.Proxy}} {
			if len(x.v) > 0 {
				r.Rules = append(r.Rules, "Kubernetes "+x.label+": "+strings.Join(x.v, ", "))
			}
		}
	}
	return r
}

func describeInject(i *Inject) string {
	switch {
	case i == nil:
		return ""
	case i.Bearer != "":
		return "; sends secret " + i.Bearer + " as a bearer token"
	case i.BasicAuth != nil:
		return fmt.Sprintf("; sends secret %s as basic auth (user %s)", i.BasicAuth.Secret, i.BasicAuth.User)
	case i.Header != nil:
		return fmt.Sprintf("; sends secret %s in header %s", i.Header.Secret, i.Header.Name)
	}
	return ""
}

func describeRules(rules []policy.Rule, vars map[string]string) []string {
	var out []string
	for _, ru := range rules {
		m := ru.Match
		for k, v := range vars {
			m = strings.ReplaceAll(m, "{"+k+"}", v)
		}
		s := m + ": " + string(ru.Action)
		if len(ru.Refs) > 0 {
			s += ", pushing " + strings.Join(ru.Refs, ", ")
		}
		if g := ru.GraphQL; g != nil {
			s += fmt.Sprintf(", GraphQL queries and mutations %s (others: %s)", strings.Join(g.Mutations, ", "), g.OtherAction())
		}
		out = append(out, s)
	}
	return out
}
