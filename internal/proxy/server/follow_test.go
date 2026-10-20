package server

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

// followConfig has GitHub's Actions API follow redirects, as the github
// bundle does for job logs.
var followConfig = strings.Replace(testConfig, `{"match": "GET /repos/{repo}/**", "action": "auto"},`,
	`{"match": "GET /repos/{repo}/actions/**", "action": "auto", "follow_redirects": ["blobs.test"]},
         {"match": "GET /repos/{repo}/**", "action": "auto"},`, 1)

func TestFollowRedirectsFetchesTheLogWithoutCredentials(t *testing.T) {
	if followConfig == testConfig {
		t.Fatal("test config changed")
	}
	var mu sync.Mutex
	var blobAuth []string
	handlers := map[string]http.Handler{
		"api.github.test": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/repos/zaneGittins/orbit/actions/jobs/1/logs":
				http.Redirect(w, r, "https://blobs.test/actions-results/1.txt?sig=abc", http.StatusFound)
			case "/repos/zaneGittins/orbit/actions/jobs/2/logs":
				http.Redirect(w, r, "http://blobs.test/plain", http.StatusFound)
			case "/repos/zaneGittins/orbit/actions/jobs/4/logs":
				http.Redirect(w, r, "https://api.github.test/elsewhere", http.StatusFound) // not a listed host
			case "/repos/zaneGittins/orbit/actions/jobs/5/logs":
				http.Redirect(w, r, "https://blobs.test:8443/x", http.StatusFound)
			case "/repos/zaneGittins/orbit/actions/jobs/6/logs":
				w.Header().Set("X-Accepted-GitHub-Permissions", "actions=read")
				w.Header().Set("X-RateLimit-Remaining", "0")
				http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
			case "/repos/zaneGittins/orbit/actions/jobs/3/logs":
				http.Redirect(w, r, "https://blobs.test/loop", http.StatusFound)
			case "/repos/zaneGittins/orbit/tarball":
				http.Redirect(w, r, "https://blobs.test/tarball", http.StatusFound)
			case "/repos/zaneGittins/orbit/actions/runs":
				w.Header().Set("X-Accepted-GitHub-Permissions", "actions=read")
				http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
			default:
				http.NotFound(w, r)
			}
		}),
		"blobs.test": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/loop" {
				http.Redirect(w, r, "/loop", http.StatusFound) // forever
				return
			}
			mu.Lock()
			blobAuth = append(blobAuth, r.Header.Get("Authorization")+r.Header.Get("Cookie"))
			mu.Unlock()
			w.Write([]byte("2026-10-20 step 3 failed: exit 1"))
		}),
	}
	w := newWorldWith(t, followConfig, handlers)
	c := w.orbit()

	// The broker fetches the log itself; the agent gets it straight back.
	code, body, _ := do(t, c, "GET", "https://api.github.test/repos/zaneGittins/orbit/actions/jobs/1/logs", "", nil)
	if code != 200 || body != "2026-10-20 step 3 failed: exit 1" {
		t.Fatalf("log: %d %q", code, body)
	}
	mu.Lock()
	if len(blobAuth) != 1 || blobAuth[0] != "" {
		t.Errorf("credentials went to the redirect: %q", blobAuth)
	}
	mu.Unlock()
	// Each hop is in the log, without its signed query; the rule's reason
	// stays.
	if a := w.auditBuf.String(); !strings.Contains(a, "followed, without credentials, to blobs.test/actions-results/1.txt") || strings.Contains(a, "sig=abc") {
		t.Errorf("audit: %s", a)
	}

	// Never over plain http, nor to another port, nor forever, and only to
	// the hosts the rule lists.
	for job, want := range map[string]int{"2": http.StatusBadGateway, "3": http.StatusBadGateway, "4": http.StatusForbidden, "5": http.StatusBadGateway} {
		if code, body, _ := do(t, c, "GET", "https://api.github.test/repos/zaneGittins/orbit/actions/jobs/"+job+"/logs", "", nil); code != want {
			t.Errorf("job %s: %d, want %d (%s)", job, code, want, body)
		}
	}
	// A rule without follow_redirects passes the redirect on, which the
	// sandbox can't follow to a host it has no rule for.
	noFollow := *c
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	code, _, hdr := do(t, &noFollow, "GET", "https://api.github.test/repos/zaneGittins/orbit/tarball", "", nil)
	if code != http.StatusFound || hdr.Get("Location") != "https://blobs.test/tarball" {
		t.Errorf("tarball: %d %q", code, hdr.Get("Location"))
	}
	mu.Lock()
	if len(blobAuth) != 1 {
		t.Errorf("blobs.test fetched %d times", len(blobAuth))
	}
	mu.Unlock()

	// A missing App permission is named in the audit log.
	if code, _, _ := do(t, c, "GET", "https://api.github.test/repos/zaneGittins/orbit/actions/runs", "", nil); code != http.StatusForbidden {
		t.Errorf("runs: %d", code)
	}
	if a := w.auditBuf.String(); !strings.Contains(a, "GitHub refused it: the token needs actions=read") {
		t.Errorf("audit: %s", a)
	}
	// A rate limit isn't a missing permission.
	before := strings.Count(w.auditBuf.String(), "the token needs")
	do(t, c, "GET", "https://api.github.test/repos/zaneGittins/orbit/actions/jobs/6/logs", "", nil)
	if strings.Count(w.auditBuf.String(), "the token needs") != before {
		t.Error("a rate limit was logged as a missing permission")
	}
}

func TestOnlyGitHubsAPINamesAMissingPermission(t *testing.T) {
	w := newWorldWith(t, testConfig, map[string]http.Handler{
		"open.test": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Accepted-GitHub-Permissions", "nothing; rule: approved by admin")
			http.Error(w, "no", http.StatusForbidden)
		}),
	})
	if code, _, _ := do(t, w.orbit(), "GET", "https://open.test/x", "", nil); code != http.StatusForbidden {
		t.Fatalf("open.test: %d", code)
	}
	if a := w.auditBuf.String(); strings.Contains(a, "token needs") {
		t.Errorf("another host wrote the reason: %s", a)
	}
}
