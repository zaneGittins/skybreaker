package chat

import (
	"maps"
	"net/http"
	"path"
	"strings"
)

// audioExt is each audio type kept, by what its bytes say it is, and the
// type each is served as.
var (
	audioExt  = map[string]string{"audio/mpeg": ".mp3", "audio/wave": ".wav", "application/ogg": ".ogg"}
	audioMime = map[string]string{".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".m4a": "audio/mp4"}
)

// AudioType is the extension an audio file is kept with, from its bytes:
// an MP3 (with or without its ID3 tag), WAV or Ogg; or an MPEG-4 file named
// as audio (.m4a, .aac), whose box looks the same as a video's.
func AudioType(data []byte, name string) (string, bool) {
	ctype := http.DetectContentType(data)
	if ext, ok := audioExt[ctype]; ok {
		return ext, true
	}
	if len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0 && strings.EqualFold(path.Ext(name), ".mp3") {
		return ".mp3", true // a bare MPEG frame, with no tag before it
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".m4a", ".aac":
		if len(data) >= 8 && string(data[4:8]) == "ftyp" {
			return ".m4a", true
		}
	}
	return "", false
}

// AudioTypes maps each kept audio extension to the type it's served as.
func AudioTypes() map[string]string { return maps.Clone(audioMime) }
