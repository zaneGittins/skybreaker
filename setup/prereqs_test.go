package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checksByName(checks []Check) map[string]Check {
	m := map[string]Check{}
	for _, c := range checks {
		m[c.Name] = c
	}
	return m
}

func TestPrereqsReady(t *testing.T) {
	l := testLayout(t)
	f := newFakeRunner(l)
	f.out["docker version"] = "27.3.1\n"
	f.out["docker info"] = `{"runc":{"path":"runc"},"runsc":{"path":"/usr/bin/runsc"}}`
	f.out["runsc --version"] = "runsc version release-20260901.0\nspec: 1.1.0\n"
	h := &Host{Layout: l, Cmd: f}
	checks := h.Prereqs(context.Background())
	for _, c := range checks {
		if !c.OK {
			t.Errorf("%s: %s", c.Name, c.Detail)
		}
	}
	if d := checksByName(checks)["gVisor"].Detail; d != "runsc version release-20260901.0" {
		t.Errorf("gVisor detail = %q", d)
	}
}

func TestPrereqsWithoutDockerBlock(t *testing.T) {
	l := testLayout(t)
	f := newFakeRunner(l)
	f.fail["docker"] = errors.New("not found")
	f.fail["apt-get"] = errors.New("not found")
	f.fail["runsc"] = errors.New("not found")
	checks := (&Host{Layout: l, Cmd: f}).Prereqs(context.Background())
	var names []string
	for _, c := range Blocked(checks) {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "Docker,gVisor" {
		t.Errorf("blocked = %v", names)
	}
	if !strings.Contains(checksByName(checks)["gVisor"].Detail, "gvisor.dev") {
		t.Error("no pointer to gVisor's install guide")
	}
}

func TestPrereqsFixGVisorOnApt(t *testing.T) {
	l := testLayout(t)
	f := newFakeRunner(l)
	f.out["docker version"] = "27.3.1"
	f.out["docker info"] = `{"runc":{"path":"runc"}}`
	f.fail["runsc"] = errors.New("not found")
	f.fail["nft"] = errors.New("not found")
	checks := (&Host{Layout: l, Cmd: f}).Prereqs(context.Background())
	if b := Blocked(checks); len(b) != 0 {
		t.Fatalf("blocked: %v", b)
	}
	m := checksByName(checks)
	for _, n := range []string{"gVisor", "gVisor in Docker", "nftables"} {
		if m[n].OK || m[n].Fix == nil {
			t.Errorf("%s: %+v", n, m[n])
		}
	}
}

func TestRegisterRunscKeepsDockerSettings(t *testing.T) {
	l := testLayout(t)
	os.MkdirAll(l.Docker[:strings.LastIndex(l.Docker, "/")], 0o755)
	os.WriteFile(l.Docker, []byte(`{"log-driver": "journald", "runtimes": {"nvidia": {"path": "nvidia-container-runtime"}}}`), 0o644)
	f := newFakeRunner(l)
	f.out["sh -c command -v runsc"] = "/usr/local/bin/runsc\n"
	f.out["docker info"] = `{"runc":{},"runsc":{}}`
	if err := registerRunsc(context.Background(), &Host{Layout: l, Cmd: f}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		LogDriver string `json:"log-driver"`
		Runtimes  map[string]struct {
			Path        string   `json:"path"`
			RuntimeArgs []string `json:"runtimeArgs"`
		} `json:"runtimes"`
	}
	b, _ := os.ReadFile(l.Docker)
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.LogDriver != "journald" || got.Runtimes["nvidia"].Path == "" {
		t.Errorf("lost existing settings: %s", b)
	}
	if r := got.Runtimes["runsc"]; r.Path != "/usr/local/bin/runsc" || len(r.RuntimeArgs) != 2 {
		t.Errorf("runsc = %+v", r)
	}
	if backup, _ := os.ReadFile(l.Docker + ".before-skybreaker"); !strings.Contains(string(backup), "nvidia") {
		t.Error("no backup of the original daemon.json")
	}
	if !f.ran("systemctl restart docker") {
		t.Error("Docker not restarted")
	}
}

func TestRegisterRunscRefusesBrokenDaemonJSON(t *testing.T) {
	l := testLayout(t)
	os.MkdirAll(l.Docker[:strings.LastIndex(l.Docker, "/")], 0o755)
	os.WriteFile(l.Docker, []byte(`{"log-driver": `), 0o644)
	f := newFakeRunner(l)
	f.out["sh -c command -v runsc"] = "/usr/bin/runsc"
	if err := registerRunsc(context.Background(), &Host{Layout: l, Cmd: f}); err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(l.Docker); string(b) != `{"log-driver": ` || f.ran("systemctl restart docker") {
		t.Error("touched a daemon.json it couldn't read")
	}
}

func osRelease(t *testing.T, l *Layout, content string) {
	t.Helper()
	l.OSRelease = filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(l.OSRelease, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPrereqsFixDockerOnDebianAndUbuntu(t *testing.T) {
	for _, c := range []struct{ release, distro, codename string }{
		{"PRETTY_NAME=\"Ubuntu 24.04.3 LTS\"\nID=ubuntu\nID_LIKE=debian\nVERSION_CODENAME=noble\n", "ubuntu", "noble"},
		{"ID=debian\nVERSION_CODENAME='trixie'\n", "debian", "trixie"},
	} {
		l := testLayout(t)
		osRelease(t, &l, c.release)
		f := newFakeRunner(l)
		f.fail["docker"] = errors.New("not found")
		f.fail["runsc"] = errors.New("not found")
		h := &Host{Layout: l, Cmd: f}
		checks := h.Prereqs(context.Background())
		if b := Blocked(checks); len(b) != 0 {
			t.Fatalf("%s: blocked: %v", c.distro, b)
		}
		m := checksByName(checks)
		// Registering runsc needs Docker, which the fixes install first.
		for _, n := range []string{"Docker", "gVisor", "gVisor in Docker"} {
			if m[n].OK || m[n].Fix == nil {
				t.Errorf("%s: %s: %+v", c.distro, n, m[n])
			}
		}
		if err := m["Docker"].Fix.Apply(context.Background(), h); err != nil {
			t.Fatal(err)
		}
		if !f.ran("sh -c " + dockerAptScript + " sh " + c.distro + " " + c.codename) {
			t.Errorf("%s: calls:\n%s", c.distro, strings.Join(f.calls, "\n"))
		}
	}
}

func TestPrereqsDockerElsewhereBlocks(t *testing.T) {
	for _, release := range []string{
		"ID=fedora\nVERSION_CODENAME=\n",
		"ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_CODENAME=wilma\n",
		"ID=ubuntu\nVERSION_CODENAME=\"noble; rm -rf /\"\n",
		"ID=debian\n",
	} {
		l := testLayout(t)
		osRelease(t, &l, release)
		f := newFakeRunner(l)
		f.fail["docker"] = errors.New("not found")
		c := checksByName((&Host{Layout: l, Cmd: f}).Prereqs(context.Background()))["Docker"]
		if c.Fix != nil || !strings.Contains(c.Detail, "docs.docker.com") {
			t.Errorf("%q: %+v", release, c)
		}
	}
}

func TestPrereqsDockerNotRunning(t *testing.T) {
	l := testLayout(t)
	osRelease(t, &l, "ID=ubuntu\nVERSION_CODENAME=noble\n")
	f := newFakeRunner(l)
	f.fail["docker version"] = errors.New("Cannot connect to the Docker daemon")
	f.out["docker --version"] = "Docker version 28.4.0"
	c := checksByName((&Host{Layout: l, Cmd: f}).Prereqs(context.Background()))["Docker"]
	if c.OK || c.Fix != nil || !strings.Contains(c.Detail, "not running") {
		t.Errorf("%+v", c)
	}
}
