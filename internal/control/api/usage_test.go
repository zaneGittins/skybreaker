package api

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/usage"
)

func TestUsageAPI(t *testing.T) {
	f := newFixture(t, you)
	if code, _ := f.do(t, "GET", "/api/usage", "", nil); code != 404 {
		t.Errorf("without a store: %d", code)
	}
	st, err := usage.Open(filepath.Join(t.TempDir(), "usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.Add("claude", chat.Usage{Input: 5, Cached: 50, Output: 2})
	st.SetLimits("codex", []chat.Limit{{Name: "primary", Minutes: 300, Used: 12}})
	f = newFixture(t, you, func(d *Deps) { d.Usage = st })
	code, body := f.do(t, "GET", "/api/usage", "", nil)
	var out struct {
		Days   int          `json:"days"`
		Hours  []usage.Hour `json:"hours"`
		Limits map[string]usage.Limits
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if out.Days != chartDays || len(out.Hours) != 1 || out.Hours[0].Harness != "claude" || out.Hours[0].Cached != 50 || out.Limits["codex"].Windows[0].Used != 12 {
		t.Errorf("%s", body)
	}
}
