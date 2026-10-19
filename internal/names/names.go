// Package names holds the identifier rules both binaries share. A project
// name becomes a container, network, tmux and URL component; a repo is
// substituted into the broker's path rules as {repo}. Both must be plain, so
// neither can read as a flag, a path or a wildcard.
package names

import (
	"regexp"
	"strings"
)

var (
	project = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	repo    = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)
)

// Project reports whether s is a valid project name. web/shared/lib.js
// keeps a copy of this rule for routing.
func Project(s string) bool { return project.MatchString(s) }

// Repo reports whether s is a GitHub "owner/name" with no dot segments.
func Repo(s string) bool {
	return repo.MatchString(s) && !strings.Contains(s, "..") && !strings.HasSuffix(s, "/.")
}
