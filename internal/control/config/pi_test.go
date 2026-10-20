package config

import (
	"encoding/json"
	"strings"
	"testing"
)

const piConfig = `{
  "data_dir": "/var/lib/skybreaker",
  "users": ["zane@example.com"],
  "sandbox": {"image": "img"},
  "projects": {
    "orbit": {"repo": "a/orbit", "harnesses": ["claude", "pi"], "capabilities": ["local"]},
    "comet": {"repo": "a/comet", "harnesses": ["claude"], "capabilities": ["local"]},
    "nova": {"repo": "a/nova", "harnesses": ["pi"]}
  },
  "capabilities": {
    "local": [
      {"route": "/ollama", "upstream": "http://192.168.77.50:11434", "allow_private": ["192.168.77.50"], "rules": [{"match": "* /**", "action": "auto"}]},
      {"route": "/llama", "upstream": "http://127.0.0.1:8081", "allow_private": ["127.0.0.1"], "rules": [{"match": "* /**", "action": "auto"}]}
    ]
  },
  "pi": {"providers": {
    "ollama": {"route": "/ollama", "api": "openai-completions", "models": ["qwen3-coder:30b"]},
    "llama": {"route": "/llama", "api": "llama.cpp"}
  }}
}`

// pi in a sandbox is told about the model servers its project's routes
// reach: in models.json, which names the sandbox's token by variable, and
// a llama.cpp router through pi's own environment variables.
func TestPiConfig(t *testing.T) {
	c, err := load(t, piConfig)
	if err != nil {
		t.Fatal(err)
	}
	b, env := c.PiConfig("orbit", "tok-123")
	var m struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			API     string `json:"api"`
			APIKey  string `json:"apiKey"`
			Models  []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	p := m.Providers["ollama"]
	if p.BaseURL != "http://broker:8080/ollama/v1" || p.API != "openai-completions" || p.APIKey != "$SKYBREAKER_TOKEN" || len(p.Models) != 1 || p.Models[0].ID != "qwen3-coder:30b" {
		t.Fatalf("ollama provider %+v", p)
	}
	if strings.Contains(string(b), "tok-123") {
		t.Fatal("the token is in models.json")
	}
	if _, ok := m.Providers["llama"]; ok {
		t.Fatal("llama.cpp went into models.json")
	}
	if env["LLAMA_BASE_URL"] != "http://broker:8080/llama" || env["LLAMA_API_KEY"] != "tok-123" {
		t.Fatalf("env %v", env)
	}
	// Only projects that run pi, and only routes the project has.
	if b, env := c.PiConfig("comet", "t"); b != nil || env != nil {
		t.Fatalf("comet doesn't run pi: %s %v", b, env)
	}
	if b, env := c.PiConfig("nova", "t"); b != nil || env != nil {
		t.Fatalf("nova has no route: %s %v", b, env)
	}
}

func TestPiConfigInvalid(t *testing.T) {
	for name, edit := range map[string][2]string{
		"unknown route":   {`"route": "/ollama", "api"`, `"route": "/nowhere", "api"`},
		"no models":       {`"models": ["qwen3-coder:30b"]`, `"models": []`},
		"bad api":         {`"api": "openai-completions"`, `"api": "Open AI!"`},
		"bad model":       {`"qwen3-coder:30b"`, `"-rf"`},
		"bad name":        {`"ollama": {"route"`, `"Ol Lama": {"route"`},
		"two llama.cpp":   {`"api": "openai-completions", "models": ["qwen3-coder:30b"]`, `"api": "llama.cpp"`},
		"path not a path": {`"api": "openai-completions"`, `"api": "openai-completions", "path": "v1?x"`},
	} {
		if _, err := load(t, strings.Replace(piConfig, edit[0], edit[1], 1)); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}
