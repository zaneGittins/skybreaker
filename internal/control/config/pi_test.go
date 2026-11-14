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
    "ollama": {"route": "/ollama", "api": "openai-completions", "models": ["qwen3-coder:30b", {"id": "glm-4.7", "contextWindow": 32768, "maxTokens": 8192, "reasoning": true, "cost": {"input": 0.3, "output": 1.2}}]},
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
			BaseURL string           `json:"baseUrl"`
			API     string           `json:"api"`
			APIKey  string           `json:"apiKey"`
			Models  []map[string]any `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	p := m.Providers["ollama"]
	if p.BaseURL != "http://broker:8080/ollama/v1" || p.API != "openai-completions" || p.APIKey != "$SKYBREAKER_TOKEN" || len(p.Models) != 2 {
		t.Fatalf("ollama provider %+v", p)
	}
	// A model is an ID, or pi's own fields for it, written through.
	if m := p.Models[0]; len(m) != 1 || m["id"] != "qwen3-coder:30b" {
		t.Fatalf("plain model %v", m)
	}
	glm := p.Models[1]
	if glm["id"] != "glm-4.7" || glm["contextWindow"] != 32768.0 || glm["maxTokens"] != 8192.0 || glm["reasoning"] != true {
		t.Fatalf("model with settings %v", glm)
	}
	if c, _ := glm["cost"].(map[string]any); c["input"] != 0.3 || c["output"] != 1.2 {
		t.Fatalf("model cost %v", glm["cost"])
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
		"unknown route":    {`"route": "/ollama", "api"`, `"route": "/nowhere", "api"`},
		"no models":        {`"models": ["qwen3-coder:30b", {"id": "glm-4.7", "contextWindow": 32768, "maxTokens": 8192, "reasoning": true, "cost": {"input": 0.3, "output": 1.2}}]`, `"models": []`},
		"bad api":          {`"api": "openai-completions"`, `"api": "Open AI!"`},
		"bad model":        {`"qwen3-coder:30b"`, `"-rf"`},
		"bad name":         {`"ollama": {"route"`, `"Ol Lama": {"route"`},
		"two llama.cpp":    {`"api": "openai-completions", "models": ["qwen3-coder:30b", {"id": "glm-4.7", "contextWindow": 32768, "maxTokens": 8192, "reasoning": true, "cost": {"input": 0.3, "output": 1.2}}]`, `"api": "llama.cpp"`},
		"path not a path":  {`"api": "openai-completions"`, `"api": "openai-completions", "path": "v1?x"`},
		"model no id":      {`{"id": "glm-4.7", `, `{`},
		"model bad id":     {`{"id": "glm-4.7"`, `{"id": "-rf"`},
		"model id number":  {`{"id": "glm-4.7"`, `{"id": 7`},
		"model not object": {`"qwen3-coder:30b", {`, `"qwen3-coder:30b", 7, {`},
		"model odd field":  {`"reasoning": true`, `"reasoning": true, "two words": 1`},
		"model twice":      {`{"id": "glm-4.7"`, `{"id": "qwen3-coder:30b"`},
		// Where a request goes and with what key stay the route's.
		"model baseUrl":  {`"reasoning": true`, `"reasoning": true, "baseUrl": "https://evil.example/v1"`},
		"model apiKey":   {`"reasoning": true`, `"reasoning": true, "apiKey": "sk-123"`},
		"model headers":  {`"reasoning": true`, `"reasoning": true, "headers": {"Authorization": "Bearer x"}`},
		"model api":      {`"reasoning": true`, `"reasoning": true, "api": "anthropic-messages"`},
		"model provider": {`"reasoning": true`, `"reasoning": true, "provider": "openai"`},
	} {
		if _, err := load(t, strings.Replace(piConfig, edit[0], edit[1], 1)); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}
