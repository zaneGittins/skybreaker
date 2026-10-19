package chat

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// Log keeps each session's stored events, one JSON line each, in a file
// under its directory. Only the control plane writes there; sandboxes can't
// reach it. A session's events are held in memory once read.
type Log struct {
	dir string

	mu       sync.Mutex
	sessions map[string]*sessionLog
	shown    map[string]map[string][]byte // in memory only, when dir is empty
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
	return &Log{dir: dir, sessions: map[string]*sessionLog{}, shown: map[string]map[string][]byte{}}, nil
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

// Delete drops a session's log.
func (l *Log) Delete(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.sessions, id)
	delete(l.shown, id)
	if l.dir == "" || !logName.MatchString(id) {
		return nil
	}
	if err := os.RemoveAll(l.shownDir(id)); err != nil {
		return err
	}
	if err := os.Remove(l.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Kept copies of files shown to the user live beside the session's log,
// named by their content. Each session has a quota, so an agent can't fill
// the disk by showing things.
const (
	maxShownBytes = 64 << 20
	maxShownFiles = 500
)

var (
	shownName = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|gif|webp|txt)$`)
	shownExt  = map[string]bool{".png": true, ".jpg": true, ".gif": true, ".webp": true, ".txt": true}
)

func (l *Log) shownDir(id string) string { return filepath.Join(l.dir, id+".shown") }

// Keep stores a copy of a shown file for session id and returns its name.
func (l *Log) Keep(id string, data []byte, ext string) (string, error) {
	if !logName.MatchString(id) {
		return "", errors.New("chat: bad session id")
	}
	if !shownExt[ext] {
		return "", fmt.Errorf("chat: can't keep a %q file", ext)
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:16]) + ext
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dir == "" {
		files := l.shown[id]
		if _, ok := files[name]; ok {
			return name, nil
		}
		var used int64
		for _, b := range files {
			used += int64(len(b))
		}
		if err := shownRoom(used, len(files), len(data)); err != nil {
			return "", err
		}
		if files == nil {
			files = map[string][]byte{}
			l.shown[id] = files
		}
		files[name] = append([]byte(nil), data...)
		return name, nil
	}
	dir := l.shownDir(id)
	if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
		return name, nil
	}
	var used int64
	n := 0
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			used += info.Size()
			n++
		}
	}
	if err := shownRoom(used, n, len(data)); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".keep-*")
	if err != nil {
		return "", err
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
		return "", err
	}
	return name, nil
}

func shownRoom(used int64, files, more int) error {
	if files >= maxShownFiles || used+int64(more) > maxShownBytes {
		return fmt.Errorf("this chat has shown its limit of files (%d MB or %d files)", maxShownBytes>>20, maxShownFiles)
	}
	return nil
}

// Kept returns a kept file. Anything but a well-formed name of this
// session's is fs.ErrNotExist.
func (l *Log) Kept(id, name string) ([]byte, error) {
	if !logName.MatchString(id) || !shownName.MatchString(name) {
		return nil, fs.ErrNotExist
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dir == "" {
		b, ok := l.shown[id][name]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return b, nil
	}
	root, err := os.OpenRoot(l.shownDir(id))
	if err != nil {
		return nil, fs.ErrNotExist
	}
	defer root.Close()
	return root.ReadFile(name)
}
