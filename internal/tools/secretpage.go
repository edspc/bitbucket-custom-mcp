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

// maxSecretForm caps the size of the submitted form; tokens and secrets are a few KB at most.
const maxSecretForm = 64 << 10

// secretPage is a one-off local web page where the user enters secrets: an access token or
// secured variable values. They go from the browser straight to this server, past the MCP
// client and the model, which is what URL-mode elicitation is for.
type secretPage struct {
	path   string // unguessable, so only the URL handed to the user reaches the page
	info   secretPageInfo
	tmpl   *template.Template
	values chan map[string]string // by field name
}

// secretPageInfo is what the page tells the user and asks for.
type secretPageInfo struct {
	Tool    string     // MCP name of the tool that asks; set by askSecrets
	Title   string     // its human title
	Need    string     // what the user has to do now
	Outcome string     // what happens with the values
	Token   *tokenHelp // set when the user first creates an access token
	Fields  []secretField
	Until   time.Time // when the page closes; set by askSecrets
}

// tokenHelp tells the user where and how to create the access token to paste.
type tokenHelp struct {
	Repo        string
	CreateURL   string
	Name        string
	Permissions []string
}

type secretField struct {
	Name  string // form field name and key of the submitted value
	Label string
	Hint  string
}

func newSecretPage(tmpl *template.Template, info secretPageInfo) *secretPage {
	return &secretPage{
		path:   "/" + rand.Text(),
		info:   info,
		tmpl:   tmpl,
		values: make(chan map[string]string, 1),
	}
}

func (p *secretPage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Cache-Control", "no-store")
	// Not no-referrer: with it browsers post the form with "Origin: null", which the check above
	// refuses. The secret path still stays out of other sites' Referer.
	w.Header().Set("Referrer-Policy", "same-origin")

	switch r.Method {
	case http.MethodGet:
		p.render(w, http.StatusOK, "", false)
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxSecretForm)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		values := map[string]string{}
		var missing []string
		for _, f := range p.info.Fields {
			v := strings.TrimSpace(r.PostForm.Get(f.Name))
			if v == "" {
				missing = append(missing, f.Label)
			}
			values[f.Name] = v
		}
		if len(missing) > 0 {
			p.render(w, http.StatusBadRequest, "Fill in "+strings.Join(missing, ", ")+".", false)
			return
		}
		select {
		case p.values <- values:
			p.render(w, http.StatusOK, "", true)
		default:
			p.render(w, http.StatusConflict, "The values for this request were already received.", false)
		}
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (p *secretPage) render(w http.ResponseWriter, status int, problem string, done bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = p.tmpl.Execute(w, struct {
		secretPageInfo
		Problem string
		Done    bool
	}{p.info, problem, done})
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
			log.Error("secret page server failed", "error", err)
		}
	}()
	// Shutdown rather than Close: the page is stopped right after the values arrive, and the
	// browser must still get its "received" response.
	stop = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
	}
	return "http://" + ln.Addr().String(), stop, nil
}
