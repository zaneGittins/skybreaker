package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/zaneGittins/skybreaker/internal/names"
)

// File is a file a routine shared with an update: a podcast it made, a
// report. The bytes are kept beside the store, under Kept.
type File struct {
	Name string `json:"name"` // its own name, as the agent had it
	Kind string `json:"kind"` // audio, image or file
	Size int64  `json:"size"`
	Kept string `json:"kept"`
}

var (
	keptFile = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|gif|webp|mp3|wav|ogg|m4a|bin)$`)
	// maxShared is all an assistant's shared files together.
	maxShared int64 = 2 << 30
)

// ShareFile posts an update carrying a file. The file goes when its update
// does, as newer ones push it out.
func (s *Store) ShareFile(name, session, text string, f File, data []byte, ext string) (Update, error) {
	if !names.Project(name) {
		return Update{}, fmt.Errorf("invalid assistant name %q", name)
	}
	sum := sha256.Sum256(data)
	f.Kept, f.Size = hex.EncodeToString(sum[:16])+ext, int64(len(data))
	if !keptFile.MatchString(f.Kept) {
		return Update{}, fmt.Errorf("can't keep a %q file", ext)
	}
	if text == "" {
		text = f.Name
	}
	if err := line("update", text, MaxUpdate); err != nil {
		return Update{}, err
	}
	if err := s.keepFile(name, f, data); err != nil {
		return Update{}, err
	}
	u, err := s.postUpdate(name, session, text, &f)
	s.tidyFiles(name)
	return u, err
}

// Shared is the kept copy of a file one of the assistant's updates carries.
func (s *Store) Shared(name, kept string) ([]byte, error) {
	if !names.Project(name) || !keptFile.MatchString(kept) || !s.carried(name)[kept] {
		return nil, os.ErrNotExist
	}
	if s.path == "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if b, ok := s.mem[name][kept]; ok {
			return b, nil
		}
		return nil, os.ErrNotExist
	}
	root, err := os.OpenRoot(s.filesDir(name))
	if err != nil {
		return nil, os.ErrNotExist
	}
	defer root.Close()
	return root.ReadFile(kept)
}

func (s *Store) filesDir(name string) string {
	return filepath.Join(filepath.Dir(s.path), "assistant-files", name)
}

// carried is the files the assistant's updates carry.
func (s *Store) carried(name string) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	if a := s.st.Assistants[name]; a != nil {
		for _, u := range a.Updates {
			if u.File != nil {
				out[u.File.Kept] = true
			}
		}
	}
	return out
}

func (s *Store) keepFile(name string, f File, data []byte) error {
	if s.path == "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.mem == nil {
			s.mem = map[string]map[string][]byte{}
		}
		if s.mem[name] == nil {
			s.mem[name] = map[string][]byte{}
		}
		s.mem[name][f.Kept] = slices.Clone(data)
		return nil
	}
	dir := s.filesDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var used int64
	if es, err := os.ReadDir(dir); err == nil {
		for _, e := range es {
			if i, err := e.Info(); err == nil && e.Name() != f.Kept {
				used += i.Size()
			}
		}
	}
	if used+f.Size > maxShared {
		return fmt.Errorf("%d GB of shared files kept: %w", maxShared>>30, ErrTooMany)
	}
	tmp, err := os.CreateTemp(dir, ".keep-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(dir, f.Kept))
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// tidyFiles removes the files no update carries any more.
func (s *Store) tidyFiles(name string) {
	keep := s.carried(name)
	if s.path == "" {
		s.mu.Lock()
		for k := range s.mem[name] {
			if !keep[k] {
				delete(s.mem[name], k)
			}
		}
		s.mu.Unlock()
		return
	}
	es, err := os.ReadDir(s.filesDir(name))
	if err != nil {
		return
	}
	for _, e := range es {
		if !keep[e.Name()] {
			os.Remove(filepath.Join(s.filesDir(name), e.Name()))
		}
	}
}
