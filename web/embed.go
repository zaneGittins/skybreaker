// Package web holds Skybreaker's UI: plain HTML, CSS and JavaScript with no
// build step, embedded into the binary. shared/ has the theme, fonts and
// pure helpers; skybreaker/ is the app, layered over it.
package web

import (
	"embed"
	"io/fs"
	"slices"
	"strings"
)

//go:embed shared skybreaker
var files embed.FS

// Skybreaker is the UI.
func Skybreaker() fs.FS { return layered("skybreaker") }

func layered(dir string) fs.FS {
	top, _ := fs.Sub(files, dir)
	shared, _ := fs.Sub(files, "shared")
	return overlay{top, shared}
}

// overlay serves each name from the first layer that has it. The test file
// lib.test.js stays out of the served set.
type overlay []fs.FS

func (o overlay) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, ".test.js") {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	var firstErr error
	for _, l := range o {
		f, err := l.Open(name)
		if err == nil {
			return f, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

func (o overlay) ReadDir(name string) ([]fs.DirEntry, error) {
	seen := map[string]bool{}
	var out []fs.DirEntry
	found := false
	for _, l := range o {
		entries, err := fs.ReadDir(l, name)
		if err != nil {
			continue
		}
		found = true
		for _, e := range entries {
			if !seen[e.Name()] && !strings.HasSuffix(e.Name(), ".test.js") {
				seen[e.Name()] = true
				out = append(out, e)
			}
		}
	}
	if !found {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	slices.SortFunc(out, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return out, nil
}
