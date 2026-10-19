// Package configfile loads the config files of both binaries strictly:
// unknown fields are errors, because a misspelt key in a policy file would
// otherwise drop a rule without a word.
package configfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Load decodes the file at path into v.
func Load(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := decode(path, b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// DecodeJSON is the strict decoder every format ends in: YAML is converted
// to JSON first, so both refuse unknown fields the same way.
func DecodeJSON(b []byte, v any) error {
	if err := checkDuplicateKeys(b); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the config document")
	}
	return nil
}

// checkDuplicateKeys refuses an object that names a key twice, ignoring
// case. encoding/json matches field names case-insensitively and keeps the
// last value, so "Hosts" followed by "hosts" would silently drop a block.
func checkDuplicateKeys(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	var walk func() error
	walk = func() error {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return err
				}
				k := strings.ToLower(kt.(string))
				if seen[k] {
					return fmt.Errorf("duplicate key %q (keys are matched ignoring case)", kt)
				}
				seen[k] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token() // the closing brace
			return err
		case json.Delim('['):
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	return walk()
}

// Duration is a time.Duration written as "30s" or "15m".
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

// Or returns def when d is unset.
func (d Duration) Or(def time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return time.Duration(d)
}

func (d *Duration) set(s string) error {
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	if v < 0 {
		return fmt.Errorf("negative duration %q", s)
	}
	*d = Duration(v)
	return nil
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	return d.set(s)
}

func (d *Duration) UnmarshalText(b []byte) error { return d.set(string(b)) }

func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// ReadSecret reads a one-line secret file, refusing files that others on the
// machine can read.
func ReadSecret(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is readable by group or others (mode %v); chmod 600 it", path, fi.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return v, nil
}
