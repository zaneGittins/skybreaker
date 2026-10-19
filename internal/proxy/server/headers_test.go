package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestImpersonationRefused(t *testing.T) {
	// The injected service account may hold impersonate rights; a sandbox
	// must never borrow them, on an auto rule or an approved one.
	w := newWorld(t)
	for _, h := range []string{"Impersonate-User", "impersonate-group", "Impersonate-Uid", "Impersonate-Extra-Scopes"} {
		hdr := map[string]string{"Authorization": "Bearer " + w.token, h: "system:admin"}
		code, _, rh := do(t, http.DefaultClient, "GET", w.proxy.URL+"/k8s/prod/api/v1/namespaces/apps/pods", "", hdr)
		if code != http.StatusForbidden || rh.Get("X-Skybreaker-Approval") != "" {
			t.Errorf("%s: %d", h, code)
		}
	}
	if code, _, _ := do(t, w.orbit(), "GET", "https://api.github.test/repos/zaneGittins/orbit", "", map[string]string{"Impersonate-User": "x"}); code != http.StatusForbidden {
		t.Errorf("impersonation on a mitm host: %d", code)
	}
	if n := len(w.hitsFor("kube.test")) + len(w.hitsFor("api.github.test")); n != 0 {
		t.Fatalf("%d impersonating requests reached upstream", n)
	}
	if n := len(w.approvals.List()); n != 0 {
		t.Fatalf("%d approval cards for refused requests", n)
	}
}

func TestApprovalBoundToHeaders(t *testing.T) {
	w := newWorld(t)
	u := "https://api.github.test/repos/zaneGittins/orbit/pulls/5/merge"
	_, _, hdr := do(t, w.orbit(), "PUT", u, "{}", map[string]string{"X-GitHub-Api-Version": "2022-11-28", "If-Match": `"abc"`})
	id := hdr.Get("X-Skybreaker-Approval")
	if id == "" {
		t.Fatal("no approval id")
	}
	a, _ := w.approvals.Get(id)
	shown := ""
	for _, f := range a.View.Fields {
		shown += f.Label + "=" + f.Value + "\n"
	}
	if !strings.Contains(shown, "If-Match") || !strings.Contains(shown, "X-Github-Api-Version") {
		t.Fatalf("headers not on the card:\n%s", shown)
	}
	w.approvals.Decide(id, true, "zane@example.com")
	// The same request with a header changed is a different request.
	if code, _, _ := do(t, w.orbit(), "PUT", u, "{}", map[string]string{"X-GitHub-Api-Version": "2022-11-28", "If-Match": `"other"`}); code == 200 {
		t.Fatal("approval spent on a request with a different header")
	}
	if code, _, _ := do(t, w.orbit(), "PUT", u, "{}", map[string]string{"X-GitHub-Api-Version": "2022-11-28"}); code == 200 {
		t.Fatal("approval spent on a request missing a header")
	}
	if code, _, _ := do(t, w.orbit(), "PUT", u, "{}", map[string]string{"x-github-api-version": "2022-11-28", "if-match": `"abc"`}); code != 200 {
		t.Fatalf("identical retry (names in another case): %d", code)
	}
}

func TestHeaderBindingIsUnambiguous(t *testing.T) {
	cases := []http.Header{
		{"X-A": {"b, c"}},
		{"X-A": {"b", "c"}},
		{"X-A": {"b"}, "C": {""}},
		{"X-A": {"b\nC: "}},
		{},
	}
	seen := map[string]int{}
	for i, h := range cases {
		b := headerBinding(h)
		if j, dup := seen[b]; dup {
			t.Fatalf("headers %v and %v bind the same: %q", cases[j], h, b)
		}
		seen[b] = i
	}
	// Order of names doesn't matter; order of values does.
	if headerBinding(http.Header{"A": {"1"}, "B": {"2"}}) != headerBinding(http.Header{"B": {"2"}, "A": {"1"}}) {
		t.Fatal("name order changed the binding")
	}
	if headerBinding(http.Header{"A": {"1", "2"}}) == headerBinding(http.Header{"A": {"2", "1"}}) {
		t.Fatal("value order ignored")
	}
}

func TestK8sProxySubresourceRefused(t *testing.T) {
	w := newWorld(t)
	for _, p := range []string{"/k8s/prod/api/v1/nodes/worker/proxy/pods", "/k8s/prod/api/v1/namespaces/apps/services/web:80/proxy/"} {
		if code, _ := routeReq(t, w, "GET", p, "", w.token); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", p, code)
		}
	}
	if n := len(w.hitsFor("kube.test")); n != 0 {
		t.Fatalf("%d proxy requests reached the cluster", n)
	}
}

func TestTwoContentTypesRefused(t *testing.T) {
	// The card shows one content type; an upstream reading the other would
	// act on something the approver never saw.
	w := newWorld(t)
	req, _ := http.NewRequest("PUT", "https://api.github.test/repos/zaneGittins/orbit/pulls/6/merge", strings.NewReader("{}"))
	req.Header["Content-Type"] = []string{"application/json", "application/merge-patch+json"}
	resp, err := w.orbit().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("X-Skybreaker-Approval") != "" || len(w.approvals.List()) != 0 {
		t.Fatalf("two content types: %d", resp.StatusCode)
	}
}
