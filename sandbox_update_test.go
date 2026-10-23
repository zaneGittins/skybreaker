package skybreaker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runSandboxUpdate runs deploy/skybreaker-sandbox-update with a fake docker
// that logs each call and fails the subcommand named in fail, and returns
// the log.
func runSandboxUpdate(t *testing.T, fail string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	fake := `#!/bin/sh
echo "$*" >> "` + log + `"
case "$1" in
  ` + fail + `) exit 1 ;;
  image) [ "$2" = inspect ] && echo "sha256:$(grep -c '^tag ' "` + log + `")" ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "deploy/skybreaker-sandbox-update", filepath.Join(dir, "ctx"), "skybreaker-sandbox:0.0.1")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	b, _ := os.ReadFile(log)
	t.Logf("output:\n%s\ndocker calls:\n%s", out, b)
	return string(b), err
}

// The rebuild runs weekly on the host's root disk. Its build cache is never
// reused (--no-cache), images a sandbox has moved off go dangling, and a
// failed build leaves its candidate behind, so each is cleared or the disk
// fills until the build itself fails for space.
func TestSandboxUpdateLeavesNothingBehind(t *testing.T) {
	for _, fail := range []string{"none", "build", "run"} {
		t.Run("fail "+fail, func(t *testing.T) {
			calls, err := runSandboxUpdate(t, fail)
			if (err != nil) != (fail != "none") {
				t.Fatalf("err = %v", err)
			}
			lines := strings.Split(strings.TrimSpace(calls), "\n")
			first := func(prefix string) int {
				for i, l := range lines {
					if strings.HasPrefix(l, prefix) {
						return i
					}
				}
				return -1
			}
			build := first("build ")
			if p := first("image prune"); p < 0 || p > build {
				t.Error("dangling images aren't pruned before the build")
			}
			if p := first("builder prune"); p < 0 || p > build {
				t.Error("build cache isn't pruned before the build")
			}
			last := lines[len(lines)-2:]
			if !strings.HasPrefix(last[0], "rmi skybreaker-sandbox:candidate") || !strings.HasPrefix(last[1], "builder prune") {
				t.Errorf("doesn't end by removing the candidate and the build cache: %q", last)
			}
		})
	}
}
