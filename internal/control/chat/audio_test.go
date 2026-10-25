package chat

import "testing"

// Audio is known by its bytes, as images are; an MPEG-4 file only by its
// name too, since the same box holds video.
func TestAudioType(t *testing.T) {
	for _, c := range []struct {
		data, name, want string
	}{
		{"ID3\x04\x00\x00\x00\x00\x00\x00rest of an mp3", "podcast.mp3", ".mp3"},
		{"\xff\xfb\x90\x64\x00\x00\x00\x00\x00\x00\x00\x00", "x.mp3", ".mp3"},
		{"RIFF\x24\x00\x00\x00WAVEfmt ", "a.wav", ".wav"},
		{"OggS\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00", "a.ogg", ".ogg"},
		{"\x00\x00\x00\x20ftypM4A \x00\x00\x00\x00", "episode.m4a", ".m4a"},
		{"\x00\x00\x00\x20ftypisom\x00\x00\x02\x00", "clip.mp4", ""},
		{"%PDF-1.7\n", "a.mp3", ""},
		{"plain text", "notes.txt", ""},
	} {
		got, _ := AudioType([]byte(c.data), c.name)
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	for ext, mime := range AudioTypes() {
		if ext == "" || mime[:6] != "audio/" {
			t.Errorf("%s served as %s", ext, mime)
		}
	}
}
