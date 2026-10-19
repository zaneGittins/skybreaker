package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const repo = "zaneGittins/skybreaker"

// AssetName is this platform's binary in a release.
func AssetName() string { return "skybreaker-" + runtime.GOOS + "-" + runtime.GOARCH }

// Updater replaces this binary with the latest release's, after checking
// it against the release's checksums.txt.
type Updater struct {
	API     string // https://api.github.com
	Exe     string // the binary to replace
	Current string
	Token   string // optional: GITHUB_TOKEN, for a private fork
	HTTP    *http.Client
	// ServerInstalled reports that Exe is the server's own binary, which
	// skybreaker-setup manages.
	ServerInstalled func() bool
}

func DefaultUpdater(current string) (Updater, error) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return Updater{}, err
	}
	tok := os.Getenv("GITHUB_TOKEN")
	if tok == "" {
		tok = os.Getenv("GH_TOKEN")
	}
	return Updater{API: "https://api.github.com", Exe: exe, Current: current, Token: tok, HTTP: http.DefaultClient,
		ServerInstalled: func() bool {
			_, err := os.Stat("/etc/skybreaker")
			return exe == "/usr/local/bin/skybreaker" && err == nil
		}}, nil
}

type releaseAsset struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Download string `json:"browser_download_url"`
}

func (u Updater) Run(ctx context.Context) (string, error) {
	if u.ServerInstalled != nil && u.ServerInstalled() {
		return "", errors.New("this is the Skybreaker server's own binary; upgrade it with: sudo skybreaker-setup upgrade")
	}
	b, err := u.get(ctx, u.API+"/repos/"+repo+"/releases/latest", "application/vnd.github+json", 1<<20)
	if err != nil {
		return "", fmt.Errorf("couldn't check for a new release: %w", err)
	}
	var rel struct {
		Tag    string         `json:"tag_name"`
		Assets []releaseAsset `json:"assets"`
	}
	if err := json.Unmarshal(b, &rel); err != nil {
		return "", err
	}
	if CompareVersions(u.Current, rel.Tag) >= 0 {
		return "skybreaker is up to date (" + rel.Tag + ")", nil
	}
	bin, err := u.asset(ctx, rel.Assets, AssetName(), 200<<20)
	if err != nil {
		return "", err
	}
	sums, err := u.asset(ctx, rel.Assets, "checksums.txt", 1<<20)
	if err != nil {
		return "", err
	}
	if err := verify(sums, AssetName(), bin); err != nil {
		return "", err
	}
	if err := replace(u.Exe, bin); err != nil {
		return "", err
	}
	return "Updated skybreaker to " + rel.Tag, nil
}

func (u Updater) asset(ctx context.Context, assets []releaseAsset, name string, limit int64) ([]byte, error) {
	for _, a := range assets {
		if a.Name != name {
			continue
		}
		// Without a token, the public download; with one, the API, which
		// also serves private repositories.
		if u.Token != "" {
			return u.get(ctx, a.URL, "application/octet-stream", limit)
		}
		return u.get(ctx, a.Download, "", limit)
	}
	return nil, fmt.Errorf("the latest release has no %s", name)
}

func (u Updater) get(ctx context.Context, url, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if u.Token != "" {
		req.Header.Set("Authorization", "Bearer "+u.Token)
	}
	c := u.HTTP
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("%s: larger than expected", url)
	}
	return b, err
}

func verify(sums []byte, name string, bin []byte) error {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			got := sha256.Sum256(bin)
			if hex.EncodeToString(got[:]) != strings.ToLower(f[0]) {
				return fmt.Errorf("%s doesn't match the release's checksum; not updating", name)
			}
			return nil
		}
	}
	return fmt.Errorf("checksums.txt has no checksum for %s; not updating", name)
}

// replace swaps exe for bin in one rename, so a failure leaves the old one.
func replace(exe string, bin []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".skybreaker-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil { // on disk before it takes the old one's place
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), exe)
}

// CompareVersions orders release tags: v0.2.0-dev.1 < v0.2.0 < v0.2.1.
func CompareVersions(a, b string) int {
	ac, ap, _ := strings.Cut(strings.TrimPrefix(a, "v"), "-")
	bc, bp, _ := strings.Cut(strings.TrimPrefix(b, "v"), "-")
	if c := compareDotted(ac, bc); c != 0 {
		return c
	}
	switch {
	case ap == bp:
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	}
	return compareDotted(ap, bp)
}

func compareDotted(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil && xn != yn:
			if xn < yn {
				return -1
			}
			return 1
		case (xerr != nil || yerr != nil) && x != y:
			return strings.Compare(x, y)
		}
	}
	return 0
}
