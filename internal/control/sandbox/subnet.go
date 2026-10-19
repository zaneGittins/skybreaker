package sandbox

import (
	"fmt"
	"hash/fnv"
	"net/netip"
)

// SubnetFor derives a project's /24 from its name inside base, which must be
// an IPv4 /16. The result never is the base's first /24, so the base address
// itself stays free. It is stable across config reorderings, which matters
// because the nftables rules and existing Docker networks are keyed on it.
// Collisions are possible; the config layer must check for them.
// An unusable base gives an invalid prefix.
func SubnetFor(project string, base netip.Prefix) netip.Prefix {
	if !base.IsValid() || !base.Addr().Is4() || base.Bits() != 16 {
		return netip.Prefix{}
	}
	h := fnv.New32a()
	h.Write([]byte(project))
	b := base.Masked().Addr().As4()
	b[2] = byte(1 + h.Sum32()%255)
	return netip.PrefixFrom(netip.AddrFrom4(b), 24)
}

// Gateway is the host's address on a sandbox network: its first host
// address, as Docker assigns it to the bridge.
func Gateway(subnet netip.Prefix) netip.Addr {
	return subnet.Masked().Addr().Next()
}

// Addr is the sandbox's own address on its network, the one after the
// gateway, fixed at start so the host knows where to reach it.
func Addr(subnet netip.Prefix) netip.Addr {
	return Gateway(subnet).Next()
}

// BridgeName is the Linux interface name of a project's bridge. Interface
// names are capped at 15 bytes, so long project names are shortened with a
// hash suffix. The "sky-" prefix lets nftables match every sandbox bridge.
func BridgeName(project string) string {
	if len(project) <= 11 {
		return "sky-" + project
	}
	h := fnv.New32a()
	h.Write([]byte(project))
	return fmt.Sprintf("sky-%s%04x", project[:7], h.Sum32()&0xffff)
}
