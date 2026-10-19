package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/zaneGittins/skybreaker/internal/configfile"
	"github.com/zaneGittins/skybreaker/internal/names"
)

// Added is a project created in the app, kept in projects.json beside the
// config file. It holds only what the app may set: a repo, its agents, a
// board key and the names of capabilities the config file defines. Hosts,
// rules, env and subnets stay in the file you edit, so nothing added
// through the app can reach further than the bundles you wrote.
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
func ProjectsPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "projects.json")
}

// Added reports whether a project was created in the app.
func (c *Config) Added(name string) bool { return c.added[name] }

func readAdded(path string) (addedFile, error) {
	var f addedFile
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := configfile.DecodeJSON(b, &f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// merge adds the app's projects to c. A name in both places is an error:
// the file you edit is never silently overridden.
func (c *Config) merge(f addedFile, path string) error {
	for name, a := range f.Projects {
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
		c.Projects[name] = &Project{Repo: a.Repo, Branch: a.Branch, Harnesses: a.Harnesses, Key: a.Key, Capabilities: a.Capabilities}
		c.added[name] = true
	}
	return nil
}

var addMu sync.Mutex

// AddProject adds a project to projects.json, after checking that the
// whole configuration still loads with it and passes check (the broker's
// secret bindings). On any failure the file is left as it was.
func AddProject(configPath, name string, a Added, check func(*Config) error) error {
	addMu.Lock()
	defer addMu.Unlock()
	path := ProjectsPath(configPath)
	f, err := readAdded(path)
	if err != nil {
		return err
	}
	if _, dup := f.Projects[name]; dup {
		return fmt.Errorf("project %q already exists", name)
	}
	if f.Projects == nil {
		f.Projects = map[string]*Added{}
	}
	f.Projects[name] = &a
	b, err := jsonIndent(f)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".projects-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	c, err := loadWith(configPath, tmp.Name())
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(c); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), path)
}

func jsonIndent(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	return append(b, '\n'), err
}
