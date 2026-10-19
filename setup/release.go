package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Releases fetches Skybreaker's release assets from GitHub. Assets are
// fetched through the API, so a private repository works the same way
// given a token.
type Releases struct {
	API   string // https://api.github.com
	Repo  string // owner/name
	Token string // GITHUB_TOKEN, for a private repository
	HTTP  *http.Client
}

type Release struct {
	Tag    string
	assets map[string]string // name → API URL
}

// DefaultReleases reads a token from GITHUB_TOKEN or GH_TOKEN.
func DefaultReleases() Releases {
	tok := os.Getenv("GITHUB_TOKEN")
	if tok == "" {
		tok = os.Getenv("GH_TOKEN")
	}
	return Releases{API: "https://api.github.com", Repo: "zaneGittins/skybreaker", Token: tok, HTTP: &http.Client{Timeout: 10 * time.Minute}}
}

// Channel is what a build at version follows: "dev", the newest release
// prereleases included, for a prerelease or a dev build, and "" (GitHub's
// latest, never a prerelease) for a release.
func Channel(version string) string {
	if version == "dev" || strings.Contains(version, "-") {
		return "dev"
	}
	return ""
}

// Get finds a release by tag. "" and "latest" mean GitHub's latest; "dev"
// means the newest, prereleases included.
func (r Releases) Get(ctx context.Context, tag string) (*Release, error) {
	if tag == "dev" {
		return r.newest(ctx)
	}
	path := "/releases/latest"
	if tag != "" && tag != "latest" {
		path = "/releases/tags/" + url.PathEscape(tag)
	}
	body, err := r.get(ctx, r.API+"/repos/"+r.Repo+path, "application/vnd.github+json", 1<<20)
	if err != nil {
		if errors.Is(err, errNotFound) {
			hint := ""
			if r.Token == "" {
				hint = " (a private repository needs GITHUB_TOKEN)"
			}
			return nil, fmt.Errorf("no release %s of %s%s", orLatest(tag), r.Repo, hint)
		}
		return nil, err
	}
	var v apiRelease
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("release %s: %w", orLatest(tag), err)
	}
	return v.release(), nil
}

type apiRelease struct {
	Tag    string `json:"tag_name"`
	Draft  bool   `json:"draft"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"assets"`
}

func (v apiRelease) release() *Release {
	rel := &Release{Tag: v.Tag, assets: map[string]string{}}
	for _, a := range v.Assets {
		rel.assets[a.Name] = a.URL
	}
	return rel
}

// newest is the highest-versioned release, prereleases included.
func (r Releases) newest(ctx context.Context) (*Release, error) {
	body, err := r.get(ctx, r.API+"/repos/"+r.Repo+"/releases?per_page=50", "application/vnd.github+json", 8<<20)
	if errors.Is(err, errNotFound) && r.Token == "" {
		return nil, fmt.Errorf("no releases of %s (a private repository needs GITHUB_TOKEN)", r.Repo)
	}
	if err != nil {
		return nil, err
	}
	var list []apiRelease
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("releases: %w", err)
	}
	var best *apiRelease
	for i, v := range list {
		if !v.Draft && (best == nil || CompareVersions(v.Tag, best.Tag) > 0) {
			best = &list[i]
		}
	}
	if best == nil {
		return nil, fmt.Errorf("%s has no releases yet", r.Repo)
	}
	return best.release(), nil
}

// Asset downloads one asset and checks it against the release's
// checksums.txt. An asset the checksums don't list is refused.
func (r Releases) Asset(ctx context.Context, rel *Release, name string) ([]byte, error) {
	sums, err := r.fetch(ctx, rel, "checksums.txt", 1<<20)
	if err != nil {
		return nil, err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if sum, file, ok := strings.Cut(strings.TrimSpace(line), "  "); ok && strings.TrimPrefix(file, "*") == name {
			want = sum
		}
	}
	if want == "" {
		return nil, fmt.Errorf("%s %s: no checksum in checksums.txt", rel.Tag, name)
	}
	data, err := r.fetch(ctx, rel, name, 512<<20)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want {
		return nil, fmt.Errorf("%s %s: checksum mismatch", rel.Tag, name)
	}
	return data, nil
}

func (r Releases) fetch(ctx context.Context, rel *Release, name string, limit int64) ([]byte, error) {
	u, ok := rel.assets[name]
	if !ok {
		return nil, fmt.Errorf("release %s has no %s", rel.Tag, name)
	}
	b, err := r.get(ctx, u, "application/octet-stream", limit)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", rel.Tag, name, err)
	}
	return b, nil
}

var errNotFound = errors.New("not found")

func (r Releases) get(ctx context.Context, u, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if r.Token != "" {
		// Go drops it on the redirect to the asset store's own host.
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("larger than %d bytes", limit)
	}
	return b, nil
}

func orLatest(tag string) string {
	if tag == "" {
		return "latest"
	}
	return tag
}
