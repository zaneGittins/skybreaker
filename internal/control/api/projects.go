package api

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"

	"github.com/zaneGittins/skybreaker/internal/control/config"
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
