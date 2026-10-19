// Package netrules generates the nftables ruleset that pins sandboxes to the
// broker. A sandbox may reach the host's broker and hook ports and nothing
// else: nothing is forwarded anywhere, so a tool that ignores HTTPS_PROXY
// simply fails. Those two ports answer sandboxes only, never the LAN.
//
// The rules live in their own table next to Docker's iptables-nft rules. A
// drop verdict in any table is final even when Docker accepts the same packet
// in its own, which is why a separate table is enough. Priority -10 runs it
// before Docker's filter chains.
package netrules

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Input is everything the ruleset depends on.
type Input struct {
	Sandboxes []netip.Prefix // project subnets
	ProxyPort uint16         // the broker, on the host's bridge gateways
	HookPort  uint16         // the hook listener, likewise
}

// Generate returns an `nft -f` script that atomically replaces table inet
// skybreaker. Output is deterministic, so it can be diffed against what's loaded.
func Generate(in Input) (string, error) {
	subnets, err := normalise(in.Sandboxes)
	if err != nil {
		return "", err
	}
	if in.ProxyPort == 0 || in.HookPort == 0 || in.ProxyPort == in.HookPort {
		return "", fmt.Errorf("netrules: need two distinct ports, not %d and %d", in.ProxyPort, in.HookPort)
	}
	ports := fmt.Sprintf("{ %d, %d }", min(in.ProxyPort, in.HookPort), max(in.ProxyPort, in.HookPort))

	elems := make([]string, len(subnets))
	for i, s := range subnets {
		elems[i] = s.String()
	}
	var w strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&w, format+"\n", a...) }
	// Creating the table first makes the delete safe on a fresh host; nft
	// applies the whole file as one transaction.
	line("table inet skybreaker {}")
	line("delete table inet skybreaker")
	line("table inet skybreaker {")
	line("\tset sandboxes {")
	line("\t\ttype ipv4_addr")
	line("\t\tflags interval")
	line("\t\telements = { %s }", strings.Join(elems, ", "))
	line("\t}")
	line("")
	line("\tchain forward {")
	line("\t\ttype filter hook forward priority -10; policy accept;")
	line("\t\tiifname \"sky-*\" meta nfproto ipv6 drop")
	line("\t\tip saddr @sandboxes counter drop")
	line("\t}")
	line("")
	line("\tchain input {")
	line("\t\ttype filter hook input priority -10; policy accept;")
	line("\t\tiifname \"sky-*\" meta nfproto ipv6 drop")
	line("\t\tip saddr @sandboxes tcp dport %s accept", ports)
	line("\t\tip saddr @sandboxes ct state established,related accept")
	line("\t\tip saddr @sandboxes counter drop")
	line("\t\ttcp dport %s counter drop", ports)
	line("\t}")
	line("}")
	return w.String(), nil
}

// normalise masks, sorts and dedupes the subnets. Overlaps are refused:
// nft rejects overlapping elements in an interval set, and an overlap means
// two projects share an address range.
func normalise(in []netip.Prefix) ([]netip.Prefix, error) {
	if len(in) == 0 {
		return nil, errors.New("netrules: no sandbox subnets")
	}
	out := make([]netip.Prefix, 0, len(in))
	for _, p := range in {
		if !p.IsValid() || !p.Addr().Is4() {
			return nil, fmt.Errorf("netrules: sandbox subnet %v must be IPv4", p)
		}
		if p.Bits() < 8 {
			return nil, fmt.Errorf("netrules: sandbox subnet %v is implausibly large", p)
		}
		out = append(out, p.Masked())
	}
	slices.SortFunc(out, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	out = slices.Compact(out)
	for i := 1; i < len(out); i++ {
		if out[i-1].Overlaps(out[i]) {
			return nil, fmt.Errorf("netrules: sandbox subnets %v and %v overlap", out[i-1], out[i])
		}
	}
	return out, nil
}
