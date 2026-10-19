package api

import (
	"net/http"

	"github.com/zaneGittins/skybreaker/internal/control/preview"
	"github.com/zaneGittins/skybreaker/internal/webutil"
)

type previewView struct {
	URL       string              `json:"url"`
	Port      int                 `json:"port"` // 0: none chosen
	Listening []preview.Listening `json:"listening"`
	Error     string              `json:"error,omitempty"` // why listening is empty
}

// previewOf answers the project's preview, or 404 when there is none.
func (s *server) previewOf(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	name := r.PathValue("p")
	if !s.known(w, name) {
		return "", "", false
	}
	origin := ""
	if s.Previews != nil {
		origin = s.Previews.Origin(name)
	}
	if origin == "" {
		webutil.Error(w, http.StatusNotFound, "previews are off")
		return "", "", false
	}
	return name, origin, true
}

func (s *server) preview(w http.ResponseWriter, r *http.Request) {
	name, origin, ok := s.previewOf(w, r)
	if !ok {
		return
	}
	v := previewView{URL: origin, Listening: []preview.Listening{}}
	if p, ok := s.Previews.Target(name); ok {
		v.Port = int(p)
	}
	if l, err := s.Backend.Listening(r.Context(), name); err != nil {
		v.Error = "couldn't list the sandbox's ports: is it running?"
	} else if l != nil {
		v.Listening = l
	}
	webutil.WriteJSON(w, http.StatusOK, v)
}

func (s *server) setPreview(w http.ResponseWriter, r *http.Request) {
	name, origin, ok := s.previewOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Port int `json:"port"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Previews.SetTarget(name, in.Port); err != nil {
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webutil.WriteJSON(w, http.StatusOK, previewView{URL: origin, Port: in.Port, Listening: []preview.Listening{}})
}
