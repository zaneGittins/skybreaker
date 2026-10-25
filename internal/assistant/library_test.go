package assistant

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func shared(t *testing.T, s *Store, name, kept string) string {
	t.Helper()
	f, err := s.Shared(name, kept)
	if err != nil {
		t.Fatalf("Shared(%q, %q): %v", name, kept, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func audio(title, collection, data string) Share {
	return Share{Item: Item{Kind: KindAudio, Name: "episode.mp3", Title: title, Collection: collection}, Data: []byte(data), Ext: ".mp3"}
}

func ptr[T any](v T) *T { return &v }

func stores(t *testing.T) map[string]*Store {
	disk, _ := open(t)
	mem, _ := Open("")
	return map[string]*Store{"disk": disk, "memory": mem}
}

// A shared file is an item in the library, with what the agent said about
// it, read back by its kept name.
func TestAddItem(t *testing.T) {
	for kind, s := range stores(t) {
		t.Run(kind, func(t *testing.T) {
			sh := audio("Sunday, October 25", "Morning briefing", "ID3 sunday")
			sh.Item.Summary = "Rain from 4"
			sh.Item.Chapters = []Chapter{{At: 70, Title: "Weather"}, {At: 0, Title: "Intro"}}
			sh.Cover = &Blob{Data: []byte("\x89PNG cover"), Ext: ".png"}
			sh.Transcript = []byte("Good morning.")
			it, err := s.AddItem("pip", sh)
			if err != nil {
				t.Fatal(err)
			}
			if it.ID == "" || it.Kind != KindAudio || it.Collection != "morning-briefing" || it.Size != 10 || it.Summary != "Rain from 4" {
				t.Fatalf("item %+v", it)
			}
			if len(it.Chapters) != 2 || it.Chapters[0].Title != "Intro" {
				t.Errorf("chapters not in order: %+v", it.Chapters)
			}
			if got := shared(t, s, "pip", it.Kept); got != "ID3 sunday" {
				t.Errorf("file %q", got)
			}
			if got := shared(t, s, "pip", it.Cover); got != "\x89PNG cover" {
				t.Errorf("cover %q", got)
			}
			if got := shared(t, s, "pip", it.Transcript); got != "Good morning." {
				t.Errorf("transcript %q", got)
			}
			// Naming a collection makes it, titled as the agent wrote it.
			a := s.Get("pip")
			if len(a.Collections) != 1 || a.Collections[0].ID != "morning-briefing" || a.Collections[0].Title != "Morning briefing" || a.Collections[0].Layout != LayoutList {
				t.Fatalf("collections %+v", a.Collections)
			}
			for _, bad := range [][2]string{{"pip", "../assistants.json"}, {"pip", "x.mp3"}, {"other", it.Kept}, {"../pip", it.Kept}} {
				if _, err := s.Shared(bad[0], bad[1]); err == nil {
					t.Errorf("Shared(%q, %q)", bad[0], bad[1])
				}
			}
		})
	}
}

func TestAddItemRejects(t *testing.T) {
	s, _ := Open("")
	for name, sh := range map[string]Share{
		"unknown kind": {Item: Item{Kind: "video", Name: "a"}, Data: []byte("x"), Ext: ".bin"},
		"bad ext":      {Item: Item{Kind: KindFile, Name: "a.exe"}, Data: []byte("MZ"), Ext: ".exe"},
		"empty":        {Item: Item{Kind: KindFile, Name: "a"}, Ext: ".bin"},
		"cover":        {Item: Item{Kind: KindAudio, Name: "a"}, Data: []byte("ID3"), Ext: ".mp3", Cover: &Blob{Data: []byte("x"), Ext: ".mp3"}},
		"transcript":   {Item: Item{Kind: KindAudio, Name: "a"}, Data: []byte("ID3"), Ext: ".mp3", Transcript: []byte{0xff, 0xfe}},
		"collection":   {Item: Item{Kind: KindAudio, Name: "a", Collection: "!!!"}, Data: []byte("ID3"), Ext: ".mp3"},
		"chapter":      {Item: Item{Kind: KindAudio, Name: "a", Chapters: []Chapter{{At: -1, Title: "x"}}}, Data: []byte("ID3"), Ext: ".mp3"},
	} {
		if _, err := s.AddItem("pip", sh); err == nil {
			t.Errorf("%s: kept", name)
		}
	}
	if _, err := s.AddItem("../pip", audio("x", "", "ID3")); err == nil {
		t.Error("kept for a bad name")
	}
}

// Agent text is clipped to a line, not refused: it's only read at a glance.
func TestAddItemClipsText(t *testing.T) {
	s, _ := Open("")
	sh := audio(strings.Repeat("long ", 100), "", "ID3")
	sh.Item.Summary = "two\nlines"
	it, err := s.AddItem("pip", sh)
	if err != nil {
		t.Fatal(err)
	}
	if len(it.Title) > MaxItemTitle || it.Summary != "two lines" {
		t.Errorf("title %d bytes, summary %q", len(it.Title), it.Summary)
	}
	if it, _ := s.AddItem("pip", Share{Item: Item{Kind: KindFile, Name: "dir/report.pdf"}, Data: []byte("%PDF"), Ext: ".pdf"}); it.Title != "report.pdf" {
		t.Errorf("untitled item is called %q", it.Title)
	}
}

// Sharing the same file again is the same item, where you were in it kept.
func TestReshareKeepsProgress(t *testing.T) {
	s, _ := Open("")
	first, _ := s.AddItem("pip", audio("Draft", "Weekly", "ID3 same"))
	if _, err := s.SetItemState("pip", first.ID, ItemState{Progress: ptr(42.0)}); err != nil {
		t.Fatal(err)
	}
	again, err := s.AddItem("pip", audio("Final", "Weekly", "ID3 same"))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.Title != "Final" || again.Progress != 42 {
		t.Errorf("reshared %+v", again)
	}
	if n := len(s.Get("pip").Library); n != 1 {
		t.Errorf("%d items", n)
	}
}

func TestItemState(t *testing.T) {
	s, _ := Open("")
	var heard []string
	s.OnLibrary = func(name string) { heard = append(heard, name) }
	it, _ := s.AddItem("pip", audio("x", "", "ID3"))
	heard = nil
	// Starting it is news: it's no longer new.
	got, err := s.SetItemState("pip", it.ID, ItemState{Progress: ptr(2.0), Duration: ptr(360.0)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress != 2 || got.Duration != 360 || !got.Seen || len(heard) != 1 {
		t.Errorf("state %+v, heard %v", got, heard)
	}
	// Where you are after that is saved quietly: nothing redraws every few seconds.
	heard = nil
	if got, _ = s.SetItemState("pip", it.ID, ItemState{Progress: ptr(12.5)}); got.Progress != 12.5 || len(heard) != 0 {
		t.Errorf("state %+v, heard %v", got, heard)
	}
	// Finishing, starring and marking new again are news.
	got, _ = s.SetItemState("pip", it.ID, ItemState{Finished: ptr(true), Star: ptr(true)})
	if !got.Finished || !got.Star || len(heard) != 1 {
		t.Errorf("state %+v, heard %v", got, heard)
	}
	got, _ = s.SetItemState("pip", it.ID, ItemState{Seen: ptr(false)})
	if got.Seen || got.Finished || got.Progress != 0 {
		t.Errorf("marked new but %+v", got)
	}
	for _, bad := range []ItemState{{Progress: ptr(-1.0)}, {Duration: ptr(0.0)}, {Progress: ptr(1e9)}} {
		if _, err := s.SetItemState("pip", it.ID, bad); err == nil {
			t.Errorf("took %+v", bad)
		}
	}
	if _, err := s.SetItemState("pip", "i999", ItemState{Seen: ptr(true)}); err == nil {
		t.Error("changed a missing item")
	}
}

func TestDeleteItem(t *testing.T) {
	for kind, s := range stores(t) {
		t.Run(kind, func(t *testing.T) {
			it, _ := s.AddItem("pip", audio("x", "", "ID3 gone"))
			if err := s.DeleteItem("pip", it.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Shared("pip", it.Kept); err == nil {
				t.Error("the file outlived its item")
			}
			if err := s.DeleteItem("pip", it.ID); err == nil {
				t.Error("deleted twice")
			}
		})
	}
}

func TestSetCollection(t *testing.T) {
	s, _ := Open("")
	c, err := s.SetCollection("pip", "Morning briefing", CollectionPatch{Layout: ptr(LayoutPodcast), About: ptr("Every day at 7"), Speed: ptr(1.25), Keep: ptr(14)}, &Blob{Data: []byte("\x89PNG"), Ext: ".png"})
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "morning-briefing" || c.Layout != LayoutPodcast || c.Speed != 1.25 || c.Keep != 14 || c.KeepDays != defaultKeepDays || c.Cover == "" {
		t.Fatalf("collection %+v", c)
	}
	// What isn't given stays as it was.
	c, _ = s.SetCollection("pip", "morning briefing", CollectionPatch{Notify: ptr(true), KeepDays: ptr(0)}, nil)
	if c.Layout != LayoutPodcast || !c.Notify || c.KeepDays != 0 || c.Title != "morning briefing" || c.Cover == "" {
		t.Errorf("patched %+v", c)
	}
	for name, p := range map[string]CollectionPatch{
		"layout": {Layout: ptr("carousel")}, "speed": {Speed: ptr(9.0)}, "keep": {Keep: ptr(-1)}, "days": {KeepDays: ptr(-2)},
	} {
		if _, err := s.SetCollection("pip", "Morning briefing", p, nil); err == nil {
			t.Errorf("%s: took %+v", name, p)
		}
	}
	if _, err := s.SetCollection("pip", "  ", CollectionPatch{}, nil); err == nil {
		t.Error("made a collection with no name")
	}
}

// A collection keeps its newest items, and those younger than its days;
// starred ones always stay. Items in no collection keep a month.
func TestRetention(t *testing.T) {
	s, _ := Open("")
	start := time.Now().UTC()
	s.now = func() time.Time { return start }
	s.SetCollection("pip", "Daily", CollectionPatch{Keep: ptr(2), KeepDays: ptr(0)}, nil)
	var ids []string
	for i, d := range []string{"ID3 a", "ID3 b", "ID3 c"} {
		s.now = func() time.Time { return start.Add(time.Duration(i) * time.Hour) }
		it, _ := s.AddItem("pip", audio(d, "Daily", d))
		ids = append(ids, it.ID)
		if i == 0 {
			s.SetItemState("pip", it.ID, ItemState{Star: ptr(true)})
		}
	}
	loose, _ := s.AddItem("pip", audio("loose", "", "ID3 loose"))
	if got := itemIDs(s.Get("pip")); got != strings.Join(append(ids, loose.ID), ",") {
		t.Fatalf("kept %s", got)
	}
	s.now = func() time.Time { return start.Add(3 * time.Hour) }
	d, _ := s.AddItem("pip", audio("d", "Daily", "ID3 d"))
	// a is starred, so it stays; b is the oldest of the rest past the two kept.
	if got := itemIDs(s.Get("pip")); got != strings.Join([]string{ids[0], ids[2], loose.ID, d.ID}, ",") {
		t.Errorf("kept %s", got)
	}
	s.now = func() time.Time { return start.Add(31 * 24 * time.Hour) }
	s.SetCollection("pip", "Daily", CollectionPatch{}, nil)
	if got := itemIDs(s.Get("pip")); got != strings.Join([]string{ids[0], ids[2], d.ID}, ",") {
		t.Errorf("after a month kept %s", got)
	}
}

func itemIDs(a Assistant) string {
	var ids []string
	for _, it := range a.Library {
		ids = append(ids, it.ID)
	}
	return strings.Join(ids, ",")
}

// A routine's share is an item and an update on Today pointing at it.
func TestShareFile(t *testing.T) {
	for kind, s := range stores(t) {
		t.Run(kind, func(t *testing.T) {
			it, u, err := s.ShareFile("pip", "", audio("This week's podcast", "", "ID3 podcast"))
			if err != nil {
				t.Fatal(err)
			}
			if u.Item != it.ID || u.Text != "This week's podcast" || u.File != nil {
				t.Fatalf("update %+v", u)
			}
			// Updates come and go; the item stays.
			s.now = func() time.Time { return time.Now().UTC().Add(48 * time.Hour) }
			for i := range maxUpdates {
				s.PostUpdate("pip", "", "later")
				if i%maxUpdatesPerDay == maxUpdatesPerDay-1 {
					now := s.now()
					s.now = func() time.Time { return now.Add(25 * time.Hour) }
				}
			}
			if got := shared(t, s, "pip", it.Kept); got != "ID3 podcast" {
				t.Errorf("file %q after its update went", got)
			}
		})
	}
}

// Files shared before the library are moved into it on open.
func TestOldSharedFilesMoveIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "assistants.json")
	kept := "0123456789abcdef0123456789abcdef.mp3"
	os.MkdirAll(filepath.Join(dir, "assistant-files", "pip"), 0o700)
	os.WriteFile(filepath.Join(dir, "assistant-files", "pip", kept), []byte("ID3 old"), 0o600)
	at := time.Date(2026, 10, 24, 7, 0, 0, 0, time.UTC)
	old := map[string]any{"assistants": map[string]any{"pip": map[string]any{
		"look":    map[string]any{"name": "Pip", "shape": "dot", "color": "clay", "temper": "calm"},
		"updates": []any{map[string]any{"id": "u1", "text": "Podcast", "at": at, "file": map[string]any{"name": "p.mp3", "kind": "audio", "size": 7, "kept": kept}}},
	}}, "next": 1}
	b, _ := json.Marshal(old)
	os.WriteFile(path, b, 0o600)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a := s.Get("pip")
	if len(a.Library) != 1 || a.Library[0].Kept != kept || a.Library[0].Title != "Podcast" || !a.Library[0].At.Equal(at) {
		t.Fatalf("library %+v", a.Library)
	}
	if a.Updates[0].File != nil || a.Updates[0].Item != a.Library[0].ID {
		t.Errorf("update %+v", a.Updates[0])
	}
	if got := shared(t, s, "pip", kept); got != "ID3 old" {
		t.Errorf("file %q", got)
	}
}

func TestOnNewItem(t *testing.T) {
	s, _ := Open("")
	type heard struct {
		id     string
		notify bool
	}
	var got []heard
	s.OnNewItem = func(name string, it Item, notify bool) { got = append(got, heard{it.ID, notify}) }
	s.SetCollection("pip", "Daily", CollectionPatch{Notify: ptr(true)}, nil)
	a, _ := s.AddItem("pip", audio("a", "Daily", "ID3 a"))
	b, _ := s.AddItem("pip", audio("b", "", "ID3 b"))
	s.AddItem("pip", audio("a again", "Daily", "ID3 a"))
	if len(got) != 2 || got[0] != (heard{a.ID, true}) || got[1] != (heard{b.ID, false}) {
		t.Errorf("heard %+v", got)
	}
}

// What a collection lets go goes on its own, not only when something new
// comes in.
func TestPruneWithoutNewItems(t *testing.T) {
	for kind, s := range stores(t) {
		t.Run(kind, func(t *testing.T) {
			start := time.Now().UTC()
			s.now = func() time.Time { return start }
			it, _ := s.AddItem("pip", audio("old", "", "ID3 old"))
			s.now = func() time.Time { return start.Add(31 * 24 * time.Hour) }
			s.Prune()
			if n := len(s.Get("pip").Library); n != 0 {
				t.Errorf("%d items after a month", n)
			}
			if _, err := s.Shared("pip", it.Kept); err == nil {
				t.Error("its file stayed")
			}
		})
	}
}

// Opening the store applies retention too.
func TestPruneOnOpen(t *testing.T) {
	s, path := open(t)
	s.now = func() time.Time { return time.Now().UTC().Add(-40 * 24 * time.Hour) }
	s.AddItem("pip", audio("old", "", "ID3 old"))
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(again.Get("pip").Library); n != 0 {
		t.Errorf("%d items kept past their time", n)
	}
}

// An item just added is kept, however full the library is of starred ones.
func TestNewItemSurvivesAFullLibrary(t *testing.T) {
	s, _ := Open("")
	was := maxItems
	maxItems = 2
	t.Cleanup(func() { maxItems = was })
	for _, d := range []string{"ID3 a", "ID3 b"} {
		it, _ := s.AddItem("pip", audio(d, "", d))
		s.SetItemState("pip", it.ID, ItemState{Star: ptr(true)})
	}
	c, err := s.AddItem("pip", audio("c", "", "ID3 c"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(itemIDs(s.Get("pip")), c.ID) {
		t.Errorf("the new item was let go: %s", itemIDs(s.Get("pip")))
	}
}

// Keeping a file and tidying others never lose a file an item holds, with
// many at once sharing the same bytes and letting items go.
func TestKeepAndTidyRace(t *testing.T) {
	s, _ := open(t)
	cover := &Blob{Data: []byte("\x89PNG shared cover"), Ext: ".png"}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sh := audio(fmt.Sprint("ep ", i), "", fmt.Sprint("ID3 ", i))
			sh.Cover = cover
			it, err := s.AddItem("pip", sh)
			if err == nil && i%2 == 0 {
				s.DeleteItem("pip", it.ID)
			}
		}()
	}
	wg.Wait()
	for _, it := range s.Get("pip").Library {
		for _, k := range []string{it.Kept, it.Cover} {
			if f, err := s.Shared("pip", k); err != nil {
				t.Errorf("item %s lost %s", it.ID, k)
			} else {
				f.Close()
			}
		}
	}
}
