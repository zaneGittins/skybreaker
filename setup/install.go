package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zaneGittins/skybreaker"
)

// ErrCancelled is returned when the user says no.
var ErrCancelled = errors.New("cancelled")

var units = []string{
	"skybreaker-nftables.service", "skybreaker.service",
	"skybreaker-sandbox-update.service", "skybreaker-sandbox-update.timer",
}

// Install sets Skybreaker up: fixes, the binary, its user, config, units,
// the sandbox image, and the services started and seen to stay up. With a
// nil a it keeps the config already in place, so running it again finishes
// a failed install or repairs one.
func (h *Host) Install(ctx context.Context, a *Answers, checks []Check) error {
	if b := Blocked(checks); len(b) > 0 {
		var s []string
		for _, c := range b {
			s = append(s, c.Name+": "+c.Detail)
		}
		return fmt.Errorf("this host isn't ready:\n  %s", strings.Join(s, "\n  "))
	}
	switch {
	case a != nil && h.Installed():
		return fmt.Errorf("%s exists; run skybreaker-setup install again without answers to keep it, or upgrade", h.Config())
	case a == nil && !h.Installed():
		return errors.New("no config to install with")
	}
	var fixes []*Fix
	for _, c := range checks {
		if !c.OK && c.Fix != nil {
			fixes = append(fixes, c.Fix)
		}
	}
	h.Out.Steps(len(fixes) + 7)
	for _, f := range fixes {
		h.Out.Step(f.Step)
		if err := f.Apply(ctx, h); err != nil {
			return h.fail(f.Step, err)
		}
		h.Out.Done(f.Done)
	}

	rel, err := h.download(ctx, h.Binary())
	if err != nil {
		return err
	}

	h.Out.Step("Creating the skybreaker user")
	if _, err := h.run(ctx, "id", "-u", "skybreaker"); err == nil {
		// Docker's group is what lets it run sandboxes.
		if _, err := h.run(ctx, "usermod", "-aG", "docker", "skybreaker"); err != nil {
			return h.fail("Creating the skybreaker user", err)
		}
		h.Out.Done("The skybreaker user exists")
	} else {
		if _, err := h.run(ctx, "useradd", "--system", "--home-dir", h.Data, "--no-create-home",
			"--shell", "/usr/sbin/nologin", "--groups", "docker", "skybreaker"); err != nil {
			return h.fail("Creating the skybreaker user", err)
		}
		h.Out.Done("Created the skybreaker user")
	}

	h.Out.Step("Writing the config")
	if a != nil {
		if err := h.writeConfig(ctx, a); err != nil {
			return h.fail("Writing the config", err)
		}
		h.Out.Done("Wrote " + h.Config())
	} else {
		h.Out.Done("Kept " + h.Config())
	}

	h.Out.Step("Checking the config")
	if _, err := h.run(ctx, h.Binary(), "check", "-config", h.Config()); err != nil {
		return h.fail("Checking the config", err)
	}
	h.Out.Done("The config and secrets check out")

	h.Out.Step("Installing services")
	if _, err := h.syncServices(); err != nil {
		return h.fail("Installing services", err)
	}
	if _, err := h.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return h.fail("Installing services", err)
	}
	h.Out.Done("Installed the services and the sandbox image's build context")

	if err := h.sandboxImage(ctx); err != nil {
		return err
	}

	h.Out.Step("Starting Skybreaker")
	if _, err := h.run(ctx, "systemctl", "enable", "--now", units[0], units[1], units[3]); err != nil {
		return h.fail("Starting Skybreaker", err)
	}
	if err := h.verify(ctx); err != nil {
		return h.fail("Starting Skybreaker", err)
	}
	h.Out.Done("Skybreaker " + rel + " is running")
	return nil
}

func (h *Host) fail(step string, err error) error {
	h.Out.Fail(step + " failed")
	return err
}

// download fetches the release's binary to path.
func (h *Host) download(ctx context.Context, path string) (string, error) {
	h.Out.Step("Downloading skybreaker")
	rel, err := h.Releases.Get(ctx, h.Version)
	if err != nil {
		return "", h.fail("Downloading skybreaker", err)
	}
	bin, err := h.Releases.Asset(ctx, rel, "skybreaker-linux-"+h.Arch)
	if err != nil {
		return "", h.fail("Downloading skybreaker", err)
	}
	if _, err := writeFile(path, bin, 0o755); err != nil {
		return "", h.fail("Downloading skybreaker", err)
	}
	h.Out.Done("Downloaded skybreaker " + rel.Tag)
	return rel.Tag, nil
}

func (h *Host) writeConfig(ctx context.Context, a *Answers) error {
	files, err := a.Files(h.Layout)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(h.Etc, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(h.Etc, 0o700); err != nil {
		return err
	}
	for _, f := range files {
		if _, err := writeFile(f.Path, f.Data, f.Mode); err != nil {
			return err
		}
	}
	_, err = h.run(ctx, "chown", "-R", "skybreaker:skybreaker", h.Etc)
	return err
}

// syncServices installs the units, the weekly image rebuild and the image's
// build context from this release, and reports how many files changed.
func (h *Host) syncServices() (int, error) {
	changed := 0
	put := func(src, dst string, mode os.FileMode) error {
		b, err := skybreaker.Files.ReadFile(src)
		if err != nil {
			return err
		}
		c, err := writeFile(dst, b, mode)
		if c {
			changed++
		}
		return err
	}
	for _, u := range units {
		if err := put("deploy/"+u, filepath.Join(h.Units, u), 0o644); err != nil {
			return changed, err
		}
	}
	if err := put("deploy/skybreaker-sandbox-update", filepath.Join(h.Bin, "skybreaker-sandbox-update"), 0o755); err != nil {
		return changed, err
	}
	ctxFS, err := fs.Sub(skybreaker.Files, "images/sandbox")
	if err != nil {
		return changed, err
	}
	want := map[string]bool{}
	err = fs.WalkDir(ctxFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		want[filepath.Join(h.Image, p)] = true
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		return put("images/sandbox/"+p, filepath.Join(h.Image, p), mode)
	})
	if err != nil {
		return changed, err
	}
	err = filepath.WalkDir(h.Image, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || want[p] {
			return err
		}
		changed++
		return os.Remove(p)
	})
	return changed, err
}

// sandboxImage pulls the configured image, or builds it here when it can't
// be pulled (a private registry, say). The weekly timer rebuilds it either
// way.
func (h *Host) sandboxImage(ctx context.Context) error {
	image := configuredImage(h.Config())
	if image == "" {
		image = DefaultImage
	}
	h.Out.Step("Pulling the sandbox image")
	if _, err := h.run(ctx, "docker", "pull", image); err == nil {
		h.Out.Done("Pulled " + image)
		return nil
	}
	h.Out.Warn("Couldn't pull " + image + "; building it here instead")
	h.Out.Step("Building the sandbox image (several minutes)")
	if _, err := h.run(ctx, "docker", "build", "-t", image, h.Image); err != nil {
		return h.fail("Building the sandbox image", err)
	}
	h.Out.Done("Built " + image)
	return nil
}

var imageRe = regexp.MustCompile(`(?m)^\s*image:\s*"?([^"\s]*)"?\s*$`)

// configuredImage reads sandbox.image the way skybreaker-sandbox-update
// does.
func configuredImage(config string) string {
	b, err := os.ReadFile(config)
	if err != nil {
		return ""
	}
	if m := imageRe.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// verify waits for skybreaker to settle and fails if it isn't running or
// systemd has restarted it meanwhile, with the end of its log.
func (h *Host) verify(ctx context.Context) error {
	restarts := func() string {
		n, _ := h.run(ctx, "systemctl", "show", "-p", "NRestarts", "--value", "skybreaker")
		return n
	}
	before := restarts()
	h.Sleep(h.Settle)
	state, _ := h.run(ctx, "systemctl", "is-active", "skybreaker")
	if after := restarts(); state == "active" && after == before {
		return nil
	}
	log, _ := h.run(ctx, "journalctl", "-u", "skybreaker", "-n", "20", "--no-pager", "-o", "cat")
	return fmt.Errorf("skybreaker isn't staying up (%s). Its log ends:\n%s", state, log)
}

// InstalledVersion asks the installed binary, or returns "".
func (h *Host) InstalledVersion(ctx context.Context) string {
	out, err := h.run(ctx, h.Binary(), "version")
	if err != nil {
		return ""
	}
	f := strings.Fields(out)
	if len(f) < 2 {
		return ""
	}
	return f[1]
}

// Upgrade installs the release's binary and units, after the new binary
// has checked the config and confirm has said yes. The previous binary is
// kept as skybreaker.prev, and put back if the new one doesn't stay up.
func (h *Host) Upgrade(ctx context.Context, confirm func(from, to string) bool) error {
	if !h.Installed() {
		return errors.New("Skybreaker isn't installed here; run skybreaker-setup install")
	}
	h.Out.Steps(5)
	h.Out.Step("Checking for a new release")
	cur := h.InstalledVersion(ctx)
	rel, err := h.Releases.Get(ctx, h.Version)
	if err != nil {
		return h.fail("Checking for a new release", err)
	}
	// A version without a v is a hand build, which any release replaces.
	if strings.HasPrefix(cur, "v") && CompareVersions(cur, rel.Tag) >= 0 {
		h.Out.Done("skybreaker " + cur + " is up to date")
		return nil
	}
	h.Out.Done(fmt.Sprintf("skybreaker %s → %s", orNone(cur), rel.Tag))

	next := h.Binary() + ".new"
	defer os.Remove(next)
	if _, err := h.download(ctx, next); err != nil {
		return err
	}
	h.Out.Step("Checking your config against " + rel.Tag)
	if _, err := h.run(ctx, next, "check", "-config", h.Config()); err != nil {
		h.Out.Fail("skybreaker " + rel.Tag + " rejects your config")
		return fmt.Errorf("%w\nnothing was changed; update %s for %s (see its release notes) and run upgrade again", err, h.Config(), rel.Tag)
	}
	h.Out.Done("Your config checks out with " + rel.Tag)

	if confirm != nil && !confirm(orNone(cur), rel.Tag) {
		return ErrCancelled
	}

	h.Out.Step("Updating services")
	n, err := h.syncServices()
	if err == nil {
		_, err = h.run(ctx, "systemctl", "daemon-reload")
	}
	if err != nil {
		return h.fail("Updating services", err)
	}
	h.Out.Done(fmt.Sprintf("Updated services (%d files changed)", n))

	h.Out.Step("Restarting Skybreaker")
	prev := h.Binary() + ".prev"
	if err := os.Rename(h.Binary(), prev); err != nil && !os.IsNotExist(err) {
		return h.fail("Restarting Skybreaker", err)
	}
	if err := os.Rename(next, h.Binary()); err != nil {
		os.Rename(prev, h.Binary())
		return h.fail("Restarting Skybreaker", err)
	}
	if err := h.restart(ctx); err != nil {
		h.Out.Fail(rel.Tag + " didn't stay up; rolling back to " + orNone(cur))
		if rerr := os.Rename(prev, h.Binary()); rerr != nil {
			return fmt.Errorf("%w\nand rolling back failed: %v", err, rerr)
		}
		if rerr := h.restart(ctx); rerr != nil {
			return fmt.Errorf("%w\nand %s didn't come back either: %v", err, orNone(cur), rerr)
		}
		return fmt.Errorf("rolled back to %s: %w", orNone(cur), err)
	}
	h.Out.Done("Skybreaker " + rel.Tag + " is running")
	return nil
}

// Rollback swaps the previous binary back in.
func (h *Host) Rollback(ctx context.Context) error {
	prev := h.Binary() + ".prev"
	if _, err := os.Stat(prev); err != nil {
		return errors.New("no previous version to roll back to")
	}
	h.Out.Steps(2)
	h.Out.Step("Checking the config against the previous version")
	if _, err := h.run(ctx, prev, "check", "-config", h.Config()); err != nil {
		return h.fail("Checking the config", err)
	}
	h.Out.Done("Your config checks out")
	h.Out.Step("Rolling back")
	swap := h.Binary() + ".swap"
	for _, mv := range [][2]string{{h.Binary(), swap}, {prev, h.Binary()}, {swap, prev}} {
		if err := os.Rename(mv[0], mv[1]); err != nil {
			return h.fail("Rolling back", err)
		}
	}
	if err := h.restart(ctx); err != nil {
		return h.fail("Rolling back", err)
	}
	h.Out.Done("Rolled back to " + orNone(h.InstalledVersion(ctx)))
	return nil
}

// restart restarts the firewall (a release may change its rules) and
// skybreaker, and waits to see it stay up.
func (h *Host) restart(ctx context.Context) error {
	if _, err := h.run(ctx, "systemctl", "restart", units[0], units[1]); err != nil {
		return err
	}
	return h.verify(ctx)
}

// Status prints the installed version, the services' states and the URL.
func (h *Host) Status(ctx context.Context, w io.Writer) {
	fmt.Fprintf(w, "%s %s\n", LabelStyle.Render("skybreaker"), orNone(h.InstalledVersion(ctx)))
	for _, u := range []string{units[0], units[1], units[3]} {
		state, _ := h.run(ctx, "systemctl", "is-active", u)
		style := OKStyle
		if state != "active" {
			style = ErrorStyle
		}
		fmt.Fprintf(w, "  %-34s %s\n", u, style.Render(orNone(state)))
	}
	if u := configuredURL(h.Config()); u != "" {
		fmt.Fprintf(w, "\n  %s\n", AccentStyle.Render(u))
	}
}

var urlRe = regexp.MustCompile(`(?m)^ui_url:\s*"?([^"\s]*)"?\s*$`)

func configuredURL(config string) string {
	b, err := os.ReadFile(config)
	if err != nil {
		return ""
	}
	if m := urlRe.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
