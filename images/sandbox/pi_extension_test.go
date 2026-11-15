package sandbox_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runLostArgs loads pi-lost-arguments.mjs as pi would, with a stand-in for
// pi's extension API, and returns what its message_end handler makes of
// each message: the replacement's text, or null when it leaves one alone.
func runLostArgs(t *testing.T, messages ...map[string]any) []any {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	in, _ := json.Marshal(messages)
	script := `
import ext from "./pi-lost-arguments.mjs";
const handlers = {};
ext({ on: (name, fn) => { handlers[name] = fn; } });
const out = [];
for (const message of ` + string(in) + `) {
  const r = await handlers.message_end({ type: "message_end", message }, {});
  if (r && r.message && r.message.role !== message.role) throw new Error("role changed");
  out.push(r && r.message ? r.message.content.map((c) => c.text).join("") : null);
}
console.log(JSON.stringify(out));
`
	cmd := exec.Command("node", "--input-type=module", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var got []any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return got
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"role": "toolResult", "toolCallId": "call_1", "toolName": "bash", "isError": isError,
		"content": []map[string]any{{"type": "text", "text": text}},
	}
}

// A tool call that reaches pi with no arguments at all lost them on the way:
// pi repairs malformed or cut-off JSON, so a call the model wrote badly
// still keeps some fields. The model is told that, instead of a list of
// missing fields it takes for its own formatting mistake.
func TestPiLostArguments(t *testing.T) {
	lost := "Validation failed for tool \"bash\":\n  - command: missing\n\nReceived arguments:\n{}"
	partial := "Validation failed for tool \"write\":\n  - content: missing\n\nReceived arguments:\n{\n  \"path\": \"a.go\"\n}"
	got := runLostArgs(t,
		toolResult(lost, true),
		toolResult(partial, true),
		toolResult(lost, false), // not an error: someone's real output
		toolResult("ok", false),
		map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": lost}}},
	)
	text, _ := got[0].(string)
	for _, want := range []string{"bash", "no arguments", "not your formatting"} {
		if !strings.Contains(text, want) {
			t.Errorf("replacement %q lacks %q", text, want)
		}
	}
	for i, g := range got[1:] {
		if g != nil {
			t.Errorf("message %d replaced: %v", i+1, g)
		}
	}
}

// skybreaker-pi loads each extension the image ships, before pi's own
// arguments, and nothing when there are none.
func TestPiWrapperExtensions(t *testing.T) {
	bin, ext := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(bin, "pi"), []byte("#!/bin/sh\nprintf '%s|' \"$@\"\n"), 0o755)
	os.WriteFile(filepath.Join(bin, "node"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	run := func() string {
		t.Helper()
		cmd := exec.Command("sh", "skybreaker-pi.sh", "--print", "hi")
		cmd.Env = append(os.Environ(), "SKYBREAKER_ETC="+t.TempDir(), "SKYBREAKER_PI_EXTENSIONS="+ext, "PATH="+bin+":/usr/bin:/bin")
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if got := run(); got != "--print|hi|" {
		t.Errorf("without extensions %q", got)
	}
	a, b := filepath.Join(ext, "a.mjs"), filepath.Join(ext, "b.mjs")
	os.WriteFile(a, nil, 0o644)
	os.WriteFile(b, nil, 0o644)
	if got, want := run(), "-e|"+a+"|-e|"+b+"|--print|hi|"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// pi-fallback.mjs registers <provider>/auto for each fallback list in the
// sandbox's pi-models.json. It stays on one model through a turn, moves to
// the next when a model is out of its daily quota, and gives that model a
// rest before trying it again.
func TestPiFallback(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	etc := t.TempDir()
	os.WriteFile(filepath.Join(etc, "pi-models.json"), []byte(`{"providers":{"do":{"models":[{"id":"a","contextWindow":200000,"maxTokens":65536},{"id":"b"}]}},"fallbacks":{"do":["a","b"]}}`), 0o644)
	script := `
import ext from "./pi-fallback.mjs";
const defs = [];
ext({ registerVirtualModel: (d) => defs.push(d) });
const find = (p, id) => ({ provider: p, id });
const ctx = { modelRegistry: { find } };
const quota = "429: {\"message\":\"Rate limit exceeded: tokens_per_day.\",\"type\":\"rate_limit_exceeded_error\"}";
const now = Date.now();
const r = (req) => { const o = defs[0].route({ thinkingLevel: "medium", messages: [], ...req }, ctx); return [o.model.id, o.thinkingLevel, o.state ?? null]; };
const out = {
  defs: defs.map((d) => d.provider + "/" + d.id),
  limits: [defs[0].contextWindow, defs[0].maxTokens, (defs[0].input ?? []).join(",")],
  user: r({ reason: "user" }),
  sticky: r({ reason: "continuation", previous: { model: find("do", "b"), thinkingLevel: "high" } }),
  outOfQuota: r({ reason: "retry", failed: { model: find("do", "a"), thinkingLevel: "high", message: { errorMessage: quota } } }),
  otherError: r({ reason: "retry", failed: { model: find("do", "a"), thinkingLevel: "high", message: { errorMessage: "502 bad gateway" } } }),
  resting: r({ reason: "user", state: { resting: { a: now + 60000 } } }),
  rested: r({ reason: "user", state: { resting: { a: now - 1 } } }),
  allOut: r({ reason: "retry", state: { resting: { a: now + 60000 } }, failed: { model: find("do", "b"), thinkingLevel: "high", message: { errorMessage: quota } } }),
};
console.log(JSON.stringify(out));
`
	cmd := exec.Command("node", "--input-type=module", "-e", script)
	cmd.Env = append(os.Environ(), "SKYBREAKER_ETC="+etc)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	var out map[string][]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	model := func(k string) any { return out[k][0] }
	if len(out["defs"]) != 1 || out["defs"][0] != "do/auto" {
		t.Fatalf("registered %v", out["defs"])
	}
	for k, want := range map[string]string{"user": "a", "sticky": "b", "outOfQuota": "b", "otherError": "a", "resting": "b", "rested": "a", "allOut": "b"} {
		if model(k) != want {
			t.Errorf("%s: routed to %v, want %s", k, model(k), want)
		}
	}
	// It shows the first model's limits and input until a model answers.
	if l := out["limits"]; l[0] != 200000.0 || l[1] != 65536.0 || l[2] != "text" {
		t.Errorf("limits %v", l)
	}
	if out["sticky"][1] != "high" {
		t.Errorf("sticky thinking level %v", out["sticky"][1])
	}
	// Out of quota, a rests; another error changes nothing.
	if st, _ := out["outOfQuota"][2].(map[string]any); st == nil || st["resting"].(map[string]any)["a"] == nil {
		t.Errorf("out of quota state %v", out["outOfQuota"][2])
	}
	if out["otherError"][2] != nil {
		t.Errorf("other error state %v", out["otherError"][2])
	}

	// No fallbacks, no model.
	os.WriteFile(filepath.Join(etc, "pi-models.json"), []byte(`{"providers":{}}`), 0o644)
	cmd = exec.Command("node", "--input-type=module", "-e", `import ext from "./pi-fallback.mjs"; let n = 0; ext({ registerVirtualModel: () => n++ }); console.log(n);`)
	cmd.Env = append(os.Environ(), "SKYBREAKER_ETC="+etc)
	if b, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(b)) != "0" {
		t.Errorf("without fallbacks: %v %s", err, b)
	}
}
