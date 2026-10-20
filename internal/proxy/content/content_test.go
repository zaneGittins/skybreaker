package content

import (
	"strings"
	"testing"
)

func mustCompile(t *testing.T, r *Rules) *Set {
	t.Helper()
	s, err := Compile(r)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCheckCommit(t *testing.T) {
	s := mustCompile(t, &Rules{Commit: &Commit{MaxSubject: 10, MaxMessage: 40, Forbid: []string{`(?im)^co-authored-by:.*claude`}}})
	if got := s.CheckCommit("Short\n\nBody.\n"); got != nil {
		t.Errorf("clean commit: %v", got)
	}
	// Characters, not bytes: ten é's fit a limit of 10.
	if got := s.CheckCommit(strings.Repeat("é", 10) + "\n"); got != nil {
		t.Errorf("ten characters: %v", got)
	}
	got := s.CheckCommit("A subject that is too long\n\nBody that runs on and on and on.\n\nCo-Authored-By: Claude <noreply@anthropic.com>\n")
	want := []string{
		"subject is 26 characters; the limit is 10",
		"message is 108 characters; the limit is 40",
		`message matches forbidden pattern (?im)^co-authored-by:.*claude: "Co-Authored-By: Claude"`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestCheckPullRequest(t *testing.T) {
	s := mustCompile(t, &Rules{PullRequest: &PullRequest{MaxTitle: 5, MaxBody: 10, Forbid: []string{`(?i)generated with \[claude code\]`}}})
	title, body := "Too long a title", "🤖 Generated with [Claude Code](https://claude.com/claude-code)"
	got := s.CheckPullRequest(&title, &body)
	if len(got) != 3 || !strings.HasPrefix(got[0], "title is 16 characters") || !strings.HasPrefix(got[1], "body is") || !strings.HasPrefix(got[2], "body matches") {
		t.Errorf("got %q", got)
	}
	// A field the request doesn't set isn't checked.
	ok := "Fine"
	if got := s.CheckPullRequest(&ok, nil); got != nil {
		t.Errorf("title only: %v", got)
	}
	if s.Commits() || !s.PullRequests() {
		t.Error("wrong kinds")
	}
}

func TestCompile(t *testing.T) {
	for name, r := range map[string]*Rules{
		"bad regexp":     {Commit: &Commit{Forbid: []string{"("}}},
		"empty pattern":  {PullRequest: &PullRequest{Forbid: []string{""}}},
		"negative limit": {Commit: &Commit{MaxSubject: -1}},
		"huge limit":     {PullRequest: &PullRequest{MaxBody: 1 << 30}},
	} {
		if _, err := Compile(r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, r := range []*Rules{nil, {}, {Commit: &Commit{}}} {
		if s := mustCompile(t, r); s != nil || s.Commits() || s.PullRequests() {
			t.Errorf("%+v: not empty", r)
		}
	}
	var none *Set
	if none.CheckCommit("anything") != nil || none.CheckPullRequest(nil, nil) != nil {
		t.Error("nil set checked something")
	}
}

func TestMerge(t *testing.T) {
	file := &Rules{Commit: &Commit{MaxSubject: 72, Forbid: []string{"a"}}}
	app := &Rules{Commit: &Commit{MaxSubject: 50, MaxMessage: 500, Forbid: []string{"a", "b"}}, PullRequest: &PullRequest{MaxTitle: 60}}
	m := Merge(file, nil, app)
	if c := m.Commit; c.MaxSubject != 50 || c.MaxMessage != 500 || strings.Join(c.Forbid, ",") != "a,b" {
		t.Errorf("commit: %+v", c)
	}
	if p := m.PullRequest; p == nil || p.MaxTitle != 60 {
		t.Errorf("pull_request: %+v", p)
	}
	// A larger limit from another source never loosens a smaller one.
	if m := Merge(&Rules{Commit: &Commit{MaxSubject: 50}}, &Rules{Commit: &Commit{MaxSubject: 100}}); m.Commit.MaxSubject != 50 {
		t.Errorf("loosened to %d", m.Commit.MaxSubject)
	}
}
