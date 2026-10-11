package tools

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// maxSecretForm caps the size of the submitted form; tokens and secrets are a few KB at most.
	maxSecretForm = 64 << 10
	// pageLinger keeps a page's port after it closes, for the reload of a tab left open.
	pageLinger = time.Minute
	// tabParam carries the key of the tab that opened a page in its URL.
	tabParam = "tab"
)

// secretPage is a one-off local web page where the user enters secrets: an access token or
// secured variable values. They go from the browser straight to this server, past the MCP
// client and the model, which is what URL-mode elicitation is for.
type secretPage struct {
	path   string // unguessable, so only the URL handed to the user reaches the page
	info   secretPageInfo
	tmpl   *template.Template
	values chan map[string]string // by field name

	mu        sync.Mutex
	tab       string // key of the browser tab that opened the page first
	submitted bool
	closed    bool // the call is over; the page no longer takes values
}

// secretPageInfo is what the page tells the user and asks for.
type secretPageInfo struct {
	Tool      string     // MCP name of the tool that asks; set by askSecrets
	Title     string     // its human title
	Need      string     // what the user has to do now
	Outcome   string     // what happens with the values
	Token     *tokenHelp // set when the user first creates an access token
	Fields    []secretField
	Until     time.Time // when the page closes; set by askSecrets
	Workspace string
	Arguments string // complete non-secret arguments, rendered as escaped JSON
	Commit    string // pinned pipeline SHA, retained on the server for the approved run
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

	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case r.Method != http.MethodGet && r.Method != http.MethodPost:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	case p.submitted && r.Method == http.MethodGet:
		p.render(w, http.StatusOK, pageView{Done: true})
	case p.submitted:
		p.render(w, http.StatusConflict, pageView{Closed: "The values for this request were already received."})
	case p.closed || !time.Now().Before(p.info.Until):
		p.render(w, http.StatusGone, pageView{Closed: "This request is closed. Return to your MCP client; calling the tool again opens a new page."})
	case p.tab == "" && r.Method == http.MethodGet:
		p.tab = rand.Text()
		http.Redirect(w, r, p.tabURL(), http.StatusSeeOther)
	case !p.sameTab(r):
		p.render(w, http.StatusForbidden, pageView{Closed: "This page was already opened in another tab or browser. Use that tab if it is yours; if you did not open it, decline the request in your MCP client. Calling the tool again opens a new page."})
	case r.Method == http.MethodGet:
		p.render(w, http.StatusOK, pageView{})
	default:
		p.receive(w, r)
	}
}

// The page belongs to the tab that opens it first: that tab is sent on to the page under a key
// only it learns, so whoever else gets the URL handed to the user, from a process list for one,
// can neither read the form nor submit to it. Reloads keep the key, being the same URL.
func (p *secretPage) tabURL() string { return p.path + "?" + tabParam + "=" + p.tab }

func (p *secretPage) sameTab(r *http.Request) bool {
	key := r.URL.Query().Get(tabParam)
	return p.tab != "" && subtle.ConstantTimeCompare([]byte(key), []byte(p.tab)) == 1
}

// receive takes the submitted values. It answers with a redirect to the page, which then shows
// that they arrived: reloading that tab sends nothing again.
func (p *secretPage) receive(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSecretForm)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if r.PostForm.Get("approval.action") != "confirm" {
		p.render(w, http.StatusBadRequest, pageView{Problem: "Confirm the operation shown on this page."})
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
		p.render(w, http.StatusBadRequest, pageView{Problem: "Fill in " + strings.Join(missing, ", ") + "."})
		return
	}
	p.submitted = true
	p.values <- values
	http.Redirect(w, r, p.tabURL(), http.StatusSeeOther)
}

// close stops the page taking values once the call that opened it is over.
func (p *secretPage) close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}

// pageView is the state of the page that one response shows.
type pageView struct {
	Problem string // what is wrong with a submission
	Done    bool   // the values arrived
	Closed  string // why the page takes no values, when it does not
}

func (p *secretPage) render(w http.ResponseWriter, status int, v pageView) {
	refresh := 0
	if !v.Done && v.Closed == "" {
		// The form reloads when the page closes, so a tab left open drops it before the
		// listener goes; a form posted after that could reach whatever then holds the port.
		refresh = int(time.Until(p.info.Until)/time.Second) + 1
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = p.tmpl.Execute(w, struct {
		secretPageInfo
		pageView
		Refresh int
	}{p.info, v, refresh})
}

// serveLocal serves h on a random loopback port until until plus pageLinger, also after the
// page stops taking values: a tab the user still has open must reach this server, which refuses
// the values, and never another local process that took over a released port. The shutdown is
// set when the server starts, so no path can leave a listener behind.
func serveLocal(log *slog.Logger, h http.Handler, until time.Time) (baseURL string, closed <-chan struct{}, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	done := make(chan struct{})
	srv := pageServer(h)
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			log.Error("secret page server failed", "error", err)
		}
	}()
	time.AfterFunc(time.Until(until)+pageLinger, func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
	})
	return "http://" + ln.Addr().String(), done, nil
}

// pageServer bounds each connection in time, so idle ones held open by another local process
// cannot pile up while a page lives.
func pageServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second}
}
