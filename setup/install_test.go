package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker"
)

// testHost is a host with Docker, gVisor and systemd, and a release
// v1.1.0 (plus v1.0.0) whose binary is the given text.
func testHost(t *testing.T, binaries map[string]string) (*Host, *fakeRunner, *strings.Builder) {
	t.Helper()
	l := testLayout(t)
	f := newFakeRunner(l)
	releases := map[string]map[string][]byte{}
	for tag, bin := range binaries {
		releases[tag] = withChecksums(map[string][]byte{"skybreaker-linux-amd64": []byte(bin)})
	}
	gh := newFakeGitHub(t, "", releases)
	var out strings.Builder
	h := &Host{
		Layout: l, Cmd: f, Releases: gh.releasesClient(), Out: &Progress{W: &out},
		Arch: "amd64", Version: "dev", Sleep: func(time.Duration) {}, Settle: time.Second,
	}
	return h, f, &out
}

func readyChecks() []Check {
	return []Check{{Name: "Docker", OK: true}, {Name: "gVisor", OK: true}}
}

func TestInstall(t *testing.T) {
	h, f, out := testHost(t, map[string]string{"v1.1.0": "v1.1.0"})
	f.fail["id -u skybreaker"] = errors.New("no such user")
	a := testAnswers(t)
	if err := h.Install(context.Background(), &a, readyChecks()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if b, _ := os.ReadFile(h.Binary()); string(b) != "v1.1.0" {
		t.Errorf("binary = %q", b)
	}
	if fi, _ := os.Stat(h.Binary()); fi.Mode().Perm() != 0o755 {
		t.Errorf("binary mode %v", fi.Mode())
	}
	for _, p := range []string{
		filepath.Join(h.Units, "skybreaker.service"), filepath.Join(h.Units, "skybreaker-nftables.service"),
		filepath.Join(h.Units, "skybreaker-sandbox-update.timer"), filepath.Join(h.Image, "Dockerfile"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
	if fi, err := os.Stat(filepath.Join(h.Bin, "skybreaker-sandbox-update")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("sandbox-update script: %v", err)
	}
	if fi, _ := os.Stat(h.Etc); fi.Mode().Perm() != 0o700 {
		t.Errorf("%s mode %v", h.Etc, fi.Mode())
	}
	// In order: the user before files it owns, the config checked before
	// anything starts, services last.
	order := []string{
		"useradd --system", "chown -R skybreaker:skybreaker " + h.Etc, h.Binary() + " check -config " + h.Config(),
		"systemctl daemon-reload", "docker pull " + DefaultImage,
		"systemctl enable --now skybreaker-nftables.service skybreaker.service skybreaker-sandbox-update.timer",
		"systemctl is-active skybreaker",
	}
	last := -1
	for _, c := range order {
		i := f.index(c)
		if i <= last {
			t.Fatalf("%q at %d, after %d; calls:\n%s", c, i, last, strings.Join(f.calls, "\n"))
		}
		last = i
	}
	if f.ran("docker build") {
		t.Error("built the image though the pull worked")
	}
}

func TestInstallBuildsImageWhenPullFails(t *testing.T) {
	h, f, out := testHost(t, map[string]string{"v1.1.0": "v1.1.0"})
	f.fail["docker pull"] = errors.New("denied")
	a := testAnswers(t)
	if err := h.Install(context.Background(), &a, readyChecks()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !f.ran("docker build -t " + DefaultImage + " " + h.Image) {
		t.Errorf("no local build; calls:\n%s", strings.Join(f.calls, "\n"))
	}
}

func TestInstallStopsOnRejectedConfig(t *testing.T) {
	h, f, _ := testHost(t, map[string]string{"v1.1.0": "v1.1.0 rejects"})
	a := testAnswers(t)
	err := h.Install(context.Background(), &a, readyChecks())
	if err == nil || !strings.Contains(err.Error(), "orbit_board") {
		t.Fatalf("err = %v", err)
	}
	if f.ran("systemctl enable") {
		t.Error("started with a config the binary rejects")
	}
}

func TestInstallReportsCrashLoop(t *testing.T) {
	h, f, _ := testHost(t, map[string]string{"v1.1.0": "v1.1.0 crash"})
	f.out["journalctl -u skybreaker"] = "tailnet: invalid auth key\n"
	a := testAnswers(t)
	err := h.Install(context.Background(), &a, readyChecks())
	if err == nil || !strings.Contains(err.Error(), "invalid auth key") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallRefusesBlockedHost(t *testing.T) {
	h, f, _ := testHost(t, map[string]string{"v1.1.0": "v1.1.0"})
	a := testAnswers(t)
	err := h.Install(context.Background(), &a, []Check{{Name: "Docker", Detail: "install Docker"}})
	if err == nil || !strings.Contains(err.Error(), "Docker") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("ran %v on a blocked host", f.calls)
	}
}

func TestInstallAppliesFixesFirst(t *testing.T) {
	h, f, out := testHost(t, map[string]string{"v1.1.0": "v1.1.0"})
	applied := false
	checks := append(readyChecks(), Check{Name: "gVisor in Docker", Fix: &Fix{Step: "Registering", Done: "Registered",
		Apply: func(context.Context, *Host) error { applied = len(f.calls) == 0; return nil }}})
	a := testAnswers(t)
	if err := h.Install(context.Background(), &a, checks); err != nil {
		t.Fatal(err)
	}
	if !applied || !strings.Contains(out.String(), "Registered") {
		t.Errorf("fix not applied first:\n%s", out)
	}
}

// Installing over an existing config keeps it: a second run finishes a
// failed install or repairs one.
func TestInstallKeepsExistingConfig(t *testing.T) {
	h, _, out := testHost(t, map[string]string{"v1.1.0": "v1.1.0"})
	a := testAnswers(t)
	writeConfig(t, a, h.Layout)
	before, _ := os.ReadFile(h.Config())
	if err := h.Install(context.Background(), nil, readyChecks()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if after, _ := os.ReadFile(h.Config()); string(after) != string(before) {
		t.Error("config rewritten")
	}
	if err := h.Install(context.Background(), &a, readyChecks()); err == nil {
		t.Error("overwrote an existing config with new answers")
	}
}

// A repair replaces the binary under a running service: it restarts it,
// or the old process goes on answering the check.
func TestRepairRestarts(t *testing.T) {
	h, f, out := testHost(t, map[string]string{"v1.1.0": "v1.1.0"})
	writeConfig(t, testAnswers(t), h.Layout)
	if err := h.Install(context.Background(), nil, readyChecks()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !f.ran("systemctl restart skybreaker-nftables.service skybreaker.service") {
		t.Fatal("the running service wasn't restarted")
	}
}

func installed(t *testing.T, binaries map[string]string, current string) (*Host, *fakeRunner, *strings.Builder) {
	t.Helper()
	h, f, out := testHost(t, binaries)
	writeConfig(t, testAnswers(t), h.Layout)
	if _, err := writeFile(h.Binary(), []byte(current), 0o755); err != nil {
		t.Fatal(err)
	}
	return h, f, out
}

func TestUpgrade(t *testing.T) {
	h, f, out := installed(t, map[string]string{"v1.0.0": "v1.0.0", "v1.1.0": "v1.1.0"}, "v1.0.0")
	var asked [2]string
	err := h.Upgrade(context.Background(), func(from, to string) bool { asked = [2]string{from, to}; return true })
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if asked != [2]string{"v1.0.0", "v1.1.0"} {
		t.Errorf("asked %v", asked)
	}
	if b, _ := os.ReadFile(h.Binary()); string(b) != "v1.1.0" {
		t.Errorf("binary = %q", b)
	}
	if b, _ := os.ReadFile(h.Binary() + ".prev"); string(b) != "v1.0.0" {
		t.Errorf("previous binary = %q", b)
	}
	if !f.ran("systemctl restart skybreaker-nftables.service skybreaker.service") {
		t.Errorf("not restarted; calls:\n%s", strings.Join(f.calls, "\n"))
	}
	if _, err := os.Stat(filepath.Join(h.Units, "skybreaker.service")); err != nil {
		t.Error("units not refreshed")
	}
}

func TestUpgradeUpToDate(t *testing.T) {
	h, f, out := installed(t, map[string]string{"v1.1.0": "v1.1.0"}, "v1.1.0")
	if err := h.Upgrade(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if f.ran("systemctl restart") || !strings.Contains(out.String(), "up to date") {
		t.Errorf("restarted an up-to-date install:\n%s", out)
	}
}

func TestUpgradeDeclined(t *testing.T) {
	h, f, _ := installed(t, map[string]string{"v1.1.0": "v1.1.0"}, "v1.0.0")
	if err := h.Upgrade(context.Background(), func(string, string) bool { return false }); !errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(h.Binary()); string(b) != "v1.0.0" || f.ran("systemctl") {
		t.Error("changed the host after a no")
	}
	if _, err := os.Stat(h.Binary() + ".new"); err == nil {
		t.Error("left the download behind")
	}
}

// A release that rejects the config is caught before anything changes.
func TestUpgradeRejectedConfigChangesNothing(t *testing.T) {
	h, f, _ := installed(t, map[string]string{"v1.1.0": "v1.1.0 rejects"}, "v1.0.0")
	err := h.Upgrade(context.Background(), func(string, string) bool { return true })
	if err == nil || !strings.Contains(err.Error(), "orbit_board") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(h.Binary()); string(b) != "v1.0.0" || f.ran("systemctl restart") {
		t.Error("changed the host")
	}
	if _, err := os.Stat(h.Binary() + ".new"); err == nil {
		t.Error("left the download behind")
	}
}

func TestUpgradeRollsBackACrash(t *testing.T) {
	h, f, out := installed(t, map[string]string{"v1.1.0": "v1.1.0 crash"}, "v1.0.0")
	f.out["journalctl -u skybreaker"] = "panic: boom\n"
	err := h.Upgrade(context.Background(), func(string, string) bool { return true })
	if err == nil || !strings.Contains(err.Error(), "panic: boom") || !strings.Contains(err.Error(), "rolled back to v1.0.0") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(h.Binary()); string(b) != "v1.0.0" {
		t.Errorf("binary = %q", b)
	}
	if n := f.count("systemctl restart skybreaker-nftables.service skybreaker.service"); n != 2 {
		t.Errorf("%d restarts", n)
	}
}

// What a failed upgrade changed besides the binary (units, the image's
// build context) goes back too: the old binary may not run under the new.
func TestUpgradeRollbackRestoresServices(t *testing.T) {
	h, _, out := installed(t, map[string]string{"v1.1.0": "v1.1.0 crash"}, "v1.0.0")
	unit := filepath.Join(h.Units, "skybreaker.service")
	os.MkdirAll(h.Units, 0o755)
	os.WriteFile(unit, []byte("old unit\n"), 0o644)
	extra := filepath.Join(h.Image, "old-only.sh")
	os.MkdirAll(h.Image, 0o755)
	os.WriteFile(extra, []byte("echo old\n"), 0o755)
	if err := h.Upgrade(context.Background(), func(string, string) bool { return true }); err == nil {
		t.Fatalf("the crash went unnoticed\n%s", out)
	}
	if b, _ := os.ReadFile(unit); string(b) != "old unit\n" {
		t.Errorf("unit after rollback: %q", b)
	}
	if b, _ := os.ReadFile(extra); string(b) != "echo old\n" {
		t.Errorf("image file removed by the failed upgrade: %q", b)
	}
}

func TestRollback(t *testing.T) {
	h, f, out := installed(t, map[string]string{"v1.1.0": "v1.1.0"}, "v1.1.0")
	if err := h.Rollback(context.Background()); err == nil {
		t.Error("rolled back with nothing to roll back to")
	}
	writeFile(h.Binary()+".prev", []byte("v1.0.0"), 0o755)
	if err := h.Rollback(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cur, _ := os.ReadFile(h.Binary())
	prev, _ := os.ReadFile(h.Binary() + ".prev")
	if string(cur) != "v1.0.0" || string(prev) != "v1.1.0" {
		t.Errorf("current %q, previous %q", cur, prev)
	}
	if !f.ran("systemctl restart skybreaker-nftables.service skybreaker.service") {
		t.Error("not restarted")
	}
}

func TestSyncServicesMatchesRepository(t *testing.T) {
	h, _, _ := testHost(t, nil)
	n, err := h.syncServices(nil)
	if err != nil || n == 0 {
		t.Fatalf("%d, %v", n, err)
	}
	want, _ := skybreaker.Files.ReadFile("images/sandbox/Dockerfile")
	if got, _ := os.ReadFile(filepath.Join(h.Image, "Dockerfile")); string(got) != string(want) {
		t.Error("Dockerfile differs")
	}
	os.WriteFile(filepath.Join(h.Image, "stale.sh"), []byte("x"), 0o644)
	if n, err := h.syncServices(nil); err != nil || n != 1 {
		t.Errorf("second sync changed %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(h.Image, "stale.sh")); err == nil {
		t.Error("a file the image context no longer has was kept")
	}
}

func TestInstalledVersion(t *testing.T) {
	h, _, _ := installed(t, map[string]string{"v1.0.0": "v1.0.0"}, "v1.0.0")
	if v := h.InstalledVersion(context.Background()); v != "v1.0.0" {
		t.Errorf("version %q", v)
	}
	os.Remove(h.Binary())
	if v := h.InstalledVersion(context.Background()); v != "" {
		t.Errorf("version %q with no binary", v)
	}
}

// A binary built by hand (make build) isn't a release tag; any release
// replaces it.
func TestUpgradeReplacesHandBuild(t *testing.T) {
	h, f, out := installed(t, map[string]string{"v0.0.1-dev.1": "v0.0.1-dev.1"}, "0.0.1")
	if err := h.Upgrade(context.Background(), func(string, string) bool { return true }); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if b, _ := os.ReadFile(h.Binary()); string(b) != "v0.0.1-dev.1" || !f.ran("systemctl restart") {
		t.Errorf("binary = %q\n%s", b, out)
	}
}

// tailnetJournal makes the running service log a sign-in link, and come up
// once the installer has waited for it.
func tailnetJournal(h *Host, f *fakeRunner, approve bool) {
	f.out["systemctl show -p InvocationID"] = "inv1\n"
	f.out["journalctl _SYSTEMD_INVOCATION_ID=inv1"] = "2026/10/21 10:00:00 To start this tsnet server, restart with TS_AUTHKEY set, or go to: https://login.tailscale.com/a/abc123\n"
	h.Sleep = func(time.Duration) {
		if approve && f.ran("journalctl") {
			f.mu.Lock()
			f.out["journalctl _SYSTEMD_INVOCATION_ID=inv1"] += "2026/10/21 10:00:09 skybreaker v1.1.0: UI on 100.64.0.1:443, hooks on 10.0.0.1:8080, broker on 10.0.0.1:3128, 1 projects, 0 jobs\n"
			f.mu.Unlock()
		}
	}
}

func TestInstallWithoutAuthKeyShowsSignInLink(t *testing.T) {
	h, f, out := testHost(t, map[string]string{"v1.1.0": "v1.1.0"})
	tailnetJournal(h, f, true)
	a := testAnswers(t)
	a.AuthKey = ""
	if err := h.Install(context.Background(), &a, readyChecks()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"https://login.tailscale.com/a/abc123", "Approved: Skybreaker is on your tailnet"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if strings.Count(out.String(), "login.tailscale.com") != 1 {
		t.Errorf("the link is shown more than once:\n%s", out)
	}
}

func TestInstallWithoutAuthKeyGivesUp(t *testing.T) {
	h, f, _ := testHost(t, map[string]string{"v1.1.0": "v1.1.0"})
	tailnetJournal(h, f, false)
	a := testAnswers(t)
	a.AuthKey = ""
	err := h.Install(context.Background(), &a, readyChecks())
	if err == nil || !strings.Contains(err.Error(), "skybreaker-setup status") {
		t.Fatalf("err = %v", err)
	}
	var status strings.Builder
	h.Status(context.Background(), &status)
	if !strings.Contains(status.String(), "https://login.tailscale.com/a/abc123") {
		t.Errorf("status doesn't show the link:\n%s", status.String())
	}
}

func TestInstallWithAuthKeyDoesntWait(t *testing.T) {
	h, _, out := testHost(t, map[string]string{"v1.1.0": "v1.1.0"})
	a := testAnswers(t)
	if err := h.Install(context.Background(), &a, readyChecks()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out.String(), "sign-in link") {
		t.Errorf("waited for a sign-in link with an auth key:\n%s", out)
	}
}

func TestInstallWarnsWhenTheNameIsTaken(t *testing.T) {
	// Tailscale gives a second machine asking for a taken name one of its
	// own: the install says where Skybreaker really is, and how to match.
	h, f, out := testHost(t, map[string]string{"v1.1.0": "v1.1.0"})
	f.out["systemctl show -p InvocationID"] = "inv1\n"
	f.out["journalctl _SYSTEMD_INVOCATION_ID=inv1"] = "2026/10/21 10:00:00 skybreaker: on your tailnet at https://skybreaker-1.tail1234.ts.net\n" +
		"2026/10/21 10:00:01 skybreaker v1.1.0: UI on 100.64.0.1:443, hooks on 10.0.0.1:8080, broker on 10.0.0.1:3128, 1 projects, 0 jobs\n"
	a := testAnswers(t)
	if err := h.Install(context.Background(), &a, readyChecks()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"already called skybreaker", "https://skybreaker-1.tail1234.ts.net", "--hostname"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	var status strings.Builder
	h.Status(context.Background(), &status)
	if !strings.Contains(status.String(), "https://skybreaker-1.tail1234.ts.net") {
		t.Errorf("status shows the config's address, not the real one:\n%s", status.String())
	}

	// Its own name: nothing to say.
	h, f, out = testHost(t, map[string]string{"v1.1.0": "v1.1.0"})
	f.out["systemctl show -p InvocationID"] = "inv1\n"
	f.out["journalctl _SYSTEMD_INVOCATION_ID=inv1"] = "2026/10/21 10:00:00 skybreaker: on your tailnet at https://skybreaker.tail1234.ts.net\n"
	a = testAnswers(t)
	if err := h.Install(context.Background(), &a, readyChecks()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out.String(), "already called") {
		t.Errorf("warned about its own name:\n%s", out)
	}
}
