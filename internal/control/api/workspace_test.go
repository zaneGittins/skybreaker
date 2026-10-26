package api

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/app"
)

func (b *fakeBackend) FillWorkspace(_ context.Context, project string, archive io.Reader) error {
	data, _ := io.ReadAll(archive)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.filled = append(b.filled, project+": "+string(data))
	return b.fillErr
}

func TestFillWorkspace(t *testing.T) {
	f := newFixture(t, you)
	put := func(project, ct string) (int, string) {
		t.Helper()
		r, _ := http.NewRequest("PUT", f.ts.URL+"/api/projects/"+project+"/workspace", strings.NewReader("archive"))
		r.Header.Set("Content-Type", ct)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := put("orbit", "application/gzip"); code != http.StatusNoContent {
		t.Fatalf("copy: %d %s", code, body)
	}
	if !slices.Equal(f.backend.filled, []string{"orbit: archive"}) {
		t.Fatalf("filled %q", f.backend.filled)
	}
	if code, _ := put("comet", "application/gzip"); code != http.StatusNotFound {
		t.Errorf("unknown project: %d", code)
	}
	if code, _ := put("orbit", "application/json"); code != http.StatusUnsupportedMediaType {
		t.Errorf("not an archive: %d", code)
	}
	f.backend.fillErr = app.ErrWorkspaceFull
	if code, body := put("orbit", "application/gzip"); code != http.StatusConflict || !strings.Contains(body, "isn't empty") {
		t.Errorf("full workspace: %d %s", code, body)
	}
}
