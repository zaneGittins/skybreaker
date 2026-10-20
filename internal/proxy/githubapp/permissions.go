package githubapp

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// Needed are the permissions agents use, which a new App asks for: write
// to code, pull requests and issues, and read CI (Actions runs and logs,
// check runs and commit statuses), so an agent can see why its pull
// request failed. Never workflows, administration or secrets.
var Needed = map[string]string{
	"contents": "write", "pull_requests": "write", "issues": "write", "metadata": "read",
	"actions": "read", "checks": "read", "statuses": "read",
}

// Lacking is what the App, and each installation of it, has short of
// Needed, as "name:level". An installation lags the App until its owner
// accepts the App's new permissions there.
type Lacking struct {
	App []string `json:"app,omitempty"`
	// PermissionsURL is where the App's permissions are changed.
	PermissionsURL string            `json:"permissions_url,omitempty"`
	Installations  []InstallationLag `json:"installations,omitempty"`
}

type InstallationLag struct {
	Account string   `json:"account"`
	URL     string   `json:"url"` // where its owner accepts new permissions
	Lacks   []string `json:"lacks"`
}

// None reports whether nothing is lacking.
func (l Lacking) None() bool { return len(l.App) == 0 && len(l.Installations) == 0 }

// Lacking checks the App's permissions, and its installations', against
// Needed.
func (a *App) Lacking(ctx context.Context) (Lacking, error) {
	var app struct {
		Slug        string            `json:"slug"`
		Permissions map[string]string `json:"permissions"`
		Owner       struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"owner"`
	}
	if err := a.call(ctx, http.MethodGet, "/app", nil, http.StatusOK, &app); err != nil {
		return Lacking{}, err
	}
	var l Lacking
	if l.App = lacks(app.Permissions); len(l.App) > 0 && slugRe.MatchString(app.Slug) {
		l.PermissionsURL = "https://github.com/settings/apps/" + app.Slug + "/permissions"
		if app.Owner.Type == "Organization" && orgRe.MatchString(app.Owner.Login) {
			l.PermissionsURL = "https://github.com/organizations/" + app.Owner.Login + "/settings/apps/" + app.Slug + "/permissions"
		}
	}
	for page := 1; page <= 10; page++ {
		var installs []struct {
			Account struct {
				Login string `json:"login"`
			} `json:"account"`
			HTMLURL     string            `json:"html_url"`
			Permissions map[string]string `json:"permissions"`
			SuspendedAt *string           `json:"suspended_at"`
		}
		if err := a.call(ctx, http.MethodGet, fmt.Sprintf("/app/installations?per_page=100&page=%d", page), nil, http.StatusOK, &installs); err != nil {
			return Lacking{}, err
		}
		for _, in := range installs {
			if in.SuspendedAt != nil {
				continue // its owner turned it off; it mints nothing
			}
			// What the App itself lacks, its installations lack too; only
			// the rest waits on the installation's owner.
			missing := slices.DeleteFunc(lacks(in.Permissions), func(p string) bool { return slices.Contains(l.App, p) })
			if len(missing) == 0 {
				continue
			}
			url := in.HTMLURL
			if !strings.HasPrefix(url, "https://github.com/") {
				url = ""
			}
			l.Installations = append(l.Installations, InstallationLag{Account: in.Account.Login, URL: url, Lacks: missing})
		}
		if len(installs) < 100 {
			break
		}
	}
	return l, nil
}

// lacks lists the Needed permissions held at a lower level, or not at
// all, sorted.
func lacks(have map[string]string) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(Needed)) {
		if level[have[name]] < level[Needed[name]] {
			out = append(out, name+":"+Needed[name])
		}
	}
	return out
}

var level = map[string]int{"read": 1, "write": 2, "admin": 3}
