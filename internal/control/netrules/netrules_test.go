package netrules

import (
	"math/rand/v2"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const golden = `table inet skybreaker {}
delete table inet skybreaker
table inet skybreaker {
	set sandboxes {
		type ipv4_addr
		flags interval
		elements = { 10.89.7.0/24, 10.89.42.0/24, 10.89.200.0/24 }
	}

	chain forward {
		type filter hook forward priority -10; policy accept;
		iifname "sky-*" counter drop
		ip saddr @sandboxes counter drop
	}

	chain input {
		type filter hook input priority -10; policy accept;
		iifname "sky-*" meta nfproto ipv6 drop
		iifname "sky-*" ip saddr @sandboxes tcp dport { 7701, 8080 } accept
		iifname "sky-*" ip saddr @sandboxes ct state established,related accept
		iifname "sky-*" counter drop
		ip saddr @sandboxes counter drop
		tcp dport { 7701, 8080 } counter drop
	}
}
`

func prefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func input() Input {
	return Input{
		Sandboxes: prefixes("10.89.42.0/24", "10.89.200.0/24", "10.89.7.0/24"),
		ProxyPort: 8080,
		HookPort:  7701,
	}
}

func TestGenerateGolden(t *testing.T) {
	got, err := Generate(input())
	if err != nil {
		t.Fatal(err)
	}
	if got != golden {
		t.Errorf("got:\n%s\nwant:\n%s", got, golden)
	}
}

// A sandbox can write raw packets (runsc runs with --net-raw), so it can put
// any source address on them: what comes in on a sandbox bridge is judged by
// the bridge, not by the address it claims.
func TestSpoofedSourcesAreDropped(t *testing.T) {
	got, _ := Generate(input())
	forward := got[strings.Index(got, "chain forward"):strings.Index(got, "chain input")]
	if !strings.Contains(forward, "iifname \"sky-*\" counter drop") {
		t.Fatalf("forward lets a forged source off a sandbox bridge:\n%s", forward)
	}
	in := got[strings.Index(got, "chain input"):]
	for _, l := range strings.Split(in, "\n") {
		if strings.HasSuffix(l, " accept") && !strings.Contains(l, "iifname \"sky-*\"") {
			t.Fatalf("input accepts by address alone: %q", l)
		}
	}
	if !strings.Contains(in, "iifname \"sky-*\" counter drop") {
		t.Fatal("input doesn't drop the rest from sandbox bridges")
	}
}

func TestGenerateDeterministic(t *testing.T) {
	in := input()
	in.Sandboxes = append(in.Sandboxes, prefixes("10.89.1.0/24", "10.89.99.0/24", "10.89.3.0/24")...)
	want, err := Generate(in)
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 20 {
		r.Shuffle(len(in.Sandboxes), func(i, j int) { in.Sandboxes[i], in.Sandboxes[j] = in.Sandboxes[j], in.Sandboxes[i] })
		got, err := Generate(in)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("output depends on input order:\n%s\nvs\n%s", got, want)
		}
	}
}

func TestGenerateMasksAndDedupes(t *testing.T) {
	in := input()
	in.Sandboxes = prefixes("10.89.42.17/24", "10.89.42.0/24")
	got, err := Generate(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "elements = { 10.89.42.0/24 }") {
		t.Errorf("not masked and deduped:\n%s", got)
	}
}

func TestGenerateErrors(t *testing.T) {
	cases := map[string]func(*Input){
		"no sandboxes":   func(in *Input) { in.Sandboxes = nil },
		"invalid prefix": func(in *Input) { in.Sandboxes = []netip.Prefix{{}} },
		"ipv6 sandbox":   func(in *Input) { in.Sandboxes = prefixes("fd00::/64") },
		"4in6 sandbox":   func(in *Input) { in.Sandboxes = prefixes("::ffff:10.89.1.0/120") },
		"overlapping":    func(in *Input) { in.Sandboxes = prefixes("10.89.0.0/16", "10.89.42.0/24") },
		"no proxy port":  func(in *Input) { in.ProxyPort = 0 },
		"no hook port":   func(in *Input) { in.HookPort = 0 },
		"same ports":     func(in *Input) { in.HookPort = 8080 },
		"whole space":    func(in *Input) { in.Sandboxes = prefixes("0.0.0.0/0") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := input()
			mutate(&in)
			if out, err := Generate(in); err == nil {
				t.Fatalf("accepted; output:\n%s", out)
			}
		})
	}
}

// The sandboxes' accept must come before the drops, or the drops win.
func TestGenerateOrder(t *testing.T) {
	got, err := Generate(input())
	if err != nil {
		t.Fatal(err)
	}
	in := strings.Index(got, "chain input")
	accept := strings.Index(got, "tcp dport { 7701, 8080 } accept")
	drop := in + strings.Index(got[in:], "ip saddr @sandboxes counter drop")
	if outside := strings.Index(got, "\t\ttcp dport { 7701, 8080 } counter drop"); outside < accept {
		t.Errorf("the ports are closed to sandboxes too:\n%s", got)
	}
	if accept < 0 || drop < 0 || accept > drop {
		t.Errorf("broker accept (%d) must precede the drop (%d)", accept, drop)
	}
	// The script must replace the table atomically: create-if-missing, then
	// delete, then define, all in one nft transaction.
	if !strings.HasPrefix(got, "table inet skybreaker {}\ndelete table inet skybreaker\n") {
		t.Errorf("script doesn't start with the idempotent replace:\n%s", got)
	}
}

// TestRulesetLoadsInNft checks the generated script with nft itself, in a
// throwaway user and network namespace so no privilege is needed and the
// host's rules are never touched.
func TestRulesetLoadsInNft(t *testing.T) {
	nft, err := exec.LookPath("nft")
	if err != nil {
		t.Skip("nft not installed")
	}
	check := func(script string) ([]byte, error) {
		p := filepath.Join(t.TempDir(), "rules.nft")
		if err := os.WriteFile(p, []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		return exec.Command("unshare", "-rn", nft, "-c", "-f", p).CombinedOutput()
	}
	if out, err := check("table inet x {}\n"); err != nil {
		t.Skipf("can't run nft in a user namespace here: %v %s", err, out)
	}
	if _, err := check("table inet x { chain c { bogus } }\n"); err == nil {
		t.Fatal("nft -c accepted a broken script; the check proves nothing")
	}
	for name, in := range map[string]Input{
		"several sandboxes": {Sandboxes: prefixes("10.89.7.0/24", "10.89.42.0/24", "10.89.200.0/24"), ProxyPort: 8080, HookPort: 7701},
		"one":               {Sandboxes: prefixes("10.89.0.0/24"), ProxyPort: 3128, HookPort: 7701},
		"adjacent subnets":  {Sandboxes: prefixes("10.89.1.0/24", "10.89.2.0/24", "10.89.3.0/24"), ProxyPort: 8080, HookPort: 7701},
	} {
		script, err := Generate(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out, err := check(script); err != nil {
			t.Errorf("%s: nft -c rejected the ruleset: %v\n%s\n%s", name, err, out, script)
		}
	}
}
