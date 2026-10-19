package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub serves releases the way api.github.com does: metadata as JSON,
// assets by their API URL with Accept: application/octet-stream.
type fakeGitHub struct {
	*httptest.Server
	releases map[string]map[string][]byte // tag → asset name → content
	latest   string
	token    string // required when set
	sums     map[string]string
}

func newFakeGitHub(t *testing.T, latest string, releases map[string]map[string][]byte) *fakeGitHub {
	f := &fakeGitHub{releases: releases, latest: latest}
	if latest == "" {
		// GitHub's latest is the newest release that isn't a prerelease.
		for tag := range releases {
			if !strings.Contains(tag, "-") && (f.latest == "" || CompareVersions(tag, f.latest) > 0) {
				f.latest = tag
			}
		}
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// withChecksums adds a checksums.txt to every release, as the release
// workflow does.
func withChecksums(assets map[string][]byte) map[string][]byte {
	var b strings.Builder
	for name, data := range assets {
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	assets["checksums.txt"] = []byte(b.String())
	return assets
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
		http.NotFound(w, r) // GitHub hides private repos
		return
	}
	const base = "/repos/zaneGittins/skybreaker/releases/"
	path := strings.TrimPrefix(r.URL.Path, base)
	switch {
	case strings.HasPrefix(path, "assets/"):
		if r.Header.Get("Accept") != "application/octet-stream" {
			http.Error(w, "json", 415)
			return
		}
		tag, name, _ := strings.Cut(strings.TrimPrefix(path, "assets/"), "/")
		data, ok := f.releases[tag][name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	case r.URL.Path == "/repos/zaneGittins/skybreaker/releases":
		var list []map[string]any
		for tag := range f.releases {
			list = append(list, f.release(tag))
		}
		json.NewEncoder(w).Encode(list)
	case path == "latest" || strings.HasPrefix(path, "tags/"):
		tag := strings.TrimPrefix(path, "tags/")
		if path == "latest" {
			tag = f.latest
		}
		if _, ok := f.releases[tag]; !ok {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(f.release(tag))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) release(tag string) map[string]any {
	var list []map[string]string
	for name := range f.releases[tag] {
		list = append(list, map[string]string{"name": name, "url": f.URL + "/repos/zaneGittins/skybreaker/releases/assets/" + tag + "/" + name})
	}
	return map[string]any{"tag_name": tag, "prerelease": strings.Contains(tag, "-"), "assets": list}
}

func (f *fakeGitHub) releasesClient() Releases {
	return Releases{API: f.URL, Repo: "zaneGittins/skybreaker", Token: f.token, HTTP: f.Client()}
}

func TestReleaseAsset(t *testing.T) {
	gh := newFakeGitHub(t, "v1.1.0", map[string]map[string][]byte{
		"v1.0.0": withChecksums(map[string][]byte{"skybreaker-linux-amd64": []byte("old")}),
		"v1.1.0": withChecksums(map[string][]byte{"skybreaker-linux-amd64": []byte("new")}),
	})
	r := gh.releasesClient()
	ctx := context.Background()
	for tag, want := range map[string]string{"": "new", "dev": "new", "v1.0.0": "old"} {
		rel, err := r.Get(ctx, tag)
		if err != nil {
			t.Fatalf("%q: %v", tag, err)
		}
		got, err := r.Asset(ctx, rel, "skybreaker-linux-amd64")
		if err != nil || string(got) != want {
			t.Errorf("%q: %q, %v; want %q", tag, got, err, want)
		}
	}
	if _, err := r.Get(ctx, "v9.9.9"); err == nil {
		t.Error("found a release that doesn't exist")
	}
	rel, _ := r.Get(ctx, "v1.1.0")
	if _, err := r.Asset(ctx, rel, "skybreaker-linux-riscv64"); err == nil || !strings.Contains(err.Error(), "riscv64") {
		t.Errorf("missing asset: %v", err)
	}
}

func TestReleaseAssetChecksums(t *testing.T) {
	sum := sha256.Sum256([]byte("good"))
	gh := newFakeGitHub(t, "v1.0.0", map[string]map[string][]byte{"v1.0.0": {
		"skybreaker-linux-amd64": []byte("tampered"),
		"skybreaker-linux-arm64": []byte("unlisted"),
		"checksums.txt":          []byte(hex.EncodeToString(sum[:]) + "  skybreaker-linux-amd64\n"),
	}})
	r := gh.releasesClient()
	rel, err := r.Get(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Asset(context.Background(), rel, "skybreaker-linux-amd64"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("tampered asset: %v", err)
	}
	// An asset the release doesn't vouch for is refused, not trusted.
	if _, err := r.Asset(context.Background(), rel, "skybreaker-linux-arm64"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("unlisted asset: %v", err)
	}
}

func TestReleasePrivateRepo(t *testing.T) {
	gh := newFakeGitHub(t, "v1.0.0", map[string]map[string][]byte{
		"v1.0.0": withChecksums(map[string][]byte{"skybreaker-linux-amd64": []byte("bin")}),
	})
	gh.token = "ghp_x"
	r := gh.releasesClient()
	r.Token = ""
	if _, err := r.Get(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Errorf("no hint about a private repo: %v", err)
	}
	r.Token = "ghp_x"
	rel, err := r.Get(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.Asset(context.Background(), rel, "skybreaker-linux-amd64"); err != nil || string(got) != "bin" {
		t.Fatalf("%q, %v", got, err)
	}
}

// A prerelease or dev build follows prereleases; a release follows
// GitHub's latest, which never is one.
func TestReleaseChannels(t *testing.T) {
	bin := func(s string) map[string][]byte {
		return withChecksums(map[string][]byte{"skybreaker-linux-amd64": []byte(s)})
	}
	gh := newFakeGitHub(t, "", map[string]map[string][]byte{
		"v1.0.0": bin("a"), "v1.1.0-dev.1": bin("b"), "v1.1.0-dev.2": bin("c"), "v0.9.0": bin("d"),
	})
	r := gh.releasesClient()
	ctx := context.Background()
	for _, c := range []struct{ version, want string }{
		{"", "v1.0.0"}, {"latest", "v1.0.0"}, {"v1.0.0", "v1.0.0"},
		{"dev", "v1.1.0-dev.2"}, {Channel("v1.1.0-dev.1"), "v1.1.0-dev.2"}, {Channel("v0.9.0"), "v1.0.0"},
	} {
		rel, err := r.Get(ctx, c.version)
		if err != nil || rel.Tag != c.want {
			t.Errorf("Get(%q) = %v, %v; want %s", c.version, rel, err, c.want)
		}
	}
	only := newFakeGitHub(t, "", map[string]map[string][]byte{"v0.0.1-dev.1": bin("x")})
	if _, err := only.releasesClient().Get(ctx, ""); err == nil {
		t.Error("GitHub has no latest with only prereleases")
	}
	if rel, err := only.releasesClient().Get(ctx, "dev"); err != nil || rel.Tag != "v0.0.1-dev.1" {
		t.Errorf("dev with only prereleases: %v, %v", rel, err)
	}
}
