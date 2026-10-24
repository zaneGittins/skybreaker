package config

import (
	"os"
	"strings"
	"testing"
)

func withAssistant(t *testing.T) string {
	t.Helper()
	path := mainConfig(t)
	if err := AddProject(path, "pip", Added{Kind: KindAssistant, Harnesses: []string{"claude"}}, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

func addRoutine(path string, j Job) error {
	_, _, err := Edit(path, func(f *AppFiles) error {
		f.Routines = append(f.Routines, &j)
		return nil
	}, nil, false)
	return err
}

// An assistant's routines are made in the app, and run like the file's jobs.
func TestRoutines(t *testing.T) {
	path := withAssistant(t)
	if err := addRoutine(path, Job{Name: "morning-brief", Project: "pip", Schedule: "30 7 * * 1-5", Prompt: "Write my brief."}); err != nil {
		t.Fatal(err)
	}
	c := mustLoad(t, path)
	j, ok := c.Job("morning-brief")
	if !ok || j.Harness != "claude" || j.Kind != JobPrompt || !c.AppJob("morning-brief") || c.AppJob("nightly-orbit") {
		t.Fatalf("job %+v app=%v", j, c.AppJob("morning-brief"))
	}
	if fi, _ := os.Stat(c.AppPaths().Routines); fi.Mode().Perm() != 0o600 {
		t.Errorf("routines.json mode %v", fi.Mode().Perm())
	}
	for name, bad := range map[string]Job{
		"a coding project": {Name: "x", Project: "orbit", Schedule: "@daily", Prompt: "x"},
		"a board job":      {Name: "x", Project: "pip", Schedule: "@daily", Kind: JobBoard},
		"a bad schedule":   {Name: "x", Project: "pip", Schedule: "sometimes", Prompt: "x"},
		"no prompt":        {Name: "x", Project: "pip", Schedule: "@daily"},
		"a file job name":  {Name: "nightly-orbit", Project: "pip", Schedule: "@daily", Prompt: "x"},
		"a taken name":     {Name: "morning-brief", Project: "pip", Schedule: "@daily", Prompt: "x"},
		"no project":       {Name: "x", Project: "ghost", Schedule: "@daily", Prompt: "x"},
	} {
		if err := addRoutine(path, bad); err == nil {
			t.Errorf("%s: added", name)
		}
	}
}

// Removing an assistant from the config file leaves its routines behind,
// unused, rather than stopping Skybreaker from starting.
func TestRoutinesOfAGoneAssistant(t *testing.T) {
	path := withAssistant(t)
	addRoutine(path, Job{Name: "morning-brief", Project: "pip", Schedule: "@daily", Prompt: "x"})
	ps := appPaths(t, path)
	b, _ := os.ReadFile(ps.Projects)
	os.WriteFile(ps.Projects, []byte(strings.Replace(string(b), `"pip"`, `"gone"`, 1)), 0o600)
	c := mustLoad(t, path)
	if _, ok := c.Job("morning-brief"); ok {
		t.Fatal("a routine kept running for a gone assistant")
	}
}

// A routine with no agent named runs on its assistant's own.
func TestRoutineHarnessDefaultsToTheAssistants(t *testing.T) {
	path := mainConfig(t)
	AddProject(path, "pip", Added{Kind: KindAssistant, Harnesses: []string{"codex"}}, nil)
	if err := addRoutine(path, Job{Name: "brief", Project: "pip", Schedule: "@daily", Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if j, _ := mustLoad(t, path).Job("brief"); j.Harness != "codex" {
		t.Fatalf("harness %q", j.Harness)
	}
}

// Routines of an assistant that's gone, or is no longer one, are dropped
// at the next change, so they never run again or hold a name.
func TestRoutinesOfAGoneAssistantAreDropped(t *testing.T) {
	path := withAssistant(t)
	addRoutine(path, Job{Name: "brief", Project: "pip", Schedule: "@daily", Prompt: "x"})
	ps := appPaths(t, path)
	b, _ := os.ReadFile(ps.Projects)
	os.WriteFile(ps.Projects, []byte(strings.Replace(string(b), `"pip"`, `"gone"`, 1)), 0o600)
	if err := addRoutine(path, Job{Name: "other", Project: "gone", Schedule: "@daily", Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	f, _ := ReadAppFiles(ps)
	if len(f.Routines) != 1 || f.Routines[0].Name != "other" {
		t.Fatalf("routines %+v", f.Routines)
	}
}
