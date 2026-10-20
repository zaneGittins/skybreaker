package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	policy "github.com/zaneGittins/skybreaker/internal/proxy/config"
	"github.com/zaneGittins/skybreaker/internal/proxy/secrets"
)

// ErrSettingsOff is an edit from the app when the config file says
// app_settings: false.
var ErrSettingsOff = errors.New("settings can't be changed in the app here (app_settings: false in the config file)")

// Paths are the files the app writes, in <data_dir>/settings. Each is read
// after the config file and can only add to it. They live in the data
// directory because the service can't write its config directory (systemd
// keeps /etc read-only to it), so nothing it does can change the file it
// reads admins and app_settings from.
type Paths struct {
	Projects string // projects created in the app
	Access   string // the app's bundles, and what it grants each project
	Secrets  string // secrets added in the app (never shown again)
}

// AppPaths are where this configuration's app files are.
func (c *Config) AppPaths() Paths { return pathsIn(c.DataDir) }

func pathsIn(dataDir string) Paths {
	dir := filepath.Join(dataDir, "settings")
	return Paths{
		Projects: filepath.Join(dir, "projects.json"),
		Access:   filepath.Join(dir, "access.json"),
		Secrets:  filepath.Join(dir, "app-secrets.json"),
	}
}

// Access is what the app adds to the config file's policy: bundles of its
// own, and per project more bundles and hosts. It holds hosts only: routes,
// private addresses and CA files reach into your network or this host, so
// they stay in the file you edit. Hosts are exact names, never wildcards,
// and every secret stays bound to the hosts it was added with.
type Access struct {
	Capabilities map[string][]*policy.Entry `json:"capabilities,omitempty"`
	Projects     map[string]*Grant          `json:"projects,omitempty"`
}

// Grant is a project's access from one source.
type Grant struct {
	Capabilities []string        `json:"capabilities,omitempty"`
	Allow        []*policy.Entry `json:"allow,omitempty"`
}

// AppFiles is everything the app may change.
type AppFiles struct {
	Projects map[string]*Added
	Access   Access
	Secrets  secrets.AppFile
}

// ReadAppFiles reads the app's files as they are on disk.
func ReadAppFiles(p Paths) (AppFiles, error) {
	var f AppFiles
	a, err := readAdded(p.Projects)
	if err != nil {
		return f, err
	}
	f.Projects = a.Projects
	if err := readJSON(p.Access, &f.Access); err != nil {
		return f, err
	}
	f.Secrets, err = secrets.ReadAppFile(p.Secrets)
	return f, err
}

// Grant is the app's part of a project's access: for a project made in the
// app, the bundles it was created with too.
func (f *AppFiles) Grant(project string) Grant {
	var g Grant
	if a := f.Projects[project]; a != nil {
		g.Capabilities = append(g.Capabilities, a.Capabilities...)
	}
	if x := f.Access.Projects[project]; x != nil {
		g.Capabilities = append(g.Capabilities, x.Capabilities...)
		g.Allow = append(g.Allow, x.Allow...)
	}
	return g
}

// SetGrant replaces the app's part of a project's access.
func (f *AppFiles) SetGrant(project string, g Grant) {
	if a := f.Projects[project]; a != nil {
		a.Capabilities = []string{}
	}
	if len(g.Capabilities) == 0 && len(g.Allow) == 0 {
		delete(f.Access.Projects, project)
		return
	}
	if f.Access.Projects == nil {
		f.Access.Projects = map[string]*Grant{}
	}
	f.Access.Projects[project] = &g
}

var bundleRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// checkAppEntry refuses what the app may not add: anything but a host.
func checkAppEntry(e *policy.Entry) error {
	switch {
	case e == nil:
		return errors.New("an entry is empty")
	case e.Route != "" || e.Upstream != "" || e.K8s != nil || len(e.AllowPrivate) > 0 || e.CAFile != "":
		return errors.New("the app adds hosts only: routes, private addresses and CA files stay in the config file")
	case e.Host == "":
		return errors.New("an entry needs a host")
	}
	return nil
}

// mergeAccess adds the app's bundles and grants to c. A bundle name the
// file uses is an error; a grant for a project that no longer exists is
// dropped, since grants only ever add.
func (c *Config) mergeAccess(a Access, path string) error {
	for _, name := range slices.Sorted(maps.Keys(a.Capabilities)) {
		if _, dup := c.Capabilities[name]; dup {
			return fmt.Errorf("%s: bundle %q is already in the config file", path, name)
		}
		if !bundleRe.MatchString(name) {
			return fmt.Errorf("%s: bundle name %q: use lowercase letters, digits, dashes and underscores", path, name)
		}
		for _, e := range a.Capabilities[name] {
			if err := checkAppEntry(e); err != nil {
				return fmt.Errorf("%s: bundle %s: %w", path, name, err)
			}
		}
		if c.Capabilities == nil {
			c.Capabilities = map[string][]*policy.Entry{}
		}
		c.Capabilities[name] = a.Capabilities[name]
		c.appBundles[name] = true
	}
	for _, name := range slices.Sorted(maps.Keys(a.Projects)) {
		g, p := a.Projects[name], c.Projects[name]
		if g == nil || p == nil {
			continue
		}
		for _, e := range g.Allow {
			if err := checkAppEntry(e); err != nil {
				return fmt.Errorf("%s: project %s: %w", path, name, err)
			}
		}
		for _, cp := range g.Capabilities {
			if !slices.Contains(p.Capabilities, cp) {
				p.Capabilities = append(p.Capabilities, cp)
			}
		}
		p.Allow = append(slices.Clone(p.Allow), g.Allow...)
	}
	return nil
}

// FromFile is a project's access as the config file gives it, before the
// app's; nothing for a project made in the app.
func (c *Config) FromFile(project string) Grant { return c.fileAccess[project] }

// AppBundle reports whether a bundle was made in the app.
func (c *Config) AppBundle(name string) bool { return c.appBundles[name] }

// Admin reports whether login may change settings in the app. Logins
// compare as tsauth.RequireUsers compares them, ignoring case.
func (c *Config) Admin(login string) bool {
	list := c.Admins
	if len(list) == 0 {
		list = c.Users
	}
	return login != "" && slices.ContainsFunc(list, func(l string) bool { return strings.EqualFold(l, login) })
}

// SettingsInApp reports whether admins may change settings in the app.
func (c *Config) SettingsInApp() bool { return c.AppSettings == nil || *c.AppSettings }

var editMu sync.Mutex

// Edit changes the app's files. It reads them, applies change, and saves
// what changed only if the whole configuration still loads and passes
// check (the broker's secret bindings), which sees the app's secrets as
// they would be saved. It returns the configuration before and after; a dry
// run checks and reports but saves nothing. On any failure every file is
// left as it was.
func Edit(configPath string, change func(*AppFiles) error, check func(*Config) error, dry bool) (before, after *Config, err error) {
	editMu.Lock()
	defer editMu.Unlock()
	if before, err = loadWith(configPath, nil); err != nil {
		return nil, nil, err
	}
	if !before.SettingsInApp() {
		return nil, nil, ErrSettingsOff
	}
	paths := before.AppPaths()
	if err := os.MkdirAll(filepath.Dir(paths.Access), 0o700); err != nil {
		return nil, nil, err
	}
	f, err := ReadAppFiles(paths)
	if err != nil {
		return nil, nil, err
	}
	if err := change(&f); err != nil {
		return nil, nil, err
	}
	if len(f.Secrets.Secrets) > 0 && f.Secrets.Key == "" {
		k := make([]byte, 16)
		rand.Read(k)
		f.Secrets.Key = hex.EncodeToString(k)
	}

	staged := paths
	var renames [][2]string
	defer func() {
		for _, r := range renames {
			os.Remove(r[0]) // gone already once renamed
		}
	}()
	for _, x := range []struct {
		path *string
		v    any
		zero any
	}{
		{&staged.Projects, addedFile{Projects: f.Projects}, addedFile{}},
		{&staged.Access, f.Access, Access{}},
		{&staged.Secrets, f.Secrets, secrets.AppFile{}},
	} {
		b, err := jsonIndent(x.v)
		if err != nil {
			return nil, nil, err
		}
		old, rerr := os.ReadFile(*x.path)
		zero, _ := jsonIndent(x.zero)
		if rerr == nil && bytes.Equal(old, b) || errors.Is(rerr, os.ErrNotExist) && bytes.Equal(b, zero) {
			continue
		}
		tmp, err := writeTemp(*x.path, b)
		if err != nil {
			return nil, nil, err
		}
		renames = append(renames, [2]string{tmp, *x.path})
		*x.path = tmp
	}
	if after, err = loadWith(configPath, &staged); err != nil {
		return nil, nil, err
	}
	if check != nil {
		if err := check(after); err != nil {
			return nil, nil, err
		}
	}
	after.AppSecretsFile = paths.Secrets
	if dry {
		return before, after, nil
	}
	for _, r := range renames {
		if err := os.Rename(r[0], r[1]); err != nil {
			return nil, nil, err
		}
	}
	return before, after, nil
}

func jsonIndent(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	return append(b, '\n'), err
}
