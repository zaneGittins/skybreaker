package names

import "testing"

func TestProject(t *testing.T) {
	for s, want := range map[string]bool{
		"orbit":                        true,
		"a":                            true,
		"bifract-2":                    true,
		"0day":                         true,
		"":                             false,
		"-orbit":                       false, // would read as a docker or tmux flag
		"Orbit":                        false,
		"el_ara":                       false,
		"el.ara":                       false,
		"el/ara":                       false,
		"../x":                         false,
		"el ara":                       false,
		"orbit\n":                      false,
		"x" + string(make([]byte, 40)): false,
		"abcdefghijabcdefghijabcdefghijabcdefghij":  true, // 40
		"abcdefghijabcdefghijabcdefghijabcdefghijk": false,
	} {
		if got := Project(s); got != want {
			t.Errorf("Project(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestRepo(t *testing.T) {
	for s, want := range map[string]bool{
		"zaneGittins/orbit":  true,
		"a/b":                true,
		"octo-cat/my.repo_2": true,
		"zaneGittins":        false,
		"/orbit":             false,
		"zaneGittins/":       false,
		"a/b/c":              false,
		"zaneGittins/*":      false, // would widen {repo} rules to every repo
		"zaneGittins/**":     false,
		"zaneGittins/.":      false,
		"zaneGittins/..":     false,
		"zaneGittins/a..b":   false,
		"zane_g/orbit":       false,
		"a/b%2fc":            false,
		"a/b\n":              false,
		"a/b?x=1":            false,
	} {
		if got := Repo(s); got != want {
			t.Errorf("Repo(%q) = %v, want %v", s, got, want)
		}
	}
}
