package sandbox

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func testBrowser() BrowserSpec {
	return BrowserSpec{Project: "pip", Image: "ghcr.io/zanegittins/skybreaker-sandbox:1", Subnet: netip.MustParsePrefix("10.89.42.0/24"), Profile: "/var/lib/skybreaker/assistants/pip/browser"}
}

// The browser runs beside an assistant's sandbox with nothing of the
// sandbox's: no capabilities, no mounts but its own profile, no proxy
// settings or token in its environment, and nothing it listens on.
func TestBrowserRunArgs(t *testing.T) {
	b := testBrowser()
	args, err := b.RunArgs()
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "run" || args[len(args)-2] != b.Image || args[len(args)-1] != "infinity" {
		t.Fatalf("args %v", args)
	}
	for flag, want := range map[string]string{
		"--name": "skybreaker-pip-browser", "--runtime": "runsc", "--network": "skybreaker-pip",
		"--ip": "10.89.42.3", "--dns": "127.0.0.1", "--entrypoint": "/bin/sleep",
	} {
		if got := pairs(args, flag); len(got) != 1 || got[0] != want {
			t.Errorf("%s = %v, want %s", flag, got, want)
		}
	}
	if m := pairs(args, "--mount"); len(m) != 1 || m[0] != "type=bind,source=/var/lib/skybreaker/assistants/pip/browser,target=/profile" {
		t.Errorf("mounts %v", m)
	}
	joined := strings.Join(args, " ")
	for _, never := range []string{"--cap-add", "--privileged", "PROXY", "TOKEN", "-p ", "--publish", "workspace", "/root"} {
		if strings.Contains(joined, never) {
			t.Errorf("browser args contain %q: %v", never, args)
		}
	}
	for _, want := range []string{"--cap-drop", "no-new-privileges", "--pids-limit", "/downloads:size=512m"} {
		if !strings.Contains(joined, want) {
			t.Errorf("browser args lack %s", want)
		}
	}
	if !slices.Contains(args, "--read-only") {
		t.Error("root filesystem is writable")
	}
	if BrowserAddr(b.Subnet) == Addr(b.Subnet) || BrowserAddr(b.Subnet) == Gateway(b.Subnet) {
		t.Error("the browser's address is taken")
	}
	b.Profile = "relative/dir"
	if _, err := b.RunArgs(); err == nil {
		t.Error("accepted a relative profile")
	}
	b = testBrowser()
	b.Profile = "/x,target=/root"
	if _, err := b.RunArgs(); err == nil {
		t.Error("accepted a profile that rewrites the mount")
	}
}
