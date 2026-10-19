// Package skybreaker carries what an install lays down beside the binary:
// the systemd units and helpers in deploy/ and the sandbox image's build
// context. skybreaker-setup (the setup module) installs them from here, so
// they never drift from the repository's copies.
package skybreaker

import "embed"

//go:embed deploy images/sandbox
var Files embed.FS
