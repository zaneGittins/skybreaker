package app

import (
	"context"
	"errors"
	"log"
	"strconv"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// writeScript writes its stdin to $1 inside the sandbox, replacing the file
// only once all $2 bytes have arrived, and tidies attachments older than a
// month under $3. Paths are arguments, never part of the script.
const writeScript = `umask 077 && mkdir -p -- "${1%/*}" && cat > "$1.part" && [ "$(wc -c < "$1.part")" -eq "$2" ] && mv -f -- "$1.part" "$1" || { rm -f -- "$1.part"; exit 1; }
find "$3" -type f -mtime +30 -delete 2>/dev/null; exit 0`

// attach writes a message's attachments into the session's sandbox,
// starting it first if it isn't running. Why a write failed is logged, not
// returned: the sandbox chooses those words, and the user would see them.
func (a *App) attach(ctx context.Context, s store.Session, files []chat.SandboxFile) error {
	if err := a.StartSandbox(ctx, s.Project); err != nil {
		return err
	}
	if err := a.waitReady(ctx, s.Project); err != nil {
		return err
	}
	for _, f := range files {
		_, err := a.sandbox.ExecInput(ctx, s.Project, f.Data, 4<<10, "sh", "-c", writeScript, "sh", f.Path, strconv.Itoa(len(f.Data)), chat.AttachDir)
		if err != nil {
			log.Printf("skybreaker: attach %s in %s: %v", f.Path, s.Project, err)
			return errors.New("the sandbox didn't take them")
		}
	}
	return nil
}
