package tools

import (
	"context"
	"crypto/rand"
	"errors"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// maxTokenForm caps the size of the submitted form; access tokens are a few hundred bytes.
const maxTokenForm = 16 << 10

// tokenPage is a one-off local web page where the user pastes the access token. The token
// goes from the browser straight to this server, past the MCP client and the model, which is
// what URL-mode elicitation is for.
type tokenPage struct {
	path   string // unguessable, so only the URL handed to the user reaches the page
	info   tokenPageInfo
	tokens chan string
}

type tokenPageInfo struct {
	SourceRepo  string
	TokensPage  string
	TokenName   string
	Permissions string
	Variable    string
	Place       string
}

func newTokenPage(info tokenPageInfo) *tokenPage {
	return &tokenPage{path: "/" + rand.Text(), info: info, tokens: make(chan string, 1)}
}

func (p *tokenPage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != p.path {
		http.NotFound(w, r)
		return
	}
	// A DNS rebinding page reaches 127.0.0.1 under its own host name; a cross-site form
	// post carries its own origin.
	local, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	origin := r.Header.Get("Origin")
	if local == nil || r.Host != local.String() || origin != "" && origin != "http://"+local.String() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
	// Not no-referrer: with it browsers post the form with "Origin: null", which the check above
	// refuses. The secret path still stays out of other sites' Referer.
	w.Header().Set("Referrer-Policy", "same-origin")

	switch r.Method {
	case http.MethodGet:
		p.render(w, http.StatusOK, "")
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxTokenForm)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		token := strings.TrimSpace(r.PostForm.Get("token"))
		if token == "" {
			p.render(w, http.StatusBadRequest, "Paste the token first.")
			return
		}
		select {
		case p.tokens <- token:
			p.render(w, http.StatusOK, "Token received. You can close this tab and return to the MCP client.")
		default:
			p.render(w, http.StatusConflict, "A token was already received for this request.")
		}
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (p *tokenPage) render(w http.ResponseWriter, status int, notice string) {
	tmpl := template.Must(template.New("page").Parse(tokenPageHTML))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = tmpl.Execute(w, struct {
		tokenPageInfo
		Notice string
	}{p.info, notice})
}

// serveLocal serves h on a random loopback port until stop is called.
func serveLocal(log *slog.Logger, h http.Handler) (baseURL string, stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			log.Error("token page server failed", "error", err)
		}
	}()
	// Shutdown rather than Close: the page is stopped right after the token arrives, and the
	// browser must still get its "Token received" response.
	stop = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
	}
	return "http://" + ln.Addr().String(), stop, nil
}

const tokenPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Access token for {{.Variable}}</title>
<style>
body { font: 16px/1.5 system-ui, sans-serif; max-width: 40rem; margin: 2rem auto; padding: 0 1rem; }
input { width: 100%; padding: .5rem; font: inherit; box-sizing: border-box; -webkit-text-security: disc; }
button { margin-top: 1rem; padding: .5rem 1.5rem; font: inherit; }
code { background: #eee; padding: 0 .25rem; }
.notice { padding: .75rem; background: #fff4ce; }
</style>
</head>
<body>
<h1>Repository access token</h1>
{{if .Notice}}<p class="notice">{{.Notice}}</p>{{end}}
<ol>
<li>Open <a href="{{.TokensPage}}" target="_blank" rel="noopener noreferrer">access tokens of {{.SourceRepo}}</a> and create a token.<br>
Suggested name: <code>{{.TokenName}}</code><br>
Permissions: <code>{{.Permissions}}</code></li>
<li>Paste the token below. It goes only to the local MCP server, which stores it in the secured variable <code>{{.Variable}}</code> in {{.Place}}.</li>
</ol>
<form method="post" autocomplete="off">
<input type="text" name="token" aria-label="Access token" autocomplete="off" spellcheck="false" required autofocus>
<button type="submit">Submit</button>
</form>
</body>
</html>
`
