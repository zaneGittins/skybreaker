// Package content holds the rules the broker applies to what an agent
// writes for others to read: the messages of commits it pushes, and the
// titles and bodies of pull requests it opens or edits. They are a few fixed
// checks (lengths and forbidden patterns), not a language: each kind of
// object needs a parser in the broker that fails closed, so a new kind is a
// deliberate change, never configuration.
package content

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Rules are the content rules as written in config.
type Rules struct {
	Commit      *Commit      `json:"commit,omitempty" yaml:"commit,omitempty"`
	PullRequest *PullRequest `json:"pull_request,omitempty" yaml:"pull_request,omitempty"`
}

// Commit applies to every commit in a push.
type Commit struct {
	MaxSubject int      `json:"max_subject,omitempty" yaml:"max_subject,omitempty"` // characters in the first line
	MaxMessage int      `json:"max_message,omitempty" yaml:"max_message,omitempty"` // characters in the whole message
	Forbid     []string `json:"forbid,omitempty" yaml:"forbid,omitempty"`           // patterns the message must not match
}

// PullRequest applies to a pull request's title and body when it is opened
// or edited.
type PullRequest struct {
	MaxTitle int      `json:"max_title,omitempty" yaml:"max_title,omitempty"`
	MaxBody  int      `json:"max_body,omitempty" yaml:"max_body,omitempty"`
	Forbid   []string `json:"forbid,omitempty" yaml:"forbid,omitempty"` // patterns neither may match
}

// The bounds keep the largest rules within a settings request.
const (
	maxPatterns   = 20
	maxPatternLen = 500
	maxLimit      = 1 << 20
)

// Merge combines rules from several sources (the config file and the app),
// keeping the strictest: the smaller of each limit and every pattern. A
// source can only add to what another asks for.
func Merge(sources ...*Rules) *Rules {
	var out Rules
	for _, r := range sources {
		if r == nil {
			continue
		}
		if c := r.Commit; c != nil {
			if out.Commit == nil {
				out.Commit = &Commit{}
			}
			out.Commit.MaxSubject = stricter(out.Commit.MaxSubject, c.MaxSubject)
			out.Commit.MaxMessage = stricter(out.Commit.MaxMessage, c.MaxMessage)
			out.Commit.Forbid = union(out.Commit.Forbid, c.Forbid)
		}
		if p := r.PullRequest; p != nil {
			if out.PullRequest == nil {
				out.PullRequest = &PullRequest{}
			}
			out.PullRequest.MaxTitle = stricter(out.PullRequest.MaxTitle, p.MaxTitle)
			out.PullRequest.MaxBody = stricter(out.PullRequest.MaxBody, p.MaxBody)
			out.PullRequest.Forbid = union(out.PullRequest.Forbid, p.Forbid)
		}
	}
	return &out
}

// stricter is the smaller limit; 0 means none.
func stricter(a, b int) int {
	if a == 0 || b != 0 && b < a {
		return b
	}
	return a
}

func union(a, b []string) []string {
	for _, s := range b {
		if !slices.Contains(a, s) {
			a = append(a, s)
		}
	}
	return a
}

// Set is compiled rules. A nil *Set checks nothing.
type Set struct {
	commit *checks
	pr     *checks
}

type checks struct {
	maxFirst, maxAll int // subject and message, or title and body
	forbid           []*regexp.Regexp
}

// Compile validates rules. Patterns are Go regular expressions (RE2), which
// run in time linear in the text, so no pattern can stall the broker.
func Compile(r *Rules) (*Set, error) {
	if r == nil {
		return nil, nil
	}
	var s Set
	var err error
	if c := r.Commit; c != nil {
		if s.commit, err = compile("commit", c.MaxSubject, c.MaxMessage, c.Forbid); err != nil {
			return nil, err
		}
	}
	if p := r.PullRequest; p != nil {
		if s.pr, err = compile("pull_request", p.MaxTitle, p.MaxBody, p.Forbid); err != nil {
			return nil, err
		}
	}
	if s.commit == nil && s.pr == nil {
		return nil, nil
	}
	return &s, nil
}

func compile(what string, first, all int, forbid []string) (*checks, error) {
	if first < 0 || all < 0 || first > maxLimit || all > maxLimit {
		return nil, fmt.Errorf("content_rules.%s: limits must be between 0 (none) and %d", what, maxLimit)
	}
	if len(forbid) > maxPatterns {
		return nil, fmt.Errorf("content_rules.%s: at most %d forbid patterns", what, maxPatterns)
	}
	c := &checks{maxFirst: first, maxAll: all}
	for _, p := range forbid {
		if p == "" || len(p) > maxPatternLen {
			return nil, fmt.Errorf("content_rules.%s: forbid patterns must be 1 to %d bytes", what, maxPatternLen)
		}
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("content_rules.%s: forbid %q: %w", what, p, err)
		}
		c.forbid = append(c.forbid, re)
	}
	if c.maxFirst == 0 && c.maxAll == 0 && len(c.forbid) == 0 {
		return nil, nil
	}
	return c, nil
}

// Commits reports whether there are commit rules.
func (s *Set) Commits() bool { return s != nil && s.commit != nil }

// PullRequests reports whether there are pull request rules.
func (s *Set) PullRequests() bool { return s != nil && s.pr != nil }

// CheckCommit returns what is wrong with a commit message, one line each;
// none when it passes. The subject is the message's first line.
func (s *Set) CheckCommit(message string) []string {
	if !s.Commits() {
		return nil
	}
	message = strings.TrimRight(message, "\n")
	subject, _, _ := strings.Cut(message, "\n")
	var out []string
	if n := length(subject); s.commit.maxFirst > 0 && n > s.commit.maxFirst {
		out = append(out, fmt.Sprintf("subject is %d characters; the limit is %d", n, s.commit.maxFirst))
	}
	if n := length(message); s.commit.maxAll > 0 && n > s.commit.maxAll {
		out = append(out, fmt.Sprintf("message is %d characters; the limit is %d", n, s.commit.maxAll))
	}
	return append(out, forbidden(s.commit.forbid, "message", message)...)
}

// CheckPullRequest returns what is wrong with a pull request's title and
// body; nil means the request doesn't set that field.
func (s *Set) CheckPullRequest(title, body *string) []string {
	if !s.PullRequests() {
		return nil
	}
	var out []string
	if title != nil {
		if n := length(*title); s.pr.maxFirst > 0 && n > s.pr.maxFirst {
			out = append(out, fmt.Sprintf("title is %d characters; the limit is %d", n, s.pr.maxFirst))
		}
		out = append(out, forbidden(s.pr.forbid, "title", *title)...)
	}
	if body != nil {
		if n := length(*body); s.pr.maxAll > 0 && n > s.pr.maxAll {
			out = append(out, fmt.Sprintf("body is %d characters; the limit is %d", n, s.pr.maxAll))
		}
		out = append(out, forbidden(s.pr.forbid, "body", *body)...)
	}
	return out
}

func forbidden(res []*regexp.Regexp, field, text string) []string {
	var out []string
	for _, re := range res {
		if loc := re.FindStringIndex(text); loc != nil {
			out = append(out, fmt.Sprintf("%s matches forbidden pattern %s: %q", field, re, clip(text[loc[0]:loc[1]])))
		}
	}
	return out
}

// length counts characters; text that isn't UTF-8 counts bytes.
func length(s string) int {
	if utf8.ValidString(s) {
		return utf8.RuneCountInString(s)
	}
	return len(s)
}

func clip(s string) string {
	const max = 80
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
