package setup

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Defaults fills in what the host already knows: the tailnet and your
// login from a local tailscale, and the LAN from its interfaces.
func (h *Host) Defaults(ctx context.Context) Answers {
	a := Answers{Branch: "main", Harnesses: []string{"claude", "codex"}, Image: DefaultImage, Block: LANs()}
	if out, err := h.run(ctx, "tailscale", "status", "--json"); err == nil {
		a.Tailnet, a.Users = parseTailscaleStatus([]byte(out))
	}
	return a
}

func parseTailscaleStatus(b []byte) (tailnet string, users []string) {
	var v struct {
		MagicDNSSuffix string
		Self           struct{ UserID int64 }
		User           map[string]struct{ LoginName string }
	}
	if json.Unmarshal(b, &v) != nil {
		return "", nil
	}
	// A tagged node belongs to "tagged-devices", not to a person.
	if u := v.User[strconv.FormatInt(v.Self.UserID, 10)].LoginName; strings.Contains(u, "@") {
		users = []string{u}
	}
	return strings.TrimSuffix(v.MagicDNSSuffix, "."), users
}

type iface struct {
	Name     string
	Up, Loop bool
	Addrs    []netip.Prefix
}

// LANs lists the private IPv4 networks the host is on, leaving out Docker's
// and Tailscale's own.
func LANs() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var list []iface
	for _, i := range ifs {
		f := iface{Name: i.Name, Up: i.Flags&net.FlagUp != 0, Loop: i.Flags&net.FlagLoopback != 0}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(n.IP); ok {
					ones, _ := n.Mask.Size()
					f.Addrs = append(f.Addrs, netip.PrefixFrom(ip.Unmap(), ones))
				}
			}
		}
		list = append(list, f)
	}
	return lanPrefixes(list)
}

var virtualIfaces = []string{"docker", "br-", "veth", "tailscale", "virbr", "cni", "flannel", "lxc", "vnet", "tun", "wg"}

func lanPrefixes(ifs []iface) []string {
	var out []string
	for _, i := range ifs {
		if !i.Up || i.Loop || slices.ContainsFunc(virtualIfaces, func(p string) bool { return strings.HasPrefix(i.Name, p) }) {
			continue
		}
		for _, p := range i.Addrs {
			if s := p.Masked().String(); p.Addr().Is4() && p.Addr().IsPrivate() && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}
