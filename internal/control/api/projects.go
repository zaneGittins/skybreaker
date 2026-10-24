package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/harness"
	"github.com/zaneGittins/skybreaker/internal/webutil"
)

// ProjectAdder creates projects from the app (see config.AddProject).
type ProjectAdder interface {
	AddProject(ctx context.Context, by, name string, a config.Added) (AddedProject, error)
}

// AddedProject is what creating a project did.
type AddedProject struct {
	Name string `json:"name"`
	// Restart is when Skybreaker restarts to run it: "now", or "when_idle"
	// once no agent is working.
	Restart string        `json:"restart"`
	GitHub  *GitHubAccess `json:"github,omitempty"`
}

// GitHubAccess is whether the GitHub App can reach the project's repo.
type GitHubAccess struct {
	Installed  bool   `json:"installed"`
	InstallURL string `json:"install_url,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (s *server) createProject(w http.ResponseWriter, r *http.Request) {
	by, ok := s.admin(w, r)
	if !ok {
		return
	}
	if s.Projects == nil {
		webutil.Error(w, http.StatusNotFound, "projects can't be created here")
		return
	}
	var in struct {
		Name string `json:"name"`
		config.Added
	}
	// Strict: only the fields the app may set. Hosts and rules stay in the
	// config file.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		webutil.Error(w, http.StatusBadRequest, "bad project: "+err.Error())
		return
	}
	out, err := s.Projects.AddProject(r.Context(), by, in.Name, in.Added)
	if err != nil {
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webutil.WriteJSON(w, http.StatusOK, out)
}

type capabilityView struct {
	Name    string   `json:"name"`
	Reaches []string `json:"reaches"` // hosts and routes
}

// capabilities lists the bundles a new project can be given.
func (s *server) capabilities(w http.ResponseWriter, r *http.Request) {
	out := []capabilityView{}
	for _, name := range slices.Sorted(maps.Keys(s.Config.Capabilities)) {
		v := capabilityView{Name: name, Reaches: []string{}}
		for _, e := range s.Config.Capabilities[name] {
			switch {
			case e.Host != "":
				v.Reaches = append(v.Reaches, e.Host)
			case e.Route != "":
				v.Reaches = append(v.Reaches, e.Route)
			}
		}
		out = append(out, v)
	}
	webutil.WriteJSON(w, http.StatusOK, out)
}

// setHarnesses changes which agents a project made in the app runs, in
// order: the first is the one it uses unless told otherwise. A bundle an
// agent needs to sign in can be granted with it. Like any access change,
// it applies when Skybreaker restarts.
func (s *server) setHarnesses(w http.ResponseWriter, r *http.Request) {
	by, ok := s.editor(w, r)
	if !ok {
		return
	}
	name := r.PathValue("p")
	var in struct {
		Harnesses  []string `json:"harnesses"`
		AddBundles []string `json:"add_bundles"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	var list []string
	for _, h := range in.Harnesses {
		switch {
		case h == harness.Shell:
			// every project has a shell
		case slices.Contains(list, h):
			webutil.Error(w, http.StatusBadRequest, h+" is listed twice")
			return
		default:
			list = append(list, h)
		}
	}
	if len(list) == 0 {
		webutil.Error(w, http.StatusBadRequest, "choose at least one agent")
		return
	}
	out, err := s.Settings.Apply(r.Context(), by, "set the agents of "+name+" to "+strings.Join(list, ", "), func(f *config.AppFiles) error {
		a := f.Projects[name]
		if a == nil {
			if _, inFile := s.Config.Projects[name]; inFile {
				return errors.New(name + "'s agents are set in the config file")
			}
			return fmt.Errorf("no project %s", name)
		}
		a.Harnesses = list
		if len(in.AddBundles) > 0 {
			g := f.Grant(name)
			for _, b := range in.AddBundles {
				if !slices.Contains(g.Capabilities, b) {
					g.Capabilities = append(g.Capabilities, b)
				}
			}
			f.SetGrant(name, g)
		}
		return nil
	}, false)
	if err != nil {
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]string{"restart": out.Restart})
}
