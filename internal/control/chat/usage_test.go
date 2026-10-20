package chat

import (
	"bufio"
	"os"
	"testing"
	"time"
)

// readAll feeds a recorded session to a driver and sums the tokens it
// reports, keeping the last limits.
func readAll(t *testing.T, d Driver, file string) (Usage, []Limit) {
	t.Helper()
	f, err := os.Open("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 4<<20)
	var sum Usage
	var limits []Limit
	for sc.Scan() {
		out := d.Read(sc.Bytes())
		if u := out.Usage; u != nil {
			sum.Input += u.Input
			sum.Cached += u.Cached
			sum.Output += u.Output
		}
		if out.Limits != nil {
			limits = out.Limits
		}
	}
	return sum, limits
}

// A turn's tokens come from the result's per-model usage, which counts
// subagents and compaction too; it grows over the process's life, so each
// result reports what it added. Cache writes are fresh input.
func TestClaudeUsage(t *testing.T) {
	u, _ := readAll(t, newClaude(), "claude-bash.jsonl")
	if u != (Usage{Input: 4 + 11668, Cached: 33208, Output: 102}) {
		t.Errorf("usage %+v", u)
	}
	_, limits := readAll(t, newClaude(), "claude-compact.jsonl")
	want := []Limit{
		{Name: "five_hour", Minutes: 300, Used: 5, Resets: time.Unix(1792473000, 0).UTC()},
		{Name: "seven_day", Minutes: 7 * 24 * 60, Used: 50, Resets: time.Unix(1792594800, 0).UTC()},
	}
	if len(limits) != len(want) {
		t.Fatalf("limits %+v", limits)
	}
	for i := range want {
		if limits[i] != want[i] {
			t.Errorf("limit %d = %+v, want %+v", i, limits[i], want[i])
		}
	}

	// Two turns, the second a compaction: the total is the last result's
	// per-model usage, not the sum of both.
	u, _ = readAll(t, newClaude(), "claude-compact.jsonl")
	if u.Input+u.Cached+u.Output == 0 || u.Cached != 25540 || u.Output != 918 {
		t.Errorf("compact usage %+v", u)
	}

	// A new process starts its count again.
	d := newClaude()
	d.Read([]byte(`{"type":"result","subtype":"success","modelUsage":{"m":{"inputTokens":10,"outputTokens":5,"cacheReadInputTokens":100,"cacheCreationInputTokens":0}}}`))
	out := d.Read([]byte(`{"type":"result","subtype":"success","modelUsage":{"m":{"inputTokens":3,"outputTokens":1,"cacheReadInputTokens":7,"cacheCreationInputTokens":2}}}`))
	if out.Usage == nil || *out.Usage != (Usage{Input: 5, Cached: 7, Output: 1}) {
		t.Errorf("after a restart %+v", out.Usage)
	}
	// A result with nothing new reports nothing.
	out = d.Read([]byte(`{"type":"result","subtype":"success","modelUsage":{"m":{"inputTokens":3,"outputTokens":1,"cacheReadInputTokens":7,"cacheCreationInputTokens":2}}}`))
	if out.Usage != nil {
		t.Errorf("unchanged %+v", out.Usage)
	}
}

func TestClaudeLimitsIgnoreJunk(t *testing.T) {
	d := newClaude()
	for _, l := range []string{
		`{"type":"rate_limit_event"}`,
		`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"five_hour":{"utilization":7,"resetsAt":1}}}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"five_hour":{"utilization":-0.1}}}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"\u202ehour":{"utilization":0.1}}}}`,
	} {
		if out := d.Read([]byte(l)); len(out.Limits) != 0 {
			t.Errorf("%s: %+v", l, out.Limits)
		}
	}
}

// Codex reports each request's tokens; its input counts the cached part.
// The account's windows come from account/rateLimits/read and later
// updates, which may leave a window out.
func TestCodexUsage(t *testing.T) {
	d, _ := handshake(t, Options{})
	var limitsID any
	for id, m := range d.sent {
		if m == "account/rateLimits/read" {
			limitsID = id
		}
	}
	if limitsID == nil {
		t.Fatal("rate limits never asked for")
	}
	out := d.Read(line(map[string]any{"id": limitsID, "result": map[string]any{
		"rateLimits": map[string]any{"limitId": "codex", "primary": map[string]any{"usedPercent": 12, "windowDurationMins": 300, "resetsAt": 1792473000},
			"secondary": map[string]any{"usedPercent": 40.5, "windowDurationMins": 10080, "resetsAt": 1792594800}, "planType": "pro"},
	}}))
	if len(out.Limits) != 2 || out.Limits[0] != (Limit{Name: "primary", Minutes: 300, Used: 12, Resets: time.Unix(1792473000, 0).UTC()}) ||
		out.Limits[1].Minutes != 10080 || out.Limits[1].Used != 40.5 {
		t.Fatalf("limits %+v", out.Limits)
	}
	out = d.Read([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":15,"windowDurationMins":300,"resetsAt":1792473000},"secondary":null}}}`))
	if len(out.Limits) != 2 || out.Limits[0].Used != 15 || out.Limits[1].Used != 40.5 {
		t.Fatalf("updated limits %+v", out.Limits)
	}

	usage := func(total, input, cached, output int64) []byte {
		b := map[string]any{"totalTokens": total, "inputTokens": input, "cachedInputTokens": cached, "cacheWriteInputTokens": 0, "outputTokens": output, "reasoningOutputTokens": 0}
		return line(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": "thr-1", "turnId": "t1",
			"tokenUsage": map[string]any{"total": b, "last": b, "modelContextWindow": 272000}}})
	}
	out = d.Read(usage(1200, 1000, 800, 200))
	if out.Usage == nil || *out.Usage != (Usage{Input: 200, Cached: 800, Output: 200}) {
		t.Fatalf("usage %+v", out.Usage)
	}
	// The same report again (as on a resume) adds nothing.
	if out = d.Read(usage(1200, 1000, 800, 200)); out.Usage != nil {
		t.Errorf("repeat %+v", out.Usage)
	}
	// Another thread's isn't ours.
	if out = d.Read([]byte(`{"method":"thread/tokenUsage/updated","params":{"threadId":"other","tokenUsage":{"total":{"totalTokens":5,"inputTokens":5},"last":{"totalTokens":5,"inputTokens":5}}}}`)); out.Usage != nil {
		t.Errorf("other thread %+v", out.Usage)
	}
}
