package tools

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func httptestGet(pageURL string) *http.Request {
	return httptest.NewRequest(http.MethodGet, pageURL, nil)
}

// postForm submits a form to a secret page.
func postForm(pageURL, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, pageURL, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func newTestPage(info secretPageInfo) (*secretPage, *stubPages, string) {
	p := newSecretPage(template.Must(template.New("page").Parse(secretPageHTML)), info)
	pages := &stubPages{}
	base, _, _ := pages.serve(p)
	return p, pages, base + p.path
}

func tokenPageInfo() secretPageInfo {
	return secretPageInfo{
		Tool: "link_repos_by_token", Title: "Link <repositories>", Need: "Create a token in repo-a.",
		Outcome: "It becomes <V>.", Until: time.Date(2026, 10, 10, 14, 5, 0, 0, time.Local),
		Token: &tokenHelp{Repo: "repo-a", CreateURL: "https://bitbucket.example/ws/repo-a/admin/access-tokens",
			Name: "repo-b-pipelines", Permissions: []string{"repository", "pipeline"}},
		Fields: []secretField{{Name: "token", Label: "Access token"}},
	}
}

func TestSecretPageShowsHeaderAndTokenLink(t *testing.T) {
	_, pages, pageURL := newTestPage(tokenPageInfo())
	rec := pages.do(httptestGet(pageURL))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<h1>Link &lt;repositories&gt;</h1>", `<span class="tool">link_repos_by_token</span>`,
		"What is needed from you</b>Create a token in repo-a.",
		`href="https://bitbucket.example/ws/repo-a/admin/access-tokens" target="_blank" rel="noopener noreferrer"`,
		"<code>repo-b-pipelines</code>", `<span class="chip">repository</span><span class="chip">pipeline</span>`,
		`name="token"`, "It becomes &lt;V&gt;.", "open until 14:05",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	// no-referrer would make browsers post with "Origin: null", which the page refuses.
	for header, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "same-origin"} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q", header, got)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") ||
		!strings.Contains(csp, "base-uri 'none'") || strings.Contains(csp, "script-src") {
		t.Errorf("CSP = %q, want no scripts and no base URL", csp)
	}
}

func TestSecretPageWithoutTokenHasNoTokenStep(t *testing.T) {
	_, pages, pageURL := newTestPage(secretPageInfo{
		Tool: "set_variable", Title: "Set a secured pipeline variable", Need: "Enter the value of DEPLOY_KEY.",
		Fields: []secretField{{Name: "DEPLOY_KEY", Label: "DEPLOY_KEY", Hint: "Secured"}, {Name: "OTHER", Label: "OTHER"}},
	})
	body := pages.do(httptestGet(pageURL)).Body.String()
	if strings.Contains(body, "Create the token") || strings.Contains(body, "access token") {
		t.Error("a page that asks for no token must not show token steps")
	}
	for _, want := range []string{`name="DEPLOY_KEY"`, `name="OTHER"`, "set_variable", "Enter the value of DEPLOY_KEY."} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
}

func TestSecretPageRefusesForeignRequests(t *testing.T) {
	p, pages, pageURL := newTestPage(tokenPageInfo())
	for name, tc := range map[string]struct {
		req  func() *http.Request
		want int
	}{
		"wrong path": {func() *http.Request { return httptestGet("http://" + stubPagesHost + "/guess") }, http.StatusNotFound},
		"foreign origin": {func() *http.Request {
			r := postForm(pageURL, "token=x")
			r.Header.Set("Origin", "https://evil.example")
			return r
		}, http.StatusForbidden},
		"opaque origin": {func() *http.Request {
			r := postForm(pageURL, "token=x")
			r.Header.Set("Origin", "null")
			return r
		}, http.StatusForbidden},
		"oversized body": {func() *http.Request {
			return postForm(pageURL, "token="+strings.Repeat("x", maxSecretForm))
		}, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			if got := pages.do(tc.req()).Code; got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}

	// DNS rebinding: the browser reaches 127.0.0.1 but sends the attacker's host name.
	r := postForm(pageURL, "token=x")
	r.Host = "evil.example:4242"
	if got := pages.do(r).Code; got != http.StatusForbidden {
		t.Fatalf("rebinding status = %d, want 403", got)
	}
	if len(p.values) != 0 {
		t.Fatal("no value may be accepted")
	}
}

func TestSecretPageAcceptsValuesOnce(t *testing.T) {
	p, pages, pageURL := newTestPage(secretPageInfo{Fields: []secretField{{Name: "A", Label: "A"}, {Name: "B", Label: "B"}}})
	rec := pages.do(postForm(pageURL, url.Values{"A": {"secret-a"}, "B": {"  "}}.Encode()))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Fill in B.") {
		t.Fatalf("missing value: status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret-a") {
		t.Fatal("submitted values must not be rendered back")
	}
	if got := pages.submit(pageURL, url.Values{"A": {" a "}, "B": {"b\n"}}); got != http.StatusOK {
		t.Fatalf("status = %d", got)
	}
	if got := pages.submit(pageURL, url.Values{"A": {"x"}, "B": {"y"}}); got != http.StatusConflict {
		t.Fatalf("second submit status = %d", got)
	}
	if got := <-p.values; got["A"] != "a" || got["B"] != "b" || len(got) != 2 {
		t.Fatalf("values = %q", got)
	}
}

func TestSecretPageConfirmsReceipt(t *testing.T) {
	_, pages, pageURL := newTestPage(tokenPageInfo())
	body := pages.do(postForm(pageURL, "token=tok")).Body.String()
	if !strings.Contains(body, "<h2>Received</h2>") || strings.Contains(body, "<form") {
		t.Fatal("after the values arrive the page must confirm and drop the form")
	}
}
