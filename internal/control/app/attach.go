package app

import (
	"bytes"
	"context"
	"errors"
	"log"
	"maps"
	"slices"
	"strconv"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// writeScript writes its stdin to $1 inside the sandbox, replacing the file
// only once all $2 bytes have arrived. The path is an argument, never part
// of the script.
const writeScript = `umask 077 && mkdir -p -- "${1%/*}" && cat > "$1.part" && [ "$(wc -c < "$1.part")" -eq "$2" ] && mv -f -- "$1.part" "$1" || { rm -f -- "$1.part"; exit 1; }`

// tidyScript removes attachments untouched for a month. A chat that still
// refers to one has it put back when its agent next starts.
const tidyScript = `find "$1" -type f -mtime +30 -delete 2>/dev/null; exit 0`

// missingScript prints which of its arguments don't exist, each ending in NUL.
const missingScript = `for p; do [ -e "$p" ] || printf '%s\0' "$p"; done`

// attach writes a message's attachments into the session's sandbox,
// starting it first if it isn't running.
func (a *App) attach(ctx context.Context, s store.Session, files []chat.SandboxFile) error {
	if err := a.StartSandbox(ctx, s.Project); err != nil {
		return err
	}
	if err := a.waitReady(ctx, s.Project); err != nil {
		return err
	}
	if err := a.writeFiles(ctx, s.Project, files); err != nil {
		return err
	}
	a.sandbox.Exec(ctx, s.Project, "sh", "-c", tidyScript, "sh", chat.AttachDir)
	return nil
}

// writeFiles writes files into a project's sandbox. Why a write failed is
// logged, not returned: the sandbox chooses those words, and the user
// would see them.
func (a *App) writeFiles(ctx context.Context, project string, files []chat.SandboxFile) error {
	for _, f := range files {
		_, err := a.sandbox.ExecInput(ctx, project, bytes.NewReader(f.Data), 4<<10, "sh", "-c", writeScript, "sh", f.Path, strconv.Itoa(len(f.Data)))
		if err != nil {
			log.Printf("skybreaker: attach %s in %s: %q", f.Path, project, err.Error())
			return errors.New("the sandbox didn't take them")
		}
	}
	return nil
}

// restoreAttachments puts back, from the chat's own copies, files its
// messages attached that the sandbox no longer has.
func (a *App) restoreAttachments(ctx context.Context, s store.Session) {
	events, err := a.chat.Events(s.ID, 0)
	if err != nil {
		return
	}
	kept := map[string]string{} // sandbox path → kept copy
	for _, e := range events {
		for _, at := range e.Attach {
			kept[at.Path] = at.Kept
		}
	}
	if len(kept) == 0 {
		return
	}
	out, err := a.sandbox.Exec(ctx, s.Project, append([]string{"sh", "-c", missingScript, "sh"}, slices.Sorted(maps.Keys(kept))...)...)
	if err != nil {
		log.Printf("skybreaker: restore attachments of %s: %q", s.ID, err.Error())
		return
	}
	var files []chat.SandboxFile
	for p := range bytes.SplitSeq(bytes.TrimSuffix(out, []byte{0}), []byte{0}) {
		name, ok := kept[string(p)] // the sandbox's answer, so only paths asked about
		if !ok {
			continue
		}
		if data, err := a.chat.Shown(s.ID, name); err == nil {
			files = append(files, chat.SandboxFile{Path: string(p), Data: data})
		}
	}
	if err := a.writeFiles(ctx, s.Project, files); err != nil {
		log.Printf("skybreaker: restore attachments of %s: %v", s.ID, err)
	}
}
