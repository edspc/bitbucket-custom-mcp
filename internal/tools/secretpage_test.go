package tools

import (
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
	base, _, _ := pages.serve(p, info.Until)
	return p, pages, base + p.path
}

func tokenPageInfo() secretPageInfo {
	return secretPageInfo{
		Tool: "link_repos_by_token", Title: "Link <repositories>", Need: "Create a token in repo-a.",
		Outcome: "It becomes <V>.", Until: time.Now().Add(time.Hour),
		Token: &tokenHelp{Repo: "repo-a", CreateURL: "https://bitbucket.example/ws/repo-a/admin/access-tokens",
			Name: "repo-b-pipelines", Permissions: []string{"repository", "pipeline"}},
		Fields: []secretField{{Name: "token", Label: "Access token"}},
	}
}

func TestSecretPageShowsHeaderAndTokenLink(t *testing.T) {
	info := tokenPageInfo()
	_, pages, pageURL := newTestPage(info)
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
		`name="token"`, "It becomes &lt;V&gt;.", "open until " + info.Until.Format("15:04"),
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
		Until:  time.Now().Add(time.Minute),
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
	pages.do(httptestGet(pageURL))
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
	p, pages, pageURL := newTestPage(secretPageInfo{Fields: []secretField{{Name: "A", Label: "A"}, {Name: "B", Label: "B"}},
		Until: time.Now().Add(time.Minute)})
	pages.do(httptestGet(pageURL))
	rec := pages.do(postForm(pageURL, url.Values{"A": {"secret-a"}, "B": {"  "}, "approval.action": {"confirm"}}.Encode()))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Fill in B.") {
		t.Fatalf("missing value: status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret-a") {
		t.Fatal("submitted values must not be rendered back")
	}
	if got := pages.submit(pageURL, url.Values{"A": {" a "}, "B": {"b\n"}}); got != http.StatusSeeOther {
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
	if got := pages.submit(pageURL, url.Values{"token": {"tok"}}); got != http.StatusSeeOther {
		t.Fatalf("status = %d", got)
	}
	// Post/redirect/get: the tab shows the receipt on a GET, so reloading it sends nothing again.
	body := pages.do(httptestGet(pageURL)).Body.String()
	if !strings.Contains(body, "<h2>Received</h2>") || strings.Contains(body, "<form") || strings.Contains(body, "refresh") {
		t.Fatal("after the values arrive the page must confirm and drop the form")
	}
}

func TestSecretPageRefusesAnotherTab(t *testing.T) {
	p, pages, pageURL := newTestPage(tokenPageInfo())
	first := pages.do(httptestGet(pageURL))
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "<form") || pages.tab.Get(tabParam) == "" {
		t.Fatalf("first open: status %d, tab %v", first.Code, pages.tab)
	}
	// Another tab or browser, or a process that learned the URL, has only the URL.
	other := &stubPages{}
	_, _, _ = other.serve(p, time.Time{})
	if rec := other.do(httptestGet(pageURL)); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("another tab read the form: %d", rec.Code)
	}
	for _, query := range []string{"", tabParam + "=guess"} {
		r := postForm(pageURL+"?"+query, "token=planted&approval.action=confirm")
		r.Header.Set("Origin", "http://"+stubPagesHost)
		if got := other.do(r).Code; got != http.StatusForbidden {
			t.Fatalf("another tab submitted with %q: %d", query, got)
		}
	}
	// Nor can a submission come before any tab opened the page.
	fresh, freshPages, freshURL := newTestPage(tokenPageInfo())
	if got := freshPages.do(postForm(freshURL, "token=planted&approval.action=confirm")).Code; got != http.StatusForbidden {
		t.Fatalf("blind submission: %d", got)
	}
	if len(p.values) != 0 || len(fresh.values) != 0 {
		t.Fatal("no value may be accepted")
	}
	if got := pages.submit(pageURL, url.Values{"token": {"tok"}}); got != http.StatusSeeOther {
		t.Fatalf("the first tab was locked out: %d", got)
	}
}

// A page whose call is over, or whose time is up, keeps answering a tab left open but takes no
// values, and its form reloads when the time is up.
func TestClosedSecretPageTakesNoValues(t *testing.T) {
	for _, name := range []string{"closed", "expired"} {
		t.Run(name, func(t *testing.T) {
			info := tokenPageInfo()
			info.Until = time.Now().Add(time.Minute)
			if name == "expired" {
				info.Until = time.Now().Add(-time.Second)
			}
			p, pages, pageURL := newTestPage(info)
			if name == "closed" {
				body := pages.do(httptestGet(pageURL)).Body.String()
				_, after, ok := strings.Cut(body, `<meta http-equiv="refresh" content="`)
				seconds, err := strconv.Atoi(strings.SplitN(after, `"`, 2)[0])
				if !ok || err != nil || seconds < 1 || seconds > 60 {
					t.Fatal("the form does not reload when the page closes")
				}
				p.close()
			}
			if got := pages.submit(pageURL, url.Values{"token": {"tok"}}); got != http.StatusGone {
				t.Fatalf("status = %d", got)
			}
			body := pages.do(httptestGet(pageURL)).Body.String()
			if !strings.Contains(body, "This request is closed") || strings.Contains(body, "<form") {
				t.Fatal("a closed page still shows the form")
			}
			if len(p.values) != 0 {
				t.Fatal("a closed page took values")
			}
		})
	}
}

// A closed page keeps its port, so a tab left open reaches this server, which refuses the
// values, rather than a process that took over a released port.
func TestClosedSecretPageKeepsItsPort(t *testing.T) {
	info := tokenPageInfo()
	info.Until = time.Now().Add(time.Minute)
	p := newSecretPage(template.Must(template.New("page").Parse(secretPageHTML)), info)
	base, _, err := serveLocal(slog.New(slog.DiscardHandler), p, info.Until)
	if err != nil {
		t.Fatal(err)
	}
	p.close()
	resp, err := http.Get(base + p.path)
	if err != nil {
		t.Fatalf("the port was released: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410", resp.StatusCode)
	}
}

// The listener goes by itself a pageLinger after the page's time is up: nothing else ends it.
func TestSecretPageServerStopsAfterItsTime(t *testing.T) {
	p := newSecretPage(template.Must(template.New("page").Parse(secretPageHTML)), tokenPageInfo())
	base, closed, err := serveLocal(slog.New(slog.DiscardHandler), p, time.Now().Add(-pageLinger+50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	eventually(t, func() bool {
		resp, err := client.Get(base + p.path)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err != nil
	})
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("listener shutdown was not reported")
	}
}

// Without these, net/http waits for the next request on an idle connection forever, and a local
// process could hold any number of them open while a page lives.
func TestSecretPageServerBoundsConnections(t *testing.T) {
	srv := pageServer(http.NotFoundHandler())
	for name, d := range map[string]time.Duration{
		"ReadHeaderTimeout": srv.ReadHeaderTimeout, "ReadTimeout": srv.ReadTimeout,
		"WriteTimeout": srv.WriteTimeout, "IdleTimeout": srv.IdleTimeout,
	} {
		if d <= 0 || d > time.Minute {
			t.Errorf("%s = %v", name, d)
		}
	}
}
