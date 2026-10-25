package chat

import (
	"bytes"
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
// as audio (.m4a, .aac), or an .mp4 with no video track, whose box looks
// the same as a video's.
func AudioType(data []byte, name string) (string, bool) {
	ctype := http.DetectContentType(data)
	if ext, ok := audioExt[ctype]; ok {
		return ext, true
	}
	if len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0 && strings.EqualFold(path.Ext(name), ".mp3") {
		return ".mp3", true // a bare MPEG frame, with no tag before it
	}
	if len(data) < 12 || string(data[4:8]) != "ftyp" {
		return "", false
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".m4a", ".aac":
		return ".m4a", true
	case ".mp4":
		// The same box holds video: an audio brand, or no video track, is a
		// recording to play, such as a podcast saved as .mp4.
		if brand := string(data[8:12]); brand == "M4A " || brand == "M4B " || !hasVideoTrack(data) {
			return ".m4a", true
		}
	}
	return "", false
}

// hasVideoTrack reports whether an MPEG-4 file's handlers, as far as data
// goes, include video. A track's hdlr box names its kind 8 bytes in.
func hasVideoTrack(data []byte) bool {
	for i := 0; ; {
		j := bytes.Index(data[i:], []byte("hdlr"))
		if j < 0 {
			return false
		}
		i += j + 4
		if i+12 <= len(data) && string(data[i+8:i+12]) == "vide" {
			return true
		}
	}
}

// AudioTypes maps each kept audio extension to the type it's served as.
func AudioTypes() map[string]string { return maps.Clone(audioMime) }
