// Package usage keeps the tokens harnesses report using, by UTC hour, and
// the account limits each last reported, for the home page's charts. Only
// what chats and scheduled runs report is counted: a terminal session's
// harness tells Skybreaker nothing.
package usage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
)

// keep is how long hours are kept.
const keep = 31 * 24 * time.Hour

// saveAfter batches saves: a busy chat reports after every request.
const saveAfter = 5 * time.Second

var harnessRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Hour is one harness's tokens in one UTC hour.
type Hour struct {
	T       time.Time `json:"t"`
	Harness string    `json:"harness"`
	Input   int64     `json:"input"`
	Cached  int64     `json:"cached"`
	Output  int64     `json:"output"`
}

// Limits are a harness's account windows as it last reported them.
type Limits struct {
	At      time.Time    `json:"at"`
	Windows []chat.Limit `json:"windows"`
}

// Report is what the UI draws.
type Report struct {
	Hours  []Hour            `json:"hours"`
	Limits map[string]Limits `json:"limits"`
}

type data struct {
	Hours  []Hour            `json:"hours"`
	Limits map[string]Limits `json:"limits"`
}

// Store is the counts, in a JSON file.
type Store struct {
	path string
	now  func() time.Time

	mu    sync.Mutex
	d     data
	timer *time.Timer
}

// Open reads the file, which may not exist yet.
func Open(path string) (*Store, error) {
	s := &Store{path: path, now: time.Now, d: data{Limits: map[string]Limits{}}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(b, &s.d); err != nil {
		return nil, err
	}
	if s.d.Limits == nil {
		s.d.Limits = map[string]Limits{}
	}
	return s, nil
}

// Add counts tokens a harness reported using.
func (s *Store) Add(harness string, u chat.Usage) {
	if !harnessRe.MatchString(harness) || u.Input < 0 || u.Cached < 0 || u.Output < 0 || u == (chat.Usage{}) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	t := now.Truncate(time.Hour)
	i := slices.IndexFunc(s.d.Hours, func(h Hour) bool { return h.T.Equal(t) && h.Harness == harness })
	if i < 0 {
		s.d.Hours = append(s.d.Hours, Hour{T: t, Harness: harness})
		i = len(s.d.Hours) - 1
	}
	h := &s.d.Hours[i]
	h.Input += u.Input
	h.Cached += u.Cached
	h.Output += u.Output
	s.d.Hours = slices.DeleteFunc(s.d.Hours, func(h Hour) bool { return now.Sub(h.T) > keep })
	s.saveLater()
}

// SetLimits keeps a harness's account windows as reported now.
func (s *Store) SetLimits(harness string, ls []chat.Limit) {
	if !harnessRe.MatchString(harness) || len(ls) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Limits[harness] = Limits{At: s.now().UTC(), Windows: slices.Clone(ls)}
	s.saveLater()
}

// Report gives the hours of the last days, oldest first, and every
// harness's limits.
func (s *Store) Report(days int) Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	since := s.now().Add(-time.Duration(days) * 24 * time.Hour)
	r := Report{Hours: []Hour{}, Limits: map[string]Limits{}}
	for _, h := range s.d.Hours {
		if !h.T.Before(since.Truncate(time.Hour)) {
			r.Hours = append(r.Hours, h)
		}
	}
	slices.SortFunc(r.Hours, func(a, b Hour) int {
		if c := a.T.Compare(b.T); c != 0 {
			return c
		}
		return strings.Compare(a.Harness, b.Harness)
	})
	for k, v := range s.d.Limits {
		v.Windows = slices.Clone(v.Windows)
		r.Limits[k] = v
	}
	return r
}

func (s *Store) saveLater() {
	if s.timer == nil {
		s.timer = time.AfterFunc(saveAfter, func() { s.Flush() })
	}
}

// Flush saves now.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	b, err := json.Marshal(s.d)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".usage-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
