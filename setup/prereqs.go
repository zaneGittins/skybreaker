package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zaneGittins/skybreaker"
)

// Check is one prerequisite. One that isn't OK either has a Fix the
// installer applies first, or blocks the install.
type Check struct {
	Name   string
	OK     bool
	Detail string // the version found, or what to do
	Fix    *Fix
}

type Fix struct {
	Title string // what the installer will do, such as "Install gVisor"
	Step  string // shown while it runs
	Done  string
	Apply func(context.Context, *Host) error
}

// Blocked lists the checks that failed with nothing the installer can do.
func Blocked(checks []Check) []Check {
	var out []Check
	for _, c := range checks {
		if !c.OK && c.Fix == nil {
			out = append(out, c)
		}
	}
	return out
}

// Prereqs checks the host: systemd, Docker, nftables and gVisor registered
// with Docker. Missing gVisor and nftables are installed from apt where
// there is apt; everything else is the user's to install.
func (h *Host) Prereqs(ctx context.Context) []Check {
	first := func(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }
	_, aptErr := h.run(ctx, "apt-get", "--version")
	apt := aptErr == nil
	var checks []Check

	c := Check{Name: "systemd"}
	if out, err := h.run(ctx, "systemctl", "--version"); err == nil {
		c.OK, c.Detail = true, first(out)
	} else {
		c.Detail = "Skybreaker runs as a systemd service"
	}
	checks = append(checks, c)

	docker := Check{Name: "Docker"}
	if out, err := h.run(ctx, "docker", "version", "--format", "{{.Server.Version}}"); err == nil {
		docker.OK, docker.Detail = true, "Docker "+first(out)
	} else {
		docker.Detail = "install Docker Engine and start it: https://docs.docker.com/engine/install/"
	}
	checks = append(checks, docker)

	c = Check{Name: "nftables"}
	if out, err := h.run(ctx, "nft", "--version"); err == nil {
		c.OK, c.Detail = true, first(out)
	} else if apt {
		c.Detail, c.Fix = "not installed", &Fix{Title: "Install nftables", Step: "Installing nftables", Done: "Installed nftables",
			Apply: func(ctx context.Context, h *Host) error {
				_, err := h.run(ctx, "apt-get", "install", "-y", "nftables")
				return err
			}}
	} else {
		c.Detail = "install nftables; it pins sandboxes to the broker"
	}
	checks = append(checks, c)

	gvisor := Check{Name: "gVisor"}
	if out, err := h.run(ctx, "runsc", "--version"); err == nil {
		gvisor.OK, gvisor.Detail = true, first(out)
	} else if apt {
		gvisor.Detail, gvisor.Fix = "not installed", &Fix{Title: "Install gVisor from gvisor.dev's apt repository", Step: "Installing gVisor", Done: "Installed gVisor", Apply: installGVisor}
	} else {
		gvisor.Detail = "install gVisor: https://gvisor.dev/docs/user_guide/install/"
	}
	checks = append(checks, gvisor)

	if docker.OK && (gvisor.OK || gvisor.Fix != nil) {
		c = Check{Name: "gVisor in Docker"}
		if out, err := h.run(ctx, "docker", "info", "--format", "{{json .Runtimes}}"); err == nil && strings.Contains(out, `"runsc"`) {
			c.OK, c.Detail = true, "runsc is registered"
		} else {
			c.Detail, c.Fix = "runsc isn't registered", &Fix{Title: "Register gVisor with Docker (restarts Docker)", Step: "Registering gVisor with Docker", Done: "Registered gVisor with Docker", Apply: registerRunsc}
		}
		checks = append(checks, c)
	}
	return checks
}

// gvisor.dev/docs/user_guide/install, the apt repository.
const gvisorAptScript = `set -e
apt-get update
apt-get install -y ca-certificates curl gnupg
curl -fsSL https://gvisor.dev/archive.key | gpg --batch --yes --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" > /etc/apt/sources.list.d/gvisor.list
apt-get update
apt-get install -y runsc
`

func installGVisor(ctx context.Context, h *Host) error {
	_, err := h.run(ctx, "sh", "-c", gvisorAptScript)
	return err
}

// registerRunsc merges deploy/docker-daemon.json's runsc runtime into
// dockerd's config, keeping everything else there, and restarts Docker.
func registerRunsc(ctx context.Context, h *Host) error {
	path, err := h.run(ctx, "sh", "-c", "command -v runsc")
	if err != nil || path == "" {
		return errors.New("runsc is not on PATH")
	}
	tmpl, err := skybreaker.Files.ReadFile("deploy/docker-daemon.json")
	if err != nil {
		return err
	}
	var want struct {
		Runtimes map[string]map[string]any `json:"runtimes"`
	}
	if err := json.Unmarshal(tmpl, &want); err != nil {
		return err
	}
	runsc := want.Runtimes["runsc"]
	runsc["path"] = path

	cfg := map[string]any{}
	old, err := os.ReadFile(h.Docker)
	switch {
	case err == nil:
		if err := json.Unmarshal(old, &cfg); err != nil {
			return fmt.Errorf("%s isn't valid JSON (%v); add the runsc runtime from deploy/docker-daemon.json by hand", h.Docker, err)
		}
		if _, err := os.Stat(h.Docker + ".before-skybreaker"); os.IsNotExist(err) {
			if _, err := writeFile(h.Docker+".before-skybreaker", old, 0o644); err != nil {
				return err
			}
		}
	case !os.IsNotExist(err):
		return err
	}
	runtimes, _ := cfg["runtimes"].(map[string]any)
	if runtimes == nil {
		runtimes = map[string]any{}
	}
	runtimes["runsc"] = runsc
	cfg["runtimes"] = runtimes
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if _, err := writeFile(h.Docker, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if _, err := h.run(ctx, "systemctl", "restart", "docker"); err != nil {
		return err
	}
	if out, err := h.run(ctx, "docker", "info", "--format", "{{json .Runtimes}}"); err != nil || !strings.Contains(out, `"runsc"`) {
		return fmt.Errorf("Docker still doesn't list runsc after a restart: %v %s", err, out)
	}
	return nil
}
