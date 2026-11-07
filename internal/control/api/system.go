package api

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/zaneGittins/skybreaker/internal/control/app"
	host "github.com/zaneGittins/skybreaker/internal/control/system"
	"github.com/zaneGittins/skybreaker/internal/tsauth"
	"github.com/zaneGittins/skybreaker/internal/webutil"
)

type System interface {
	ProjectStorage(context.Context, string) (host.Storage, error)
	CleanStorage(context.Context, string, host.Cleanup) (host.Cleaned, error)
}

func (s *server) systemAdmin(w http.ResponseWriter, r *http.Request) bool {
	p, _ := tsauth.FromContext(r.Context())
	if !s.Config.Admin(p.Login) {
		webutil.Error(w, http.StatusForbidden, "only admins can view host resources")
		return false
	}
	if s.System == nil {
		webutil.Error(w, http.StatusNotFound, "host resources are unavailable")
		return false
	}
	return true
}

func (s *server) systemStatus(w http.ResponseWriter, r *http.Request) {
	if !s.systemAdmin(w, r) {
		return
	}
	webutil.WriteJSON(w, http.StatusOK, s.hostMetrics.Snapshot())
}

func (s *server) projectStorage(w http.ResponseWriter, r *http.Request) {
	if !s.systemAdmin(w, r) {
		return
	}
	project := r.PathValue("p")
	if _, ok := s.Config.Projects[project]; !ok {
		webutil.Error(w, http.StatusNotFound, "project not found")
		return
	}
	result, err := s.System.ProjectStorage(r.Context(), project)
	if err != nil {
		fail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, result)
}

func (s *server) cleanStorage(w http.ResponseWriter, r *http.Request) {
	if !s.systemAdmin(w, r) {
		return
	}
	by, ok := s.admin(w, r)
	if !ok {
		return
	}
	project := r.PathValue("p")
	if _, ok := s.Config.Projects[project]; !ok {
		webutil.Error(w, http.StatusNotFound, "project not found")
		return
	}
	var opts host.Cleanup
	if !decodeStrict(w, r, &opts) {
		return
	}
	if (!opts.Go && !opts.Docker) || opts.OlderThanDays < 1 || opts.OlderThanDays > 365 {
		webutil.Error(w, http.StatusBadRequest, "choose a cache and an age between 1 and 365 days")
		return
	}
	result, err := s.System.CleanStorage(r.Context(), project, opts)
	if err != nil {
		if errors.Is(err, app.ErrCleanupBusy) {
			webutil.Error(w, http.StatusConflict, err.Error())
		} else {
			fail(w, err)
		}
		return
	}
	log.Printf("skybreaker: %s cleaned caches in %s: %d bytes recovered, complete=%t", by, project, result.Recovered, result.Error == "")
	webutil.WriteJSON(w, http.StatusOK, result)
}
