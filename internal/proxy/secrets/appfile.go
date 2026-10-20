package secrets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/zaneGittins/skybreaker/internal/configfile"
)

// AppFile is the secrets added in Skybreaker's settings, kept in the data
// directory. The settings never show a value again: only its name, hosts
// and a fingerprint.
type AppFile struct {
	// Key keys the fingerprints, so one can't be checked against a guess
	// without this file.
	Key     string   `json:"key"`
	Secrets []Stored `json:"secrets"`
}

// Stored is one secret added in the app.
type Stored struct {
	Spec
	AddedBy string    `json:"added_by"`
	Added   time.Time `json:"added"`
	// App describes a GitHub App created in the app, for its settings page.
	App *AppInfo `json:"app,omitempty"`
}

type AppInfo struct {
	Slug    string `json:"slug"`
	Owner   string `json:"owner"`
	HTMLURL string `json:"html_url"`
}

// ReadAppFile reads the app's secrets; a missing file (or none) has none.
func ReadAppFile(path string) (AppFile, error) {
	var f AppFile
	if path == "" {
		return f, nil
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if _, err := configfile.ReadSecret(path); err != nil {
		return f, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if err := configfile.DecodeJSON(b, &f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// Fingerprint tells two values apart without giving either away.
func (f AppFile) Fingerprint(value string) string {
	m := hmac.New(sha256.New, []byte(f.Key))
	m.Write([]byte(value))
	return hex.EncodeToString(m.Sum(nil))[:12]
}

// Merge adds the app's secrets to the config file's. The app may add only
// values typed into it (static) and GitHub Apps it created, never a path
// or variable on this host to read, and never a name the file defines,
// except to fill in a github_app the file leaves to it (from_app), whose
// hosts stay the file's.
func Merge(file []Spec, app []Stored) ([]Spec, error) {
	out := append([]Spec(nil), file...)
	at := map[string]int{}
	for i, sp := range file {
		at[sp.Name] = i
	}
	seen := map[string]bool{}
	for _, st := range app {
		sp := st.Spec
		if seen[sp.Name] {
			return nil, fmt.Errorf("secrets: %s is added twice in the app", sp.Name)
		}
		seen[sp.Name] = true
		switch {
		case sp.Kind == "static" && sp.GitHubApp == nil && sp.TokenRequest == nil && sp.File == "" && sp.Env == "":
		case sp.Kind == "github_app" && sp.GitHubApp != nil && sp.GitHubApp.Key != "" && sp.GitHubApp.KeyFile == "" &&
			sp.GitHubApp.APIBase == "" && !sp.GitHubApp.FromApp && sp.File == "" && sp.Env == "" && sp.Value == "":
		default:
			return nil, fmt.Errorf("secrets: %s: the app can add only typed-in values and the GitHub App it creates", sp.Name)
		}
		i, dup := at[sp.Name]
		if !dup {
			at[sp.Name] = len(out)
			out = append(out, sp)
			continue
		}
		f := file[i]
		if f.Kind != "github_app" || f.GitHubApp == nil || !f.GitHubApp.FromApp || sp.Kind != "github_app" {
			return nil, fmt.Errorf("secrets: %s is in the secrets file and in the app", sp.Name)
		}
		g := *f.GitHubApp
		g.FromApp, g.AppID, g.Key = false, sp.GitHubApp.AppID, sp.GitHubApp.Key
		f.GitHubApp = &g
		out[i] = f
	}
	return out, nil
}
