package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/config"
)

type fakeAdder struct {
	got []config.Added
}

func (f *fakeAdder) AddProject(_ context.Context, _, name string, a config.Added) (AddedProject, error) {
	if name == "orbit" {
		return AddedProject{}, errors.New(`project "orbit" already exists in the config file`)
	}
	f.got = append(f.got, a)
	return AddedProject{Name: name, Restart: "now", GitHub: &GitHubAccess{Installed: false, InstallURL: "https://github.com/apps/sky/installations/new"}}, nil
}

func TestCreateProject(t *testing.T) {
	adder := &fakeAdder{}
	f := newFixture(t, you, func(d *Deps) { d.Projects = adder })
	body := `{"name":"comet","repo":"zaneGittins/comet","harnesses":["claude"],"capabilities":["github"],"key":"COM"}`
	code, out := f.do(t, "POST", "/api/projects", body, nil)
	var got AddedProject
	json.Unmarshal([]byte(out), &got)
	if code != 200 || got.Name != "comet" || got.Restart != "now" || got.GitHub == nil || got.GitHub.Installed {
		t.Fatalf("create: %d %s", code, out)
	}
	if len(adder.got) != 1 || adder.got[0].Repo != "zaneGittins/comet" || adder.got[0].Key != "COM" || adder.got[0].Capabilities[0] != "github" {
		t.Fatalf("added %+v", adder.got)
	}
	if code, out := f.do(t, "POST", "/api/projects", `{"name":"orbit","repo":"a/b","harnesses":["claude"]}`, nil); code != http.StatusBadRequest || !strings.Contains(out, "already exists") {
		t.Fatalf("existing: %d %s", code, out)
	}
	// Only the fields the app may set: anything else is refused.
	if code, _ := f.do(t, "POST", "/api/projects", `{"name":"nova","repo":"a/b","harnesses":["claude"],"allow":[{"host":"evil.example"}]}`, nil); code != http.StatusBadRequest {
		t.Fatalf("allow smuggled in: %d", code)
	}
	// A preview page (another port of this host) can't create one.
	u, _ := url.Parse(f.ts.URL)
	if code, _ := f.do(t, "POST", "/api/projects", body, map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://" + u.Hostname() + ":1"}); code != http.StatusForbidden {
		t.Fatalf("from another origin: %d", code)
	}
	if len(adder.got) != 1 {
		t.Fatalf("%d projects added", len(adder.got))
	}
	off := newFixture(t, you)
	if code, _ := off.do(t, "POST", "/api/projects", body, nil); code != http.StatusNotFound {
		t.Fatalf("without an adder: %d", code)
	}
}

func TestCapabilitiesList(t *testing.T) {
	f := newFixture(t, you)
	code, out := f.do(t, "GET", "/api/capabilities", "", nil)
	var got []struct {
		Name    string   `json:"name"`
		Reaches []string `json:"reaches"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	// The fixture defines none; the list is empty, not null.
	if out != "[]\n" && len(got) != 0 {
		t.Fatalf("capabilities %s", out)
	}
}
