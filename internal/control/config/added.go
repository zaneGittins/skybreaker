package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/zaneGittins/skybreaker/internal/configfile"
	"github.com/zaneGittins/skybreaker/internal/names"
)

// Added is a project created in the app, kept in projects.json beside the
// config file. It holds a repo, its agents, a board key and the names of
// the bundles it was given. Hosts and rules the app grants live in
// access.json (see Access), where they are checked as the app's; env and
// subnets stay in the file you edit.
type Added struct {
	Repo         string   `json:"repo"`
	Branch       string   `json:"branch,omitempty"`
	Harnesses    []string `json:"harnesses"`
	Key          string   `json:"key,omitempty"`
	Capabilities []string `json:"capabilities"`
}

type addedFile struct {
	Projects map[string]*Added `json:"projects"`
}

// ProjectsPath is where projects created in the app are kept.
func ProjectsPath(configPath string) string { return PathsFor(configPath).Projects }

// Added reports whether a project was created in the app.
func (c *Config) Added(name string) bool { return c.added[name] }

func readAdded(path string) (addedFile, error) {
	var f addedFile
	err := readJSON(path, &f)
	return f, err
}

// readJSON decodes a file the app writes; a missing one is empty.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := configfile.DecodeJSON(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// merge adds the app's projects to c. A name in both places is an error:
// the file you edit is never silently overridden.
func (c *Config) merge(f addedFile, path string) error {
	for _, name := range slices.Sorted(maps.Keys(f.Projects)) {
		a := f.Projects[name]
		if a == nil {
			return fmt.Errorf("%s: project %q is empty", path, name)
		}
		if _, dup := c.Projects[name]; dup {
			return fmt.Errorf("%s: project %q already exists in the config file", path, name)
		}
		if !names.Project(name) {
			return fmt.Errorf("%s: invalid project name %q", path, name)
		}
		if !names.Repo(a.Repo) {
			return fmt.Errorf("%s: project %q: invalid repo %q (owner/name)", path, name, a.Repo)
		}
		if len(a.Harnesses) == 0 {
			return fmt.Errorf("%s: project %q: choose at least one agent", path, name)
		}
		if c.Projects == nil {
			c.Projects = map[string]*Project{}
		}
		if c.added == nil {
			c.added = map[string]bool{}
		}
		c.Projects[name] = &Project{Repo: a.Repo, Branch: a.Branch, Harnesses: a.Harnesses, Key: a.Key, Capabilities: slices.Clone(a.Capabilities)}
		c.added[name] = true
	}
	return nil
}

// AddProject adds a project to projects.json, after checking that the
// whole configuration still loads with it and passes check (the broker's
// secret bindings). On any failure the file is left as it was.
func AddProject(configPath, name string, a Added, check func(*Config) error) error {
	_, _, err := Edit(configPath, func(f *AppFiles) error {
		if _, dup := f.Projects[name]; dup {
			return fmt.Errorf("project %q already exists", name)
		}
		if f.Projects == nil {
			f.Projects = map[string]*Added{}
		}
		f.Projects[name] = &a
		return nil
	}, check, false)
	return err
}

// writeTemp writes b to a new 0600 file beside path, for a rename into
// place once it checks out.
func writeTemp(path string, b []byte) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return "", err
	}
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}
