package sandbox_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
)

// check-protocols.sh, which gates every new image, checks each flag the
// chat drivers start their harness with, so a CLI that drops one isn't
// promoted.
func TestCheckProtocolsCoversDriverFlags(t *testing.T) {
	script, err := os.ReadFile("check-protocols.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "pi"} {
		d := chat.New(name)
		for _, o := range []chat.Options{
			{Mode: chat.ModeAsk, NewID: "n", MCP: "http://broker/mcp", Model: "m"},
			{Mode: chat.ModeAuto, Resume: "r", Fork: true},
			{Resume: "r"},
		} {
			for _, arg := range d.Argv(o) {
				flag, _, _ := strings.Cut(arg, "=")
				if !strings.HasPrefix(flag, "--") {
					continue
				}
				if !regexp.MustCompile(`(^|[ "])` + regexp.QuoteMeta(flag) + `([ =;"]|$)`).Match(script) {
					t.Errorf("%s driver passes %s, which check-protocols.sh doesn't check", name, flag)
				}
			}
		}
	}
}
