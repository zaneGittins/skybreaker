package config

import (
	"slices"
	"testing"

	policy "github.com/zaneGittins/skybreaker/internal/proxy/config"
)

// Every install has the Google bundle, on no project until given one.
func TestBuiltinGoogleBundle(t *testing.T) {
	path := mainConfig(t)
	c := mustLoad(t, path)
	if len(c.Capabilities["google"]) == 0 || !c.BuiltinBundle("google") || c.AppBundle("google") {
		t.Fatalf("google bundle %v builtin=%v", c.Capabilities["google"], c.BuiltinBundle("google"))
	}
	for name, p := range c.Projects {
		if slices.Contains(p.Capabilities, "google") {
			t.Fatalf("%s has it by default", name)
		}
	}
	if _, _, err := Edit(path, func(f *AppFiles) error {
		f.SetGrant("orbit", Grant{Capabilities: []string{"google"}})
		return nil
	}, nil, false); err != nil {
		t.Fatal(err)
	}
	pp, _ := mustLoad(t, path).Policy.Project("orbit")
	for _, h := range []string{"accounts.google.com", "mail.google.com", "calendar.google.com", "notebooklm.google.com", "notebooklm.google",
		"gmail.googleapis.com", "lh3.googleusercontent.com", "fonts.gstatic.com", "google.com", "accounts.youtube.com"} {
		if r := pp.Host(h); r == nil || r.Mode != policy.ModeTunnel {
			t.Errorf("%s: %+v", h, r)
		}
	}
	for _, h := range []string{"evilgoogle.com", "google.com.evil.example", "googleapis.com.evil.example"} {
		if pp.Host(h) != nil {
			t.Errorf("reaches %s", h)
		}
	}
}

// A bundle of the same name in the config file, or made in the app,
// replaces the built-in one.
func TestBuiltinBundleReplaced(t *testing.T) {
	path := mainConfig(t)
	if _, _, err := Edit(path, func(f *AppFiles) error {
		f.Access.Capabilities = map[string][]*policy.Entry{"google": {{Host: "mail.google.com", Mode: policy.ModeTunnel}}}
		return nil
	}, nil, false); err != nil {
		t.Fatal(err)
	}
	c := mustLoad(t, path)
	if len(c.Capabilities["google"]) != 1 || c.BuiltinBundle("google") || !c.AppBundle("google") {
		t.Fatalf("google %v", c.Capabilities["google"])
	}
}
