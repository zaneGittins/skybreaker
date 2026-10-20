// Package audit writes the broker's append-only decision log: one JSON line
// per request decision, approval and session change.
package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

type Entry struct {
	Time       time.Time `json:"time"`
	Sender     string    `json:"sender"`
	Project    string    `json:"project,omitempty"`
	Mode       string    `json:"mode,omitempty"`
	Host       string    `json:"host,omitempty"`
	Method     string    `json:"method,omitempty"`
	Path       string    `json:"path,omitempty"`
	Decision   string    `json:"decision"`
	Reason     string    `json:"reason,omitempty"`
	Status     int       `json:"status,omitempty"`
	ApprovalID string    `json:"approval_id,omitempty"`
	By         string    `json:"by,omitempty"`
}

type Logger struct {
	mu sync.Mutex
	w  io.Writer
	c  io.Closer
}

// Open appends to path, creating it (0600) and its directory (0700). O_APPEND
// means every record lands at the end even if something else holds the file.
func Open(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	return &Logger{w: f, c: f}, nil
}

func New(w io.Writer) *Logger { return &Logger{w: w} }

// Log writes e as a single line. encoding/json escapes control characters,
// so a hostile path can't forge a second record.
func (l *Logger) Log(e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Time = e.Time.UTC()
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.w.Write(b); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}

func (l *Logger) Close() error {
	if l.c == nil {
		return nil
	}
	return l.c.Close()
}

// Tail returns the last n well-formed entries, oldest first. It reads the
// file backwards so a long log costs only what is returned.
func Tail(path string, n int) ([]Entry, error) {
	if n <= 0 {
		return nil, nil
	}
	var out []Entry // newest first while collecting
	err := Back(path, func(e Entry) bool {
		out = append(out, e)
		return len(out) < n
	})
	if err != nil {
		return nil, err
	}
	slices.Reverse(out)
	return out, nil
}

// Back calls each with the log's well-formed entries, newest first, until
// it returns false. It reads the file backwards in chunks, so stopping
// early costs only what was read.
func Back(path string, each func(Entry) bool) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	const chunk = 64 << 10
	var (
		tail []byte // partial line carried between chunks
		pos  = fi.Size()
	)
	for pos > 0 {
		size := int64(chunk)
		if pos < size {
			size = pos
		}
		pos -= size
		buf := make([]byte, size, size+int64(len(tail)))
		if _, err := f.ReadAt(buf, pos); err != nil {
			return fmt.Errorf("audit: %w", err)
		}
		buf = append(buf, tail...)
		lines := bytes.Split(buf, []byte{'\n'})
		// The first piece may be the end of a line that starts in an earlier chunk.
		tail = lines[0]
		if pos == 0 {
			tail = nil
		} else {
			lines = lines[1:]
		}
		for i := len(lines) - 1; i >= 0; i-- {
			if e, ok := parse(lines[i]); ok && !each(e) {
				return nil
			}
		}
	}
	return nil
}

func parse(line []byte) (Entry, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return Entry{}, false
	}
	var e Entry
	if err := json.Unmarshal(line, &e); err != nil {
		return Entry{}, false
	}
	return e, true
}
