package scheduler

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// openRunFile opens <runs>/<id>/<name>. The runs directory is writable from
// inside the sandbox, so the lookup goes through an os.Root at <runs>: a
// symlink planted anywhere below it can't lead out. The file must be a
// regular file reached without following a final symlink, and non-blocking
// open keeps a planted FIFO from hanging the caller.
func openRunFile(path string) (*os.File, error) {
	dir, name := filepath.Split(filepath.Clean(path))
	runs, id := filepath.Split(filepath.Clean(dir))
	root, err := os.OpenRoot(runs)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(filepath.Join(id, name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return f, nil
}

// ReadRunFile reads the last max bytes of a run file: a harness's result
// comes at the end of its log.
func ReadRunFile(path string, max int64) ([]byte, error) {
	f, err := openRunFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > max {
		if _, err := f.Seek(fi.Size()-max, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(io.LimitReader(f, max))
}

// realDir checks that path is a directory and not a symlink to one.
func realDir(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New(path + " is not a plain directory")
	}
	return nil
}
