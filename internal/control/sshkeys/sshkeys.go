// Package sshkeys keeps the SSH public keys allowed into sandboxes, for
// editors like VS Code (Remote-SSH) on your own machines. sshd in a sandbox
// reads them from its read-only /etc/skybreaker, so an agent can't add its
// own, and every key is re-written in one canonical form: a parsed key type
// and blob and a comment of ours, so no authorized_keys option (command=,
// from=, ...) or second line can ride in on a key.
package sshkeys

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// types are the key types accepted, as OpenSSH names them.
var types = map[string]bool{
	"ssh-ed25519": true, "ssh-rsa": true,
	"ecdsa-sha2-nistp256": true, "ecdsa-sha2-nistp384": true, "ecdsa-sha2-nistp521": true,
	"sk-ssh-ed25519@openssh.com": true, "sk-ecdsa-sha2-nistp256@openssh.com": true,
}

const (
	maxBlob = 8 << 10
	maxName = 100
)

type Key struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"` // the machine, such as "laptop"
	Type        string    `json:"type"`
	Blob        string    `json:"blob"` // base64
	Fingerprint string    `json:"fingerprint"`
	AddedBy     string    `json:"added_by"`
	Added       time.Time `json:"added"`
}

func (k Key) line() string { return k.Type + " " + k.Blob + " skybreaker-" + k.ID }

// Parse reads one public key line ("type base64 [comment]"). Anything else
// (options, more lines, an unknown or mismatched type) is refused.
func Parse(line string) (Key, error) {
	line = strings.TrimSpace(line)
	if strings.ContainsAny(line, "\r\n") {
		return Key{}, errors.New("give one key, on one line")
	}
	f := strings.Fields(line)
	if len(f) < 2 || !types[f[0]] {
		return Key{}, errors.New("not an SSH public key (ssh-ed25519 AAAA..., without options)")
	}
	raw, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(raw) > maxBlob {
		return Key{}, errors.New("the key's data isn't a public key")
	}
	if len(raw) < 4 {
		return Key{}, errors.New("the key's data is truncated")
	}
	n := binary.BigEndian.Uint32(raw)
	if uint64(n) > uint64(len(raw)-4) || string(raw[4:4+n]) != f[0] || len(raw) == int(4+n) {
		return Key{}, errors.New("the key's data doesn't match its type")
	}
	sum := sha256.Sum256(raw)
	return Key{Type: f[0], Blob: base64.StdEncoding.EncodeToString(raw), Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])}, nil
}

// Store is the keys, in a JSON file.
type Store struct {
	path string
	mu   sync.Mutex
	keys []Key
}

func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.keys); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func (s *Store) List() []Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.keys)
}

var (
	// ErrInvalid is a key or name refused as given.
	ErrInvalid = errors.New("invalid")
	// ErrNotFound is a key that isn't there.
	ErrNotFound = errors.New("not found")
)

// failure is an error that reads as its message and matches its kind.
type failure struct {
	kind error
	msg  string
}

func (f failure) Error() string        { return f.msg }
func (f failure) Is(target error) bool { return target == f.kind }

// Add allows a key in, named for the machine it's on.
func (s *Store) Add(name, line, by string) (Key, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxName || strings.ContainsAny(name, "\r\n") {
		return Key{}, failure{ErrInvalid, fmt.Sprintf("name the key (up to %d characters)", maxName)}
	}
	k, err := Parse(line)
	if err != nil {
		return Key{}, failure{ErrInvalid, err.Error()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.keys {
		if o.Blob == k.Blob {
			return Key{}, failure{ErrInvalid, fmt.Sprintf("that key is already added, as %q", o.Name)}
		}
	}
	id := make([]byte, 6)
	rand.Read(id)
	k.ID, k.Name, k.AddedBy, k.Added = hex.EncodeToString(id), name, by, time.Now().UTC()
	s.keys = append(s.keys, k)
	if err := s.save(); err != nil {
		s.keys = s.keys[:len(s.keys)-1]
		return Key{}, err
	}
	return k, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.keys, func(k Key) bool { return k.ID == id })
	if i < 0 {
		return failure{ErrNotFound, fmt.Sprintf("no key %q", id)}
	}
	old := slices.Clone(s.keys)
	s.keys = slices.Delete(s.keys, i, i+1)
	if err := s.save(); err != nil {
		s.keys = old
		return err
	}
	return nil
}

// AuthorizedKeys is the keys as sshd reads them.
func (s *Store) AuthorizedKeys() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b bytes.Buffer
	for _, k := range s.keys {
		b.WriteString(k.line() + "\n")
	}
	return b.Bytes()
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.keys, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".ssh-keys-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
