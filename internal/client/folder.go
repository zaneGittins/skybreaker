package client

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zaneGittins/skybreaker/internal/names"
)

// Folder is what `skybreaker new-project` would copy, for you to confirm.
type Folder struct {
	Files   int      // files and links
	Bytes   int64    // in the files
	Secrets []string // paths that look like credentials
	Skipped []string // sockets, pipes and devices, which can't be copied
}

// secretName matches file names that usually hold credentials.
var secretName = regexp.MustCompile(`^(\.env(\..+)?|.+\.(pem|key|p12|pfx)|id_(rsa|dsa|ecdsa|ed25519)|\.netrc|\.npmrc|\.pypirc)$`)

// Templates of an .env hold no values.
var secretTemplate = regexp.MustCompile(`^\.env\.(example|sample|template)$`)

// walkFolder calls each for everything under root, by its slash-separated
// path, without following links. Kinds tar can't carry go to skip.
func walkFolder(root string, each func(rel string, d fs.DirEntry) error, skip func(rel string)) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if t := d.Type(); t&(fs.ModeSocket|fs.ModeNamedPipe|fs.ModeDevice|fs.ModeCharDevice|fs.ModeIrregular) != 0 {
			skip(rel)
			return nil
		}
		return each(rel, d)
	})
}

// ScanFolder counts what's in root and picks out what looks secret.
func ScanFolder(root string) (Folder, error) {
	var f Folder
	err := walkFolder(root, func(rel string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		f.Files++
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			f.Bytes += info.Size()
			if base := path.Base(rel); secretName.MatchString(base) && !secretTemplate.MatchString(base) {
				f.Secrets = append(f.Secrets, rel)
			}
		}
		return nil
	}, func(rel string) { f.Skipped = append(f.Skipped, rel) })
	return f, err
}

// WriteArchive writes root to w as a gzipped tar: everything in it, dot
// files and directories too. Links stay links; owners are left out.
func WriteArchive(w io.Writer, root string) error {
	zw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	tw := tar.NewWriter(zw)
	err := walkFolder(root, func(rel string, d fs.DirEntry) error {
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if d.Type()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				return err
			}
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name, h.Uid, h.Gid, h.Uname, h.Gname = rel, 0, 0, "", ""
		if d.IsDir() {
			h.Name += "/"
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			return nil
		}
		file, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		defer file.Close()
		if _, err := io.CopyN(tw, file, h.Size); err != nil {
			return fmt.Errorf("%s: %w (did it change while being copied?)", rel, err)
		}
		return nil
	}, func(string) {})
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = zw.Close()
	}
	return err
}

// githubRemote matches the GitHub URLs git accepts for a repo.
var githubRemote = regexp.MustCompile(`^(?:https://(?:[^@/]+@)?github\.com/|git@github\.com:|ssh://git@github\.com/)([^/]+/[^/]+?)(?:\.git)?/?$`)

// GitHubRepo is the "owner/name" root's own repo has as its origin on
// GitHub, or "" for none. A folder inside a repo has none: the repo's
// clone would put the rest of it around the folder.
func GitHubRepo(ctx context.Context, run Runner, root string) string {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return ""
	}
	out, err := run(ctx, "git", "-C", root, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	m := githubRemote.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil || !names.Repo(m[1]) {
		return ""
	}
	return m[1]
}

var notName = regexp.MustCompile(`[^a-z0-9]+`)

// ProjectNameFor suggests a project name from a folder's: "My App" is
// "my-app". It's "" when nothing usable is left.
func ProjectNameFor(dir string) string {
	n := strings.Trim(notName.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-"), "-")
	if len(n) > 40 {
		n = strings.TrimRight(n[:40], "-")
	}
	if !names.Project(n) {
		return ""
	}
	return n
}
