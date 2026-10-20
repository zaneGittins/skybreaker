package board

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// open returns a store on disk whose clock ticks a millisecond per read, so
// orderings by time are unambiguous.
func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "board.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	clock := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(time.Millisecond)
		return clock
	}
	return s
}

func mustProject(t *testing.T, s *Store, name string) {
	t.Helper()
	if err := s.Sync([]Spec{{Slug: strings.ToLower(name)}}); err != nil {
		t.Fatal(err)
	}
}

func mustItem(t *testing.T, s *Store, project string, in ItemInput) Item {
	t.Helper()
	it, err := s.CreateItem(project, in, "")
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func ptr[T any](v T) *T { return &v }

func refs(items []Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Ref
	}
	return out
}

// --- boards -------------------------------------------------------------------

func TestSyncKeys(t *testing.T) {
	s := open(t)
	if err := s.Sync([]Spec{{Slug: "bifract"}, {Slug: "bifrost"}, {Slug: "skybreaker", Key: "sky"}, {Slug: "9lives"}}); err != nil {
		t.Fatal(err)
	}
	for slug, want := range map[string]string{"bifract": "BIF", "bifrost": "BIF2", "skybreaker": "SKY", "9lives": "LIV"} {
		if p, err := s.GetProject(slug); err != nil || p.Key != want {
			t.Errorf("%s: key %q %v, want %s", slug, p.Key, err, want)
		}
	}
	// A board keeps its key when the config stops naming one.
	if err := s.Sync([]Spec{{Slug: "skybreaker"}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.GetProject("skybreaker"); p.Key != "SKY" {
		t.Fatalf("key changed to %s", p.Key)
	}
	// Projects are found by key too, ignoring case.
	if p, err := s.GetProject("sky"); err != nil || p.Slug != "skybreaker" {
		t.Fatalf("by key: %+v %v", p, err)
	}
	if err := s.Sync([]Spec{{Slug: "bifrost", Key: "BIF"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("taken key: %v", err)
	}
	if err := s.Sync([]Spec{{Slug: "x", Key: "1AB"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad key: %v", err)
	}
	// The board survives a reload, with its counts.
	mustItem(t, s, "bifract", ItemInput{Title: "a", Status: "todo"})
	s2, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := s2.GetProject("bifract"); p.Key != "BIF" || p.Counts["todo"] != 1 || p.Next != 2 {
		t.Fatalf("after reload: %+v", p)
	}
}

func TestOnChange(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	var got []string
	s.OnChange = func(project, ref string) { got = append(got, project+":"+ref) }
	mustItem(t, s, "bifract", ItemInput{Title: "a"})
	s.AddNote("BIF-1", "z", "hi")
	s.UpdateItem("BIF-1", ItemPatch{Title: ptr("b")}, "")
	s.DeleteItem("BIF-1")
	if fmt.Sprint(got) != "[bifract:BIF-1 bifract:BIF-1 bifract:BIF-1 bifract:]" {
		t.Fatalf("changes: %v", got)
	}
}

func TestCreateItemDefaultsAndValidation(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	it := mustItem(t, s, "bifract", ItemInput{Title: "  hello  ", Labels: []string{"UI", " ui", "", "Search"}})
	if it.Type != "feature" || it.Status != "backlog" || it.Priority != PriorityNormal || it.Title != "hello" {
		t.Fatalf("defaults: %+v", it)
	}
	if it := mustItem(t, s, "bifract", ItemInput{Title: "l", Labels: []string{"a,b", "#Two Words", "a-b", ",,"}}); fmt.Sprint(it.Labels) != "[a-b two-words]" {
		t.Fatalf("labels with commas and spaces: %v", it.Labels)
	}
	if fmt.Sprint(it.Labels) != "[ui search]" {
		t.Fatalf("labels cleaned: %v", it.Labels)
	}
	if len(it.Notes) != 1 || it.Notes[0].Kind != "event" {
		t.Fatalf("creation event: %+v", it.Notes)
	}
	if it := mustItem(t, s, "BIF", ItemInput{Title: "x", Type: "Feat", Status: "In Progress"}); it.Type != "feature" || it.Status != "in_progress" {
		t.Fatalf("aliases: %s %s", it.Type, it.Status)
	}
	done := mustItem(t, s, "bifract", ItemInput{Title: "x", Status: "done"})
	if done.CompletedAt == nil {
		t.Fatal("item created done has no completed_at")
	}

	bad := []ItemInput{
		{Title: ""},
		{Title: "   "},
		{Title: "x", Type: "epic"},
		{Title: "x", Status: "blocked"},
		{Title: "x", Priority: ptr(4)},
		{Title: "x", Priority: ptr(-1)},
	}
	for _, in := range bad {
		if _, err := s.CreateItem("bifract", in, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: want ErrInvalid, got %v", in, err)
		}
	}
	if _, err := s.CreateItem("nope", ItemInput{Title: "x"}, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing project: %v", err)
	}
}

func TestItemNumbersArePerProject(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	mustProject(t, s, "Atlas")
	a := mustItem(t, s, "bifract", ItemInput{Title: "a"})
	b := mustItem(t, s, "atlas", ItemInput{Title: "b"})
	c := mustItem(t, s, "bifract", ItemInput{Title: "c"})
	if a.Ref != "BIF-1" || b.Ref != "ATL-1" || c.Ref != "BIF-2" {
		t.Fatalf("refs %s %s %s", a.Ref, b.Ref, c.Ref)
	}
	// Numbers are never reused, even after a delete.
	s.DeleteItem("BIF-2")
	if d := mustItem(t, s, "bifract", ItemInput{Title: "d"}); d.Ref != "BIF-3" {
		t.Fatalf("after delete got %s", d.Ref)
	}
}

func TestConcurrentCreatesGetDistinctNumbers(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	var wg sync.WaitGroup
	seen := sync.Map{}
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			it, err := s.CreateItem("bifract", ItemInput{Title: fmt.Sprint(i)}, "")
			if err != nil {
				errs <- err
				return
			}
			if _, dup := seen.LoadOrStore(it.Ref, true); dup {
				errs <- fmt.Errorf("duplicate ref %s", it.Ref)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestNewItemsGoOnTopOfTheirColumn(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	mustItem(t, s, "bifract", ItemInput{Title: "a", Status: "todo"})
	mustItem(t, s, "bifract", ItemInput{Title: "b", Status: "todo"})
	mustItem(t, s, "bifract", ItemInput{Title: "c", Status: "backlog"})
	items, _ := s.ListItems(Filter{Project: "bifract", Statuses: []string{"todo"}})
	if got := fmt.Sprint(refs(items)); got != "[BIF-2 BIF-1]" {
		t.Fatalf("todo order %s", got)
	}
}

func TestStatusTransitions(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	mustItem(t, s, "bifract", ItemInput{Title: "a", Status: "todo"})

	it, err := s.UpdateItem("BIF-1", ItemPatch{Status: ptr("done")}, "claude")
	if err != nil || it.CompletedAt == nil {
		t.Fatalf("done: %v %v", it.CompletedAt, err)
	}
	last := it.Notes[len(it.Notes)-1]
	if last.Kind != "event" || last.Author != "claude" || last.Body != "moved Todo → Done" {
		t.Fatalf("event: %+v", last)
	}
	it, _ = s.UpdateItem("BIF-1", ItemPatch{Status: ptr("review")}, "")
	if it.CompletedAt != nil {
		t.Fatal("leaving done should clear completed_at")
	}
	// Edits that do not move the item leave no event.
	before := len(it.Notes)
	it, _ = s.UpdateItem("BIF-1", ItemPatch{Title: ptr("renamed"), Status: ptr("review")}, "")
	if len(it.Notes) != before || it.Title != "renamed" {
		t.Fatalf("no-op move logged an event or lost the title: %d -> %d", before, len(it.Notes))
	}
}

func TestAssigneeFollowsTheWork(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	mustItem(t, s, "bifract", ItemInput{Title: "a", Status: "todo"})
	s.ClaimItem("BIF-1", Claim{Agent: "claude"})

	it, _ := s.UpdateItem("BIF-1", ItemPatch{Status: ptr("review")}, "claude")
	if it.Assignee != "claude" {
		t.Fatalf("review keeps the assignee, got %q", it.Assignee)
	}
	it, _ = s.UpdateItem("BIF-1", ItemPatch{Status: ptr("todo")}, "")
	if it.Assignee != "" {
		t.Fatalf("back to todo releases the item, got %q", it.Assignee)
	}
	it, _ = s.UpdateItem("BIF-1", ItemPatch{Status: ptr("backlog"), Assignee: ptr("codex")}, "")
	if it.Assignee != "codex" {
		t.Fatalf("an explicit assignee wins, got %q", it.Assignee)
	}
}

func TestUpdateItemValidation(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	mustItem(t, s, "bifract", ItemInput{Title: "a"})
	bad := []ItemPatch{
		{Title: ptr("  ")},
		{Type: ptr("epic")},
		{Status: ptr("blocked")},
		{Priority: ptr(9)},
	}
	for _, p := range bad {
		if _, err := s.UpdateItem("BIF-1", p, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: want ErrInvalid, got %v", p, err)
		}
	}
	if _, err := s.UpdateItem("BIF-9", ItemPatch{}, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := s.UpdateItem("garbage", ItemPatch{}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad ref: %v", err)
	}
	// Clearing works: empty body and empty labels.
	s.UpdateItem("BIF-1", ItemPatch{Body: ptr("text"), Labels: &[]string{"x"}}, "")
	it, err := s.UpdateItem("BIF-1", ItemPatch{Body: ptr(""), Labels: &[]string{}}, "")
	if err != nil || it.Body != "" || len(it.Labels) != 0 {
		t.Fatalf("clear: %q %v %v", it.Body, it.Labels, err)
	}
}

func TestPositionsStayOrderedUnderRepeatedMidpoints(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	a := mustItem(t, s, "bifract", ItemInput{Title: "a", Status: "todo"})
	b := mustItem(t, s, "bifract", ItemInput{Title: "b", Status: "todo"})
	// Order is b, a. Keep dropping a new item just above a, as the UI does,
	// far more times than float64 midpoints can survive.
	var hi float64
	for i := 0; i < 80; i++ {
		it := mustItem(t, s, "bifract", ItemInput{Title: fmt.Sprint("n", i), Status: "backlog"})
		items, _ := s.ListItems(Filter{Project: "bifract", Statuses: []string{"todo"}})
		// Neighbours: the item just above a, and a itself.
		var above Item
		for j, x := range items {
			if x.Ref == a.Ref {
				above = items[j-1]
			}
		}
		cur, _ := s.GetItem(a.Ref)
		hi = cur.Position
		pos := (above.Position + hi) / 2
		if _, err := s.UpdateItem(it.Ref, ItemPatch{Status: ptr("todo"), Position: &pos}, ""); err != nil {
			t.Fatal(err)
		}
	}
	items, _ := s.ListItems(Filter{Project: "bifract", Statuses: []string{"todo"}})
	if items[0].Ref != b.Ref || items[len(items)-1].Ref != a.Ref {
		t.Fatalf("ends moved: first %s last %s", items[0].Ref, items[len(items)-1].Ref)
	}
	for i := 1; i < len(items); i++ {
		if !(items[i-1].Position < items[i].Position) {
			t.Fatalf("positions not strictly increasing at %d: %v then %v", i, items[i-1].Position, items[i].Position)
		}
	}
	// Each new item was dropped just above a, so they read newest-last: n0, n1, ... n79, a.
	for i := 1; i < len(items)-1; i++ {
		if want := fmt.Sprint("n", i-1); items[i].Title != want {
			t.Fatalf("slot %d is %q, want %q", i, items[i].Title, want)
		}
	}
}

func TestListItemsFilters(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	mustProject(t, s, "Atlas")
	mustItem(t, s, "bifract", ItemInput{Title: "crash on save", Type: "bug", Status: "todo", Labels: []string{"ui"}})
	mustItem(t, s, "bifract", ItemInput{Title: "dark mode", Body: "make it dark", Status: "backlog"})
	mustItem(t, s, "bifract", ItemInput{Title: "old", Status: "done"})
	mustItem(t, s, "atlas", ItemInput{Title: "wayland", Type: "bug", Status: "todo"})
	s.ClaimItem("ATL-1", Claim{Agent: "codex"})

	check := func(name string, f Filter, want string) {
		t.Helper()
		items, err := s.ListItems(f)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := fmt.Sprint(refs(items)); got != want {
			t.Errorf("%s: got %s, want %s", name, got, want)
		}
	}
	check("open everywhere", Filter{}, "[ATL-1 BIF-2 BIF-1]")
	check("include done", Filter{Project: "bifract", IncludeDone: true}, "[BIF-2 BIF-1 BIF-3]")
	check("explicit done", Filter{Statuses: []string{"done"}}, "[BIF-3]")
	check("type", Filter{Type: "bugs"}, "[ATL-1 BIF-1]")
	check("query title", Filter{Query: "CRASH"}, "[BIF-1]")
	check("query body", Filter{Query: "dark"}, "[BIF-2]")
	check("query label", Filter{Query: "ui"}, "[BIF-1]")
	check("assignee", Filter{Assignee: "Codex"}, "[ATL-1]")
	check("limit", Filter{Limit: 1}, "[ATL-1]")
	check("several statuses", Filter{Project: "bifract", Statuses: []string{"todo", "Done"}}, "[BIF-1 BIF-3]")

	if _, err := s.ListItems(Filter{Statuses: []string{"blocked"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad status: %v", err)
	}
	if _, err := s.ListItems(Filter{Type: "epic"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad type: %v", err)
	}
	if _, err := s.ListItems(Filter{Project: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad project: %v", err)
	}
	// LIKE wildcards in the query are literal text.
	check("percent is literal", Filter{Query: "%"}, "[]")
	check("underscore is literal", Filter{Query: "_"}, "[]")
}

func TestDoneIsNewestFirst(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	for i := 0; i < 3; i++ {
		mustItem(t, s, "bifract", ItemInput{Title: fmt.Sprint(i), Status: "todo"})
	}
	for _, ref := range []string{"BIF-2", "BIF-3", "BIF-1"} {
		s.UpdateItem(ref, ItemPatch{Status: ptr("done")}, "")
	}
	items, _ := s.ListItems(Filter{Statuses: []string{"done"}})
	if got := fmt.Sprint(refs(items)); got != "[BIF-1 BIF-3 BIF-2]" {
		t.Fatalf("done order %s", got)
	}
}

// --- claiming -------------------------------------------------------------------

func TestClaim(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	mustItem(t, s, "bifract", ItemInput{Title: "a", Status: "todo"})

	it, err := s.ClaimItem("bif-1", Claim{Agent: "claude"})
	if err != nil || it.Status != "in_progress" || it.Assignee != "claude" {
		t.Fatalf("claim: %+v %v", it, err)
	}
	if _, err := s.ClaimItem("BIF-1", Claim{Agent: "Claude"}); err != nil {
		t.Fatalf("re-claim by the same agent: %v", err)
	}
	if _, err := s.ClaimItem("BIF-1", Claim{Agent: "codex"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second agent should conflict, got %v", err)
	}
	if it, err := s.ClaimItem("BIF-1", Claim{Agent: "codex", Force: true}); err != nil || it.Assignee != "codex" {
		t.Fatalf("forced claim: %v %v", it.Assignee, err)
	}
	s.UpdateItem("BIF-1", ItemPatch{Status: ptr("done")}, "")
	if _, err := s.ClaimItem("BIF-1", Claim{Agent: "codex"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("done item should conflict, got %v", err)
	}
	if _, err := s.ClaimItem("BIF-1", Claim{Agent: "  "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank agent: %v", err)
	}
}

func TestClaimsAreHeldBySession(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	mustItem(t, s, "bifract", ItemInput{Title: "a", Status: "todo"})
	if _, err := s.ClaimItem("BIF-1", Claim{Agent: "claude", Claimant: "session-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimItem("BIF-1", Claim{Agent: "claude", Claimant: "session-a"}); err != nil {
		t.Fatalf("same session re-claiming: %v", err)
	}
	_, err := s.ClaimItem("BIF-1", Claim{Agent: "claude", Claimant: "session-b"})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "another session") {
		t.Fatalf("a second claude session must not share the claim: %v", err)
	}
	// Handing it back clears the claim for everyone.
	s.UpdateItem("BIF-1", ItemPatch{Status: ptr("todo")}, "")
	if _, err := s.ClaimItem("BIF-1", Claim{Agent: "claude", Claimant: "session-b"}); err != nil {
		t.Fatalf("after release: %v", err)
	}
	// The claim is held, and survives a reload, but never leaves the store.
	if it, _ := s.GetItem("BIF-1"); it.ClaimedBy != "" {
		t.Fatalf("claim holder leaked: %q", it.ClaimedBy)
	}
	if s2, _ := Open(s.path); s2.st.Items[0].ClaimedBy != "session-b" {
		t.Fatalf("claimedBy after reload %q", s2.st.Items[0].ClaimedBy)
	}
}

func TestConcurrentClaimsHaveOneWinner(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	mustItem(t, s, "bifract", ItemInput{Title: "a", Status: "todo"})

	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := []string{}
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(agent string) {
			defer wg.Done()
			<-start
			if _, err := s.ClaimItem("BIF-1", Claim{Agent: agent}); err == nil {
				mu.Lock()
				winners = append(winners, agent)
				mu.Unlock()
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		}(fmt.Sprint("agent", i))
	}
	close(start)
	wg.Wait()
	it, _ := s.GetItem("BIF-1")
	if len(winners) != 1 || winners[0] != it.Assignee {
		t.Fatalf("winners %v, assignee %q", winners, it.Assignee)
	}
}

func TestNextItem(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	if _, err := s.NextItem("bifract"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty todo: %v", err)
	}
	mustItem(t, s, "bifract", ItemInput{Title: "low", Status: "todo", Priority: ptr(PriorityLow)})
	mustItem(t, s, "bifract", ItemInput{Title: "normal bottom", Status: "todo"})
	mustItem(t, s, "bifract", ItemInput{Title: "normal top", Status: "todo"})
	mustItem(t, s, "bifract", ItemInput{Title: "urgent backlog", Status: "backlog", Priority: ptr(PriorityUrgent)})
	next, err := s.NextItem("bifract")
	if err != nil || next.Title != "normal top" {
		t.Fatalf("next = %q %v", next.Title, err)
	}
	s.UpdateItem("BIF-1", ItemPatch{Priority: ptr(PriorityHigh)}, "")
	if next, _ := s.NextItem("BIF"); next.Title != "low" {
		t.Fatalf("priority should win over position, got %q", next.Title)
	}
}

func TestClaimNextHandsOutDistinctItems(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	for i := 0; i < 5; i++ {
		mustItem(t, s, "bifract", ItemInput{Title: fmt.Sprint(i), Status: "todo"})
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := []string{}
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(agent string) {
			defer wg.Done()
			<-start
			it, err := s.ClaimNext("bifract", Claim{Agent: agent})
			if errors.Is(err, ErrNotFound) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			if it.Assignee != agent || it.Status != "in_progress" {
				t.Errorf("claimed %+v for %s", it, agent)
			}
			mu.Lock()
			got = append(got, it.Ref)
			mu.Unlock()
		}(fmt.Sprint("agent", i))
	}
	close(start)
	wg.Wait()
	sort.Strings(got)
	if fmt.Sprint(got) != "[BIF-1 BIF-2 BIF-3 BIF-4 BIF-5]" {
		t.Fatalf("claimed %v", got)
	}
}

// --- notes ----------------------------------------------------------------------

func TestNotes(t *testing.T) {
	s := open(t)
	mustProject(t, s, "Bifract")
	mustItem(t, s, "bifract", ItemInput{Title: "a"})
	n, err := s.AddNote("BIF-1", "  claude ", "  found it  ")
	if err != nil || n.Author != "claude" || n.Body != "found it" || n.Kind != "comment" {
		t.Fatalf("note %+v %v", n, err)
	}
	if _, err := s.AddNote("BIF-1", "x", "   "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank note: %v", err)
	}
	if _, err := s.AddNote("BIF-7", "x", "hi"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing item: %v", err)
	}
	it, _ := s.GetItem("BIF-1")
	if it.NoteCount != 1 {
		t.Fatalf("note count %d (events must not count)", it.NoteCount)
	}
	// Events are history and cannot be deleted; comments can, once.
	if err := s.DeleteNote("BIF-1", it.Notes[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting an event: %v", err)
	}
	mustItem(t, s, "bifract", ItemInput{Title: "b"})
	if err := s.DeleteNote("BIF-2", n.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting through another item's ref: %v", err)
	}
	if err := s.DeleteNote("BIF-1", n.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNote("BIF-1", n.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

// --- parsing --------------------------------------------------------------------

func TestParseQuick(t *testing.T) {
	cases := []struct {
		in, title, typ string
		prio           int
		labels         string
	}{
		{"bug: search crashes on empty query #ui !", "search crashes on empty query", "bug", PriorityHigh, "[ui]"},
		{"f: dark mode !!", "dark mode", "feature", PriorityUrgent, "[]"},
		{"plain thing about issue #12", "plain thing about issue #12", "", PriorityNormal, "[]"},
		{"chore: bump deps #deps #ci", "bump deps", "chore", PriorityNormal, "[deps ci]"},
		{"BUG:caps work", "caps work", "bug", PriorityNormal, "[]"},
		{"task: alias", "alias", "chore", PriorityNormal, "[]"},
		{"debug: not a prefix", "debug: not a prefix", "", PriorityNormal, "[]"},
		{"wow! it works", "wow! it works", "", PriorityNormal, "[]"},
		{"fix it!!!", "fix it!", "", PriorityUrgent, "[]"},
		{"x #a-b_c done", "x done", "", PriorityNormal, "[a-b_c]"},
		{"  spaced   out  ", "spaced out", "", PriorityNormal, "[]"},
		{"bug:", "", "bug", PriorityNormal, "[]"},
	}
	for _, c := range cases {
		got := ParseQuick(c.in)
		prio := PriorityNormal
		if got.Priority != nil {
			prio = *got.Priority
		}
		labels := got.Labels
		if labels == nil {
			labels = []string{}
		}
		if got.Title != c.title || got.Type != c.typ || prio != c.prio || fmt.Sprint(labels) != c.labels {
			t.Errorf("ParseQuick(%q) = %q %q %d %v; want %q %q %d %s", c.in, got.Title, got.Type, prio, labels, c.title, c.typ, c.prio, c.labels)
		}
	}
}

func TestParseRef(t *testing.T) {
	for _, ref := range []string{"BIF-12", "bif-12", "#BIF-12", " BIF-12 ", "BIF#12"} {
		k, n, err := ParseRef(ref)
		if err != nil || k != "BIF" || n != 12 {
			t.Errorf("ParseRef(%q) = %q %d %v", ref, k, n, err)
		}
	}
	if k, n, _ := ParseRef("B2-3"); k != "B2" || n != 3 {
		t.Errorf("digits in key: %q %d", k, n)
	}
	for _, bad := range []string{"", "BIF", "12", "BIF-x", "-3", "BIF-0", "BIF--1", "BIF-1.5"} {
		if _, _, err := ParseRef(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseRef(%q) should be invalid, got %v", bad, err)
		}
	}
}

func TestNormalize(t *testing.T) {
	statuses := map[string]string{
		"In Progress": "in_progress", "in-progress": "in_progress", "doing": "in_progress", "WIP": "in_progress",
		"to do": "todo", "ready": "todo", "Done": "done", "closed": "done", "in review": "review", "backlog": "backlog",
	}
	for in, want := range statuses {
		if got := NormalizeStatus(in); got != want {
			t.Errorf("NormalizeStatus(%q) = %q, want %q", in, got, want)
		}
	}
	types := map[string]string{"Feat": "feature", "idea": "feature", "fix": "bug", "Task": "chore", "bug": "bug"}
	for in, want := range types {
		if got := NormalizeType(in); got != want {
			t.Errorf("NormalizeType(%q) = %q, want %q", in, got, want)
		}
	}
}

// History is what the project page charts: what was open when the window
// starts, and every item created and finished since.
func TestHistory(t *testing.T) {
	s := open(t)
	mustProject(t, s, "orbit")
	mustProject(t, s, "comet")
	var clock time.Time
	s.now = func() time.Time { return clock }
	day := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }
	at := func(d int, f func()) { clock = day(d); f() }
	done := "done"
	var c, e Item
	at(1, func() {
		a := mustItem(t, s, "orbit", ItemInput{Title: "finished long ago"})
		at(2, func() { s.UpdateItem(a.Ref, ItemPatch{Status: &done}, "z") })
	})
	at(1, func() { mustItem(t, s, "orbit", ItemInput{Title: "open all along"}) })
	at(3, func() { c = mustItem(t, s, "orbit", ItemInput{Title: "open, then done"}) })
	at(6, func() { mustItem(t, s, "orbit", ItemInput{Title: "new and open"}) })
	at(7, func() { s.UpdateItem(c.Ref, ItemPatch{Status: &done}, "z") })
	at(8, func() { e = mustItem(t, s, "orbit", ItemInput{Title: "new and done"}) })
	at(9, func() { s.UpdateItem(e.Ref, ItemPatch{Status: &done}, "z") })
	at(9, func() { mustItem(t, s, "comet", ItemInput{Title: "another board"}) })

	h, err := s.History("orbit", day(5))
	if err != nil {
		t.Fatal(err)
	}
	if h.OpenAtStart != 2 {
		t.Errorf("open at start %d", h.OpenAtStart)
	}
	if !slices.Equal(h.Created, []time.Time{day(6), day(8)}) || !slices.Equal(h.Done, []time.Time{day(7), day(9)}) {
		t.Errorf("created %v done %v", h.Created, h.Done)
	}
	if _, err := s.History("nope", day(5)); err == nil {
		t.Error("history of a project that isn't there")
	}
}

// A board key a project asks for that another board holds is refused before
// anything changes, and explicit keys go first, whatever order specs come in.
func TestSyncKeyClash(t *testing.T) {
	s := open(t)
	if err := s.Sync([]Spec{{Slug: "bifract"}}); err != nil {
		t.Fatal(err)
	}
	clash := []Spec{{Slug: "bifract"}, {Slug: "other", Key: "BIF"}}
	if err := s.CheckSync(clash); !errors.Is(err, ErrConflict) {
		t.Fatalf("check: %v", err)
	}
	if err := s.Sync(clash); !errors.Is(err, ErrConflict) {
		t.Fatalf("sync: %v", err)
	}
	if _, err := s.GetProject("other"); err == nil {
		t.Fatal("a refused sync made a board")
	}
	for range 20 {
		fresh := open(t)
		if err := fresh.Sync([]Spec{{Slug: "bifract"}, {Slug: "other", Key: "BIF"}}); err != nil {
			t.Fatalf("fresh store: %v", err)
		}
		if p, _ := fresh.GetProject("other"); p.Key != "BIF" {
			t.Fatalf("other got %s", p.Key)
		}
	}
}
