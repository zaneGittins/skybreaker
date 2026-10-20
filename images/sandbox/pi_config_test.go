package sandbox_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// pi-config.mjs puts what Skybreaker provides into pi's own config: its MCP
// server, and the model servers in pi-models.json, replacing the providers
// it put there before and leaving the user's alone.
func TestPiConfig(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	etc, agent := t.TempDir(), t.TempDir()
	write := func(dir, name, body string) { os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644) }
	read := func(name string) map[string]any {
		var v map[string]any
		b, _ := os.ReadFile(filepath.Join(agent, name))
		json.Unmarshal(b, &v)
		return v
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("node", append([]string{"pi-config.mjs"}, args...)...)
		cmd.Env = append(os.Environ(), "SKYBREAKER_ETC="+etc, "PI_CODING_AGENT_DIR="+agent)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	write(etc, "mcp.json", `{"mcpServers":{"skybreaker":{"type":"http","url":"http://broker:7701/mcp/orbit/tok/-"}}}`)
	write(etc, "pi-models.json", `{"providers":{"ollama":{"baseUrl":"http://broker:8080/ollama/v1","api":"openai-completions","apiKey":"$SKYBREAKER_TOKEN","models":[{"id":"qwen3-coder:30b"}]}}}`)
	write(agent, "models.json", `{"providers":{"mine":{"baseUrl":"https://x.example/v1","api":"openai-completions","models":[{"id":"m"}]}}}`)
	write(agent, "mcp.json", `{"mcpServers":{"docs":{"url":"https://docs.example/mcp"}}}`)
	run()

	providers := read("models.json")["providers"].(map[string]any)
	if providers["ollama"] == nil || providers["mine"] == nil {
		t.Fatalf("providers %v", providers)
	}
	servers := read("mcp.json")["mcpServers"].(map[string]any)
	if servers["skybreaker"] == nil || servers["docs"] == nil {
		t.Fatalf("mcp servers %v", servers)
	}
	if fi, _ := os.Stat(filepath.Join(agent, "models.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("models.json is %v", fi.Mode().Perm())
	}

	// A run's own MCP config wins over the sandbox's.
	write(etc, "run-mcp.json", `{"mcpServers":{"skybreaker":{"type":"http","url":"http://broker:7701/mcp/orbit/tok/s9"}}}`)
	run("--skybreaker-mcp", filepath.Join(etc, "run-mcp.json"))
	if u := read("mcp.json")["mcpServers"].(map[string]any)["skybreaker"].(map[string]any)["url"]; u != "http://broker:7701/mcp/orbit/tok/s9" {
		t.Fatalf("run MCP url %v", u)
	}

	// When Skybreaker stops providing one, what it put there goes; the
	// user's stays.
	os.Remove(filepath.Join(etc, "pi-models.json"))
	run()
	providers = read("models.json")["providers"].(map[string]any)
	if providers["ollama"] != nil || providers["mine"] == nil {
		t.Fatalf("after removal %v", providers)
	}
}

// skybreaker-pi adds every agent's instructions to pi's prompt when there
// are any, after taking its own flag off.
func TestPiWrapperInstructions(t *testing.T) {
	etc, bin := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(bin, "pi"), []byte("#!/bin/sh\nprintf '%s|' \"$@\"\n"), 0o755)
	os.WriteFile(filepath.Join(bin, "node"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	run := func() string {
		t.Helper()
		cmd := exec.Command("sh", "skybreaker-pi.sh", "--skybreaker-mcp", "/x/mcp.json", "--print", "hi")
		cmd.Env = append(os.Environ(), "SKYBREAKER_ETC="+etc, "PATH="+bin+":/usr/bin:/bin")
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if got := run(); got != "--print|hi|" {
		t.Errorf("without instructions %q", got)
	}
	os.WriteFile(filepath.Join(etc, "instructions.md"), []byte("Be brief."), 0o644)
	if got, want := run(), "--append-system-prompt|"+filepath.Join(etc, "instructions.md")+"|--print|hi|"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
