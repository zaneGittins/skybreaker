package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/board"
	"github.com/zaneGittins/skybreaker/internal/tsauth"
)

func newBoards(t *testing.T) *board.Store {
	t.Helper()
	b, err := board.Open("")
	if err != nil {
		t.Fatal(err)
	}
	// "retired" is a board whose project left the config.
	if err := b.Sync([]board.Spec{{Slug: "orbit", Key: "ORB"}, {Slug: "retired"}}); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBoardFromTheUI(t *testing.T) {
	f := newFixture(t, you)
	code, body := f.do(t, "POST", "/api/projects/orbit/items", `{"text":"bug: crash on save #ui !"}`, nil)
	var it board.Item
	json.Unmarshal([]byte(body), &it)
	if code != 200 || it.Ref != "ORB-1" || it.Type != "bug" || it.Priority != board.PriorityHigh || it.Labels[0] != "ui" || it.Title != "crash on save" {
		t.Fatalf("quick capture: %d %s", code, body)
	}
	// Chips win over what the text implies.
	_, body = f.do(t, "POST", "/api/projects/orbit/items", `{"text":"bug: x","type":"chore","status":"todo"}`, nil)
	json.Unmarshal([]byte(body), &it)
	if it.Type != "chore" || it.Status != "todo" {
		t.Fatalf("chips: %s", body)
	}
	if code, _ := f.do(t, "PATCH", "/api/items/ORB-1", `{"status":"review"}`, nil); code != 200 {
		t.Fatalf("move: %d", code)
	}
	if code, _ := f.do(t, "POST", "/api/items/ORB-1/notes", `{"body":"looks good"}`, nil); code != 200 {
		t.Fatalf("note: %d", code)
	}
	code, body = f.do(t, "GET", "/api/items/orb-1", "", nil)
	// The item names its own project's chat agents, wherever it's opened.
	if !strings.Contains(body, `"chat":["claude"]`) {
		t.Fatalf("item chat: %s", body)
	}
	if code != 200 || !strings.Contains(body, `"author":"zane@example.com","body":"looks good"`) || !strings.Contains(body, "moved Backlog → Review") {
		t.Fatalf("item: %d %s", code, body)
	}
	code, body = f.do(t, "GET", "/api/projects/orbit/board", "", nil)
	if code != 200 || !strings.Contains(body, `"key":"ORB"`) || !strings.Contains(body, `"ref":"ORB-2"`) {
		t.Fatalf("board: %d %s", code, body)
	}
	// The project page's chart: two items made just now, none finished.
	var b struct {
		History board.History `json:"history"`
	}
	json.Unmarshal([]byte(body), &b)
	if len(b.History.Created) != 2 || len(b.History.Done) != 0 || b.History.OpenAtStart != 0 || time.Since(b.History.Since) < 14*24*time.Hour {
		t.Fatalf("history: %+v", b.History)
	}
	// Items waiting in Review are yours to look at.
	_, body = f.do(t, "GET", "/api/inbox", "", nil)
	if !strings.Contains(body, `"review":[{"ref":"ORB-1"`) {
		t.Fatalf("inbox: %s", body)
	}
	// What's ready to pick up next: Todo, highest priority first.
	if !strings.Contains(body, `"next":[{"ref":"ORB-2"`) {
		t.Fatalf("inbox next: %s", body)
	}
	if code, _ := f.do(t, "PATCH", "/api/items/ORB-1", `{"status":"blocked"}`, nil); code != 400 {
		t.Errorf("bad status: %d", code)
	}
	if code, _ := f.do(t, "DELETE", "/api/items/ORB-2", "", nil); code != 204 {
		t.Errorf("delete: %d", code)
	}
	if code, _ := f.do(t, "GET", "/api/items/ORB-2", "", nil); code != 404 {
		t.Errorf("deleted item: %d", code)
	}
}

// A board sends its newest done items, as many as asked for, and how many
// there are; a query narrows every column, done ones long gone included.
func TestBoardDoneIsPagedAndFiltered(t *testing.T) {
	f := newFixture(t, you)
	for i := 1; i <= 30; i++ {
		f.boards.CreateItem("orbit", board.ItemInput{Title: fmt.Sprintf("old %d", i), Status: "done"}, "")
	}
	f.boards.CreateItem("orbit", board.ItemInput{Title: "open crash"}, "")
	f.boards.CreateItem("orbit", board.ItemInput{Title: "calm"}, "")
	get := func(query string) (refs []string, total int) {
		t.Helper()
		code, body := f.do(t, "GET", "/api/projects/orbit/board"+query, "", nil)
		var b struct {
			Items     []board.Item `json:"items"`
			DoneTotal int          `json:"done_total"`
		}
		if err := json.Unmarshal([]byte(body), &b); code != 200 || err != nil {
			t.Fatalf("board%s: %d %s", query, code, body)
		}
		for _, it := range b.Items {
			refs = append(refs, it.Ref)
		}
		return refs, b.DoneTotal
	}
	refs, total := get("")
	if len(refs) != 22 || total != 30 || refs[2] != "ORB-30" {
		t.Fatalf("default: %d items, %d done, %v", len(refs), total, refs)
	}
	if refs, _ := get("?done=25"); len(refs) != 27 {
		t.Fatalf("done=25: %d items", len(refs))
	}
	if refs, total := get("?q=old+1"); total != 11 || len(refs) != 11 || slices.Contains(refs, "ORB-31") {
		t.Fatalf("q: %d done, %v", total, refs)
	}
	if refs, _ := get("?q=crash"); !slices.Equal(refs, []string{"ORB-31"}) {
		t.Fatalf("q open: %v", refs)
	}
	if refs, total := get("?q=old&done=5"); total != 30 || len(refs) != 5 {
		t.Fatalf("q past done: %d done, %v", total, refs)
	}
	if refs, total := get("?q=nothing"); total != 0 || len(refs) != 0 {
		t.Fatalf("no match: %d done, %v", total, refs)
	}
	for _, bad := range []string{"x", "0", "-1"} {
		if code, _ := f.do(t, "GET", "/api/projects/orbit/board?done="+bad, "", nil); code != 400 {
			t.Fatalf("done=%s: %d", bad, code)
		}
	}
}

func TestRetiredBoardsAreOutOfReach(t *testing.T) {
	f := newFixture(t, you)
	f.boards.CreateItem("retired", board.ItemInput{Title: "old"}, "")
	if code, _ := f.do(t, "GET", "/api/items/RET-1", "", nil); code != 404 {
		t.Fatalf("retired item: %d", code)
	}
	if code, _ := f.do(t, "GET", "/api/projects/retired/board", "", nil); code != 404 {
		t.Fatalf("retired board: %d", code)
	}
}

func TestWorkOnAnItem(t *testing.T) {
	f := newFixture(t, you)
	f.do(t, "POST", "/api/projects/orbit/items", `{"title":"board tab","status":"todo"}`, nil)
	code, body := f.do(t, "POST", "/api/items/ORB-1/work", `{}`, nil)
	if code != 200 || !strings.Contains(body, `"id":"c1"`) {
		t.Fatalf("work: %d %s", code, body)
	}
	calls := strings.Join(f.backend.chat.got(), "\n")
	if !strings.Contains(calls, "new orbit claude") || !strings.Contains(calls, "send c1 Work on ORB-1 from this project's board: board tab") {
		t.Fatalf("calls: %s", calls)
	}
	if code, _ := f.do(t, "POST", "/api/items/ORB-1/work", `{"harness":"shell"}`, nil); code != 400 {
		t.Fatalf("shell can't chat: %d", code)
	}
	if code, _ := f.do(t, "POST", "/api/items/ORB-1/work", `{"own_branch":true}`, nil); code != 200 {
		t.Fatalf("work on its own branch: %d", code)
	}
	if calls := f.backend.chat.got(); !slices.Contains(calls, "new orbit claude own-branch") {
		t.Errorf("calls: %q", calls)
	}
}

func TestMCPForYou(t *testing.T) {
	f := newFixture(t, you)
	f.do(t, "POST", "/api/projects/orbit/items", `{"title":"board tab"}`, nil)
	post := func(sid, body string) (int, http.Header, string) {
		req, _ := http.NewRequest("POST", f.ts.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if sid != "" {
			req.Header.Set("Mcp-Session-Id", sid)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return resp.StatusCode, resp.Header, b.String()
	}
	code, h, _ := post("", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"claude-code"}}}`)
	sid := h.Get("Mcp-Session-Id")
	if code != 200 || sid == "" {
		t.Fatalf("initialize: %d", code)
	}
	_, _, body := post(sid, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_items","arguments":{}}}`)
	if !strings.Contains(body, "ORB-1") {
		t.Fatalf("list_items: %s", body)
	}
	// As you: every board, and done is yours to set.
	_, _, body = post(sid, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"update_item","arguments":{"ref":"ORB-1","status":"done"}}}`)
	if !strings.Contains(body, "Updated ORB-1") {
		t.Fatalf("done: %s", body)
	}
	// Strangers get nothing.
	g := newFixture(t, tsauth.Peer{Node: "x", Login: "eve@example.com"})
	req, _ := http.NewRequest("POST", g.ts.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Content-Type", "application/json")
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 403 {
		t.Fatalf("stranger: %d", resp.StatusCode)
	}
}
