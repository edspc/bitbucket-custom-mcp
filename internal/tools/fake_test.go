package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

// fakeBitbucket keeps just enough repository state to serve the endpoints the tools use.
type fakeBitbucket struct {
	mu         sync.Mutex
	pipelines  map[string]bool
	sshKeys    map[string]string
	deployKeys map[string][]bitbucket.DeployKey
	envs       map[string][]bitbucket.Environment
	vars       map[string][]bitbucket.Variable // by repo, or repo/envUUID for deployment variables
	validToken string
	otherToken string // a genuine token of another repository
	failAddKey bool
	mutations  []string
	nextID     int
	pages      *stubPages
	tokenWait  time.Duration
	tools      *tools // set by connect, for tests that inspect the token page bookkeeping
}

// stubPages stands in for the local token page server, so tests open no sockets.
type stubPages struct {
	mu      sync.Mutex
	handler http.Handler
	stops   int
}

const stubPagesHost = "127.0.0.1:4242"

func (s *stubPages) serve(h http.Handler) (string, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
	return "http://" + stubPagesHost, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.stops++
	}, nil
}

func (s *stubPages) stopCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stops
}

func (s *stubPages) served() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handler != nil
}

// do sends a request to the token page as if it arrived on the page's loopback address; the
// Host header comes from the request URL unless the test changes it.
func (s *stubPages) do(r *http.Request) *httptest.ResponseRecorder {
	s.mu.Lock()
	h := s.handler
	s.mu.Unlock()
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4242}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func (s *stubPages) submit(pageURL, token string) int {
	r := httptest.NewRequest(http.MethodPost, pageURL, strings.NewReader(url.Values{"token": {token}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://"+stubPagesHost) // what a browser sends with Referrer-Policy: same-origin
	return s.do(r).Code
}

func newFake() *fakeBitbucket {
	return &fakeBitbucket{
		pipelines:  map[string]bool{"repo-a": true, "repo-b": true},
		sshKeys:    map[string]string{"repo-a": "ssh-rsa KEYA"},
		deployKeys: map[string][]bitbucket.DeployKey{},
		envs:       map[string][]bitbucket.Environment{"repo-b": {{UUID: "{env-prod}", Name: "Production"}, {UUID: "{env-test}", Name: "Test"}}},
		vars:       map[string][]bitbucket.Variable{},
		validToken: "repo-a-token",
		otherToken: "repo-c-token",
		nextID:     100,
		pages:      &stubPages{},
		tokenWait:  5 * time.Second,
	}
}

func (f *fakeBitbucket) handler(t *testing.T) http.Handler {
	const repo = "/2.0/repositories/ws/{repo}"
	mux := http.NewServeMux()
	page := func(w http.ResponseWriter, values any) { writeJSON(w, map[string]any{"values": values}) }

	// Endpoints an access token is checked against: only repo-a's own token may read them.
	for _, path := range []string{"", "/pipelines", "/hooks"} {
		mux.HandleFunc("GET "+repo+path, func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("Authorization") {
			case "Bearer " + f.validToken:
				writeJSON(w, map[string]any{"values": []any{}})
			case "Bearer " + f.otherToken:
				w.WriteHeader(http.StatusForbidden)
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		})
	}
	mux.HandleFunc("GET "+repo+"/pipelines_config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"enabled": f.pipelines[r.PathValue("repo")]})
	})
	mux.HandleFunc("GET "+repo+"/pipelines_config/ssh/key_pair", func(w http.ResponseWriter, r *http.Request) {
		key, ok := f.sshKeys[r.PathValue("repo")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]string{"public_key": key})
	})
	mux.HandleFunc("GET "+repo+"/deploy-keys", func(w http.ResponseWriter, r *http.Request) {
		page(w, f.deployKeys[r.PathValue("repo")])
	})
	mux.HandleFunc("POST "+repo+"/deploy-keys", func(w http.ResponseWriter, r *http.Request) {
		if f.failAddKey {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var k bitbucket.DeployKey
		decode(t, r, &k)
		f.nextID++
		k.ID = f.nextID
		f.deployKeys[r.PathValue("repo")] = append(f.deployKeys[r.PathValue("repo")], k)
		writeJSON(w, k)
	})
	mux.HandleFunc("DELETE "+repo+"/deploy-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		keys := f.deployKeys[r.PathValue("repo")]
		for i, k := range keys {
			if k.ID == id {
				f.deployKeys[r.PathValue("repo")] = append(keys[:i:i], keys[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("GET "+repo+"/environments", func(w http.ResponseWriter, r *http.Request) {
		page(w, f.envs[r.PathValue("repo")])
	})

	varsKey := func(r *http.Request) string {
		if env := r.PathValue("env"); env != "" {
			return r.PathValue("repo") + "/" + env
		}
		return r.PathValue("repo")
	}
	listVars := func(w http.ResponseWriter, r *http.Request) {
		var out []bitbucket.Variable
		for _, v := range f.vars[varsKey(r)] {
			if v.Secured {
				v.Value = ""
			}
			out = append(out, v)
		}
		page(w, out)
	}
	createVar := func(w http.ResponseWriter, r *http.Request) {
		var v bitbucket.Variable
		decode(t, r, &v)
		f.nextID++
		v.UUID = fmt.Sprintf("{var-%d}", f.nextID)
		f.vars[varsKey(r)] = append(f.vars[varsKey(r)], v)
		writeJSON(w, v)
	}
	updateVar := func(w http.ResponseWriter, r *http.Request) {
		var v bitbucket.Variable
		decode(t, r, &v)
		vars := f.vars[varsKey(r)]
		for i := range vars {
			if vars[i].UUID == r.PathValue("uuid") {
				v.UUID = vars[i].UUID
				vars[i] = v
				writeJSON(w, v)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}
	for _, prefix := range []string{repo + "/pipelines_config/variables", repo + "/deployments_config/environments/{env}/variables"} {
		mux.HandleFunc("GET "+prefix, listVars)
		mux.HandleFunc("POST "+prefix, createVar)
		mux.HandleFunc("PUT "+prefix+"/{uuid}", updateVar)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method != http.MethodGet {
			f.mutations = append(f.mutations, r.Method+" "+r.URL.Path)
		}
		mux.ServeHTTP(w, r)
	})
}

// handlerTransport serves requests in-process, so tests need no network listener.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func decode(t *testing.T, r *http.Request, v any) {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		t.Errorf("decoding %s %s: %v", r.Method, r.URL.Path, err)
	}
}

// connect serves the tools against f and returns a client session that supports form and URL
// elicitation; elicit may be nil to model a client without elicitation support.
func connect(t *testing.T, f *fakeBitbucket, elicit func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	return connectWith(t, f, clientOptions(elicit), "")
}

func clientOptions(elicit func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientOptions {
	opts := &mcp.ClientOptions{ElicitationHandler: elicit}
	if elicit != nil {
		opts.Capabilities = &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{
			Form: &mcp.FormElicitationCapabilities{}, URL: &mcp.URLElicitationCapabilities{},
		}}
	}
	return opts
}

// connectWith is connect with explicit client options and MCP protocol version; "" means the latest.
func connectWith(t *testing.T, f *fakeBitbucket, opts *mcp.ClientOptions, version string) *mcp.ClientSession {
	t.Helper()
	bb := bitbucket.New(&http.Client{Transport: handlerTransport{f.handler(t)}}, bitbucket.Config{
		Workspace: "ws", Email: "dev@example.com", Token: "api-secret",
	})
	s := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0"}, nil)
	f.tools = &tools{bb: bb, log: slog.New(slog.DiscardHandler), serve: f.pages.serve, tokenWait: f.tokenWait}
	register(s, f.tools)

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, opts)
	cs, err := c.Connect(ctx, clientT, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call runs a tool and returns its text output and whether it reported an error.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	return text, res.IsError
}

func (f *fakeBitbucket) mutationList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.mutations...)
}

func (f *fakeBitbucket) keysOf(repo string) []bitbucket.DeployKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bitbucket.DeployKey(nil), f.deployKeys[repo]...)
}

func (f *fakeBitbucket) varsOf(key string) []bitbucket.Variable {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bitbucket.Variable(nil), f.vars[key]...)
}
