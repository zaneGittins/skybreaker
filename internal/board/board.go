// Package board is every project's task board: bugs, features and chores in
// Backlog → Todo → In Progress → Review → Done, with notes and a claim so two
// agents never take the same item. You work it in the UI; agents work their
// own project's board through MCP (mcp.go).
//
// The data is small, so it lives in memory and is written as one JSON
// snapshot, atomically, on every change. Item refs (BIF-12) are never reused.
package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Statuses, in board order. Todo is the column agents pull from; backlog is
// where captured-but-not-ready work waits.
var Statuses = []string{"backlog", "todo", "in_progress", "review", "done"}

// Types an item can have.
var Types = []string{"bug", "feature", "chore"}

// Priorities run from 0 (low) to 3 (urgent); 1 is the default.
const (
	PriorityLow    = 0
	PriorityNormal = 1
	PriorityHigh   = 2
	PriorityUrgent = 3
)

var PriorityNames = []string{"low", "normal", "high", "urgent"}

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Project is one board. Its slug is the Skybreaker project's name; its key
// prefixes item refs and is kept once given, so refs in commits stay valid.
type Project struct {
	Slug   string         `json:"slug"`
	Key    string         `json:"key"`
	Next   int            `json:"next"`
	Counts map[string]int `json:"counts,omitempty"`
}

type Item struct {
	Ref      string   `json:"ref"`
	Project  string   `json:"project"`
	Number   int      `json:"number"`
	Type     string   `json:"type"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Status   string   `json:"status"`
	Priority int      `json:"priority"`
	Position float64  `json:"position"`
	Assignee string   `json:"assignee"`
	Labels   []string `json:"labels"`
	// ClaimedBy identifies who holds a claim: an MCP session for agents, so
	// two sessions both called "claude" are still told apart. It never
	// leaves the store.
	ClaimedBy   string     `json:"claimed_by,omitempty"`
	NoteCount   int        `json:"note_count"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Notes       []Note     `json:"notes,omitempty"`
}

type Note struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"` // "comment" or "event"
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type state struct {
	Projects map[string]*Project `json:"projects"`
	Items    []*Item             `json:"items"`
	NextNote int64               `json:"next_note"`
}

type Store struct {
	path string
	now  func() time.Time

	mu     sync.Mutex
	st     state
	active []string // the configured projects, from the last Sync
	// OnChange, if set, hears about every change: the project, and the item
	// ref when one item changed. Called without the lock held.
	OnChange func(project, ref string)
}

// Open loads path, or starts empty when it doesn't exist. An empty path keeps
// everything in memory.
func Open(path string) (*Store, error) {
	s := &Store{path: path, now: func() time.Time { return time.Now().UTC() }, st: state{Projects: map[string]*Project{}, NextNote: 1}}
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
		return nil, fmt.Errorf("board: %s: %w", path, err)
	}
	if s.st.Projects == nil {
		s.st.Projects = map[string]*Project{}
	}
	if s.st.NextNote < 1 {
		s.st.NextNote = 1
	}
	return s, nil
}

// save writes the snapshot to a temp file and renames it over the old one.
// Callers hold s.mu.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.Marshal(s.st)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	f, err := os.CreateTemp(dir, ".board-*.json")
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

func (s *Store) changed(project, ref string) {
	if s.OnChange != nil {
		s.OnChange(project, ref)
	}
}

// --- projects ---------------------------------------------------------------

// Spec is a board a Skybreaker project should have. Key may be empty: an
// existing board keeps its key, a new one gets one derived from the name.
type Spec struct{ Slug, Key string }

var validKey = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,7}$`)

// Sync makes sure every project has a board. Boards of projects no longer
// configured are kept, out of sight, so their refs stay reserved.
func (s *Store) Sync(specs []Spec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = nil
	for _, sp := range specs {
		s.active = append(s.active, sp.Slug)
		key := strings.ToUpper(strings.TrimSpace(sp.Key))
		if key != "" && !validKey.MatchString(key) {
			return invalid("board key %q: use one letter then up to seven letters or digits", sp.Key)
		}
		p, ok := s.st.Projects[sp.Slug]
		if !ok {
			if key == "" {
				key = s.suggestKey(sp.Slug)
			}
			p = &Project{Slug: sp.Slug, Next: 1}
			s.st.Projects[sp.Slug] = p
		}
		if key != "" && key != p.Key {
			if other := s.keyOwner(key); other != "" && other != sp.Slug {
				return fmt.Errorf("%w: board key %s is %s's", ErrConflict, key, other)
			}
			p.Key = key
		}
	}
	return s.save()
}

// Slugs lists the configured projects' boards, sorted.
func (s *Store) Slugs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.active)
	slices.Sort(out)
	return out
}

func (s *Store) keyOwner(key string) string {
	for slug, p := range s.st.Projects {
		if strings.EqualFold(p.Key, key) {
			return slug
		}
	}
	return ""
}

// suggestKey derives a short unused key from a slug: "bifract" → "BIF".
func (s *Store) suggestKey(slug string) string {
	var letters []rune
	for _, r := range strings.ToUpper(slug) {
		if (r >= 'A' && r <= 'Z') || (len(letters) > 0 && r >= '0' && r <= '9') {
			letters = append(letters, r)
		}
	}
	base := string(letters)
	if base == "" {
		base = "P"
	}
	base = base[:min(3, len(base))]
	if s.keyOwner(base) == "" {
		return base
	}
	for i := 2; ; i++ {
		k := base + strconv.Itoa(i)
		if len(k) > 8 {
			k = "P" + strconv.Itoa(i)
		}
		if s.keyOwner(k) == "" {
			return k
		}
	}
}

// project finds a board by slug or key. Callers hold s.mu.
func (s *Store) project(ident string) (*Project, error) {
	if p, ok := s.st.Projects[ident]; ok {
		return p, nil
	}
	for _, p := range s.st.Projects {
		if strings.EqualFold(p.Key, ident) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("%w: no board %q", ErrNotFound, ident)
}

// GetProject returns a board with its item counts per status.
func (s *Store) GetProject(ident string) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.project(ident)
	if err != nil {
		return Project{}, err
	}
	out := *p
	out.Counts = map[string]int{}
	for _, st := range Statuses {
		out.Counts[st] = 0
	}
	for _, it := range s.st.Items {
		if it.Project == p.Slug {
			out.Counts[it.Status]++
		}
	}
	return out, nil
}

// --- items ------------------------------------------------------------------

type ItemInput struct {
	Type     string   `json:"type"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Status   string   `json:"status"`
	Priority *int     `json:"priority"`
	Labels   []string `json:"labels"`
}

type ItemPatch struct {
	Type     *string   `json:"type"`
	Title    *string   `json:"title"`
	Body     *string   `json:"body"`
	Status   *string   `json:"status"`
	Priority *int      `json:"priority"`
	Assignee *string   `json:"assignee"`
	Labels   *[]string `json:"labels"`
	Position *float64  `json:"position"`
	// claimant is set by claims only, never from JSON.
	claimant *string
}

type Filter struct {
	Project     string
	Statuses    []string
	Type        string
	Assignee    string
	Query       string
	IncludeDone bool
	Limit       int
}

// ParseRef splits "BIF-12" into its key and number.
func ParseRef(ref string) (string, int, error) {
	ref = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), "#"))
	i := strings.LastIndexAny(ref, "-#")
	if i <= 0 {
		return "", 0, invalid("ref %q should look like BIF-12", ref)
	}
	key := strings.ToUpper(ref[:i])
	n, err := strconv.Atoi(ref[i+1:])
	if err != nil || n <= 0 || !validKey.MatchString(key) {
		return "", 0, invalid("ref %q should look like BIF-12", ref)
	}
	return key, n, nil
}

// item finds an item by ref. Callers hold s.mu.
func (s *Store) item(ref string) (*Item, error) {
	key, n, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	for _, it := range s.st.Items {
		if it.Number == n && strings.EqualFold(s.st.Projects[it.Project].Key, key) {
			return it, nil
		}
	}
	return nil, fmt.Errorf("%w: no item %s-%d", ErrNotFound, key, n)
}

// view copies an item for callers: the claim holder stays inside, notes only
// when asked for.
func view(it *Item, notes bool) Item {
	out := *it
	out.ClaimedBy = ""
	out.Labels = slices.Clone(it.Labels)
	out.NoteCount = 0
	for _, n := range it.Notes {
		if n.Kind == "comment" {
			out.NoteCount++
		}
	}
	if notes {
		out.Notes = slices.Clone(it.Notes)
		if out.Notes == nil {
			out.Notes = []Note{}
		}
	} else {
		out.Notes = nil
	}
	return out
}

// GetItem returns an item with its notes, oldest first.
func (s *Store) GetItem(ref string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, err := s.item(ref)
	if err != nil {
		return Item{}, err
	}
	return view(it, true), nil
}

func statusIndex(st string) int { return slices.Index(Statuses, st) }

// ListItems returns matching items, board by board, each column in its board
// order; Done is newest first.
// History is a board's recent past, for a chart: how many items were open
// at since, and when each item created or finished after it was. Callers
// bucket the times into their own days.
type History struct {
	Since       time.Time   `json:"since"`
	OpenAtStart int         `json:"open_at_start"`
	Created     []time.Time `json:"created"`
	Done        []time.Time `json:"done"`
}

func (s *Store) History(project string, since time.Time) (History, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.project(project)
	if err != nil {
		return History{}, err
	}
	h := History{Since: since, Created: []time.Time{}, Done: []time.Time{}}
	for _, it := range s.st.Items {
		if it.Project != p.Slug {
			continue
		}
		done := it.Status == "done" && it.CompletedAt != nil
		if it.CreatedAt.Before(since) {
			if !done || !it.CompletedAt.Before(since) {
				h.OpenAtStart++
			}
		} else {
			h.Created = append(h.Created, it.CreatedAt)
		}
		if done && !it.CompletedAt.Before(since) {
			h.Done = append(h.Done, *it.CompletedAt)
		}
	}
	slices.SortFunc(h.Created, time.Time.Compare)
	slices.SortFunc(h.Done, time.Time.Compare)
	return h, nil
}

func (s *Store) ListItems(f Filter) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var project string
	if f.Project != "" {
		p, err := s.project(f.Project)
		if err != nil {
			return nil, err
		}
		project = p.Slug
	}
	var statuses []string
	for _, st := range f.Statuses {
		st = NormalizeStatus(st)
		if !ValidStatus(st) {
			return nil, invalid("unknown status %q (use %s)", st, strings.Join(Statuses, ", "))
		}
		statuses = append(statuses, st)
	}
	typ := ""
	if f.Type != "" {
		typ = NormalizeType(f.Type)
		if !ValidType(typ) {
			return nil, invalid("unknown type %q (use %s)", f.Type, strings.Join(Types, ", "))
		}
	}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	var out []*Item
	for _, it := range s.st.Items {
		switch {
		case project != "" && it.Project != project:
		case len(statuses) > 0 && !slices.Contains(statuses, it.Status):
		case len(statuses) == 0 && !f.IncludeDone && it.Status == "done":
		case typ != "" && it.Type != typ:
		case f.Assignee != "" && !strings.EqualFold(it.Assignee, f.Assignee):
		case q != "" && !strings.Contains(strings.ToLower(it.Title), q) && !strings.Contains(strings.ToLower(it.Body), q) &&
			!slices.ContainsFunc(it.Labels, func(l string) bool { return strings.Contains(l, q) }):
		default:
			out = append(out, it)
		}
	}
	slices.SortStableFunc(out, func(a, b *Item) int {
		if c := strings.Compare(a.Project, b.Project); c != 0 {
			return c
		}
		if c := statusIndex(a.Status) - statusIndex(b.Status); c != 0 {
			return c
		}
		if a.Status == "done" {
			return completed(b).Compare(completed(a))
		}
		if a.Position != b.Position {
			if a.Position < b.Position {
				return -1
			}
			return 1
		}
		return a.Number - b.Number
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	items := make([]Item, len(out))
	for i, it := range out {
		items[i] = view(it, false)
	}
	return items, nil
}

func completed(it *Item) time.Time {
	if it.CompletedAt != nil {
		return *it.CompletedAt
	}
	return it.UpdatedAt
}

// topPosition is a position above everything in a column. Callers hold s.mu.
func (s *Store) topPosition(project, status string) float64 {
	top, any := 0.0, false
	for _, it := range s.st.Items {
		if it.Project == project && it.Status == status && (!any || it.Position < top) {
			top, any = it.Position, true
		}
	}
	if !any {
		return 0
	}
	return top - 1
}

func (s *Store) addNote(it *Item, kind, author, body string) Note {
	n := Note{ID: s.st.NextNote, Kind: kind, Author: author, Body: body, CreatedAt: s.now()}
	s.st.NextNote++
	it.Notes = append(it.Notes, n)
	return n
}

func (s *Store) CreateItem(project string, in ItemInput, actor string) (Item, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return Item{}, invalid("title is required")
	}
	if in.Type == "" {
		in.Type = "feature"
	}
	in.Type = NormalizeType(in.Type)
	if !ValidType(in.Type) {
		return Item{}, invalid("unknown type %q (use %s)", in.Type, strings.Join(Types, ", "))
	}
	if in.Status == "" {
		in.Status = "backlog"
	}
	in.Status = NormalizeStatus(in.Status)
	if !ValidStatus(in.Status) {
		return Item{}, invalid("unknown status %q (use %s)", in.Status, strings.Join(Statuses, ", "))
	}
	prio := PriorityNormal
	if in.Priority != nil {
		prio = *in.Priority
	}
	if prio < PriorityLow || prio > PriorityUrgent {
		return Item{}, invalid("priority must be 0 (low) to 3 (urgent)")
	}

	s.mu.Lock()
	p, err := s.project(project)
	if err != nil {
		s.mu.Unlock()
		return Item{}, err
	}
	t := s.now()
	it := &Item{
		Ref: fmt.Sprintf("%s-%d", p.Key, p.Next), Project: p.Slug, Number: p.Next, Type: in.Type, Title: in.Title,
		Body: strings.TrimSpace(in.Body), Status: in.Status, Priority: prio, Position: s.topPosition(p.Slug, in.Status),
		Labels: cleanLabels(in.Labels), CreatedAt: t, UpdatedAt: t,
	}
	if in.Status == "done" {
		it.CompletedAt = &t
	}
	p.Next++
	s.addNote(it, "event", actor, "created in "+StatusLabel(in.Status))
	s.st.Items = append(s.st.Items, it)
	if err := s.save(); err != nil {
		s.mu.Unlock()
		return Item{}, err
	}
	out := view(it, true)
	s.mu.Unlock()
	s.changed(out.Project, out.Ref)
	return out, nil
}

func (s *Store) UpdateItem(ref string, patch ItemPatch, actor string) (Item, error) {
	return s.inItem(ref, func(*Item) (ItemPatch, error) { return patch, nil }, actor)
}

// inItem loads an item under the lock, lets decide inspect it and choose the
// patch, and applies that patch before letting go, so checks made in decide
// still hold when the write lands.
func (s *Store) inItem(ref string, decide func(*Item) (ItemPatch, error), actor string) (Item, error) {
	return s.mutate(func() (*Item, error) { return s.item(ref) }, decide, actor)
}

// mutate is inItem with the item chosen by pick, under the same lock.
func (s *Store) mutate(pick func() (*Item, error), decide func(*Item) (ItemPatch, error), actor string) (Item, error) {
	s.mu.Lock()
	it, err := pick()
	if err != nil {
		s.mu.Unlock()
		return Item{}, err
	}
	patch, err := decide(it)
	if err != nil {
		s.mu.Unlock()
		return Item{}, err
	}
	// Apply to a copy, so a validation error leaves the item untouched.
	next := *it
	next.Labels = slices.Clone(it.Labels)
	next.Notes = slices.Clone(it.Notes)
	if err := s.applyPatch(&next, patch, actor); err != nil {
		s.mu.Unlock()
		return Item{}, err
	}
	*it = next
	if patch.Position != nil {
		s.respaceIfCrowded(it.Project, it.Status)
	}
	if err := s.save(); err != nil {
		s.mu.Unlock()
		return Item{}, err
	}
	out := view(it, true)
	s.mu.Unlock()
	s.changed(out.Project, out.Ref)
	return out, nil
}

// applyPatch changes it in place. Callers hold s.mu.
func (s *Store) applyPatch(it *Item, patch ItemPatch, actor string) error {
	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if title == "" {
			return invalid("title is required")
		}
		it.Title = title
	}
	if patch.Body != nil {
		it.Body = strings.TrimSpace(*patch.Body)
	}
	if patch.Type != nil {
		t := NormalizeType(*patch.Type)
		if !ValidType(t) {
			return invalid("unknown type %q (use %s)", *patch.Type, strings.Join(Types, ", "))
		}
		it.Type = t
	}
	if patch.Priority != nil {
		if *patch.Priority < PriorityLow || *patch.Priority > PriorityUrgent {
			return invalid("priority must be 0 (low) to 3 (urgent)")
		}
		it.Priority = *patch.Priority
	}
	if patch.Labels != nil {
		it.Labels = cleanLabels(*patch.Labels)
	}
	if patch.Assignee != nil {
		it.Assignee = strings.TrimSpace(*patch.Assignee)
		it.ClaimedBy = strings.ToLower(it.Assignee)
	}
	if patch.claimant != nil {
		it.ClaimedBy = *patch.claimant
	}
	from := it.Status
	if patch.Status != nil {
		st := NormalizeStatus(*patch.Status)
		if !ValidStatus(st) {
			return invalid("unknown status %q (use %s)", *patch.Status, strings.Join(Statuses, ", "))
		}
		it.Status = st
	}
	moved := it.Status != from
	if patch.Position != nil {
		if math.IsNaN(*patch.Position) || math.IsInf(*patch.Position, 0) {
			return invalid("position must be a finite number")
		}
		it.Position = *patch.Position
	} else if moved {
		it.Position = s.topPosition(it.Project, it.Status)
	}
	t := s.now()
	if moved {
		it.CompletedAt = nil
		if it.Status == "done" {
			it.CompletedAt = &t
		}
		// Work handed back to the queue is nobody's any more.
		if (it.Status == "todo" || it.Status == "backlog") && patch.Assignee == nil {
			it.Assignee, it.ClaimedBy = "", ""
		}
		s.addNote(it, "event", actor, "moved "+StatusLabel(from)+" → "+StatusLabel(it.Status))
	}
	it.UpdatedAt = t
	return nil
}

// minGap is the closest two positions may get before a column is respaced.
// The UI drops cards at the midpoint of their neighbours, which halves the gap
// each time; respacing long before float64 runs out keeps the order exact.
const minGap = 1e-6

// respaceIfCrowded renumbers a column 0, 1, 2… in its current order once any
// two neighbouring positions are closer than minGap. Callers hold s.mu.
func (s *Store) respaceIfCrowded(project, status string) {
	var col []*Item
	for _, it := range s.st.Items {
		if it.Project == project && it.Status == status {
			col = append(col, it)
		}
	}
	slices.SortStableFunc(col, func(a, b *Item) int {
		if a.Position != b.Position {
			if a.Position < b.Position {
				return -1
			}
			return 1
		}
		return a.Number - b.Number
	})
	crowded := false
	for i := 1; i < len(col); i++ {
		if col[i].Position-col[i-1].Position < minGap {
			crowded = true
		}
	}
	if crowded {
		for i, it := range col {
			it.Position = float64(i)
		}
	}
}

// Claim says who is claiming: Agent is the name shown on the board, Claimant
// the identity that holds the claim (an MCP session). An empty Claimant
// means the agent name itself.
type Claim struct {
	Agent    string
	Claimant string
	Force    bool
}

func (c *Claim) normalize() error {
	c.Agent = strings.TrimSpace(c.Agent)
	if c.Agent == "" {
		return invalid("agent name is required to claim an item")
	}
	if c.Claimant == "" {
		c.Claimant = strings.ToLower(c.Agent)
	}
	return nil
}

// claimPatch moves an item to In Progress under the claim, refusing one that
// someone else holds, or one that is done, unless forced.
func claimPatch(it *Item, c Claim) (ItemPatch, error) {
	if !c.Force {
		if it.Status == "in_progress" && it.Assignee != "" && it.ClaimedBy != c.Claimant {
			who := it.Assignee
			if strings.EqualFold(it.Assignee, c.Agent) {
				who += " (in another session)"
			}
			return ItemPatch{}, fmt.Errorf("%w: %s is already in progress with %s; only take it over if the user asks", ErrConflict, it.Ref, who)
		}
		if it.Status == "done" {
			return ItemPatch{}, fmt.Errorf("%w: %s is already done", ErrConflict, it.Ref)
		}
	}
	status := "in_progress"
	return ItemPatch{Status: &status, Assignee: &c.Agent, claimant: &c.Claimant}, nil
}

// ClaimItem moves an item to In Progress under the claim. Of two agents
// racing for one item, exactly one wins and the other gets ErrConflict.
func (s *Store) ClaimItem(ref string, c Claim) (Item, error) {
	if err := c.normalize(); err != nil {
		return Item{}, err
	}
	return s.inItem(ref, func(it *Item) (ItemPatch, error) { return claimPatch(it, c) }, c.Agent)
}

// nextTodo is the highest priority Todo item, top of the column first.
// Callers hold s.mu.
func (s *Store) nextTodo(p *Project) (*Item, error) {
	var best *Item
	for _, it := range s.st.Items {
		if it.Project != p.Slug || it.Status != "todo" {
			continue
		}
		if best == nil || it.Priority > best.Priority || it.Priority == best.Priority &&
			(it.Position < best.Position || it.Position == best.Position && it.Number < best.Number) {
			best = it
		}
	}
	if best == nil {
		return nil, fmt.Errorf("%w: nothing in Todo for %s", ErrNotFound, p.Slug)
	}
	return best, nil
}

// NextItem is the item an agent should pick up next in a project.
func (s *Store) NextItem(project string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.project(project)
	if err != nil {
		return Item{}, err
	}
	it, err := s.nextTodo(p)
	if err != nil {
		return Item{}, err
	}
	return view(it, true), nil
}

// ClaimNext picks the next Todo item and claims it in one step, so agents
// asking at the same moment get different items.
func (s *Store) ClaimNext(project string, c Claim) (Item, error) {
	c.Force = false
	if err := c.normalize(); err != nil {
		return Item{}, err
	}
	return s.mutate(func() (*Item, error) {
		p, err := s.project(project)
		if err != nil {
			return nil, err
		}
		return s.nextTodo(p)
	}, func(it *Item) (ItemPatch, error) { return claimPatch(it, c) }, c.Agent)
}

func (s *Store) AddNote(ref, author, body string) (Note, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Note{}, invalid("note body is required")
	}
	s.mu.Lock()
	it, err := s.item(ref)
	if err != nil {
		s.mu.Unlock()
		return Note{}, err
	}
	n := s.addNote(it, "comment", strings.TrimSpace(author), body)
	it.UpdatedAt = n.CreatedAt
	if err := s.save(); err != nil {
		s.mu.Unlock()
		return Note{}, err
	}
	project, r := it.Project, it.Ref
	s.mu.Unlock()
	s.changed(project, r)
	return n, nil
}

func (s *Store) DeleteNote(ref string, id int64) error {
	s.mu.Lock()
	it, err := s.item(ref)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	i := slices.IndexFunc(it.Notes, func(n Note) bool { return n.ID == id && n.Kind == "comment" })
	if i < 0 {
		s.mu.Unlock()
		return fmt.Errorf("%w: no note %d on %s", ErrNotFound, id, it.Ref)
	}
	it.Notes = slices.Delete(it.Notes, i, i+1)
	if err := s.save(); err != nil {
		s.mu.Unlock()
		return err
	}
	project, r := it.Project, it.Ref
	s.mu.Unlock()
	s.changed(project, r)
	return nil
}

func (s *Store) DeleteItem(ref string) error {
	s.mu.Lock()
	it, err := s.item(ref)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.st.Items = slices.DeleteFunc(s.st.Items, func(x *Item) bool { return x == it })
	if err := s.save(); err != nil {
		s.mu.Unlock()
		return err
	}
	project := it.Project
	s.mu.Unlock()
	s.changed(project, "")
	return nil
}

// --- names ------------------------------------------------------------------

func ValidStatus(st string) bool { return slices.Contains(Statuses, st) }
func ValidType(t string) bool    { return slices.Contains(Types, t) }

// StatusLabel is the human name of a status.
func StatusLabel(st string) string {
	switch st {
	case "backlog":
		return "Backlog"
	case "todo":
		return "Todo"
	case "in_progress":
		return "In Progress"
	case "review":
		return "Review"
	case "done":
		return "Done"
	}
	return st
}

// NormalizeStatus accepts the loose spellings people and models use.
func NormalizeStatus(st string) string {
	st = strings.ToLower(strings.TrimSpace(st))
	st = strings.NewReplacer(" ", "_", "-", "_").Replace(st)
	switch st {
	case "inprogress", "doing", "wip", "progress", "started":
		return "in_progress"
	case "to_do", "ready":
		return "todo"
	case "complete", "completed", "closed", "finished":
		return "done"
	case "in_review", "reviewing":
		return "review"
	}
	return st
}

// NormalizeType accepts common aliases for the item types.
func NormalizeType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	switch t {
	case "feat", "features", "enhancement", "idea":
		return "feature"
	case "bugs", "fix", "defect":
		return "bug"
	case "task", "chores", "maintenance":
		return "chore"
	}
	return t
}

var labelJunk = regexp.MustCompile(`[\s,]+`)

// cleanLabels lowercases and de-duplicates labels; commas and spaces become
// dashes.
func cleanLabels(labels []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, l := range labels {
		l = strings.Trim(labelJunk.ReplaceAllString(strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "#"))), "-"), "-")
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

var (
	quickPrefix = regexp.MustCompile(`(?i)^(bug|b|feat|feature|f|chore|c|task)\s*:\s*`)
	quickLabel  = regexp.MustCompile(`(?:^|\s)#([a-zA-Z][\w-]*)`)
	quickBang   = regexp.MustCompile(`\s*(!{1,2})$`)
)

// ParseQuick reads the one-line capture syntax:
//
//	bug: search crashes on empty query #ui !
//
// A "bug:", "feat:" or "chore:" prefix (or b:/f:/c:) sets the type, #words
// become labels, and a trailing ! or !! makes it high or urgent.
func ParseQuick(text string) ItemInput {
	in := ItemInput{}
	text = strings.TrimSpace(text)
	if m := quickPrefix.FindStringSubmatch(text); m != nil {
		switch strings.ToLower(m[1]) {
		case "bug", "b":
			in.Type = "bug"
		case "chore", "c", "task":
			in.Type = "chore"
		default:
			in.Type = "feature"
		}
		text = text[len(m[0]):]
	}
	if m := quickBang.FindStringSubmatch(text); m != nil {
		p := PriorityHigh
		if len(m[1]) == 2 {
			p = PriorityUrgent
		}
		in.Priority = &p
		text = text[:len(text)-len(m[0])]
	}
	for _, m := range quickLabel.FindAllStringSubmatch(text, -1) {
		in.Labels = append(in.Labels, m[1])
	}
	text = quickLabel.ReplaceAllString(text, "")
	in.Title = strings.Join(strings.Fields(text), " ")
	return in
}
