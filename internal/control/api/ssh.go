package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/sshkeys"
	"github.com/zaneGittins/skybreaker/internal/tsauth"
	"github.com/zaneGittins/skybreaker/internal/webutil"
)

// sshIdle drops a client that has sent nothing, not even its keep-alive,
// for this long.
var sshIdle = 90 * time.Second

// relays are the open SSH relays, so removing a key can cut them.
type relays struct {
	mu   sync.Mutex
	next int
	open map[int]context.CancelFunc
}

func (r *relays) add(cancel context.CancelFunc) (remove func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.open == nil {
		r.open = map[int]context.CancelFunc{}
	}
	r.next++
	id := r.next
	r.open[id] = cancel
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.open, id)
	}
}

func (r *relays) cutAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cancel := range r.open {
		cancel()
	}
}

func (s *server) sshKeys(w http.ResponseWriter, r *http.Request) {
	if s.SSHKeys == nil {
		webutil.Error(w, http.StatusNotFound, "SSH is off")
		return
	}
	webutil.WriteJSON(w, http.StatusOK, nonNil(s.SSHKeys.List()))
}

// addSSHKey lets a machine's key into every sandbox, for an editor over SSH.
func (s *server) addSSHKey(w http.ResponseWriter, r *http.Request) {
	if s.SSHKeys == nil {
		webutil.Error(w, http.StatusNotFound, "SSH is off")
		return
	}
	var in struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		webutil.Error(w, http.StatusBadRequest, "bad key: "+err.Error())
		return
	}
	p, _ := tsauth.FromContext(r.Context())
	k, err := s.SSHKeys.Add(in.Name, in.Key, p.Name())
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sshkeys.ErrInvalid) {
			status = http.StatusBadRequest
		}
		webutil.Error(w, status, err.Error())
		return
	}
	if err := s.Backend.SetAuthorizedKeys(s.SSHKeys.AuthorizedKeys()); err != nil {
		webutil.Error(w, http.StatusInternalServerError, "saved, but couldn't update the sandboxes: "+err.Error())
		return
	}
	webutil.WriteJSON(w, http.StatusOK, k)
}

func (s *server) deleteSSHKey(w http.ResponseWriter, r *http.Request) {
	if s.SSHKeys == nil {
		webutil.Error(w, http.StatusNotFound, "SSH is off")
		return
	}
	if err := s.SSHKeys.Delete(r.PathValue("id")); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sshkeys.ErrNotFound) {
			status = http.StatusNotFound
		}
		webutil.Error(w, status, err.Error())
		return
	}
	err := s.Backend.SetAuthorizedKeys(s.SSHKeys.AuthorizedKeys())
	// sshd checks a key only at login, and a relay doesn't know which key
	// its connection used: cut them all. Editors reconnect with the keys
	// that remain.
	s.ssh.cutAll()
	if err != nil {
		webutil.Error(w, http.StatusInternalServerError, "removed, but couldn't update the sandboxes: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sshRelay carries an SSH connection between your machine's skybreaker
// client and the project's sandbox sshd, over the app's own origin like a
// terminal: a preview page or any other site can't open one.
func (s *server) sshRelay(w http.ResponseWriter, r *http.Request) {
	if !tsauth.SameOrigin(r) {
		webutil.Error(w, http.StatusForbidden, "cross-origin SSH refused")
		return
	}
	if s.SSHKeys == nil {
		webutil.Error(w, http.StatusNotFound, "SSH is off")
		return
	}
	name := r.PathValue("p")
	if !s.known(w, name) {
		return
	}
	tcp, err := s.Backend.SSHConn(r.Context(), name)
	if err != nil {
		webutil.Error(w, http.StatusBadGateway, "couldn't reach the sandbox's SSH server: "+err.Error())
		return
	}
	defer tcp.Close()
	ws, err := s.Upgrade(w, r)
	if err != nil {
		return
	}
	defer ws.Close(1000, "")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer s.ssh.add(cancel)()
	go func() {
		defer cancel()
		buf := make([]byte, 32<<10)
		for {
			n, err := tcp.Read(buf)
			if n > 0 {
				if werr := ws.Write(ctx, true, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		rctx, rcancel := context.WithTimeout(ctx, sshIdle)
		binary, b, err := ws.Read(rctx)
		rcancel()
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
				log.Printf("skybreaker: ssh relay to %s ended: %v", name, err)
			}
			return
		}
		if !binary {
			continue // the client's keep-alive pings
		}
		if _, err := tcp.Write(b); err != nil {
			return
		}
	}
}
