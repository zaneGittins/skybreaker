package setup

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
)

// SelfUpdate replaces this installer with the latest release's when that is
// newer, then runs it in this process's place with the same arguments plus
// --skip-self-update. It returns only if there was nothing to do or the
// update failed, which is a warning, not an error.
func SelfUpdate(ctx context.Context, r Releases, arch string, out *Progress, args []string) {
	if Version == "dev" {
		return
	}
	rel, err := r.Get(ctx, Channel(Version))
	if err != nil {
		out.Warn("Couldn't check for a newer skybreaker-setup: " + err.Error())
		return
	}
	if CompareVersions(Version, rel.Tag) >= 0 {
		return
	}
	out.Step("Updating skybreaker-setup to " + rel.Tag)
	bin, err := r.Asset(ctx, rel, "skybreaker-setup-linux-"+arch)
	if err != nil {
		out.Warn("Couldn't update skybreaker-setup: " + err.Error())
		return
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err == nil {
		_, err = writeFile(exe, bin, 0o755)
	}
	if err != nil {
		out.Warn("Couldn't update skybreaker-setup: " + err.Error())
		return
	}
	out.Done("Updated skybreaker-setup to " + rel.Tag)
	err = syscall.Exec(exe, append(append([]string{exe}, args...), "--skip-self-update"), os.Environ())
	out.Warn("Couldn't run the new skybreaker-setup: " + err.Error())
}
