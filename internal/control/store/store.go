// Package store keeps the control plane's records (sessions, scheduled runs
// and the broker token held for each sandbox) in one JSON file. The data is
// small and written rarely, so a snapshot rewritten atomically on every
// change is simpler than a database and just as safe.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

const (
	StatusRunning = "running"
	StatusExited  = "exited"
	StatusKilled  = "killed"

	RunRunning = "running"
	RunOK      = "ok"
	RunFailed  = "failed"
	RunTimeout = "timeout"
)

type Session struct {
	ID      string     `json:"id"`
	Project string     `json:"project"`
	Harness string     `json:"harness"`
	Title   string     `json:"title,omitempty"`
	Created time.Time  `json:"created"`
	Status  string     `json:"status"`
	Ended   *time.Time `json:"ended,omitempty"`
	Exit    *int       `json:"exit,omitempty"` // the harness's exit status, when tmux saw it
	// Screen is set by app.Sessions, never stored: an ended session's last
	// screen is still in tmux.
	Screen bool `json:"screen,omitempty"`

	// Mode is ModeChat for a session run in its harness's structured mode
	// (package chat), or empty for a terminal.
	Mode     string `json:"mode,omitempty"`
	Resume   string `json:"resume,omitempty"`    // the harness's own conversation ID
	PermMode string `json:"perm_mode,omitempty"` // chat.ModeAsk and friends
	// ChosenModel is one the user picked from the harness's list; empty
	// leaves the choice to the harness.
	ChosenModel string `json:"chosen_model,omitempty"`
	// Fork: the next start continues a copy of Resume's conversation, not
	// Resume itself. It clears once the copy has its own ID.
	Fork bool `json:"fork,omitempty"`
	// State is a chat's activity (Chat* below) and Note what it waits on or
	// why it failed; the inbox sorts on them.
	State   string    `json:"state,omitempty"`
	Note    string    `json:"note,omitempty"`
	Model   string    `json:"model,omitempty"` // what the harness says it runs
	Updated time.Time `json:"updated,omitzero"`
	Seen    time.Time `json:"seen,omitzero"`
	// Terminal is the tmux session a chat continues in while it's open;
	// Parent marks that session as belonging to its chat.
	Terminal string `json:"terminal,omitempty"`
	Parent   string `json:"parent,omitempty"`
	// Color and Goal are yours: a tint to tell chats apart, and the
	// objective pinned at the top of one.
	Color string `json:"color,omitempty"`
	Goal  string `json:"goal,omitempty"`
	// Worktree is the git worktree a session works in, on a branch of its
	// own, instead of the project's /workspace. Sessions that continue
	// another's work (a chat's terminal, a cleared chat) share its worktree.
	// Branch is the branch it was last on, and BranchFrom the commit the
	// branch started at. WorktreeGone marks a worktree tidied away after
	// its sessions ended; the branch stays.
	Worktree     string `json:"worktree,omitempty"`
	Branch       string `json:"branch,omitempty"`
	BranchFrom   string `json:"branch_from,omitempty"`
	WorktreeGone bool   `json:"worktree_gone,omitempty"`
}

const (
	ModeChat = "chat"

	ChatIdle    = "idle"
	ChatWorking = "working"
	ChatWaiting = "waiting" // on an approval or an answer
	ChatLogin   = "login"   // the harness needs signing in
	ChatFailed  = "failed"
)

type Run struct {
	ID        string    `json:"id"`
	Job       string    `json:"job"`
	Project   string    `json:"project"`
	Harness   string    `json:"harness"`
	Prompt    string    `json:"prompt"`
	GitSHA    string    `json:"git_sha,omitempty"`
	SessionID string    `json:"session_id,omitempty"` // the harness's own session, for --resume
	Window    string    `json:"window,omitempty"`     // tmux window in the skybreaker-runs session
	LogPath   string    `json:"log_path,omitempty"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished,omitzero"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	CostUSD   float64   `json:"cost_usd,omitempty"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
}

// TokenRef records the broker session token minted for a sandbox, by ID
// only, so it can be revoked when the sandbox stops.
type TokenRef struct {
	ID      string    `json:"id"`
	Minted  time.Time `json:"minted"`
	Expires time.Time `json:"expires,omitzero"`
}

type state struct {
	Sessions []Session           `json:"sessions"`
	Runs     []Run               `json:"runs"`
	Tokens   map[string]TokenRef `json:"tokens"`
	Previews map[string]uint16   `json:"previews,omitempty"` // project → its preview port, for good
}

type Store struct {
	path     string
	maxRuns  int
	maxEnded int // ended sessions kept; running ones always are
	onPrune  func(id string)

	mu sync.Mutex
	st state
}

// Open loads path, or starts empty when it doesn't exist. An empty path
// keeps everything in memory.
func Open(path string) (*Store, error) {
	s := &Store{path: path, maxRuns: 1000, maxEnded: 500, st: state{Tokens: map[string]TokenRef{}}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.st); err != nil {
		return nil, fmt.Errorf("store: %s: %w", path, err)
	}
	if s.st.Tokens == nil {
		s.st.Tokens = map[string]TokenRef{}
	}
	return s, nil
}

// save writes the snapshot to a temp file and renames it over the old one,
// so a crash never leaves half a file. Callers hold s.mu.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.st, "", " ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".skybreaker-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.path)
}

// --- sessions ---------------------------------------------------------------

func (s *Store) CreateSession(x Session) error {
	s.mu.Lock()
	if slices.ContainsFunc(s.st.Sessions, func(o Session) bool { return o.ID == x.ID }) {
		s.mu.Unlock()
		return fmt.Errorf("session %s: %w", x.ID, ErrExists)
	}
	s.st.Sessions = append(s.st.Sessions, x)
	gone := s.pruneSessionsLocked()
	err := s.save()
	onPrune := s.onPrune
	s.mu.Unlock()
	if onPrune != nil {
		for _, id := range gone {
			onPrune(id)
		}
	}
	return err
}

// OnPrune has f hear of each session dropped as too old, so what it kept
// for one (a chat's log and files) goes with it.
func (s *Store) OnPrune(f func(id string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onPrune = f
}

// pruneSessionsLocked drops the oldest ended sessions beyond maxEnded and
// returns their IDs.
func (s *Store) pruneSessionsLocked() []string {
	ended := 0
	for _, x := range s.st.Sessions {
		if x.Status != StatusRunning {
			ended++
		}
	}
	if ended <= s.maxEnded {
		return nil
	}
	slices.SortStableFunc(s.st.Sessions, func(a, b Session) int { return a.Created.Compare(b.Created) })
	drop := ended - s.maxEnded
	var gone []string
	s.st.Sessions = slices.DeleteFunc(s.st.Sessions, func(x Session) bool {
		if drop > 0 && x.Status != StatusRunning {
			drop--
			gone = append(gone, x.ID)
			return true
		}
		return false
	})
	return gone
}

func (s *Store) UpdateSession(id string, f func(*Session)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.st.Sessions, func(o Session) bool { return o.ID == id })
	if i < 0 {
		return fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	f(&s.st.Sessions[i])
	return s.save()
}

func (s *Store) Session(id string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.st.Sessions, func(o Session) bool { return o.ID == id })
	if i < 0 {
		return Session{}, false
	}
	return s.st.Sessions[i], true
}

// ListSessions returns a project's sessions (every project for ""), newest
// first.
func (s *Store) ListSessions(project string) []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Session
	for _, x := range s.st.Sessions {
		if project == "" || x.Project == project {
			out = append(out, x)
		}
	}
	slices.SortStableFunc(out, func(a, b Session) int { return b.Created.Compare(a.Created) })
	return out
}

// --- runs -------------------------------------------------------------------

func (s *Store) CreateRun(r Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.ContainsFunc(s.st.Runs, func(o Run) bool { return o.ID == r.ID }) {
		return fmt.Errorf("run %s: %w", r.ID, ErrExists)
	}
	s.st.Runs = append(s.st.Runs, r)
	if len(s.st.Runs) > s.maxRuns {
		slices.SortStableFunc(s.st.Runs, func(a, b Run) int { return a.Started.Compare(b.Started) })
		s.st.Runs = slices.Delete(s.st.Runs, 0, len(s.st.Runs)-s.maxRuns)
	}
	return s.save()
}

func (s *Store) UpdateRun(id string, f func(*Run)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.st.Runs, func(o Run) bool { return o.ID == id })
	if i < 0 {
		return fmt.Errorf("run %s: %w", id, ErrNotFound)
	}
	f(&s.st.Runs[i])
	return s.save()
}

func (s *Store) Run(id string) (Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.st.Runs, func(o Run) bool { return o.ID == id })
	if i < 0 {
		return Run{}, false
	}
	return s.st.Runs[i], true
}

// ListRuns returns up to limit runs (0 = all) for a project ("" = all),
// newest first.
func (s *Store) ListRuns(project string, limit int) []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Run
	for _, r := range s.st.Runs {
		if project == "" || r.Project == project {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b Run) int { return b.Started.Compare(a.Started) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// --- broker tokens ----------------------------------------------------------

func (s *Store) SetToken(project string, ref TokenRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Tokens[project] = ref
	return s.save()
}

func (s *Store) Token(project string) (TokenRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.st.Tokens[project]
	return r, ok
}

func (s *Store) DeleteToken(project string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.st.Tokens, project)
	return s.save()
}

// --- previews ---------------------------------------------------------------

// PreviewPort is the project's preview port, assigning the lowest in
// [lo, hi] that no project has had. A port is never given to another
// project, even one removed since, so a project's page can't inherit
// another's origin and what the browser kept for it.
func (s *Store) PreviewPort(project string, lo, hi uint16) (uint16, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.st.Previews[project]; ok {
		return p, nil
	}
	used := map[uint16]bool{}
	for _, p := range s.st.Previews {
		used[p] = true
	}
	for p := lo; p >= lo && p <= hi; p++ {
		if !used[p] {
			if s.st.Previews == nil {
				s.st.Previews = map[string]uint16{}
			}
			s.st.Previews[project] = p
			if err := s.save(); err != nil {
				delete(s.st.Previews, project)
				return 0, err
			}
			return p, nil
		}
	}
	return 0, fmt.Errorf("no preview ports left between %d and %d", lo, hi)
}
