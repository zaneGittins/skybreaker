package chat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // decoders for checking images
	_ "image/jpeg"
	_ "image/png"
	"maps"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// A File is one the user attached to a message.
type File struct {
	Name  string `json:"name"` // as the user's device named it
	Data  []byte `json:"data"`
	Thumb []byte `json:"thumb,omitempty"` // a small copy of an image, for previews
}

// Attachment is a file sent with a message, as the chat records it.
type Attachment struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Size  int64  `json:"size"`
	Kept  string `json:"kept"`            // the chat's copy, served as shown files are
	Thumb string `json:"thumb,omitempty"` // the kept thumbnail of an image
	Path  string `json:"path"`            // where it is in the sandbox
}

// Attachment kinds.
const (
	AttachImage = "image"
	AttachFile  = "file"
)

// Attacher writes a message's attachments into a session's sandbox.
type Attacher func(ctx context.Context, s store.Session, files []SandboxFile) error

// SandboxFile is a file to write into a sandbox.
type SandboxFile struct {
	Path string
	Data []byte
}

// imageExt is the extension each image type is kept under, by its sniffed
// type. SVG isn't one: it can carry script, so it's kept as a file.
var imageExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

// ImageType is the extension an image of the sniffed type mime is kept
// under, if it's one.
func ImageType(mime string) (string, bool) {
	ext, ok := imageExt[mime]
	return ext, ok
}

// ImageTypes maps each image type kept to its extension.
func ImageTypes() map[string]string { return maps.Clone(imageExt) }

const (
	maxAttachments = 10
	maxAttachment  = 10 << 20
	MaxAttachTotal = 25 << 20 // a message's attachments in all
	// maxImageInput is the largest image sent as image input: Claude's API
	// takes 5 MB of base64. Other images go as files.
	maxImageInput = 5 << 20 / 4 * 3
	// maxImageSide is the largest side of an image sent as image input,
	// within what every model takes.
	maxImageSide = 2000
	// AttachDir is in the sandbox's home, which outlives the container,
	// and out of the repository, where an agent might commit attachments.
	AttachDir = "/root/.skybreaker/attachments"
	// attachTimeout bounds writing one message's files into the sandbox.
	attachTimeout = time.Minute
	// A thumbnail is a JPEG or PNG the browser made, at most this large.
	maxThumb     = 96 << 10
	maxThumbSide = 960
)

// maxImagesInput bounds a message's image input, as the whole request has
// a limit too.
var maxImagesInput = 12 << 20

// attach writes files into the sandbox, then keeps them with the chat. It
// returns what the chat records, what the harness is told and the images
// it sees. Callers hold the run's sendMu, so the room checked is there to
// keep them in. A thumbnail that can't be kept is dropped.
func (m *Manager) attach(ctx context.Context, s store.Session, files []File) ([]Attachment, string, []Image, error) {
	if len(files) == 0 {
		return nil, "", nil, nil
	}
	if len(files) > maxAttachments {
		return nil, "", nil, fmt.Errorf("attach at most %d files to a message", maxAttachments)
	}
	if m.c.Attach == nil {
		return nil, "", nil, errors.New("this chat can't take attachments")
	}
	total := 0
	datas := make([][]byte, len(files))
	for i, f := range files {
		switch {
		case len(f.Data) == 0:
			return nil, "", nil, fmt.Errorf("%s is empty", displayName(f.Name))
		case len(f.Data) > maxAttachment:
			return nil, "", nil, fmt.Errorf("%s is over %d MB", displayName(f.Name), maxAttachment>>20)
		}
		total += len(f.Data)
		datas[i] = f.Data
	}
	if total > MaxAttachTotal {
		return nil, "", nil, fmt.Errorf("attachments come to over %d MB", MaxAttachTotal>>20)
	}
	if err := m.c.Log.AttachRoom(s.ID, datas); err != nil {
		return nil, "", nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, attachTimeout)
	defer cancel()
	atts := make([]Attachment, len(files))
	exts := make([]string, len(files))
	mimes := make([]string, len(files))
	writes := make([]SandboxFile, len(files))
	for i, f := range files {
		mimes[i] = http.DetectContentType(f.Data)
		ext, isImage := imageExt[mimes[i]]
		a := Attachment{Name: displayName(f.Name), Kind: AttachFile, Size: int64(len(f.Data))}
		if isImage {
			a.Kind = AttachImage
		} else {
			ext = ".bin"
		}
		a.Kept = keptName(f.Data, ext)
		a.Path = path.Join(AttachDir, s.ID, a.Kept[:8]+"-"+attachName(f.Name))
		atts[i], exts[i], writes[i] = a, ext, SandboxFile{Path: a.Path, Data: f.Data}
	}
	if err := m.c.Attach(ctx, s, writes); err != nil {
		return nil, "", nil, fmt.Errorf("couldn't put the files in the sandbox: %w", err)
	}
	var (
		note   strings.Builder
		images []Image
		inline int
	)
	for i, f := range files {
		if _, err := m.c.Log.KeepAttached(s.ID, f.Data, exts[i]); err != nil {
			return nil, "", nil, err
		}
		if ext := thumbExt(f, atts[i].Kind); ext != "" {
			if name, err := m.c.Log.KeepThumb(s.ID, f.Thumb, ext); err == nil {
				atts[i].Thumb = name
			}
		}
		a := atts[i]
		fmt.Fprintf(&note, "\n[Attached %s: %s]", a.Kind, a.Path)
		if a.Kind == AttachImage && inline+len(f.Data) <= maxImagesInput && imageInput(f.Data) {
			inline += len(f.Data)
			images = append(images, Image{Mime: mimes[i], Data: f.Data})
		}
	}
	return atts, strings.TrimPrefix(note.String(), "\n"), images, nil
}

// imageInput reports whether an image can go to the model as one: a PNG,
// JPEG or GIF that decodes whole, within the models' limits. A model
// refuses anything else, and would go on refusing it in the conversation.
func imageInput(data []byte) bool {
	if len(data) > maxImageInput {
		return false
	}
	c, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || c.Width < 1 || c.Height < 1 || c.Width > maxImageSide || c.Height > maxImageSide {
		return false
	}
	_, _, err = image.Decode(bytes.NewReader(data))
	return err == nil
}

var thumbTypes = map[string]string{"image/jpeg": ".jpg", "image/png": ".png"}

// thumbExt is the extension a file's thumbnail is kept under, or "" if it
// has none worth keeping: only an image's, only a small JPEG or PNG.
func thumbExt(f File, kind string) string {
	if kind != AttachImage || len(f.Thumb) == 0 || len(f.Thumb) > maxThumb {
		return ""
	}
	ext := thumbTypes[http.DetectContentType(f.Thumb)]
	if ext == "" {
		return ""
	}
	c, _, err := image.DecodeConfig(bytes.NewReader(f.Thumb))
	if err != nil || c.Width < 1 || c.Height < 1 || c.Width > maxThumbSide || c.Height > maxThumbSide {
		return ""
	}
	return ext
}

// baseName is a file's own name, without the folders a browser may give,
// and without control or formatting characters.
func baseName(name string) string {
	name = name[strings.LastIndexAny(name, `/\`)+1:]
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(name, "?"))
	return strings.TrimSpace(name)
}

// displayName is the name the chat shows, at most 200 bytes.
func displayName(name string) string {
	if name = baseName(name); name == "" {
		return "file"
	}
	if len(name) <= 200 {
		return name
	}
	n := 197
	for n > 0 && !utf8.RuneStart(name[n]) {
		n--
	}
	return name[:n] + "…"
}

// attachName is a safe file name for the sandbox: ASCII letters, digits,
// dots, dashes and underscores, no leading dot or dash, at most 100 bytes.
func attachName(name string) string {
	name = baseName(name)
	var b strings.Builder
	under := false
	for _, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_'
		switch {
		case ok:
			b.WriteRune(r)
			under = false
		case !under:
			b.WriteByte('_')
			under = true
		}
	}
	n := strings.TrimLeft(b.String(), ".-")
	if n == "" {
		return "file"
	}
	if len(n) > 100 {
		ext := path.Ext(n)
		if len(ext) > 10 {
			ext = ""
		}
		n = n[:100-len(ext)] + ext
	}
	return n
}
