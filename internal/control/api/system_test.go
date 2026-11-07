package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/app"
	host "github.com/zaneGittins/skybreaker/internal/control/system"
)

type fakeSystem struct {
	calls int
	err   error
}

func (f *fakeSystem) ProjectStorage(context.Context, string) (host.Storage, error) {
	return host.Storage{Total: 4096}, f.err
}
func (f *fakeSystem) CleanStorage(context.Context, string, host.Cleanup) (host.Cleaned, error) {
	f.calls++
	return host.Cleaned{Recovered: 1024}, f.err
}

func TestSystemAPI(t *testing.T) {
	sys := &fakeSystem{}
	f := newFixture(t, you, func(d *Deps) { d.System = sys })
	for _, path := range []string{"/api/system", "/api/projects/orbit/storage"} {
		code, out := f.do(t, "GET", path, "", nil)
		if code != 200 {
			t.Fatalf("%s: %d %s", path, code, out)
		}
	}
	for _, body := range []string{`{}`, `{"go":true,"older_than_days":0}`, `{"docker":true,"older_than_days":366}`, `{"go":true,"older_than_days":7,"path":"/"}`} {
		if code, _ := f.do(t, "POST", "/api/projects/orbit/cleanup", body, nil); code != 400 {
			t.Fatalf("accepted %s: %d", body, code)
		}
	}
	if sys.calls != 0 {
		t.Fatal("invalid request executed cleanup")
	}
	code, out := f.do(t, "POST", "/api/projects/orbit/cleanup", `{"go":true,"older_than_days":7}`, nil)
	if code != 200 || !strings.Contains(out, `"recovered":1024`) {
		t.Fatalf("cleanup: %d %s", code, out)
	}
	sys.err = app.ErrCleanupBusy
	if code, _ := f.do(t, "POST", "/api/projects/orbit/cleanup", `{"docker":true,"older_than_days":7}`, nil); code != 409 {
		t.Fatalf("busy: %d", code)
	}
	sys.err = errors.New("scan unavailable")
	if code, _ := f.do(t, "GET", "/api/projects/orbit/storage", "", nil); code != 500 {
		t.Fatalf("scan failure: %d", code)
	}
	if code, _ := f.do(t, "GET", "/api/projects/missing/storage", "", nil); code != 404 {
		t.Fatalf("unknown project: %d", code)
	}
}

func TestSystemAdminOnly(t *testing.T) {
	sys := &fakeSystem{}
	f := newFixture(t, you, func(d *Deps) { d.System = sys; d.Config.Admins = []string{"other"} })
	for _, req := range []struct{ method, path, body string }{{"GET", "/api/system", ""}, {"GET", "/api/projects/orbit/storage", ""}, {"POST", "/api/projects/orbit/cleanup", `{"go":true,"older_than_days":7}`}} {
		if code, _ := f.do(t, req.method, req.path, req.body, nil); code != 403 {
			t.Fatalf("%s: %d", req.path, code)
		}
	}
	if sys.calls != 0 {
		t.Fatal("non-admin cleaned")
	}
}

func TestCleanupRespectsSettingsOff(t *testing.T) {
	sys := &fakeSystem{}
	off := false
	f := newFixture(t, you, func(d *Deps) { d.System = sys; d.Config.AppSettings = &off })
	if code, _ := f.do(t, "GET", "/api/system", "", nil); code != 200 {
		t.Fatalf("read: %d", code)
	}
	if code, _ := f.do(t, "POST", "/api/projects/orbit/cleanup", `{"go":true,"older_than_days":7}`, nil); code != 403 {
		t.Fatalf("write: %d", code)
	}
	if sys.calls != 0 {
		t.Fatal("cleanup ran with app settings off")
	}
}
