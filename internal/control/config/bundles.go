package config

import (
	"maps"
	"slices"

	policy "github.com/zaneGittins/skybreaker/internal/proxy/config"
)

// builtinBundles ship with Skybreaker, on no project until given one. A
// bundle of the same name in the config file or the app replaces one.
var builtinBundles = map[string][]*policy.Entry{
	// A Google account: sign-in, Gmail, Calendar, Drive and Docs, Contacts,
	// Keep, Tasks, Meet, Photos and NotebookLM, their APIs, and the static
	// and user-content hosts their pages load from. Tunnelled: Google's own
	// TLS reaches the browser or agent, so sign-in works and nothing is read.
	"google": tunnels(
		"google.com", "*.google.com",
		"*.googleapis.com",
		"*.gstatic.com",
		"*.googleusercontent.com",
		"*.ggpht.com",
		"notebooklm.google",
		"accounts.youtube.com", // sign-in sets its cookies here too
		"goo.gl",
	),
}

func tunnels(hosts ...string) []*policy.Entry {
	out := make([]*policy.Entry, len(hosts))
	for i, h := range hosts {
		out[i] = &policy.Entry{Host: h, Mode: policy.ModeTunnel}
	}
	return out
}

// addBuiltins adds the built-in bundles the file and the app don't define.
func (c *Config) addBuiltins() {
	c.builtins = map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(builtinBundles)) {
		if _, ok := c.Capabilities[name]; ok {
			continue
		}
		if c.Capabilities == nil {
			c.Capabilities = map[string][]*policy.Entry{}
		}
		c.Capabilities[name] = builtinBundles[name]
		c.builtins[name] = true
	}
}

// BuiltinBundle reports whether a bundle is Skybreaker's own, unreplaced.
func (c *Config) BuiltinBundle(name string) bool { return c.builtins[name] }
