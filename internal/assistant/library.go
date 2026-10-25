package assistant

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zaneGittins/skybreaker/internal/names"
)

// The library is what an assistant made for the user: podcasts, reports,
// pages. Each file is an item, kept beside the store until its
// collection's retention or the user lets it go; where the user is in it
// is kept with it, so every device picks up in the same place.

// What an item is, and so how it opens.
const (
	KindAudio = "audio"
	KindImage = "image"
	KindPDF   = "pdf"
	KindText  = "text" // Markdown or plain text, read in the reader
	KindView  = "view" // an HTML page the agent made, run sandboxed
	KindFile  = "file" // anything else, to download
)

// How a collection is laid out in the library.
const (
	LayoutList    = "list"
	LayoutPodcast = "podcast"
	LayoutIssues  = "issues"
	LayoutGallery = "gallery"
)

var (
	Kinds   = []string{KindAudio, KindImage, KindPDF, KindText, KindView, KindFile}
	Layouts = []string{LayoutList, LayoutPodcast, LayoutIssues, LayoutGallery}
)

const (
	MaxItemTitle    = 120
	MaxSummary      = 300
	MaxCollection   = 60
	MaxAbout        = 120
	MaxChapters     = 200
	MaxTranscript   = 1 << 20
	maxKeep         = 1000
	defaultKeepDays = 30 // for a new collection, and items in none
	minSpeed        = 0.5
	maxSpeed        = 3
	maxSeconds      = 1e6 // longer than any recording: about 11 days
)

// Chapter is a named point in an audio item, At seconds in.
type Chapter struct {
	At    float64 `json:"at"`
	Title string  `json:"title"`
}

// Item is one file in the library: what the agent said about it, the kept
// copies, and how far the user got.
type Item struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"` // the file's own name
	Title      string    `json:"title"`
	Summary    string    `json:"summary,omitempty"`
	Collection string    `json:"collection,omitempty"` // its collection's ID
	Kept       string    `json:"kept"`
	Cover      string    `json:"cover,omitempty"`      // a kept image
	Transcript string    `json:"transcript,omitempty"` // a kept .txt
	Size       int64     `json:"size"`
	Chapters   []Chapter `json:"chapters,omitempty"`
	Session    string    `json:"session,omitempty"` // the chat it came from
	At         time.Time `json:"at"`

	Duration float64 `json:"duration,omitempty"` // seconds, as the player found it
	Seen     bool    `json:"seen,omitempty"`
	// Progress is seconds into audio, the page of a PDF, or how far down
	// anything else is read, from 0 to 1.
	Progress float64 `json:"progress,omitempty"`
	Finished bool    `json:"finished,omitempty"`
	Star     bool    `json:"star,omitempty"` // kept whatever the retention
}

// Collection groups items, such as a daily briefing, and says how they're
// shown and kept. Keep is how many of the newest, KeepDays how long; 0 for
// either is no limit.
type Collection struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Layout   string  `json:"layout"`
	About    string  `json:"about,omitempty"` // one line, e.g. Every day at 7
	Keep     int     `json:"keep,omitempty"`
	KeepDays int     `json:"keep_days"`
	Speed    float64 `json:"speed,omitempty"` // audio's starting speed
	Notify   bool    `json:"notify,omitempty"`
	Cover    string  `json:"cover,omitempty"`
}

// CollectionPatch is a change to a collection: what's nil stays.
type CollectionPatch struct {
	Layout   *string
	About    *string
	Keep     *int
	KeepDays *int
	Speed    *float64
	Notify   *bool
}

// Blob is a file's bytes and the extension it's kept with.
type Blob struct {
	Data []byte
	Ext  string
}

// Share is a file going into the library. Item.Collection is the
// collection's title as the agent gave it.
type Share struct {
	Item       Item
	Data       []byte
	Ext        string
	Cover      *Blob
	Transcript []byte
}

// ItemState is a change to where the user is in an item: what's nil stays.
type ItemState struct {
	Seen     *bool    `json:"seen"`
	Progress *float64 `json:"progress"`
	Finished *bool    `json:"finished"`
	Star     *bool    `json:"star"`
	Duration *float64 `json:"duration"`
}

// File is a shared file as updates carried one before the library; Open
// moves each into the library.
type File struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
	Kept string `json:"kept"`
}

var (
	keptFile = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|gif|webp|mp3|wav|ogg|m4a|pdf|html|md|txt|bin)$`)
	coverExt = []string{".png", ".jpg", ".gif", ".webp"}
	slugRe   = regexp.MustCompile(`[^a-z0-9]+`)
	// maxShared is all an assistant's kept files together.
	maxShared int64 = 2 << 30
	maxItems        = 1000
)

// Slug is a collection's ID from its title.
func Slug(title string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 48 {
		s = strings.TrimRight(s[:48], "-")
	}
	return s
}

func keptName(data []byte, ext string) (string, error) {
	sum := sha256.Sum256(data)
	k := hex.EncodeToString(sum[:16]) + ext
	if !keptFile.MatchString(k) {
		return "", fmt.Errorf("can't keep a %q file", ext)
	}
	return k, nil
}

func checkChapters(cs []Chapter) ([]Chapter, error) {
	if len(cs) > MaxChapters {
		return nil, fmt.Errorf("%d chapters; at most %d", len(cs), MaxChapters)
	}
	out := make([]Chapter, 0, len(cs))
	for _, c := range cs {
		if c.At < 0 || c.At > maxSeconds || math.IsNaN(c.At) {
			return nil, fmt.Errorf("chapter %q starts at %v seconds", c.Title, c.At)
		}
		if c.Title = ClipLine(c.Title, MaxItemTitle); c.Title == "" {
			return nil, fmt.Errorf("a chapter at %v seconds has no title", c.At)
		}
		out = append(out, c)
	}
	slices.SortStableFunc(out, func(a, b Chapter) int { return cmp.Compare(a.At, b.At) })
	return out, nil
}

// AddItem puts a file in the library. The same file shared again is the
// same item, with the agent's new words and the user's place kept.
func (s *Store) AddItem(name string, sh Share) (Item, error) {
	it, _, err := s.addItem(name, sh)
	return it, err
}

func (s *Store) addItem(name string, sh Share) (Item, bool, error) {
	if !names.Project(name) {
		return Item{}, false, fmt.Errorf("invalid assistant name %q", name)
	}
	it := sh.Item
	if !slices.Contains(Kinds, it.Kind) {
		return Item{}, false, fmt.Errorf("unknown kind %q", it.Kind)
	}
	if len(sh.Data) == 0 {
		return Item{}, false, fmt.Errorf("%s is empty", it.Name)
	}
	var err error
	if it.Kept, err = keptName(sh.Data, sh.Ext); err != nil {
		return Item{}, false, err
	}
	it.Size = int64(len(sh.Data))
	it.Name = ClipLine(path.Base(it.Name), MaxItemTitle)
	if it.Title = ClipLine(it.Title, MaxItemTitle); it.Title == "" {
		it.Title = it.Name
	}
	it.Summary = ClipLine(it.Summary, MaxSummary)
	if it.Chapters, err = checkChapters(it.Chapters); err != nil {
		return Item{}, false, err
	}
	title := ClipLine(it.Collection, MaxCollection)
	if it.Collection = Slug(title); title != "" && it.Collection == "" {
		return Item{}, false, fmt.Errorf("collection %q needs a letter or digit", title)
	}
	files := []Blob{{sh.Data, sh.Ext}}
	if sh.Cover != nil {
		if !slices.Contains(coverExt, sh.Cover.Ext) {
			return Item{}, false, fmt.Errorf("a cover must be an image, not %q", sh.Cover.Ext)
		}
		if it.Cover, err = keptName(sh.Cover.Data, sh.Cover.Ext); err != nil {
			return Item{}, false, err
		}
		files = append(files, *sh.Cover)
	}
	if sh.Transcript != nil {
		if len(sh.Transcript) > MaxTranscript || !utf8.Valid(sh.Transcript) || bytes.IndexByte(sh.Transcript, 0) >= 0 {
			return Item{}, false, fmt.Errorf("a transcript must be UTF-8 text up to %d KB", MaxTranscript>>10)
		}
		if it.Transcript, err = keptName(sh.Transcript, ".txt"); err != nil {
			return Item{}, false, err
		}
		files = append(files, Blob{sh.Transcript, ".txt"})
	}
	defer s.tidyFiles(name)
	for _, f := range files {
		k, _ := keptName(f.Data, f.Ext)
		defer s.holding(name, k)()
		if err := s.keepFile(name, k, f.Data); err != nil {
			return Item{}, false, err
		}
	}
	var fresh, notify bool
	err = s.change(name, func(a *Assistant) error {
		if it.Collection != "" && a.collection(it.Collection) < 0 {
			a.Collections = append(a.Collections, Collection{ID: it.Collection, Title: title, Layout: LayoutList, KeepDays: defaultKeepDays})
		}
		if i := slices.IndexFunc(a.Library, func(x Item) bool { return x.Kept == it.Kept && x.Collection == it.Collection }); i >= 0 {
			old := a.Library[i]
			it.ID, it.At, it.Session = old.ID, old.At, cmpOr(it.Session, old.Session)
			it.Duration, it.Seen, it.Progress, it.Finished, it.Star = old.Duration, old.Seen, old.Progress, old.Finished, old.Star
			a.Library[i] = it
			return nil
		}
		it.ID, it.At, fresh = s.id("i"), s.now(), true
		a.Library = append(a.Library, it)
		if c := a.collection(it.Collection); c >= 0 {
			notify = a.Collections[c].Notify
		}
		s.prune(a, it.ID)
		return nil
	}, s.OnLibrary)
	if err != nil {
		return Item{}, false, err
	}
	if fresh && s.OnNewItem != nil {
		s.OnNewItem(name, it, notify)
	}
	return it, fresh, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (a *Assistant) collection(id string) int {
	return slices.IndexFunc(a.Collections, func(c Collection) bool { return c.ID == id })
}

func (a *Assistant) item(id string) (int, error) {
	i := slices.IndexFunc(a.Library, func(x Item) bool { return x.ID == id })
	if i < 0 {
		return 0, fmt.Errorf("item %s: %w", id, ErrNotFound)
	}
	return i, nil
}

// Prune applies every assistant's retention, for the libraries nothing
// was added to lately.
func (s *Store) Prune() {
	s.mu.Lock()
	names := slices.Sorted(maps.Keys(s.st.Assistants))
	s.mu.Unlock()
	for _, name := range names {
		changed := false
		s.change(name, func(a *Assistant) error {
			n := len(a.Library)
			s.prune(a, "")
			if changed = len(a.Library) != n; !changed {
				return errUnchanged
			}
			return nil
		}, s.OnLibrary)
		if changed {
			s.tidyFiles(name)
		}
	}
}

// errUnchanged leaves an assistant as it was, unsaved.
var errUnchanged = errors.New("unchanged")

// prune lets go of what the retention says has had its time: each
// collection keeps its newest Keep and those younger than KeepDays, items
// in none keep a month, and what's starred stays whatever, as does keep,
// the item just added.
func (s *Store) prune(a *Assistant, keepID string) {
	now := s.now()
	rules := map[string]Collection{"": {KeepDays: defaultKeepDays}}
	for _, c := range a.Collections {
		rules[c.ID] = c
	}
	seen := map[string]int{}
	keep := make([]bool, len(a.Library))
	kept := 0
	for i := len(a.Library) - 1; i >= 0; i-- {
		it := a.Library[i]
		r := rules[it.Collection]
		seen[it.Collection]++
		old := r.KeepDays > 0 && now.Sub(it.At) > time.Duration(r.KeepDays)*24*time.Hour
		over := r.Keep > 0 && seen[it.Collection] > r.Keep
		keep[i] = it.Star || it.ID == keepID || (!old && !over && kept < maxItems)
		if keep[i] {
			kept++
		}
	}
	out := a.Library[:0:0]
	for i, it := range a.Library {
		if keep[i] {
			out = append(out, it)
		}
	}
	a.Library = out
}

// ShareFile is a routine's share: an item, and an update on Today that
// points at it.
func (s *Store) ShareFile(name, session string, sh Share) (Item, Update, error) {
	sh.Item.Session = session
	it, err := s.AddItem(name, sh)
	if err != nil {
		return Item{}, Update{}, err
	}
	u, err := s.postUpdate(name, session, it.Title, it.ID)
	return it, u, err
}

// SetItemState changes where the user is in an item. Only seen, finished
// and starred are announced: progress comes every few seconds while
// something plays, and redrawing pages for it would be waste.
func (s *Store) SetItemState(name, id string, st ItemState) (Item, error) {
	for _, v := range []*float64{st.Progress, st.Duration} {
		if v != nil && (*v < 0 || *v > maxSeconds || math.IsNaN(*v)) {
			return Item{}, fmt.Errorf("%v is out of range", *v)
		}
	}
	if st.Duration != nil && *st.Duration == 0 {
		return Item{}, fmt.Errorf("a duration can't be 0")
	}
	var it Item
	news := false
	err := s.change(name, func(a *Assistant) error {
		i, err := a.item(id)
		if err != nil {
			return err
		}
		x := &a.Library[i]
		was := *x
		if st.Duration != nil {
			x.Duration = *st.Duration
		}
		if st.Progress != nil {
			x.Progress = *st.Progress
			x.Seen = x.Seen || *st.Progress > 0
		}
		if st.Finished != nil {
			x.Finished = *st.Finished
			x.Seen = x.Seen || x.Finished
		}
		if st.Star != nil {
			x.Star = *st.Star
		}
		if st.Seen != nil {
			x.Seen = *st.Seen
			if !x.Seen {
				// New again: from the start, as if never opened.
				x.Finished, x.Progress = false, 0
			}
		}
		news = x.Seen != was.Seen || x.Finished != was.Finished || x.Star != was.Star
		it = *x
		return nil
	}, func(n string) {
		if news && s.OnLibrary != nil {
			s.OnLibrary(n)
		}
	})
	return it, err
}

// DeleteItem lets an item go, its files with it.
func (s *Store) DeleteItem(name, id string) error {
	err := s.change(name, func(a *Assistant) error {
		i, err := a.item(id)
		if err == nil {
			a.Library = slices.Delete(a.Library, i, i+1)
		}
		return err
	}, s.OnLibrary)
	if err == nil {
		s.tidyFiles(name)
	}
	return err
}

// SetCollection makes or changes a collection, by its title. A new one is
// a list kept for a month until it's told otherwise.
func (s *Store) SetCollection(name, title string, p CollectionPatch, cover *Blob) (Collection, error) {
	if !names.Project(name) {
		return Collection{}, fmt.Errorf("invalid assistant name %q", name)
	}
	title = ClipLine(title, MaxCollection)
	id := Slug(title)
	if id == "" {
		return Collection{}, fmt.Errorf("collection %q needs a letter or digit", title)
	}
	switch {
	case p.Layout != nil && !slices.Contains(Layouts, *p.Layout):
		return Collection{}, fmt.Errorf("unknown layout %q: use %s", *p.Layout, strings.Join(Layouts, ", "))
	case p.Speed != nil && *p.Speed != 0 && (*p.Speed < minSpeed || *p.Speed > maxSpeed):
		return Collection{}, fmt.Errorf("speed %v: use %v to %v", *p.Speed, minSpeed, float64(maxSpeed))
	case p.Keep != nil && (*p.Keep < 0 || *p.Keep > maxKeep):
		return Collection{}, fmt.Errorf("keep %d: use 0 (no limit) to %d", *p.Keep, maxKeep)
	case p.KeepDays != nil && (*p.KeepDays < 0 || *p.KeepDays > 3650):
		return Collection{}, fmt.Errorf("keep_days %d: use 0 (no limit) to 3650", *p.KeepDays)
	}
	// Tidied last, once the cover is let go: one that wasn't saved goes.
	defer s.tidyFiles(name)
	var coverKept string
	if cover != nil {
		if !slices.Contains(coverExt, cover.Ext) {
			return Collection{}, fmt.Errorf("a cover must be an image, not %q", cover.Ext)
		}
		var err error
		if coverKept, err = keptName(cover.Data, cover.Ext); err != nil {
			return Collection{}, err
		}
		defer s.holding(name, coverKept)()
		if err := s.keepFile(name, coverKept, cover.Data); err != nil {
			return Collection{}, err
		}
	}
	var c Collection
	err := s.change(name, func(a *Assistant) error {
		i := a.collection(id)
		if i < 0 {
			a.Collections = append(a.Collections, Collection{ID: id, Layout: LayoutList, KeepDays: defaultKeepDays})
			i = len(a.Collections) - 1
		}
		x := &a.Collections[i]
		x.Title = title
		if p.Layout != nil {
			x.Layout = *p.Layout
		}
		if p.About != nil {
			x.About = ClipLine(*p.About, MaxAbout)
		}
		if p.Keep != nil {
			x.Keep = *p.Keep
		}
		if p.KeepDays != nil {
			x.KeepDays = *p.KeepDays
		}
		if p.Speed != nil {
			x.Speed = *p.Speed
		}
		if p.Notify != nil {
			x.Notify = *p.Notify
		}
		if coverKept != "" {
			x.Cover = coverKept
		}
		c = *x
		s.prune(a, "")
		return nil
	}, s.OnLibrary)
	return c, err
}

// migrate moves the files updates carried before the library into it.
func (s *Store) migrate(a *Assistant) {
	for i, u := range a.Updates {
		if u.File == nil {
			continue
		}
		f := u.File
		kind := f.Kind
		if !slices.Contains(Kinds, kind) {
			kind = KindFile
		}
		id := s.id("i")
		a.Library = append(a.Library, Item{ID: id, Kind: kind, Name: f.Name, Title: ClipLine(u.Text, MaxItemTitle), Kept: f.Kept, Size: f.Size, Session: u.Session, At: u.At})
		a.Updates[i].File, a.Updates[i].Item = nil, id
	}
}

// Shared opens a kept file the assistant's library holds.
func (s *Store) Shared(name, kept string) (io.ReadSeekCloser, error) {
	if !names.Project(name) || !keptFile.MatchString(kept) || !s.carried(name)[kept] {
		return nil, os.ErrNotExist
	}
	if s.path == "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if b, ok := s.mem[name][kept]; ok {
			return nopCloser{bytes.NewReader(b)}, nil
		}
		return nil, os.ErrNotExist
	}
	root, err := os.OpenRoot(s.filesDir(name))
	if err != nil {
		return nil, os.ErrNotExist
	}
	defer root.Close()
	return root.Open(kept)
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

func (s *Store) filesDir(name string) string {
	return filepath.Join(filepath.Dir(s.path), "assistant-files", name)
}

// holding keeps a file being kept from being tidied away before what
// holds it is saved; the func it returns lets it go.
func (s *Store) holding(name, kept string) func() {
	k := name + "/" + kept
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held == nil {
		s.held = map[string]int{}
	}
	s.held[k]++
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.held[k]--; s.held[k] == 0 {
			delete(s.held, k)
		}
	}
}

// carried is the files the library holds: items, their covers and
// transcripts, and collections' covers, and any being kept now.
func (s *Store) carried(name string) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for k := range s.held {
		if n, kept, _ := strings.Cut(k, "/"); n == name {
			out[kept] = true
		}
	}
	a := s.st.Assistants[name]
	if a == nil {
		return out
	}
	for _, it := range a.Library {
		for _, k := range []string{it.Kept, it.Cover, it.Transcript} {
			if k != "" {
				out[k] = true
			}
		}
	}
	for _, c := range a.Collections {
		if c.Cover != "" {
			out[c.Cover] = true
		}
	}
	return out
}

func (s *Store) keepFile(name, kept string, data []byte) error {
	s.files.Lock()
	defer s.files.Unlock()
	if s.path == "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.mem == nil {
			s.mem = map[string]map[string][]byte{}
		}
		if s.mem[name] == nil {
			s.mem[name] = map[string][]byte{}
		}
		s.mem[name][kept] = slices.Clone(data)
		return nil
	}
	dir := s.filesDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var used int64
	if es, err := os.ReadDir(dir); err == nil {
		for _, e := range es {
			if e.Name() == kept {
				return nil // the same bytes, kept already
			}
			if i, err := e.Info(); err == nil {
				used += i.Size()
			}
		}
	}
	if used+int64(len(data)) > maxShared {
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
		err = os.Rename(tmp.Name(), filepath.Join(dir, kept))
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// tidyFiles removes the files the library no longer holds. It holds the
// files lock throughout, so a file kept meanwhile is never taken for one
// let go.
func (s *Store) tidyFiles(name string) {
	s.files.Lock()
	defer s.files.Unlock()
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
		// A file being kept is a dot file until it's renamed into place.
		if !keep[e.Name()] && !strings.HasPrefix(e.Name(), ".") {
			os.Remove(filepath.Join(s.filesDir(name), e.Name()))
		}
	}
}
