package setup

import (
	"strconv"
	"strings"
)

// Version is the release this installer belongs to, set at build time. It
// installs the skybreaker binary of the same release.
var Version = "dev"

// CompareVersions orders two release tags such as v1.2.3 or v1.2.3-rc.1,
// returning -1, 0 or 1. A pre-release sorts below its release, and "dev"
// above everything.
func CompareVersions(a, b string) int {
	if a == b {
		return 0
	}
	if a == "dev" || a == "" {
		return 1
	}
	if b == "dev" || b == "" {
		return -1
	}
	ac, ap := splitVersion(a)
	bc, bp := splitVersion(b)
	for i := range ac {
		if ac[i] != bc[i] {
			return cmp(ac[i], bc[i])
		}
	}
	switch {
	case ap == bp:
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	}
	as, bs := strings.Split(ap, "."), strings.Split(bp, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				return cmp(an, bn)
			}
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		case as[i] != bs[i]:
			return strings.Compare(as[i], bs[i])
		}
	}
	return cmp(len(as), len(bs))
}

func splitVersion(v string) ([3]int, string) {
	v = strings.TrimPrefix(v, "v")
	core, pre, _ := strings.Cut(v, "-")
	var out [3]int
	for i, p := range strings.SplitN(core, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out, pre
}

func cmp(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
