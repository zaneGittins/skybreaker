// Package approval runs the broker's ask flow. A request that policy marks
// "ask" is held while a human decides. An approval is bound to a hash of the
// exact request (project, method, host, path, body) and is single use with a
// short TTL, so it can't be replayed or stretched to cover another request.
//
// If nobody decides within the hold, the caller gets Pending and the approval
// ID; an identical retry after approval passes once.
package approval

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type Field struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// View is what the approver sees. It is built from the broker's own parse of
// the request, never from anything the agent says about it.
type View struct {
	Title  string  `json:"title"`
	Fields []Field `json:"fields,omitempty"`
	Body   string  `json:"body,omitempty"`
}

// visible returns a copy whose text can't hide or reorder what the
// approver reads: bidi overrides, zero-width and tag characters, controls and
// line separators are shown as <U+XXXX>, and invalid UTF-8 as <0xNN>. Only the
// body keeps tabs and line breaks.
func (v View) visible() View {
	out := View{Title: reveal(v.Title, false), Body: reveal(v.Body, true)}
	for _, f := range v.Fields {
		out.Fields = append(out.Fields, Field{Label: reveal(f.Label, false), Value: reveal(f.Value, false)})
	}
	return out
}

func reveal(s string, body bool) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, "<0x%02X>", s[i])
		case body && (r == '\t' || r == '\n' || r == '\r'):
			b.WriteRune(r)
		case unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp):
			fmt.Fprintf(&b, "<U+%04X>", r)
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

type Request struct {
	Project, Method, Host, Path string
	Body                        []byte
	View                        View
	Sender                      string
}

type State string

const (
	StatePending  State = "pending"
	StateApproved State = "approved"
	StateDenied   State = "denied"
	StateUsed     State = "used"
	StateExpired  State = "expired"
)

type Approval struct {
	ID        string    `json:"id"`
	Hash      string    `json:"hash"`
	Project   string    `json:"project"`
	Method    string    `json:"method"`
	Host      string    `json:"host"`
	Path      string    `json:"path"`
	Sender    string    `json:"sender"`
	View      View      `json:"view"`
	State     State     `json:"state"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires"`
	DecidedBy string    `json:"decided_by,omitempty"`
	Decided   time.Time `json:"decided,omitzero"`
}

type Outcome int

const (
	Pending Outcome = iota
	Allowed
	Denied
	Throttled // the project already has MaxPending approvals waiting
)

func (o Outcome) String() string {
	switch o {
	case Allowed:
		return "allowed"
	case Denied:
		return "denied"
	case Throttled:
		return "throttled"
	}
	return "pending"
}

var (
	ErrNotFound   = errors.New("approval: not found")
	ErrNotPending = errors.New("approval: not pending")
)

type Options struct {
	PendingTTL  time.Duration // how long a request waits for a decision (default 10m)
	ApprovedTTL time.Duration // how long a decision stands for a retry (default 5m)
	MaxPending  int           // pending approvals per project (default 20)
	Now         func() time.Time
}

// keep decided and expired approvals this long for the UI's history.
const retention = time.Hour

type entry struct {
	a       Approval
	done    chan struct{} // closed when decided
	waiting int
}

type Store struct {
	opt    Options
	mu     sync.Mutex
	byID   map[string]*entry
	byHash map[string]*entry // latest approval per request hash
	subs   map[chan Approval]struct{}
}

func New(o Options) *Store {
	if o.PendingTTL <= 0 {
		o.PendingTTL = 10 * time.Minute
	}
	if o.ApprovedTTL <= 0 {
		o.ApprovedTTL = 5 * time.Minute
	}
	if o.MaxPending <= 0 {
		o.MaxPending = 20
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Store{opt: o, byID: map[string]*entry{}, byHash: map[string]*entry{}, subs: map[chan Approval]struct{}{}}
}

// Hash binds an approval to one exact request. Every field is length-prefixed
// so bytes can't shift between fields to make two requests collide.
func Hash(project, method, host, path string, body []byte) string {
	h := sha256.New()
	var n [8]byte
	for _, f := range []string{project, method, host, path} {
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
	b := sha256.Sum256(body)
	h.Write(b[:])
	return hex.EncodeToString(h.Sum(nil))
}

func newID() string {
	b := make([]byte, 10)
	rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// Request asks for r. A standing approval is consumed at once; otherwise the
// call waits up to hold for a decision.
func (s *Store) Request(ctx context.Context, r Request, hold time.Duration) (Approval, Outcome) {
	h := Hash(r.Project, r.Method, r.Host, r.Path, r.Body)

	s.mu.Lock()
	now := s.opt.Now()
	s.pruneLocked(now)
	e := s.byHash[h]
	if e != nil {
		s.expireLocked(e, now)
		switch e.a.State {
		case StateApproved:
			e.a.State = StateUsed
			a := e.a
			s.mu.Unlock()
			return a, Allowed
		case StateDenied:
			if now.Before(e.a.Expires) {
				a := e.a
				s.mu.Unlock()
				return a, Denied
			}
			e = nil // the denial has lapsed; ask again
		case StatePending:
			// Join it.
		default:
			e = nil
		}
	}
	if e == nil {
		// Each card holds memory and a phone push; a flood of distinct
		// requests is refused rather than queued.
		if s.pendingLocked(r.Project) >= s.opt.MaxPending {
			s.mu.Unlock()
			return Approval{Project: r.Project, State: StatePending}, Throttled
		}
		e = &entry{done: make(chan struct{}), a: Approval{
			ID: newID(), Hash: h, Project: r.Project, Method: r.Method, Host: r.Host, Path: r.Path,
			Sender: r.Sender, View: r.View.visible(), State: StatePending, Created: now, Expires: now.Add(s.opt.PendingTTL),
		}}
		s.byID[e.a.ID] = e
		s.byHash[h] = e
		s.publishLocked(e.a)
	}
	e.waiting++
	s.mu.Unlock()

	if hold > 0 {
		t := time.NewTimer(hold)
		select {
		case <-e.done:
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	e.waiting--
	s.expireLocked(e, s.opt.Now())
	switch e.a.State {
	case StateApproved:
		if ctx.Err() != nil {
			// The client is gone; leave the approval for its retry.
			return e.a, Pending
		}
		// The first waiter consumes it; anyone else sharing the approval
		// still needs one of their own.
		e.a.State = StateUsed
		return e.a, Allowed
	case StateDenied:
		return e.a, Denied
	}
	return e.a, Pending
}

func (s *Store) Decide(id string, approve bool, by string) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.byID[id]
	if e == nil {
		return Approval{}, ErrNotFound
	}
	now := s.opt.Now()
	s.expireLocked(e, now)
	if e.a.State != StatePending {
		return e.a, ErrNotPending
	}
	e.a.State = StateDenied
	if approve {
		e.a.State = StateApproved
	}
	e.a.DecidedBy = by
	e.a.Decided = now
	e.a.Expires = now.Add(s.opt.ApprovedTTL)
	close(e.done)
	s.publishLocked(e.a)
	return e.a, nil
}

// Expire ends the project's pending and approved requests ("" for every
// project), as revoking its token does: what was asked under it can't be
// approved or redeemed later. It returns how many it ended.
func (s *Store) Expire(project string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.byID {
		if project != "" && e.a.Project != project {
			continue
		}
		switch e.a.State {
		case StatePending:
			close(e.done)
		case StateApproved:
		default:
			continue
		}
		e.a.State = StateExpired
		s.publishLocked(e.a)
		n++
	}
	return n
}

func (s *Store) Get(id string) (Approval, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.opt.Now())
	e := s.byID[id]
	if e == nil {
		return Approval{}, false
	}
	s.expireLocked(e, s.opt.Now())
	return e.a, true
}

// List returns pending and recent approvals, newest first.
func (s *Store) List() []Approval {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	s.pruneLocked(now)
	out := make([]Approval, 0, len(s.byID))
	for _, e := range s.byID {
		s.expireLocked(e, now)
		out = append(out, e.a)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// Subscribe streams every created or decided approval. Slow subscribers miss
// events rather than holding up requests.
func (s *Store) Subscribe() (<-chan Approval, func()) {
	ch := make(chan Approval, 64)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subs, ch)
			s.mu.Unlock()
		})
	}
}

func (s *Store) publishLocked(a Approval) {
	for ch := range s.subs {
		select {
		case ch <- a:
		default:
		}
	}
}

// expireLocked moves a pending or approved approval past its TTL to expired.
func (s *Store) pendingLocked(project string) int {
	now := s.opt.Now()
	n := 0
	for _, e := range s.byID {
		if e.a.Project == project && e.a.State == StatePending && now.Before(e.a.Expires) {
			n++
		}
	}
	return n
}

func (s *Store) expireLocked(e *entry, now time.Time) {
	if (e.a.State == StatePending || e.a.State == StateApproved) && !now.Before(e.a.Expires) {
		e.a.State = StateExpired
	}
}

func (s *Store) pruneLocked(now time.Time) {
	for id, e := range s.byID {
		if e.waiting == 0 && now.Sub(e.a.Expires) > retention {
			delete(s.byID, id)
			if s.byHash[e.a.Hash] == e {
				delete(s.byHash, e.a.Hash)
			}
		}
	}
}
