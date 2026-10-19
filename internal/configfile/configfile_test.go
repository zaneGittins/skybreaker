package configfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type sample struct {
	Name  string   `json:"name" yaml:"name"`
	Hold  Duration `json:"hold" yaml:"hold"`
	Items []string `json:"items" yaml:"items"`
}

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadJSON(t *testing.T) {
	var s sample
	if err := Load(write(t, "c.json", `{"name":"x","hold":"30s","items":["a"]}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Name != "x" || s.Hold.D() != 30*time.Second || len(s.Items) != 1 {
		t.Fatalf("got %+v", s)
	}
}

func TestUnknownFieldsRejected(t *testing.T) {
	// A typo in a policy file must not silently drop a rule.
	var s sample
	err := Load(write(t, "c.json", `{"name":"x","itmes":["a"]}`), &s)
	if err == nil || !strings.Contains(err.Error(), "itmes") {
		t.Fatalf("err = %v, want unknown field error", err)
	}
}

func TestTrailingData(t *testing.T) {
	var s sample
	if err := Load(write(t, "c.json", `{"name":"x"} {"name":"y"}`), &s); err == nil {
		t.Fatal("trailing document accepted")
	}
}

func TestDuration(t *testing.T) {
	var s sample
	if err := Load(write(t, "c.json", `{"hold":"forever"}`), &s); err == nil {
		t.Fatal("bad duration accepted")
	}
	if err := Load(write(t, "c.json", `{"hold":"-5s"}`), &s); err == nil {
		t.Fatal("negative duration accepted")
	}
	if Duration(0).Or(time.Minute) != time.Minute || Duration(time.Second).Or(time.Minute) != time.Second {
		t.Fatal("Or wrong")
	}
}

func TestMissingFile(t *testing.T) {
	var s sample
	if err := Load(filepath.Join(t.TempDir(), "nope.json"), &s); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestReadSecretFile(t *testing.T) {
	p := write(t, "tok", "  s3cret\n")
	v, err := ReadSecret(p)
	if err != nil || v != "s3cret" {
		t.Fatalf("ReadSecret = %q, %v", v, err)
	}
	os.Chmod(p, 0o644)
	if _, err := ReadSecret(p); err == nil {
		t.Fatal("world-readable secret file accepted")
	}
	empty := write(t, "e", "\n")
	if _, err := ReadSecret(empty); err == nil {
		t.Fatal("empty secret accepted")
	}
}

func TestYAMLHook(t *testing.T) {
	old := DecodeYAML
	defer func() { DecodeYAML = old }()
	var s sample
	if err := Load(write(t, "c.yaml", "name: x\n"), &s); err == nil {
		t.Fatal("YAML accepted with no YAML support built in")
	}
	// The adapter turns YAML into JSON and uses the same strict decoder.
	DecodeYAML = func(b []byte, v any) error { return DecodeJSON([]byte(`{"name":"from-yaml","hold":"1m"}`), v) }
	if err := Load(write(t, "c.yml", "name: x\n"), &s); err != nil || s.Name != "from-yaml" || s.Hold.D() != time.Minute {
		t.Fatalf("hook not used: %+v %v", s, err)
	}
	if err := Load(write(t, "c.toml", "x"), &s); err == nil {
		t.Fatal("unknown extension accepted")
	}
}

func TestDuplicateKeysRejected(t *testing.T) {
	// encoding/json matches keys case-insensitively and keeps the last, so
	// "Items" then "items" would silently drop a block of policy.
	for name, body := range map[string]string{
		"case variant":  `{"Items":["a"],"items":["b"]}`,
		"exact repeat":  `{"name":"x","name":"y"}`,
		"nested":        `{"name":"x","sub":{"a":1,"A":2}}`,
		"inside a list": `{"list":[{"k":1,"K":2}]}`,
	} {
		var v map[string]any
		if err := DecodeJSON([]byte(body), &v); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Errorf("%s: err = %v, want a duplicate key error", name, err)
		}
	}
	var s sample
	if err := DecodeJSON([]byte(`{"name":"x","items":["a"],"hold":"1s"}`), &s); err != nil {
		t.Fatalf("clean document: %v", err)
	}
}
