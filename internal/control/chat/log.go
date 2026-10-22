package chat

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Log keeps each session's stored events, one JSON line each, in a file
// under its directory. Only the control plane writes there; sandboxes can't
// reach it. A session's events are held in memory once read.
type Log struct {
	dir string

	mu       sync.Mutex
	sessions map[string]*sessionLog
	kept     map[string]map[string][]byte // by session and shelf, in memory only when dir is empty
}

type sessionLog struct {
	events []Event
	seq    int64
}

// maxLogEvents caps what one session keeps; past it the oldest go from
// memory (the file keeps them until the session is deleted).
const maxLogEvents = 20000

var logName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// OpenLog keeps logs in dir. An empty dir keeps them in memory only.
func OpenLog(dir string) (*Log, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	return &Log{dir: dir, sessions: map[string]*sessionLog{}, kept: map[string]map[string][]byte{}}, nil
}

func (l *Log) path(id string) string { return filepath.Join(l.dir, id+".jsonl") }

// load reads a session's file the first time it's needed. Callers hold mu.
func (l *Log) load(id string) (*sessionLog, error) {
	if s, ok := l.sessions[id]; ok {
		return s, nil
	}
	if !logName.MatchString(id) {
		return nil, errors.New("chat: bad session id")
	}
	s := &sessionLog{}
	if l.dir != "" {
		f, err := os.Open(l.path(id))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(nil, 4<<20)
			for sc.Scan() {
				var e Event
				if json.Unmarshal(sc.Bytes(), &e) != nil || e.Seq <= s.seq {
					continue // a torn last line from a crash
				}
				s.events = append(s.events, e)
				s.seq = e.Seq
			}
			f.Close()
			if len(s.events) > maxLogEvents {
				s.events = s.events[len(s.events)-maxLogEvents:]
			}
		}
	}
	l.sessions[id] = s
	return s, nil
}

// Append stores e with the session's next sequence number and returns it.
func (l *Log) Append(id string, e Event) (Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, err := l.load(id)
	if err != nil {
		return e, err
	}
	e.Seq = s.seq + 1
	if l.dir != "" {
		b, err := json.Marshal(e)
		if err != nil {
			return e, err
		}
		f, err := os.OpenFile(l.path(id), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return e, err
		}
		_, err = f.Write(append(b, '\n'))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return e, err
		}
	}
	s.seq = e.Seq
	s.events = append(s.events, e)
	if len(s.events) > maxLogEvents+maxLogEvents/10 {
		s.events = append([]Event(nil), s.events[len(s.events)-maxLogEvents:]...)
	}
	return e, nil
}

// Since returns the session's stored events after seq.
func (l *Log) Since(id string, seq int64) ([]Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, err := l.load(id)
	if err != nil {
		return nil, err
	}
	i := len(s.events)
	for i > 0 && s.events[i-1].Seq > seq {
		i--
	}
	return append([]Event(nil), s.events[i:]...), nil
}

// Copy appends session from's events, and the files they show, to session
// to's log, numbered afresh.
func (l *Log) Copy(from, to string) error {
	if !logName.MatchString(to) {
		return errors.New("chat: bad session id")
	}
	events, err := l.Since(from, 0)
	if err != nil {
		return err
	}
	copyKept := func(name string, keep func(string, []byte, string) (string, error)) error {
		b, err := l.Kept(from, name)
		if err != nil {
			return nil
		}
		_, err = keep(to, b, filepath.Ext(name))
		return err
	}
	for _, e := range events {
		if e.Kind == KindShow {
			if err := copyKept(e.ID, l.Keep); err != nil {
				return err
			}
		}
		for _, a := range e.Attach {
			if err := copyKept(a.Kept, l.KeepAttached); err != nil {
				return err
			}
			if err := copyKept(a.Thumb, l.KeepThumb); err != nil {
				return err
			}
		}
		e.Seq = 0
		if _, err := l.Append(to, e); err != nil {
			return err
		}
	}
	return nil
}

// Delete drops a session's log.
func (l *Log) Delete(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.sessions, id)
	for _, sh := range shelves {
		delete(l.kept, id+sh.suffix)
	}
	if l.dir == "" || !logName.MatchString(id) {
		return nil
	}
	for _, sh := range shelves {
		if err := os.RemoveAll(l.shelfDir(id, *sh)); err != nil {
			return err
		}
	}
	if err := os.Remove(l.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Kept copies of files live beside the session's log, named by their
// content, on one shelf for files agents show and another for files the
// user attaches. Each shelf has its own quota, so neither can crowd out
// the other, and an agent can't fill the disk by showing things.
type shelf struct {
	suffix   string
	what     string
	maxBytes int64
	maxFiles int
}

var (
	shownShelf    = shelf{".shown", "shown files", 64 << 20, 500}
	attachedShelf = shelf{".attached", "attachments", 256 << 20, 500}
	thumbShelf    = shelf{".thumbs", "thumbnails", 32 << 20, 1000}
	shelves       = []*shelf{&shownShelf, &attachedShelf, &thumbShelf}
)

// keptFailed logs why a kept file couldn't be read or written, which names
// paths on the host, and says only that.
func keptFailed(err error) error {
	log.Printf("skybreaker: kept files: %v", err)
	return errors.New("chat: couldn't keep the file")
}

var (
	keptPattern = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|gif|webp|txt|bin)$`)
	keptExt     = map[string]bool{".png": true, ".jpg": true, ".gif": true, ".webp": true, ".txt": true, ".bin": true}
)

// keptName is the name data is kept under.
func keptName(data []byte, ext string) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16]) + ext
}

func (l *Log) shelfDir(id string, sh shelf) string { return filepath.Join(l.dir, id+sh.suffix) }

// Keep stores a copy of a shown file for session id and returns its name.
func (l *Log) Keep(id string, data []byte, ext string) (string, error) {
	return l.keep(id, shownShelf, data, ext)
}

// KeepAttached stores a copy of a file the user attached.
func (l *Log) KeepAttached(id string, data []byte, ext string) (string, error) {
	return l.keep(id, attachedShelf, data, ext)
}

// KeepThumb stores the thumbnail of an image the user attached.
func (l *Log) KeepThumb(id string, data []byte, ext string) (string, error) {
	return l.keep(id, thumbShelf, data, ext)
}

// AttachRoom reports whether files fit on session id's attachment shelf.
func (l *Log) AttachRoom(id string, files [][]byte) error {
	if !logName.MatchString(id) {
		return errors.New("chat: bad session id")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	has, used, n, err := l.shelfUse(id, attachedShelf)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, f := range files {
		name := keptName(f, "")
		if has(name) || seen[name] {
			continue
		}
		seen[name] = true
		used += int64(len(f))
		n++
	}
	if n > attachedShelf.maxFiles || used > attachedShelf.maxBytes {
		return attachedShelf.full()
	}
	return nil
}

func (sh shelf) full() error {
	return fmt.Errorf("this chat has kept its limit of %s (%d MB or %d files)", sh.what, sh.maxBytes>>20, sh.maxFiles)
}

// shelfUse is what's on a session's shelf: whether a file with the given
// content name (without extension) is there, and the bytes and files used.
// Callers hold mu.
func (l *Log) shelfUse(id string, sh shelf) (has func(string) bool, used int64, n int, err error) {
	names := map[string]bool{}
	has = func(name string) bool { return names[name] }
	if l.dir == "" {
		for name, b := range l.kept[id+sh.suffix] {
			names[strings.TrimSuffix(name, filepath.Ext(name))] = true
			used += int64(len(b))
			n++
		}
		return has, used, n, nil
	}
	entries, err := os.ReadDir(l.shelfDir(id, sh))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, 0, 0, keptFailed(err)
	}
	for _, e := range entries {
		if !keptPattern.MatchString(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil {
			names[strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))] = true
			used += info.Size()
			n++
		}
	}
	return has, used, n, nil
}

func (l *Log) keep(id string, sh shelf, data []byte, ext string) (string, error) {
	if !logName.MatchString(id) {
		return "", errors.New("chat: bad session id")
	}
	if !keptExt[ext] {
		return "", fmt.Errorf("chat: can't keep a %q file", ext)
	}
	name := keptName(data, ext)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dir == "" {
		files := l.kept[id+sh.suffix]
		if _, ok := files[name]; ok {
			return name, nil
		}
		if err := l.room(id, sh, len(data)); err != nil {
			return "", err
		}
		if files == nil {
			files = map[string][]byte{}
			l.kept[id+sh.suffix] = files
		}
		files[name] = append([]byte(nil), data...)
		return name, nil
	}
	dir := l.shelfDir(id, sh)
	if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
		return name, nil
	}
	if err := l.room(id, sh, len(data)); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", keptFailed(err)
	}
	tmp, err := os.CreateTemp(dir, ".keep-*")
	if err != nil {
		return "", keptFailed(err)
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(dir, name))
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", keptFailed(err)
	}
	return name, nil
}

// room reports whether one more file of size bytes fits. Callers hold mu.
func (l *Log) room(id string, sh shelf, size int) error {
	_, used, n, err := l.shelfUse(id, sh)
	if err != nil {
		return err
	}
	if n >= sh.maxFiles || used+int64(size) > sh.maxBytes {
		return sh.full()
	}
	return nil
}

// Kept returns a kept file from either shelf. Anything but a well-formed
// name of this session's is fs.ErrNotExist.
func (l *Log) Kept(id, name string) ([]byte, error) {
	if !logName.MatchString(id) || !keptPattern.MatchString(name) {
		return nil, fs.ErrNotExist
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, sh := range shelves {
		if l.dir == "" {
			if b, ok := l.kept[id+sh.suffix][name]; ok {
				return b, nil
			}
			continue
		}
		root, err := os.OpenRoot(l.shelfDir(id, *sh))
		if err != nil {
			continue
		}
		b, err := root.ReadFile(name)
		root.Close()
		if err == nil {
			return b, nil
		}
	}
	return nil, fs.ErrNotExist
}
