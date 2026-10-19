package sandbox

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testToken = "tok_Abc-123_xyz"

func testSpec(t *testing.T) Spec {
	t.Helper()
	return Spec{
		Project:   "orbit",
		Image:     "ghcr.io/zanegittins/skybreaker-sandbox:0.0.1",
		Subnet:    netip.MustParsePrefix("10.89.42.0/24"),
		ProxyPort: 8080,
		Token:     testToken,
		DataDir:   filepath.Join(t.TempDir(), "projects", "orbit"),
	}
}

// pairs collects the values that follow each occurrence of flag.
func pairs(args []string, flag string) []string {
	var out []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}
	return out
}

func TestRunArgsShape(t *testing.T) {
	s := testSpec(t)
	args, err := s.RunArgs()
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "run" || args[1] != "-d" {
		t.Fatalf("args start %v, want run -d", args[:2])
	}
	if got := args[len(args)-1]; got != s.Image {
		t.Fatalf("last arg %q, want the image", got)
	}
	single := map[string]string{
		"--name":     "skybreaker-orbit",
		"--hostname": "orbit",
		"--runtime":  "runsc",
		"--network":  "skybreaker-orbit",
		"--dns":      "127.0.0.1",
		"--add-host": "broker:10.89.42.1", // the bridge gateway, where the broker listens
		"--label":    "skybreaker.project=orbit",
	}
	for flag, want := range single {
		got := pairs(args, flag)
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s = %v, want [%s]", flag, got, want)
		}
	}
	// Inner Docker's images live on disk, not in the sandbox's memory.
	for _, flag := range []string{"--privileged", "--security-opt", "--memory", "--cpus", "-p", "--publish", "--network=host", "--tmpfs"} {
		if slices.Contains(args, flag) {
			t.Errorf("unexpected %s in %v", flag, args)
		}
	}
}

func TestRunArgsEnv(t *testing.T) {
	s := testSpec(t)
	s.Env = map[string]string{"ANTHROPIC_API_KEY": "skybreaker-injected"}
	args, err := s.RunArgs()
	if err != nil {
		t.Fatal(err)
	}
	proxy := "http://orbit:" + testToken + "@broker:8080"
	want := []string{
		"ANTHROPIC_API_KEY=skybreaker-injected",
		"GH_TOKEN=skybreaker-injected",
		"GIT_SSL_CAINFO=/etc/ssl/certs/ca-certificates.crt",
		"HTTPS_PROXY=" + proxy,
		"HTTP_PROXY=" + proxy,
		"NODE_EXTRA_CA_CERTS=/etc/skybreaker/ca.crt",
		"NO_PROXY=localhost,127.0.0.1,broker",
		"REQUESTS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt",
		"SKYBREAKER_PROJECT=orbit",
		"SKYBREAKER_TOKEN=" + testToken,
		"SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
		"http_proxy=" + proxy,
		"https_proxy=" + proxy,
		"no_proxy=localhost,127.0.0.1,broker",
	}
	if got := pairs(args, "-e"); !slices.Equal(got, want) {
		t.Errorf("env:\n got %q\nwant %q", got, want)
	}
}

func TestRunArgsMounts(t *testing.T) {
	s := testSpec(t)
	args, err := s.RunArgs()
	if err != nil {
		t.Fatal(err)
	}
	d := s.DataDir
	want := []string{
		"type=bind,source=" + d + "/workspace,target=/workspace",
		"type=bind,source=" + d + "/home,target=/root",
		"type=bind,source=" + d + "/etc,target=/etc/skybreaker,readonly",
		"type=bind,source=" + d + "/etc/claude-managed-settings.json,target=/etc/claude-code/managed-settings.json,readonly",
		"type=bind,source=" + d + "/runs,target=/skybreaker/runs",
		"type=bind,source=" + d + "/docker,target=/var/lib/docker",
	}
	if got := pairs(args, "--mount"); !slices.Equal(got, want) {
		t.Errorf("mounts:\n got %q\nwant %q", got, want)
	}
	for _, m := range s.Mounts() {
		if strings.Contains(m.Source, testToken) || strings.Contains(m.Target, testToken) {
			t.Errorf("token leaked into mount %+v", m)
		}
	}
	// -v would make Docker create a missing source as a directory, which turns
	// the managed-settings file into a directory and silently drops the hooks.
	if slices.Contains(args, "-v") || slices.Contains(args, "--volume") {
		t.Error("mounts must use --mount, not -v")
	}
}

func TestRunArgsLimitsAndOverrides(t *testing.T) {
	s := testSpec(t)
	s.Hostname = "box"
	s.Memory = "8g"
	s.CPUs = "2.5"
	args, err := s.RunArgs()
	if err != nil {
		t.Fatal(err)
	}
	for flag, want := range map[string]string{"--runtime": "runsc", "--hostname": "box", "--memory": "8g", "--cpus": "2.5"} {
		if got := pairs(args, flag); len(got) != 1 || got[0] != want {
			t.Errorf("%s = %v, want %s", flag, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(*Spec){
		"empty project":       func(s *Spec) { s.Project = "" },
		"uppercase project":   func(s *Spec) { s.Project = "Orbit" },
		"leading dash":        func(s *Spec) { s.Project = "-orbit" },
		"path traversal":      func(s *Spec) { s.Project = "../etc" },
		"slash":               func(s *Spec) { s.Project = "a/b" },
		"space":               func(s *Spec) { s.Project = "a b" },
		"flag injection":      func(s *Spec) { s.Project = "--privileged" },
		"newline":             func(s *Spec) { s.Project = "a\nb" },
		"too long":            func(s *Spec) { s.Project = strings.Repeat("a", 41) },
		"unicode":             func(s *Spec) { s.Project = "elarа" },
		"no image":            func(s *Spec) { s.Image = "" },
		"image flag":          func(s *Spec) { s.Image = "--privileged" },
		"invalid subnet":      func(s *Spec) { s.Subnet = netip.Prefix{} },
		"ipv6 subnet":         func(s *Spec) { s.Subnet = netip.MustParsePrefix("fd00::/64") },
		"subnet too big":      func(s *Spec) { s.Subnet = netip.MustParsePrefix("10.0.0.0/8") },
		"subnet too small":    func(s *Spec) { s.Subnet = netip.MustParsePrefix("10.89.1.0/30") },
		"no proxy port":       func(s *Spec) { s.ProxyPort = 0 },
		"empty token":         func(s *Spec) { s.Token = "" },
		"token with at":       func(s *Spec) { s.Token = "a@evil.example" },
		"token with colon":    func(s *Spec) { s.Token = "a:b" },
		"token with slash":    func(s *Spec) { s.Token = "a/b" },
		"relative data dir":   func(s *Spec) { s.DataDir = "projects/orbit" },
		"unclean data dir":    func(s *Spec) { s.DataDir = "/srv/../etc" },
		"data dir with comma": func(s *Spec) { s.DataDir = "/srv/a,readonly=false" },
		"empty data dir":      func(s *Spec) { s.DataDir = "" },
		"bad hostname":        func(s *Spec) { s.Hostname = "a b" },
		"bad env key":         func(s *Spec) { s.Env = map[string]string{"A-B": "x"} },
		"env newline":         func(s *Spec) { s.Env = map[string]string{"A": "x\ny"} },
		"env overrides proxy": func(s *Spec) { s.Env = map[string]string{"HTTPS_PROXY": "http://evil:1"} },
		"env overrides lower": func(s *Spec) { s.Env = map[string]string{"no_proxy": "*"} },
		"env overrides token": func(s *Spec) { s.Env = map[string]string{"SKYBREAKER_TOKEN": "x"} },
		"bad memory":          func(s *Spec) { s.Memory = "8g --privileged" },
		"bad cpus":            func(s *Spec) { s.CPUs = "two" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := testSpec(t)
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatalf("Validate accepted %+v", s)
			}
			if _, err := s.RunArgs(); err == nil {
				t.Fatal("RunArgs accepted an invalid spec")
			}
		})
	}
	if err := testSpec(t).Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
}

func TestNames(t *testing.T) {
	if got := ContainerName("orbit"); got != "skybreaker-orbit" {
		t.Errorf("ContainerName = %q", got)
	}
	if got := NetworkName("orbit"); got != "skybreaker-orbit" {
		t.Errorf("NetworkName = %q", got)
	}
}

func TestBridgeName(t *testing.T) {
	for _, p := range []string{"a", "orbit", "exactly11ch", "a-much-longer-project-name", strings.Repeat("z", 40)} {
		b := BridgeName(p)
		if len(b) > 15 {
			t.Errorf("BridgeName(%q) = %q, %d chars", p, b, len(b))
		}
		if !strings.HasPrefix(b, "sky-") {
			t.Errorf("BridgeName(%q) = %q lacks sky- prefix", p, b)
		}
		if b != BridgeName(p) {
			t.Errorf("BridgeName(%q) unstable", p)
		}
	}
	if BridgeName("a-much-longer-project-name") == BridgeName("a-much-longer-project-nam2") {
		t.Error("long names sharing a prefix collide")
	}
	if got := BridgeName("orbit"); got != "sky-orbit" {
		t.Errorf("short names should be readable, got %q", got)
	}
}

func TestSubnetFor(t *testing.T) {
	base := netip.MustParsePrefix("10.89.0.0/16")
	seen := map[netip.Prefix]string{}
	for _, p := range []string{"orbit", "bifract", "skybreaker", "mercury", "zenith", "riptide"} {
		s := SubnetFor(p, base)
		if !s.IsValid() || s.Bits() != 24 {
			t.Fatalf("SubnetFor(%q) = %v", p, s)
		}
		if !base.Contains(s.Addr()) {
			t.Errorf("SubnetFor(%q) = %v outside %v", p, s, base)
		}
		if s.Addr().As4()[2] == 0 {
			t.Errorf("SubnetFor(%q) = %v is the first /24", p, s)
		}
		if s != SubnetFor(p, base) {
			t.Errorf("SubnetFor(%q) unstable", p)
		}
		if s != s.Masked() {
			t.Errorf("SubnetFor(%q) = %v not masked", p, s)
		}
		seen[s] = p
	}
	if len(seen) < 5 {
		t.Errorf("suspiciously many collisions: %v", seen)
	}
	for _, bad := range []netip.Prefix{{}, netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("10.89.0.0/24"), netip.MustParsePrefix("fd00::/16")} {
		if s := SubnetFor("orbit", bad); s.IsValid() {
			t.Errorf("SubnetFor with base %v = %v, want invalid", bad, s)
		}
	}
	// A base that isn't masked still yields a subnet inside the masked base.
	if s := SubnetFor("orbit", netip.MustParsePrefix("10.89.3.4/16")); !netip.MustParsePrefix("10.89.0.0/16").Contains(s.Addr()) {
		t.Errorf("unmasked base gave %v", s)
	}
}

func TestGateway(t *testing.T) {
	if got := Gateway(netip.MustParsePrefix("10.89.42.0/24")); got != netip.MustParseAddr("10.89.42.1") {
		t.Errorf("Gateway = %v", got)
	}
	if got := Gateway(netip.MustParsePrefix("10.89.42.77/24")); got != netip.MustParseAddr("10.89.42.1") {
		t.Errorf("Gateway of unmasked prefix = %v", got)
	}
}

// fakeRunner records every call and answers from a script keyed by the
// command line.
type fakeRunner struct {
	calls   [][]string
	replies map[string]reply
}

type reply struct {
	out string
	err error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	key := strings.Join(call, " ")
	for prefix, r := range f.replies {
		if strings.HasPrefix(key, prefix) {
			return []byte(r.out), r.err
		}
	}
	return nil, nil
}

func (f *fakeRunner) commands() []string {
	var out []string
	for _, c := range f.calls {
		// Name the command by its first two docker words.
		out = append(out, strings.Join(c[1:min(3, len(c))], " "))
	}
	return out
}

func (f *fakeRunner) find(prefix string) []string {
	for _, c := range f.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return c
		}
	}
	return nil
}

var (
	//lint:ignore ST1005 docker's own wording, which the manager matches on
	noSuchContainer = reply{err: errors.New("Error: No such container: skybreaker-orbit")}
	//lint:ignore ST1005 docker's own wording, which the manager matches on
	noSuchNetwork = reply{err: errors.New("Error response from daemon: network skybreaker-orbit not found")}
	networkOK     = reply{out: "10.89.42.0/24\n"}
)

func TestStartMissing(t *testing.T) {
	f := &fakeRunner{replies: map[string]reply{
		"docker network inspect":   networkOK,
		"docker container inspect": noSuchContainer,
	}}
	m := &Manager{R: f}
	s := testSpec(t)
	if err := m.Start(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	want := []string{"network inspect", "container inspect", "run -d"}
	if got := f.commands(); !slices.Equal(got, want) {
		t.Fatalf("commands %v, want %v", got, want)
	}
	for _, d := range []string{"workspace", "home", "etc", "runs", "docker"} {
		fi, err := os.Stat(filepath.Join(s.DataDir, d))
		if err != nil || !fi.IsDir() {
			t.Fatalf("%s not created: %v", d, err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode %v, want 0700", d, fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(s.DataDir, "etc", "claude-managed-settings.json")); err != nil {
		t.Errorf("managed settings placeholder missing: %v", err)
	}
}

func TestStartKeepsExistingSettings(t *testing.T) {
	f := &fakeRunner{replies: map[string]reply{
		"docker network inspect":   networkOK,
		"docker container inspect": noSuchContainer,
	}}
	s := testSpec(t)
	path := filepath.Join(s.DataDir, "etc", "claude-managed-settings.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(`{"hooks":{}}`), 0o644)
	if err := (&Manager{R: f}).Start(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != `{"hooks":{}}` {
		t.Errorf("Start overwrote managed settings: %s", b)
	}
}

func TestStartRunning(t *testing.T) {
	f := &fakeRunner{replies: map[string]reply{
		"docker network inspect":   networkOK,
		"docker container inspect": {out: "abc123 true\n"},
	}}
	if err := (&Manager{R: f}).Start(context.Background(), testSpec(t)); err != nil {
		t.Fatal(err)
	}
	want := []string{"network inspect", "container inspect"}
	if got := f.commands(); !slices.Equal(got, want) {
		t.Fatalf("commands %v, want %v (running is a no-op)", got, want)
	}
}

func TestStartStopped(t *testing.T) {
	f := &fakeRunner{replies: map[string]reply{
		"docker network inspect":   networkOK,
		"docker container inspect": {out: "abc123 false\n"},
	}}
	if err := (&Manager{R: f}).Start(context.Background(), testSpec(t)); err != nil {
		t.Fatal(err)
	}
	// A stopped container still carries its old session token in its env, so
	// it is replaced rather than restarted.
	want := []string{"network inspect", "container inspect", "rm -f", "run -d"}
	if got := f.commands(); !slices.Equal(got, want) {
		t.Fatalf("commands %v, want %v", got, want)
	}
	if c := f.find("docker rm -f"); c[len(c)-1] != "skybreaker-orbit" {
		t.Errorf("rm target %v", c)
	}
}

func TestStartInvalidSpecRunsNothing(t *testing.T) {
	f := &fakeRunner{}
	s := testSpec(t)
	s.Project = "../x"
	if err := (&Manager{R: f}).Start(context.Background(), s); err == nil {
		t.Fatal("invalid spec started")
	}
	if len(f.calls) != 0 {
		t.Errorf("ran %v for an invalid spec", f.calls)
	}
}

func TestStartInspectError(t *testing.T) {
	f := &fakeRunner{replies: map[string]reply{
		"docker network inspect":   networkOK,
		"docker container inspect": {err: errors.New("Cannot connect to the Docker daemon")},
	}}
	err := (&Manager{R: f}).Start(context.Background(), testSpec(t))
	if err == nil || !strings.Contains(err.Error(), "Docker daemon") {
		t.Fatalf("err = %v", err)
	}
	if f.find("docker run") != nil {
		t.Error("ran a container after inspect failed")
	}
}

func TestStatus(t *testing.T) {
	cases := []struct {
		r    reply
		want State
		err  bool
	}{
		{reply{out: "abc true\n"}, State{Exists: true, Running: true, ID: "abc"}, false},
		{reply{out: "abc false\n"}, State{Exists: true, ID: "abc"}, false},
		{noSuchContainer, State{}, false},
		{reply{err: errors.New("Error: No such object: skybreaker-orbit")}, State{}, false},
		{reply{out: "garbage"}, State{}, true},
		{reply{err: errors.New("permission denied")}, State{}, true},
	}
	for i, c := range cases {
		f := &fakeRunner{replies: map[string]reply{"docker container inspect": c.r}}
		got, err := (&Manager{R: f}).Status(context.Background(), "orbit")
		if (err != nil) != c.err || got != c.want {
			t.Errorf("case %d: got %+v, %v; want %+v, err=%v", i, got, err, c.want, c.err)
		}
		// Plain `docker inspect` would also match the network of the same name.
		if c := f.find("docker container inspect"); c == nil {
			t.Errorf("case %d: Status must use `docker container inspect`, ran %v", i, f.calls)
		}
	}
	if _, err := (&Manager{R: &fakeRunner{}}).Status(context.Background(), "../x"); err == nil {
		t.Error("Status accepted a bad project name")
	}
}

func TestEnsureNetworkCreates(t *testing.T) {
	f := &fakeRunner{replies: map[string]reply{"docker network inspect": noSuchNetwork}}
	subnet := netip.MustParsePrefix("10.89.42.0/24")
	if err := (&Manager{R: f}).EnsureNetwork(context.Background(), "a-much-longer-project-name", subnet); err != nil {
		t.Fatal(err)
	}
	c := f.find("docker network create")
	if c == nil {
		t.Fatalf("no network create in %v", f.calls)
	}
	want := []string{"docker", "network", "create", "--driver", "bridge", "--subnet", "10.89.42.0/24",
		"--opt", "com.docker.network.bridge.name=" + BridgeName("a-much-longer-project-name"),
		"--label", "skybreaker.project=a-much-longer-project-name", "skybreaker-a-much-longer-project-name"}
	if !slices.Equal(c, want) {
		t.Errorf("create:\n got %q\nwant %q", c, want)
	}
}

func TestEnsureNetworkExisting(t *testing.T) {
	f := &fakeRunner{replies: map[string]reply{"docker network inspect": networkOK}}
	if err := (&Manager{R: f}).EnsureNetwork(context.Background(), "orbit", netip.MustParsePrefix("10.89.42.0/24")); err != nil {
		t.Fatal(err)
	}
	if f.find("docker network create") != nil {
		t.Error("recreated an existing network")
	}
}

func TestEnsureNetworkSubnetMismatch(t *testing.T) {
	// The nftables rules are written for the configured subnet; a network on
	// another range would escape them.
	f := &fakeRunner{replies: map[string]reply{"docker network inspect": {out: "172.18.0.0/16\n"}}}
	err := (&Manager{R: f}).EnsureNetwork(context.Background(), "orbit", netip.MustParsePrefix("10.89.42.0/24"))
	if err == nil {
		t.Fatal("accepted a network on the wrong subnet")
	}
}

func TestEnsureNetworkOtherError(t *testing.T) {
	f := &fakeRunner{replies: map[string]reply{"docker network inspect": {err: errors.New("Cannot connect to the Docker daemon")}}}
	if err := (&Manager{R: f}).EnsureNetwork(context.Background(), "orbit", netip.MustParsePrefix("10.89.42.0/24")); err == nil {
		t.Fatal("swallowed a daemon error")
	}
	if f.find("docker network create") != nil {
		t.Error("created a network after an unexpected error")
	}
}

func TestEnsureNetworkValidates(t *testing.T) {
	f := &fakeRunner{}
	m := &Manager{R: f}
	if err := m.EnsureNetwork(context.Background(), "../x", netip.MustParsePrefix("10.89.42.0/24")); err == nil {
		t.Error("bad project accepted")
	}
	if err := m.EnsureNetwork(context.Background(), "orbit", netip.MustParsePrefix("fd00::/64")); err == nil {
		t.Error("IPv6 subnet accepted")
	}
	if len(f.calls) != 0 {
		t.Errorf("ran %v", f.calls)
	}
}

func TestStop(t *testing.T) {
	f := &fakeRunner{replies: map[string]reply{"docker container inspect": {out: "abc true\n"}}}
	if err := (&Manager{R: f}).Stop(context.Background(), "orbit"); err != nil {
		t.Fatal(err)
	}
	want := []string{"container inspect", "stop -t", "rm -f"}
	if got := f.commands(); !slices.Equal(got, want) {
		t.Fatalf("commands %v, want %v", got, want)
	}
	if c := f.find("docker stop"); !slices.Equal(c, []string{"docker", "stop", "-t", "10", "skybreaker-orbit"}) {
		t.Errorf("stop = %v", c)
	}

	f = &fakeRunner{replies: map[string]reply{"docker container inspect": noSuchContainer}}
	if err := (&Manager{R: f}).Stop(context.Background(), "orbit"); err != nil {
		t.Fatalf("stopping a missing sandbox: %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("ran %v for a missing sandbox", f.calls)
	}
}

func TestExec(t *testing.T) {
	f := &fakeRunner{replies: map[string]reply{"/usr/bin/docker exec": {out: "hi\n"}}}
	m := &Manager{R: f, Docker: "/usr/bin/docker"}
	out, err := m.Exec(context.Background(), "orbit", "tmux", "ls")
	if err != nil || string(out) != "hi\n" {
		t.Fatalf("Exec = %q, %v", out, err)
	}
	if want := []string{"/usr/bin/docker", "exec", "skybreaker-orbit", "tmux", "ls"}; !slices.Equal(f.calls[0], want) {
		t.Errorf("call %v, want %v", f.calls[0], want)
	}
	if _, err := m.Exec(context.Background(), "-it", "sh"); err == nil {
		t.Error("Exec accepted a flag as project")
	}
}

func TestExecArgv(t *testing.T) {
	m := &Manager{}
	got := m.ExecArgv("orbit", "tmux", "attach-session", "-t", "=s1")
	want := []string{"docker", "exec", "-it", "skybreaker-orbit", "tmux", "attach-session", "-t", "=s1"}
	if !slices.Equal(got, want) {
		t.Errorf("ExecArgv:\n got %q\nwant %q", got, want)
	}
}

func TestExecRunner(t *testing.T) {
	r := ExecRunner{}
	out, err := r.Run(context.Background(), "sh", "-c", "echo out; echo err >&2")
	if err != nil || string(out) != "out\n" {
		t.Fatalf("Run = %q, %v", out, err)
	}
	_, err = r.Run(context.Background(), "sh", "-c", "echo boom >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should carry stderr, got %v", err)
	}
}

func TestAlwaysGVisor(t *testing.T) {
	// Inner dockerd needs capabilities, which under runsc exist only inside
	// gVisor's own kernel. There is no other runtime to choose.
	s := testSpec(t)
	args, err := s.RunArgs()
	if err != nil {
		t.Fatal(err)
	}
	if !hasPair(args, "--cap-add", "ALL") || !hasPair(args, "--runtime", "runsc") {
		t.Errorf("sandbox args: %v", args)
	}
	if slices.Contains(args, "--privileged") {
		t.Fatalf("privileged sandbox: %v", args)
	}
}

func TestCheckRuntime(t *testing.T) {
	info := func(out string) error {
		f := &fakeRunner{replies: map[string]reply{"docker info": {out: out}}}
		return (&Manager{R: f}).CheckRuntime(context.Background())
	}
	if err := info(`{"io.containerd.runc.v2":{"path":"runc"},"runsc":{"path":"/usr/bin/runsc","runtimeArgs":["--net-raw"]}}`); err != nil {
		t.Fatalf("gVisor installed: %v", err)
	}
	for name, out := range map[string]string{
		"missing":       `{"runc":{"path":"runc"}}`,
		"runc in place": `{"runsc":{"path":"/usr/bin/runc"}}`,
		"garbage":       `not json`,
	} {
		if err := info(out); err == nil || !strings.Contains(err.Error(), "gVisor") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func hasPair(args []string, k, v string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == k && args[i+1] == v {
			return true
		}
	}
	return false
}

func TestExecPipeArgv(t *testing.T) {
	m := &Manager{Docker: "/usr/bin/docker"}
	got := m.ExecPipeArgv("orbit", "/workspace", map[string]string{"B": "2", "A": "1"}, "claude", "-p")
	want := []string{"/usr/bin/docker", "exec", "-i", "-w", "/workspace", "-e", "A=1", "-e", "B=2", "skybreaker-orbit", "claude", "-p"}
	if !slices.Equal(got, want) {
		t.Errorf("argv = %q\nwant  %q", got, want)
	}
	if got := m.ExecPipeArgv("orbit", "", nil, "x"); slices.Contains(got, "-w") || slices.Contains(got, "-t") || slices.Contains(got, "-it") {
		t.Errorf("argv = %q", got)
	}
}

type imageRunner struct{ container, image string }

func (r imageRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	switch {
	case len(args) > 1 && args[0] == "container" && args[1] == "inspect":
		if r.container == "" {
			return nil, errors.New("Error: No such container")
		}
		return []byte(r.container + "\n"), nil
	case len(args) > 1 && args[0] == "image" && args[1] == "inspect":
		return []byte(r.image + "\n"), nil
	}
	return nil, nil
}

func TestImageStale(t *testing.T) {
	for _, c := range []struct {
		container, image string
		stale            bool
	}{
		{"sha256:aaa", "sha256:aaa", false},
		{"sha256:aaa", "sha256:bbb", true},
		{"", "sha256:bbb", false}, // no container: nothing to refresh
	} {
		m := &Manager{R: imageRunner{c.container, c.image}}
		stale, err := m.ImageStale(context.Background(), "orbit", "skybreaker-sandbox:1")
		if err != nil || stale != c.stale {
			t.Errorf("%+v: stale %v, %v", c, stale, err)
		}
	}
}
