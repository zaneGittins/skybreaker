package controld

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
	"github.com/zaneGittins/skybreaker/internal/proxy/broker"
)

// TestUIDev serves the real control plane in development mode with a
// scripted docker and scripted harnesses, for working on the UI without a
// sandbox:
//
//	SKYBREAKER_UIDEV=127.0.0.1:8099 go test -run TestUIDev -timeout 0 ./internal/control/controld/
//
// Then open http://localhost:8099. Messages to Claude get a scripted turn;
// one containing "approve" asks for approval, "ask" asks a question,
// "agents" starts two background subagents that outlast the turn, and
// "fail" fails. Codex asks you to sign in.
func TestUIDev(t *testing.T) {
	addr := os.Getenv("SKYBREAKER_UIDEV")
	if addr == "" {
		t.Skip("set SKYBREAKER_UIDEV=host:port to serve the UI with scripted harnesses")
	}
	dir := t.TempDir()
	cfg := fmt.Sprintf(`{
	  "data_dir": %q, "users": ["dev@example.com"], "ui_url": "https://skybreaker.example.ts.net",
	  "tailscale": {"disabled": true, "dev_listen": %q},
	  "broker": {"listen": "127.0.0.1:0", "hold": "1ms"},
	  "sandbox": {"image": "skybreaker-sandbox:dev", "hook_listen": "127.0.0.1:0"},
	  "projects": {
	    "skybreaker": {"repo": "zaneGittins/skybreaker", "harnesses": ["claude", "codex"], "capabilities": ["github"]},
	    "bifract": {"repo": "zaneGittins/bifract", "harnesses": ["claude", "codex"], "capabilities": ["github"]}
	  },
	  "capabilities": {"github": [{"host": "api.github.com", "mode": "mitm", "rules": [
	    {"match": "GET /repos/{repo}/**", "action": "auto"},
	    {"match": "PUT /repos/{repo}/pulls/*/merge", "action": "ask"}
	  ]}]}
	}`, filepath.Join(dir, "data"), addr)
	path := filepath.Join(dir, "skybreaker.json")
	os.WriteFile(path, []byte(cfg), 0o600)
	devUsage(filepath.Join(dir, "data"))
	// A change saved in Settings restarts Skybreaker, as serve does.
	for {
		err := Run(context.Background(), Options{
			ConfigPath: path, Version: "uidev", Runner: &devDocker{}, Spawn: devSpawn,
			OnBroker: devRequests,
		})
		if !errors.Is(err, ErrRestart) {
			t.Fatal(err)
		}
	}
}

// devRequests has skybreaker's sandbox ask to merge a pull request now and then,
// so the approval cards have something to show.
func devRequests(b *broker.Broker) {
	tok, err := b.Mint(context.Background(), "skybreaker", time.Hour)
	if err != nil {
		panic(err)
	}
	// The sandbox's address on the project's own network.
	addr := sandbox.SubnetFor("skybreaker", netip.MustParsePrefix("10.89.0.0/16")).Addr().Next().Next()
	ask := func(pr int) {
		r := httptest.NewRequest("PUT", fmt.Sprintf("http://api.github.com/repos/zaneGittins/skybreaker/pulls/%d/merge", pr), strings.NewReader(`{"merge_method":"squash"}`))
		r.RemoteAddr = netip.AddrPortFrom(addr, 40000).String()
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("skybreaker:"+tok.Token)))
		b.Handler().ServeHTTP(httptest.NewRecorder(), r)
	}
	go func() {
		for pr := 41; ; pr++ {
			ask(pr)
			time.Sleep(3 * time.Minute)
		}
	}()
}

// devDocker answers the docker CLI like a host with running sandboxes.
type devDocker struct{ fakeDocker }

func (d *devDocker) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(line, "exec ") && strings.Contains(line, "tmux list-panes"):
		return nil, nil
	case strings.HasPrefix(line, "exec ") && strings.Contains(line, " git "):
		return devGit(line)
	case strings.HasPrefix(line, "exec ") && strings.Contains(line, "/proc/net/tcp"):
		// A dev server on 0.0.0.0:5173 and another on localhost:3000.
		return []byte("  sl  local_address rem_address   st\n   0: 00000000:1435 00000000:0000 0A\n   1: 0100007F:0BB8 00000000:0000 0A\n"), nil
	case strings.HasPrefix(line, "exec "):
		return nil, nil
	}
	return d.fakeDocker.Run(ctx, name, args...)
}

func devGit(line string) ([]byte, error) {
	switch {
	case strings.Contains(line, "rev-parse --abbrev-ref HEAD"):
		return []byte("feature/inbox\n"), nil
	case strings.Contains(line, "merge-base origin/HEAD HEAD"):
		return []byte("abc1234\n"), nil
	case strings.Contains(line, "merge-base"), strings.Contains(line, "--symbolic-full-name"):
		return nil, fmt.Errorf("exit status 128")
	case strings.Contains(line, "log --format"):
		return []byte("9f1c2ab\x1fAdd the inbox view\x1f2026-10-18T09:00:00Z\n"), nil
	case strings.Contains(line, "--numstat"):
		return []byte("12\t3\tweb/skybreaker/app.js\n4\t0\tweb/skybreaker/app.css\n"), nil
	case strings.Contains(line, "ls-files"):
		return nil, nil
	case strings.Contains(line, " diff "):
		return []byte(devDiff), nil
	}
	return nil, nil
}

const devDiff = `diff --git a/web/skybreaker/app.js b/web/skybreaker/app.js
--- a/web/skybreaker/app.js
+++ b/web/skybreaker/app.js
@@ -10,6 +10,15 @@ function go() {
   clearTimers();
-  closeTerminal();
+  closeTerminal();
+  closeChat();
+  closeMenu();
   state.route = route(location.pathname);
+  // The inbox is home now.
+  if (state.route.name === 'home') {
+    renderInbox();
+  }
diff --git a/web/skybreaker/app.css b/web/skybreaker/app.css
--- a/web/skybreaker/app.css
+++ b/web/skybreaker/app.css
@@ -1,3 +1,7 @@
 /* Skybreaker */
+.inbox { max-width: 860px; }
+.fab { display: none; }
+.tone-wait .tone-dot { background: var(--sun); }
+.tone-done .tone-dot { background: var(--live-dot); }
`

// devSpawn plays a harness behind `docker exec -i ... sh -c wrapper id argv`.
func devSpawn(_ context.Context, argv []string) (*chat.Proc, error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	codex := strings.Contains(strings.Join(argv, " "), " codex app-server")
	go func() {
		defer outW.Close()
		say := func(v any) {
			b, _ := json.Marshal(v)
			outW.Write(append(b, '\n'))
		}
		raw := func(s string) { io.WriteString(outW, s+"\n") }
		sc := bufio.NewScanner(inR)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			var m map[string]any
			json.Unmarshal(sc.Bytes(), &m)
			if codex {
				devCodex(m, say)
			} else {
				devClaude(m, say, raw)
			}
		}
	}()
	return &chat.Proc{Stdin: inW, Stdout: outR, Wait: func() error { return nil }, Kill: func() { inR.Close(); outW.Close() }}, nil
}

var devTurn = 0

func devClaude(m map[string]any, say func(any), raw func(string)) {
	pause := func() { time.Sleep(60 * time.Millisecond) }
	switch m["type"] {
	case "control_request":
		r := m["request"].(map[string]any)
		if r["subtype"] == "initialize" {
			say(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": m["request_id"], "response": map[string]any{"models": []any{
				map[string]any{"value": "default", "resolvedModel": "claude-opus-5-5", "displayName": "Default (recommended)", "description": "Test model · for everyday tasks"},
				map[string]any{"value": "fast", "resolvedModel": "claude-sonnet-5-5", "displayName": "Fast", "description": "Quicker answers"},
			}}}})
		}
		if r["subtype"] == "set_model" {
			say(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": m["request_id"]}})
		}
		if r["subtype"] == "interrupt" {
			raw(`{"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"aborted_streaming","duration_ms":10}`)
		}
		return
	case "control_response":
		resp := m["response"].(map[string]any)["response"].(map[string]any)
		if resp["behavior"] == "allow" {
			raw(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_test","content":"PASS  42 tests"}]},"tool_use_result":{"stdout":"> app@1.0 test\n> node --test\n\n✔ 42 tests passed (1.8s)","stderr":""}}`)
		} else {
			raw(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_test","is_error":true,"content":"The user doesn't want to proceed with this tool use."}]}}`)
		}
		pause()
		stream(say, "m-after", "All 42 tests pass. The inbox now sorts approvals first, then problems, then finished work.")
		raw(`{"type":"result","subtype":"success","is_error":false,"duration_ms":5400,"session_id":"dev-conv","modelUsage":{"claude-opus-5-5":{"contextWindow":400000}}}`)
		return
	case "user":
	default:
		return
	}
	text, _ := m["message"].(map[string]any)["content"].(string)
	devTurn++
	msg := fmt.Sprintf("m%d", devTurn)
	raw(`{"type":"system","subtype":"init","session_id":"dev-conv","model":"claude-opus-5-5","permissionMode":"default","slash_commands":["compact","review"]}`)
	pause()
	switch {
	case text == "/compact":
		devUsed = 30000
		pause()
		raw(`{"type":"system","subtype":"compact_boundary","session_id":"dev-conv","compact_metadata":{"trigger":"manual","pre_tokens":200000,"post_tokens":30000}}`)
		raw(`{"type":"result","subtype":"success","is_error":false,"duration_ms":4000,"result":"","session_id":"dev-conv"}`)
		return
	case strings.Contains(text, "approve"):
		stream(say, msg, "I'll run the test suite first.")
		raw(`{"type":"assistant","message":{"id":"` + msg + `t","content":[{"type":"tool_use","id":"toolu_test","name":"Bash","input":{"command":"npm test","description":"Run the test suite"}}]}}`)
		pause()
		raw(`{"type":"control_request","request_id":"perm-` + msg + `","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"npm test","description":"Run the test suite"},"description":"Run the test suite","permission_suggestions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"npm test"}],"behavior":"allow","destination":"session"}],"tool_use_id":"toolu_test"}}`)
		return
	case strings.Contains(text, "ask"):
		raw(`{"type":"control_request","request_id":"q-` + msg + `","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Where should finished sessions go?","header":"Inbox","multiSelect":false,"options":[{"label":"For you","description":"Until you've looked at them"},{"label":"Recent","description":"Straight away"}]}]}}}`)
		return
	case strings.Contains(text, "agents"):
		devAgents(msg, say, raw)
		return
	case strings.Contains(text, "fail"):
		raw(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 529 Overloaded. The service is busy; try again in a moment.","duration_ms":900}`)
		return
	}
	stream(say, msg, "Here's the plan for the **inbox**:\n\n1. Sort sessions by what needs you\n2. Fold ended ones away\n3. Keep rows to one line\n\nI'll start with the reducer.")
	raw(`{"type":"assistant","message":{"id":"` + msg + `a","content":[{"type":"tool_use","id":"t1-` + msg + `","name":"TodoWrite","input":{"todos":[{"content":"Group sessions by urgency","status":"completed"},{"content":"Draw one-line rows","status":"in_progress"},{"content":"Fold ended sessions","status":"pending"}]}}]}}`)
	raw(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1-` + msg + `","content":"ok"}]}}`)
	pause()
	raw(`{"type":"assistant","message":{"id":"` + msg + `b","content":[{"type":"tool_use","id":"t2-` + msg + `","name":"Grep","input":{"pattern":"inboxGroups","path":"/workspace/web"}}]}}`)
	pause()
	raw(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2-` + msg + `","content":"web/skybreaker/chatlib.js:3"}]}}`)
	raw(`{"type":"assistant","message":{"id":"` + msg + `c","content":[{"type":"tool_use","id":"t3-` + msg + `","name":"Edit","input":{"file_path":"/workspace/web/skybreaker/chatlib.js","old_string":"g.forYou.sort(newest);","new_string":"g.forYou.sort((a, b) => urgency(a) - urgency(b) || newest(a, b));"}}]}}`)
	pause()
	raw(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t3-` + msg + `","content":"updated"}]},"tool_use_result":{"filePath":"/workspace/web/skybreaker/chatlib.js","structuredPatch":[{"oldStart":212,"oldLines":3,"newStart":212,"newLines":3,"lines":["   const newest = (a, b) => when(b) - when(a);","-  g.forYou.sort(newest);","+  g.forYou.sort((a, b) => urgency(a) - urgency(b) || newest(a, b));","   g.working.sort(newest);"]}]}}`)
	raw(`{"type":"assistant","message":{"id":"` + msg + `d","content":[{"type":"tool_use","id":"t4-` + msg + `","name":"Bash","input":{"command":"node --test web/skybreaker/chatlib.test.js"}}]}}`)
	pause()
	raw(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t4-` + msg + `","content":"ok"}]},"tool_use_result":{"stdout":"✔ sessionTone and inboxGroups (3.8ms)\nℹ tests 14\nℹ pass 14\nℹ fail 0","stderr":""}}`)
	pause()
	stream(say, msg+"e", "Done. Approvals now sort to the top:\n\n```js\ng.forYou.sort((a, b) => urgency(a) - urgency(b) || newest(a, b));\n```\n\n| Group | Order |\n|---|---|\n| For you | urgency, then newest |\n| Working | newest |\n\nAll 14 tests pass. Want me to open a PR?")
	// Tokens and limits for the home page: the count grows over the
	// process's life, as Claude's does.
	n := devTokens.Add(1)
	raw(fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","unifiedWindows":{"five_hour":{"utilization":%.2f,"resetsAt":%d},"seven_day":{"utilization":0.46,"resetsAt":%d}}}}`,
		min(0.95, 0.3+0.07*float64(n)), time.Now().Add(150*time.Minute).Unix(), time.Now().Add(80*time.Hour).Unix()))
	raw(fmt.Sprintf(`{"type":"result","subtype":"success","is_error":false,"duration_ms":8200,"session_id":"dev-conv","modelUsage":{"claude-opus-5-5":{"contextWindow":400000,"inputTokens":%d,"cacheCreationInputTokens":%d,"cacheReadInputTokens":%d,"outputTokens":%d}}}`,
		n*40, n*9000, n*180000, n*2500))
}

// devAgents plays what Claude Code does with background subagents: the
// turn ends at once, their steps carry on, and each one's report back
// starts a turn of Claude's own.
func devAgents(msg string, say func(any), raw func(string)) {
	stream(say, msg, "I'll split this across two agents and keep you posted.")
	agents := []struct{ id, task, desc string }{{"ag1-" + msg, "k1-" + msg, "Audit the inbox sort"}, {"ag2-" + msg, "k2-" + msg, "Check the chat on a narrow screen"}}
	for _, a := range agents {
		raw(`{"type":"assistant","message":{"id":"` + a.id + `m","content":[{"type":"tool_use","id":"` + a.id + `","name":"Agent","input":{"description":"` + a.desc + `","prompt":"…","run_in_background":true}}]},"parent_tool_use_id":null}`)
		raw(`{"type":"system","subtype":"task_started","task_id":"` + a.task + `","tool_use_id":"` + a.id + `","description":"` + a.desc + `","is_backgrounded":true,"task_type":"local_agent"}`)
		raw(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + a.id + `","content":"Async agent launched successfully."}]},"tool_use_result":{"isAsync":true,"status":"async_launched"}}`)
	}
	stream(say, msg+"w", "Both are running in the background. I'll report back when they're done.")
	raw(`{"type":"result","subtype":"success","is_error":false,"duration_ms":2100,"session_id":"dev-conv"}`)
	step := func(a, id, tool, input, result string, d time.Duration) {
		raw(`{"type":"assistant","message":{"id":"` + id + `m","content":[{"type":"tool_use","id":"` + id + `","name":"` + tool + `","input":` + input + `}]},"parent_tool_use_id":"` + a + `"}`)
		time.Sleep(d)
		raw(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"` + result + `"}]},"parent_tool_use_id":"` + a + `"}`)
	}
	report := func(i int, summary, reply string) {
		a := agents[i]
		raw(`{"type":"system","subtype":"task_notification","task_id":"` + a.task + `","tool_use_id":"` + a.id + `","status":"completed","summary":"` + summary + `"}`)
		raw(`{"type":"system","subtype":"init","session_id":"dev-conv","model":"claude-opus-5-5","permissionMode":"default"}`)
		time.Sleep(400 * time.Millisecond)
		stream(say, a.id+"r", reply)
		raw(`{"type":"result","subtype":"success","is_error":false,"duration_ms":1500,"session_id":"dev-conv"}`)
	}
	go func() {
		a := agents[0].id
		time.Sleep(time.Second)
		step(a, a+"s1", "Grep", `{"pattern":"inboxGroups","path":"/workspace/web"}`, "web/skybreaker/chatlib.js:470", 3*time.Second)
		step(a, a+"s2", "Read", `{"file_path":"/workspace/web/skybreaker/chatlib.js"}`, "ok", 2*time.Second)
		step(a, a+"s3", "Bash", `{"command":"node --test web/skybreaker/chatlib.test.js"}`, "ok", 8*time.Second)
		report(0, "The sort is stable: approvals, then problems, then finished work. 27 tests pass.", "The inbox audit is back: the sort holds up and all 27 tests pass. Still waiting on the layout check.")
	}()
	go func() {
		a := agents[1].id
		time.Sleep(2 * time.Second)
		step(a, a+"s1", "Read", `{"file_path":"/workspace/web/skybreaker/shell.css"}`, "ok", 4*time.Second)
		step(a, a+"s2", "Bash", `{"command":"npx playwright test --project=mobile"}`, "ok", 22*time.Second)
		report(1, "At 375px the chat fits: no horizontal scroll, and the composer clears the home bar.", "The layout check is back too: at 375px nothing scrolls sideways and the composer clears the home bar. Both agents are done.")
	}()
}

var devTokens atomic.Int64

// devUsage is two weeks of tokens and both accounts' limits, for the home
// page's rail.
func devUsage(dataDir string) {
	os.MkdirAll(dataDir, 0o700)
	var hours []map[string]any
	now := time.Now().UTC().Truncate(time.Hour)
	for d := 13; d >= 0; d-- {
		for _, h := range []struct {
			name  string
			scale int64
		}{{"claude", 1 + int64((d*7)%5)}, {"codex", int64((d * 3) % 4)}} {
			if h.scale == 0 || (d%6 == 2 && h.name == "claude") {
				continue
			}
			hours = append(hours, map[string]any{"t": now.Add(-time.Duration(d*24+3) * time.Hour), "harness": h.name,
				"input": 20000 * h.scale, "cached": 900000 * h.scale, "output": 45000 * h.scale})
		}
	}
	b, _ := json.Marshal(map[string]any{"hours": hours, "limits": map[string]any{
		"codex": map[string]any{"at": time.Now().Add(-20 * time.Minute).UTC(), "windows": []map[string]any{
			{"name": "primary", "minutes": 300, "used": 18, "resets": time.Now().Add(3 * time.Hour).UTC()},
			{"name": "secondary", "minutes": 10080, "used": 72, "resets": time.Now().Add(5 * 24 * time.Hour).UTC()},
		}},
	}})
	os.WriteFile(filepath.Join(dataDir, "usage.json"), b, 0o600)
}

// devUsed is the scripted conversation's context, which each message grows.
var devUsed int64

// stream sends text as Claude Code's partial messages, then whole.
func stream(say func(any), msg, text string) {
	devUsed += 45000
	say(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_start", "message": map[string]any{"id": msg}}})
	say(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}})
	words := strings.SplitAfter(text, " ")
	for i := 0; i < len(words); i += 3 {
		chunk := strings.Join(words[i:min(i+3, len(words))], "")
		say(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": chunk}}})
		time.Sleep(25 * time.Millisecond)
	}
	say(map[string]any{"type": "assistant", "message": map[string]any{"id": msg, "content": []any{map[string]any{"type": "text", "text": text}},
		"usage": map[string]any{"input_tokens": 2, "cache_read_input_tokens": devUsed, "output_tokens": 40}}, "parent_tool_use_id": nil})
}

func devCodex(m map[string]any, say func(any)) {
	id, method := m["id"], m["method"]
	switch method {
	case "initialize":
		say(map[string]any{"id": id, "result": map[string]any{"userAgent": "dev"}})
	case "thread/start", "thread/resume":
		say(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "dev-thread"}, "model": "gpt-6.1-sol"}})
	case "account/read":
		say(map[string]any{"id": id, "result": map[string]any{"account": nil, "requiresOpenaiAuth": true}})
	case "model/list":
		say(map[string]any{"id": id, "result": map[string]any{"nextCursor": nil, "data": []any{
			map[string]any{"id": "gpt-6.1-sol", "model": "gpt-6.1-sol", "displayName": "GPT-6.1-Sol", "description": "Workhorse model", "hidden": false, "isDefault": true},
			map[string]any{"id": "gpt-daybreak-blue-latest", "model": "gpt-daybreak-blue-latest", "displayName": "Daybreak Blue", "description": "Defensive cyber model", "hidden": false, "isDefault": false},
		}}})
	case "account/login/start":
		say(map[string]any{"id": id, "result": map[string]any{"type": "chatgptDeviceCode", "loginId": "l1", "userCode": "WXYZ-4821", "verificationUrl": "https://auth.openai.com/codex/device"}})
	case "turn/start":
		say(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "turn-1"}}})
		say(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "dev-thread", "turn": map[string]any{"id": "turn-1", "status": "failed", "error": map[string]any{"message": "unexpected status 401 Unauthorized", "codexErrorInfo": map[string]any{"httpConnectionFailed": map[string]any{"httpStatusCode": 401}}}}}})
	}
}
