package tools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func httptestGet(pageURL string) *http.Request {
	return httptest.NewRequest(http.MethodGet, pageURL, nil)
}

func newTestPage() (*tokenPage, *stubPages, string) {
	p := newTokenPage(tokenPageInfo{SourceRepo: "repo-a", TokensPage: "https://bitbucket.example/x", TokenName: "n", Permissions: "pipeline", Variable: "<V>", Place: "p"})
	pages := &stubPages{}
	base, _, _ := pages.serve(p)
	return p, pages, base + p.path
}

func TestTokenPageGet(t *testing.T) {
	_, pages, pageURL := newTestPage()
	rec := pages.do(httptestGet(pageURL))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "&lt;V&gt;") || strings.Contains(body, "<V>") {
		t.Error("page values must be HTML-escaped")
	}
	// no-referrer would make browsers post with "Origin: null", which the page refuses.
	for header, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "same-origin"} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q", header, got)
		}
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Error("missing restrictive CSP")
	}
}

func TestTokenPageRefusesForeignRequests(t *testing.T) {
	p, pages, pageURL := newTestPage()
	for name, tc := range map[string]struct {
		req  func() *http.Request
		want int
	}{
		"wrong path": {func() *http.Request { return httptestGet("http://" + stubPagesHost + "/guess") }, http.StatusNotFound},
		"foreign origin": {func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, pageURL, strings.NewReader("token=x"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "https://evil.example")
			return r
		}, http.StatusForbidden},
		"opaque origin": {func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, pageURL, strings.NewReader("token=x"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "null")
			return r
		}, http.StatusForbidden},
		"oversized body": {func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, pageURL, strings.NewReader("token="+strings.Repeat("x", maxTokenForm)))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		}, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			if got := pages.do(tc.req()).Code; got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}

	// DNS rebinding: the browser reaches 127.0.0.1 but sends the attacker's host name.
	r := httptest.NewRequest(http.MethodPost, pageURL, strings.NewReader("token=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Host = "evil.example:4242"
	if got := pages.do(r).Code; got != http.StatusForbidden {
		t.Fatalf("rebinding status = %d, want 403", got)
	}
	if len(p.tokens) != 0 {
		t.Fatal("no token may be accepted")
	}
}

func TestTokenPageAcceptsOneToken(t *testing.T) {
	p, pages, pageURL := newTestPage()
	if got := pages.submit(pageURL, "  "); got != http.StatusBadRequest {
		t.Fatalf("empty token status = %d", got)
	}
	if got := pages.submit(pageURL, " tok "); got != http.StatusOK {
		t.Fatalf("status = %d", got)
	}
	if got := pages.submit(pageURL, "second"); got != http.StatusConflict {
		t.Fatalf("second submit status = %d", got)
	}
	if got := <-p.tokens; got != "tok" {
		t.Fatalf("token = %q", got)
	}
}
