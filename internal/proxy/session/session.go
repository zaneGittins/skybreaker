// Package session keeps the broker's live session tokens. A token maps to one
// project; the proxy accepts it only from that project's sandbox network, so
// a leaked token is useless anywhere else.
//
// Tokens are stored as SHA-256 hashes, in memory and on disk, so the state
// file is no help to someone who reads it.
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/names"
)

const DefaultTTL = 30 * 24 * time.Hour

type Info struct {
	ID      string    `json:"id"`
	Project string    `json:"project"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
}

type Registry struct {
	mu     sync.Mutex
	path   string
	now    func() time.Time
	byHash map[string]Info // hex sha256(token) → session
}

// stored is the on-disk form: hashes only.
type stored struct {
	Hash string `json:"hash"`
	Info
}

// NewRegistry loads path ("" keeps everything in memory). now defaults to
// time.Now.
func NewRegistry(path string, now func() time.Time) (*Registry, error) {
	if now == nil {
		now = time.Now
	}
	r := &Registry{path: path, now: now, byHash: map[string]Info{}}
	if path == "" {
		return r, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	var list []stored
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("session: reading %s: %w", path, err)
	}
	t := now()
	for _, s := range list {
		if s.Expires.After(t) {
			r.byHash[s.Hash] = s.Info
		}
	}
	return r, nil
}

func hashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func randomString(n int) string {
	b := make([]byte, n)
	rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return base64.RawURLEncoding.EncodeToString(b)
}

// Mint creates a token for project. ttl 0 means DefaultTTL.
func (r *Registry) Mint(project string, ttl time.Duration) (string, Info, error) {
	if !names.Project(project) {
		return "", Info{}, fmt.Errorf("session: invalid project name %q", project)
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	tok := randomString(32)
	t := r.now()
	info := Info{ID: randomString(9), Project: project, Created: t, Expires: t.Add(ttl)}
	h := hashToken(tok)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.byHash[h] = info
	if err := r.saveLocked(); err != nil {
		delete(r.byHash, h)
		return "", Info{}, err
	}
	return tok, info, nil
}

// Lookup returns the live session for tok. The map is keyed by a hash of the
// token, so lookup time says nothing about how close a guess was.
func (r *Registry) Lookup(tok string) (Info, bool) {
	if tok == "" {
		return Info{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	info, ok := r.byHash[hashToken(tok)]
	if !ok || !info.Expires.After(r.now()) {
		return Info{}, false
	}
	return info, true
}

// Revoke, RevokeProject and RevokeAll take effect in memory at once. A
// non-nil error means the change wasn't saved and would be undone by a
// restart, so callers must report it.
func (r *Registry) Revoke(id string) (bool, error) {
	n, err := r.revoke(func(i Info) bool { return i.ID == id })
	return n > 0, err
}

func (r *Registry) RevokeProject(project string) (int, error) {
	return r.revoke(func(i Info) bool { return i.Project == project })
}

func (r *Registry) RevokeAll() (int, error) {
	return r.revoke(func(Info) bool { return true })
}

// revoke drops matching sessions, counting only live ones. Revocation holds
// in memory even if the state file can't be written.
func (r *Registry) revoke(match func(Info) bool) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.now()
	n := 0
	for h, info := range r.byHash {
		if match(info) {
			if info.Expires.After(t) {
				n++
			}
			delete(r.byHash, h)
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, r.saveLocked()
}

// List returns live sessions, oldest first.
func (r *Registry) List() []Info {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.now()
	out := make([]Info, 0, len(r.byHash))
	for _, info := range r.byHash {
		if info.Expires.After(t) {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// saveLocked writes live sessions atomically: temp file, then rename.
func (r *Registry) saveLocked() error {
	if r.path == "" {
		return nil
	}
	t := r.now()
	list := make([]stored, 0, len(r.byHash))
	for h, info := range r.byHash {
		if info.Expires.After(t) {
			list = append(list, stored{Hash: h, Info: info})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Hash < list[j].Hash })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	f, err := os.CreateTemp(dir, ".sessions-*")
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("session: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("session: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("session: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	return nil
}
