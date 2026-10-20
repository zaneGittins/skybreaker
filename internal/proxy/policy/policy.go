// Package policy decides what an agent may do on a host: each rule pairs
// "METHODS PATTERN" with auto, ask or deny, and anything no rule matches is
// denied.
//
// Patterns are split on "/". A "**" segment matches zero or more whole
// segments; any other segment is a path.Match glob, so "*" stays inside one
// segment. {var} placeholders expand to literal text before matching, and
// matching ignores ASCII case because GitHub treats owner and repo names that
// way.
package policy

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
)

type Action string

const (
	Auto Action = "auto"
	Ask  Action = "ask"
	Deny Action = "deny"
)

func (a Action) Valid() bool { return a == Auto || a == Ask || a == Deny }

// Rule is one entry as written in config. Refs are receive-pack ref globs the
// proxy server enforces; policy only checks they stay under refs/heads/.
// GraphQL, likewise, is enforced by the server on the request body.
// FollowRedirects lists the hosts ("*.example.com" for any one label) the
// server may follow the upstream's redirects of a GET or HEAD to, fetching
// them itself with no credentials, so the sandbox needn't reach them
// (GitHub's log storage, say). It's set in the config file only.
type Rule struct {
	Match           string   `json:"match" yaml:"match"`
	Action          Action   `json:"action" yaml:"action"`
	Refs            []string `json:"refs,omitempty" yaml:"refs,omitempty"`
	GraphQL         *GraphQL `json:"graphql,omitempty" yaml:"graphql,omitempty"`
	FollowRedirects []string `json:"follow_redirects,omitempty" yaml:"follow_redirects,omitempty"`
}

var hostPatternRe = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// Follows reports whether the rule may follow a redirect to host: a listed
// host, or one label in place of a listed "*.".
func (r *Rule) Follows(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, p := range r.FollowRedirects {
		if rest, ok := strings.CutPrefix(p, "*."); ok {
			if label, tail, found := strings.Cut(host, "."); found && label != "" && tail == rest {
				return true
			}
		} else if host == p {
			return true
		}
	}
	return false
}

// GraphQL narrows a rule to some GraphQL operations: queries and the listed
// mutations get the rule's action; any other operation gets Other.
type GraphQL struct {
	Mutations []string `json:"mutations" yaml:"mutations"`
	Other     Action   `json:"other,omitempty" yaml:"other,omitempty"` // ask or deny; deny if unset
}

func (g *GraphQL) OtherAction() Action {
	if g.Other == "" {
		return Deny
	}
	return g.Other
}

func validGraphQLName(n string) bool {
	if n == "" {
		return false
	}
	for i, c := range n {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

type Decision struct {
	Action Action
	Rule   *Rule // nil when nothing matched
	Reason string
}

// Set is a compiled, ordered rule list.
type Set struct {
	rules []compiled
}

type compiled struct {
	rule    *Rule
	methods []string // nil means any method
	segs    []string // lowercased; "**" or a path.Match glob
}

// Compile expands {var} placeholders from vars and validates every rule.
func Compile(rules []Rule, vars map[string]string) (*Set, error) {
	s := &Set{}
	for i := range rules {
		r := rules[i]
		c, err := compile(&r, vars)
		if err != nil {
			return nil, fmt.Errorf("rule %d %q: %w", i+1, r.Match, err)
		}
		s.rules = append(s.rules, c)
	}
	return s, nil
}

func compile(r *Rule, vars map[string]string) (compiled, error) {
	if !r.Action.Valid() {
		return compiled{}, fmt.Errorf("invalid action %q (want auto, ask or deny)", r.Action)
	}
	for _, ref := range r.Refs {
		if !strings.HasPrefix(ref, "refs/heads/") || ref == "refs/heads/" {
			return compiled{}, fmt.Errorf("refs %q must be a branch glob under refs/heads/", ref)
		}
	}
	if g := r.GraphQL; g != nil {
		if o := g.OtherAction(); o != Ask && o != Deny {
			return compiled{}, fmt.Errorf("graphql other must be ask or deny, not %q", g.Other)
		}
		for _, m := range g.Mutations {
			if !validGraphQLName(m) {
				return compiled{}, fmt.Errorf("graphql mutation %q is not a GraphQL name", m)
			}
		}
	}
	fields := strings.Fields(r.Match)
	if len(fields) != 2 {
		return compiled{}, errors.New(`match must be "METHODS PATTERN", e.g. "GET /repos/{repo}/**"`)
	}
	c := compiled{rule: r}
	if fields[0] != "*" {
		for _, m := range strings.Split(fields[0], ",") {
			if !validMethod(m) {
				return compiled{}, fmt.Errorf("invalid method %q (uppercase tokens or *)", m)
			}
			c.methods = append(c.methods, m)
		}
	}
	// A followed redirect is fetched with the request's method and no
	// body: only reads may ask for one.
	if len(r.FollowRedirects) > 0 && (c.methods == nil || slices.ContainsFunc(c.methods, func(m string) bool { return m != "GET" && m != "HEAD" })) {
		return compiled{}, errors.New("follow_redirects is only for GET and HEAD rules")
	}
	for _, h := range r.FollowRedirects {
		if !hostPatternRe.MatchString(h) {
			return compiled{}, fmt.Errorf("follow_redirects: %q is not a host name, or *. and one", h)
		}
	}
	pattern := fields[1]
	if !strings.HasPrefix(pattern, "/") {
		return compiled{}, errors.New("pattern must start with /")
	}
	expanded, err := expand(pattern, vars)
	if err != nil {
		return compiled{}, err
	}
	for _, seg := range strings.Split(expanded, "/")[1:] {
		seg = strings.ToLower(seg)
		if seg != "**" && strings.Contains(seg, "**") {
			return compiled{}, fmt.Errorf("** must be a whole segment, not part of %q", seg)
		}
		if _, err := path.Match(seg, ""); err != nil {
			return compiled{}, fmt.Errorf("bad pattern segment %q: %w", seg, err)
		}
		c.segs = append(c.segs, seg)
	}
	return c, nil
}

func validMethod(m string) bool {
	if m == "" {
		return false
	}
	for _, r := range m {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// expand replaces {name} with the escaped value of vars[name], so a value is
// always literal text even if it holds glob characters.
func expand(pattern string, vars map[string]string) (string, error) {
	var b strings.Builder
	for {
		i := strings.IndexByte(pattern, '{')
		if i < 0 {
			b.WriteString(pattern)
			return b.String(), nil
		}
		j := strings.IndexByte(pattern[i:], '}')
		if j < 0 {
			return "", errors.New("unclosed { in pattern")
		}
		name := pattern[i+1 : i+j]
		v, ok := vars[name]
		if !ok {
			return "", fmt.Errorf("unknown variable {%s}", name)
		}
		b.WriteString(pattern[:i])
		b.WriteString(escapeGlob(v))
		pattern = pattern[i+j+1:]
	}
}

func escapeGlob(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '*', '?', '[', ']', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Decide returns the first matching rule's action, or Deny when nothing
// matches. path must already be cleaned by CleanPath.
func (s *Set) Decide(method, p string) Decision {
	// Rules name methods in uppercase. A lowercase method would slip past
	// a "DELETE ... deny" rule to a later wildcard, so it is refused.
	if !validMethod(method) {
		return Decision{Action: Deny, Reason: "method must be an uppercase token"}
	}
	if s != nil {
		segs := strings.Split(strings.ToLower(p), "/")[1:]
		for _, c := range s.rules {
			if c.matchMethod(method) && matchSegs(c.segs, segs) {
				return Decision{Action: c.rule.Action, Rule: c.rule, Reason: "rule " + c.rule.Match}
			}
		}
	}
	return Decision{Action: Deny, Reason: "no rule matched"}
}

func (c compiled) matchMethod(m string) bool {
	if c.methods == nil {
		return true
	}
	for _, x := range c.methods {
		if x == m {
			return true
		}
	}
	return false
}

func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(segs); i++ {
				if matchSegs(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], segs[0]); !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// CleanPath unescapes r.URL.EscapedPath() and refuses anything an upstream
// might read differently from the proxy: dot segments, empty segments,
// encoded slashes, backslashes, NULs and control characters.
func CleanPath(escaped string) (string, bool) {
	if escaped == "" {
		return "/", true
	}
	if !strings.HasPrefix(escaped, "/") || strings.ContainsRune(escaped, '\\') {
		return "", false
	}
	lower := strings.ToLower(escaped)
	for _, enc := range []string{"%2f", "%5c", "%00"} {
		if strings.Contains(lower, enc) {
			return "", false
		}
	}
	p, err := url.PathUnescape(escaped)
	if err != nil {
		return "", false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	segs := strings.Split(p, "/")[1:]
	for i, seg := range segs {
		if seg == "." || seg == ".." {
			return "", false
		}
		if seg == "" && i != len(segs)-1 {
			return "", false
		}
	}
	return p, true
}
