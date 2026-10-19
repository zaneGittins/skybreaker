package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func echoServer() *Server {
	return &Server{Name: "skybreaker", Version: "test", Instructions: "hi", Tools: []func(Caller) []Tool{
		func(c Caller) []Tool {
			if c.Project == "" {
				return nil
			}
			return []Tool{{
				Name: "whoami", Description: "who", Input: json.RawMessage(`{"type":"object"}`), ReadOnly: true,
				Call: func(_ context.Context, c Caller, _ json.RawMessage) (string, error) {
					return c.Project + "/" + c.Session + "/" + c.Client + "/" + c.MCP, nil
				},
			}, {
				Name: "fail", Input: json.RawMessage(`{"type":"object"}`),
				Call: func(context.Context, Caller, json.RawMessage) (string, error) { return "", errors.New("nope") },
			}}
		},
	}}
}

// mount resolves the project from the path, as the hook listener does.
func mount(s *Server) *httptest.Server {
	h := s.Handler(func(r *http.Request) (Caller, error) {
		p := r.Header.Get("X-Test-Project")
		if p == "evil" {
			return Caller{}, errors.New("no")
		}
		return Caller{Project: p, Session: "s1"}, nil
	})
	return httptest.NewServer(h)
}

func rpc(t *testing.T, url, project, session, body string) (int, http.Header, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-Test-Project", project)
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	return resp.StatusCode, resp.Header, m
}

func initialize(t *testing.T, url, project string) string {
	t.Helper()
	code, h, m := rpc(t, url, project, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"claude-code"}}}`)
	res, _ := m["result"].(map[string]any)
	if code != 200 || h.Get("Mcp-Session-Id") == "" || res["protocolVersion"] != "2025-03-26" || res["instructions"] != "hi" {
		t.Fatalf("initialize: %d %v %v", code, h, m)
	}
	return h.Get("Mcp-Session-Id")
}

func TestSessionAndTools(t *testing.T) {
	ts := mount(echoServer())
	defer ts.Close()
	sid := initialize(t, ts.URL, "bifract")

	if code, _, _ := rpc(t, ts.URL, "bifract", sid, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted {
		t.Fatalf("notification: %d", code)
	}
	_, _, m := rpc(t, ts.URL, "bifract", sid, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := m["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 || tools[0].(map[string]any)["annotations"].(map[string]any)["readOnlyHint"] != true {
		t.Fatalf("tools/list: %v", m)
	}
	_, _, m = rpc(t, ts.URL, "bifract", sid, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"whoami"}}`)
	text := m["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
	if text != "bifract/s1/claude-code/"+sid {
		t.Fatalf("caller = %v", text)
	}
	// A failing tool is a failed call the model can read, not a protocol error.
	_, _, m = rpc(t, ts.URL, "bifract", sid, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"fail","arguments":{}}}`)
	if m["result"].(map[string]any)["isError"] != true {
		t.Fatalf("failed call: %v", m)
	}
	_, _, m = rpc(t, ts.URL, "bifract", sid, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"rm_rf"}}`)
	if m["error"] == nil {
		t.Fatalf("unknown tool: %v", m)
	}
	_, _, m = rpc(t, ts.URL, "bifract", sid, `{"jsonrpc":"2.0","id":6,"method":"resources/list"}`)
	if m["error"].(map[string]any)["code"] != float64(-32601) {
		t.Fatalf("unknown method: %v", m)
	}
}

func TestSessionsStayOnTheirMount(t *testing.T) {
	ts := mount(echoServer())
	defer ts.Close()
	sid := initialize(t, ts.URL, "bifract")
	// Another project's sandbox presenting bifract's session gets nothing.
	if code, _, _ := rpc(t, ts.URL, "skybreaker", sid, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); code != 404 {
		t.Fatalf("session used from another project: %d", code)
	}
	if code, _, _ := rpc(t, ts.URL, "bifract", "made-up", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); code != 404 {
		t.Fatalf("unknown session: %d", code)
	}
	// A refused caller gets 404, not 403: Claude Code takes a 403 to mean
	// "needs sign-in" and stops trying the server for minutes.
	if code, _, _ := rpc(t, ts.URL, "evil", sid, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); code != 404 {
		t.Fatalf("refused caller: %d", code)
	}
	if code, _, _ := rpc(t, ts.URL, "evil", "", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`); code != 404 {
		t.Fatalf("refused initialize: %d", code)
	}
}

func TestProtocolChecks(t *testing.T) {
	ts := mount(echoServer())
	defer ts.Close()
	for name, body := range map[string]string{
		"batch":    `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`,
		"version":  `{"jsonrpc":"1.0","id":1,"method":"ping"}`,
		"trailing": `{"jsonrpc":"2.0","id":1,"method":"initialize"} {"x":1}`,
		"garbage":  `nope`,
	} {
		if code, _, _ := rpc(t, ts.URL, "bifract", "", body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	req, _ := http.NewRequest("POST", ts.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Content-Type", "text/plain")
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d", resp.StatusCode)
	}
	if resp, _ := http.Get(ts.URL); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET stream: %d", resp.StatusCode)
	}
	// An unknown protocol version gets the newest one.
	_, _, m := rpc(t, ts.URL, "bifract", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if m["result"].(map[string]any)["protocolVersion"] != versions[0] {
		t.Fatalf("version: %v", m)
	}
}

func TestArgs(t *testing.T) {
	var a struct{ Ref string }
	if err := Args(json.RawMessage(`{"ref":"BIF-1","sneaky":1}`), &a); err == nil {
		t.Fatal("unknown argument accepted")
	}
	if err := Args(json.RawMessage(`{"ref":"BIF-1"}`), &a); err != nil || a.Ref != "BIF-1" {
		t.Fatalf("%v %v", a, err)
	}
}
