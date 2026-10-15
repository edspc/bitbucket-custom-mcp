package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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
	mu          sync.Mutex
	pipelines   map[string]bool
	projects    map[string]string // current project key by repository
	sshKeys     map[string]string
	deployKeys  map[string][]bitbucket.DeployKey
	deployments map[string][]bitbucket.Deployment
	envs        map[string][]bitbucket.Environment
	vars        map[string][]bitbucket.Variable // by repo, repo/envUUID for deployment variables or "workspace"
	validToken  string
	otherToken  string // a genuine token of another repository
	failAddKey  bool
	mutations   []string
	nextID      int
	pages       *stubPages
	tools       *tools // set by connect, for tests that inspect or tune the server's bookkeeping

	runs              map[string][]*bitbucket.Pipeline    // by repo, oldest first
	steps             map[string][]bitbucket.Step         // by pipeline UUID
	logs              map[string]string                   // by step UUID
	runBodies         []string                            // bodies of the run requests, as sent
	listQueries       []string                            // queries of the pipeline list requests
	logRanges         []string                            // Range headers of the log requests
	logStorage        bool                                // logs are redirected to long-term storage
	refCommit         string                              // head returned by the ref endpoint
	commits           map[string]string                   // full commits by workspace/repo:revision
	offRefCommits     map[string]bool                     // commits that are on another branch than the ref
	pipelineGet       int                                 // GETs of single pipelines so far
	pipelineGetStatus int                                 // a failure GETs of single pipelines answer with
	pipelineGetHeader http.Header                         // headers that go with the failure
	schedules         map[string][]bitbucket.Schedule     // by repo
	pullRequests      map[string]*bitbucket.PullRequest   // by repo/ID
	prStatuses        map[string][]bitbucket.CommitStatus // by repo/ID
	prChanges         map[string][]fakeChange             // by repo/ID
	diffQueries       []string                            // queries of the diff requests
	prRequests        []string                            // pull request requests, by what they ask for
	prListQueries     []string                            // queries of the pull request list requests
	diffStatus        int                                 // a failure the diff answers with
	diffstatStatus    int                                 // a failure the diffstat answers with
	mergeDiff         bool                                // merged PR redirects use the merge commit
	// onPipelineGet runs, with the lock held, before the n-th GET of a single pipeline is
	// answered, so a test can move a pipeline along while it is watched.
	onPipelineGet func(n int)
}

// fakeChange is how a pull request changes one file.
type fakeChange struct {
	path string
	stat bitbucket.DiffStat
	diff string // its part of the diff
}

// stubPages stands in for the local secret page server, so tests open no sockets.
type stubPages struct {
	mu      sync.Mutex
	handler http.Handler   // the page served last
	pages   []http.Handler // every page served
	tab     url.Values     // the query the page sent the user's tab on with, which it keeps
	opened  string         // the page as the user last saw it before submitting
}

const stubPagesHost = "127.0.0.1:4242"

func (s *stubPages) serve(h http.Handler, _ time.Time) (string, <-chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
	s.pages = append(s.pages, h)
	// No actual socket survives this stub. Tests of retained listeners supply an open channel.
	closed := make(chan struct{})
	close(closed)
	return "http://" + stubPagesHost, closed, nil
}

// openedPage is the page the user last submitted, as it showed before the submission.
func (s *stubPages) openedPage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opened
}

// stopCount is how many of the pages served were closed: they take no values any more.
func (s *stubPages) stopCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, h := range s.pages {
		if p, ok := h.(*secretPage); ok {
			p.mu.Lock()
			if p.closed {
				n++
			}
			p.mu.Unlock()
		}
	}
	return n
}

func (s *stubPages) served() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handler != nil
}

// do sends a request to the secret page as if it arrived on the page's loopback address; the
// Host header comes from the request URL unless the test changes it.
// The stub is the user's one browser tab: once the page has sent it on to its own URL, it keeps
// using that URL, and like a browser it follows the redirects of a GET.
func (s *stubPages) do(r *http.Request) *httptest.ResponseRecorder {
	s.mu.Lock()
	h := s.handler
	if s.tab != nil && r.URL.RawQuery == "" {
		r.URL.RawQuery = s.tab.Encode()
	}
	s.mu.Unlock()
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4242}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusSeeOther {
		return rec
	}
	next, err := r.URL.Parse(rec.Header().Get("Location"))
	if err != nil {
		panic(err)
	}
	if next.Query().Has(tabParam) {
		s.mu.Lock()
		s.tab = next.Query()
		s.mu.Unlock()
	}
	if r.Method != http.MethodGet {
		return rec
	}
	return s.do(httptest.NewRequest(http.MethodGet, next.String(), nil))
}

// submit opens the page and posts the values with the approval, as the user does.
func (s *stubPages) submit(pageURL string, values url.Values) int {
	body := s.do(httptestGet(pageURL)).Body.String()
	s.mu.Lock()
	s.opened = body
	s.mu.Unlock()
	values = maps.Clone(values)
	if values == nil {
		values = url.Values{}
	}
	values.Set("approval.action", "confirm")
	r := postForm(pageURL, values.Encode())
	r.Header.Set("Origin", "http://"+stubPagesHost) // what a browser sends with Referrer-Policy: same-origin
	return s.do(r).Code
}

const testPipelineCommit = "0123456789012345678901234567890123456789"

func connectApproving(t *testing.T, f *fakeBitbucket) *mcp.ClientSession {
	t.Helper()
	u := &fakeUser{pages: f.pages, action: "accept", values: url.Values{}}
	return connect(t, f, u.elicit)
}

func newFake() *fakeBitbucket {
	return &fakeBitbucket{
		deployments: map[string][]bitbucket.Deployment{},
		refCommit:   testPipelineCommit,
		commits:     map[string]string{},
		pipelines:   map[string]bool{"repo-a": true, "repo-b": true},
		sshKeys:     map[string]string{"repo-a": "ssh-rsa KEYA"},
		deployKeys:  map[string][]bitbucket.DeployKey{},
		envs:        map[string][]bitbucket.Environment{"repo-b": {{UUID: "{env-prod}", Name: "Production"}, {UUID: "{env-test}", Name: "Test"}}},
		vars:        map[string][]bitbucket.Variable{},
		validToken:  "repo-a-token",
		otherToken:  "repo-c-token",
		nextID:      100,
		pages:       &stubPages{},
		runs:        map[string][]*bitbucket.Pipeline{},
		steps:       map[string][]bitbucket.Step{},
		logs:        map[string]string{},
		schedules:   map[string][]bitbucket.Schedule{},

		pullRequests: map[string]*bitbucket.PullRequest{},
		prStatuses:   map[string][]bitbucket.CommitStatus{},
		prChanges:    map[string][]fakeChange{},
	}
}

// addRun adds a pipeline run of repo with the given state and steps and returns it.
func (f *fakeBitbucket) addRun(repo string, state bitbucket.State, steps ...bitbucket.Step) *bitbucket.Pipeline {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addRunLocked(repo, state, bitbucket.RefTarget("branch", "main", ""), steps)
}

func (f *fakeBitbucket) addRunLocked(repo string, state bitbucket.State, target bitbucket.PipelineTarget, steps []bitbucket.Step) *bitbucket.Pipeline {
	f.nextID++
	p := &bitbucket.Pipeline{
		UUID: fmt.Sprintf("{00000000-0000-4000-8000-%012d}", f.nextID), BuildNumber: len(f.runs[repo]) + 1, State: state, Target: target,
		CreatedOn: fmt.Sprintf("2026-10-10T10:%02d:00Z", len(f.runs[repo])),
	}
	f.runs[repo] = append(f.runs[repo], p)
	f.steps[p.UUID] = steps
	return p
}

// setState changes the state of a pipeline and of its steps by index; nil keeps a step as is.
func (f *fakeBitbucket) setState(p *bitbucket.Pipeline, state bitbucket.State, steps ...*bitbucket.State) {
	p.State = state
	for i, s := range steps {
		if s != nil {
			f.steps[p.UUID][i].State = *s
		}
	}
}

func (f *fakeBitbucket) findRun(repo, id string) *bitbucket.Pipeline {
	for _, p := range f.runs[repo] {
		if p.UUID == id || strconv.Itoa(p.BuildNumber) == id {
			return p
		}
	}
	return nil
}

func running() bitbucket.State {
	return bitbucket.State{Name: "IN_PROGRESS", Stage: &bitbucket.StateDetail{Name: "RUNNING"}}
}

func completed(result string) bitbucket.State {
	return bitbucket.State{Name: "COMPLETED", Result: &bitbucket.StateDetail{Name: result}}
}

func (f *fakeBitbucket) handler(t *testing.T) http.Handler {
	const repo = "/2.0/repositories/ws/{repo}"
	mux := http.NewServeMux()
	page := func(w http.ResponseWriter, values any) { writeJSON(w, map[string]any{"values": values}) }

	// Endpoints an access token is checked against: only repo-a's own token may read them.
	for _, path := range []string{"", "/pipelines", "/hooks"} {
		mux.HandleFunc("GET "+repo+path, func(w http.ResponseWriter, r *http.Request) {
			if _, _, ok := r.BasicAuth(); ok && path == "" && r.URL.Query().Get("fields") == "project.key,project.uuid" {
				writeJSON(w, map[string]any{"project": map[string]string{"key": f.projects[r.PathValue("repo")]}})
				return
			}
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
	for _, collection := range []string{"branches", "tags"} {
		mux.HandleFunc("GET "+repo+"/refs/"+collection+"/{ref}", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"target": bitbucket.Commit{Hash: f.refCommit}})
		})
	}
	mux.HandleFunc("GET "+repo+"/commits", func(w http.ResponseWriter, r *http.Request) {
		// The commits reachable from include but not from exclude, the ref's head.
		var values []bitbucket.Commit
		if include := r.URL.Query().Get("include"); f.offRefCommits[include] {
			values = append(values, bitbucket.Commit{Hash: include})
		}
		page(w, values)
	})
	mux.HandleFunc("GET /2.0/repositories/{workspace}/{repo}/commit/{commit}", func(w http.ResponseWriter, r *http.Request) {
		hash := r.PathValue("commit")
		if full, ok := f.commits[r.PathValue("workspace")+"/"+r.PathValue("repo")+":"+hash]; ok {
			hash = full
		} else if len(hash) < 40 {
			hash += strings.Repeat("0", 40-len(hash))
		}
		writeJSON(w, bitbucket.Commit{Hash: hash})
	})
	mux.HandleFunc("GET "+repo+"/deployments", func(w http.ResponseWriter, r *http.Request) {
		writeCollectionPage(w, r, f.deployments[r.PathValue("repo")])
	})
	mux.HandleFunc("POST "+repo+"/environments", func(w http.ResponseWriter, r *http.Request) {
		var env bitbucket.Environment
		decode(t, r, &env)
		f.nextID++
		env.UUID = fmt.Sprintf("{00000000-0000-4000-8000-%012d}", f.nextID)
		f.envs[r.PathValue("repo")] = append(f.envs[r.PathValue("repo")], env)
		writeJSON(w, env)
	})
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
		i := slices.IndexFunc(keys, func(k bitbucket.DeployKey) bool { return k.ID == id })
		if i < 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.deployKeys[r.PathValue("repo")] = slices.Delete(keys, i, i+1)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET "+repo+"/environments", func(w http.ResponseWriter, r *http.Request) {
		page(w, f.envs[r.PathValue("repo")])
	})

	varsKey := func(r *http.Request) string {
		if env := r.PathValue("env"); env != "" {
			return r.PathValue("repo") + "/" + env
		}
		if r.PathValue("repo") == "" {
			return "workspace"
		}
		return r.PathValue("repo")
	}
	// Unlike Bitbucket, the fake returns secured values too, so tests show that the tools
	// themselves never pass them on.
	listVars := func(w http.ResponseWriter, r *http.Request) {
		page(w, f.vars[varsKey(r)])
	}
	createVar := func(w http.ResponseWriter, r *http.Request) {
		var v bitbucket.Variable
		decode(t, r, &v)
		f.nextID++
		v.UUID = fmt.Sprintf("{var-%d}", f.nextID)
		f.vars[varsKey(r)] = append(f.vars[varsKey(r)], v)
		writeJSON(w, v)
	}
	findVar := func(r *http.Request) int {
		return slices.IndexFunc(f.vars[varsKey(r)], func(v bitbucket.Variable) bool { return v.UUID == r.PathValue("uuid") })
	}
	updateVar := func(w http.ResponseWriter, r *http.Request) {
		var v bitbucket.Variable
		decode(t, r, &v)
		i := findVar(r)
		if i < 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		v.UUID = r.PathValue("uuid")
		f.vars[varsKey(r)][i] = v
		writeJSON(w, v)
	}
	deleteVar := func(w http.ResponseWriter, r *http.Request) {
		i := findVar(r)
		if i < 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.vars[varsKey(r)] = slices.Delete(f.vars[varsKey(r)], i, i+1)
		w.WriteHeader(http.StatusNoContent)
	}
	for _, prefix := range []string{
		repo + "/pipelines_config/variables", repo + "/deployments_config/environments/{env}/variables",
		"/2.0/workspaces/ws/pipelines-config/variables",
	} {
		mux.HandleFunc("GET "+prefix, listVars)
		mux.HandleFunc("POST "+prefix, createVar)
		mux.HandleFunc("PUT "+prefix+"/{uuid}", updateVar)
		mux.HandleFunc("DELETE "+prefix+"/{uuid}", deleteVar)
	}

	mux.HandleFunc("GET "+repo+"/pipelines_config/schedules", func(w http.ResponseWriter, r *http.Request) {
		page(w, f.schedules[r.PathValue("repo")])
	})
	mux.HandleFunc("POST "+repo+"/pipelines_config/schedules", func(w http.ResponseWriter, r *http.Request) {
		var s bitbucket.Schedule
		decode(t, r, &s)
		if s.Type != "pipeline_schedule" {
			t.Errorf("schedule type = %q", s.Type)
		}
		f.nextID++
		s.UUID = fmt.Sprintf("{sch-%d}", f.nextID)
		f.schedules[r.PathValue("repo")] = append(f.schedules[r.PathValue("repo")], s)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, s)
	})
	mux.HandleFunc("DELETE "+repo+"/pipelines_config/schedules/{uuid}", func(w http.ResponseWriter, r *http.Request) {
		schedules := f.schedules[r.PathValue("repo")]
		i := slices.IndexFunc(schedules, func(s bitbucket.Schedule) bool { return s.UUID == r.PathValue("uuid") })
		if i < 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.schedules[r.PathValue("repo")] = slices.Delete(schedules, i, i+1)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET "+repo+"/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		f.prListQueries = append(f.prListQueries, r.URL.RawQuery)
		q := r.URL.Query()
		var out []*bitbucket.PullRequest
		for key, pr := range f.pullRequests {
			if strings.HasPrefix(key, r.PathValue("repo")+"/") && slices.Contains(q["state"], pr.State) && filterPicks(t, q.Get("q"), pr) {
				out = append(out, pr)
			}
		}
		slices.SortFunc(out, func(a, b *bitbucket.PullRequest) int { return strings.Compare(b.UpdatedOn, a.UpdatedOn) })
		writeCollectionPage(w, r, out)
	})
	prKey := func(r *http.Request) string { return r.PathValue("repo") + "/" + r.PathValue("id") }
	mux.HandleFunc("GET "+repo+"/pullrequests/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.prRequests = append(f.prRequests, "pull request")
		pr, ok := f.pullRequests[prKey(r)]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, pr)
	})
	mux.HandleFunc("GET "+repo+"/pullrequests/{id}/statuses", func(w http.ResponseWriter, r *http.Request) {
		f.prRequests = append(f.prRequests, "statuses")
		writeCollectionPage(w, r, f.prStatuses[prKey(r)])
	})
	// Like Bitbucket, a pull request redirects to the diff and the diffstat of its commits.
	for _, kind := range []string{"diff", "diffstat"} {
		mux.HandleFunc("GET "+repo+"/pullrequests/{id}/"+kind, func(w http.ResponseWriter, r *http.Request) {
			f.prRequests = append(f.prRequests, kind+" redirect")
			pr := f.pullRequests[prKey(r)]
			sourceRepo, sourceHash := pr.Source.Repository.FullName, pr.Source.Commit.Hash
			if f.mergeDiff && pr.State == "MERGED" && pr.MergeCommit != nil {
				sourceRepo, sourceHash = "ws/"+r.PathValue("repo"), pr.MergeCommit.Hash
			}
			w.Header().Set("Location", fmt.Sprintf("%s/repositories/ws/%[2]s/%[3]s/%[5]s:%[6]s%%0D%[7]s?from_pullrequest_id=%[4]s&topic=true",
				bitbucket.APIURL, r.PathValue("repo"), kind, r.PathValue("id"),
				sourceRepo, sourceHash, pr.Destination.Commit.Hash))
			w.WriteHeader(http.StatusFound)
		})
	}
	changes := func(r *http.Request) []fakeChange {
		return f.prChanges[r.PathValue("repo")+"/"+r.URL.Query().Get("from_pullrequest_id")]
	}
	mux.HandleFunc("GET "+repo+"/diff/{spec...}", func(w http.ResponseWriter, r *http.Request) {
		f.diffQueries = append(f.diffQueries, r.URL.RawQuery)
		f.prRequests = append(f.prRequests, "diff")
		if f.diffStatus != 0 {
			w.WriteHeader(f.diffStatus)
			return
		}
		paths := r.URL.Query()["path"]
		for _, c := range changes(r) {
			if len(paths) == 0 || slices.Contains(paths, c.path) {
				_, _ = io.WriteString(w, c.diff)
			}
		}
	})
	mux.HandleFunc("GET "+repo+"/diffstat/{spec...}", func(w http.ResponseWriter, r *http.Request) {
		f.prRequests = append(f.prRequests, "diffstat")
		if f.diffstatStatus != 0 {
			w.WriteHeader(f.diffstatStatus)
			return
		}
		stats := []bitbucket.DiffStat{}
		for _, c := range changes(r) {
			stats = append(stats, c.stat)
		}
		writeCollectionPage(w, r, stats)
	})

	mux.HandleFunc("POST "+repo+"/pipelines/{$}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.runBodies = append(f.runBodies, string(body))
		var req bitbucket.RunRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decoding run request: %v", err)
		}
		p := f.addRunLocked(r.PathValue("repo"), bitbucket.State{Name: "PENDING"}, req.Target, nil)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, p)
	})
	mux.HandleFunc("GET "+repo+"/pipelines/{$}", func(w http.ResponseWriter, r *http.Request) {
		f.listQueries = append(f.listQueries, r.URL.RawQuery)
		var out []*bitbucket.Pipeline
		for _, p := range slices.Backward(f.runs[r.PathValue("repo")]) {
			if b := r.URL.Query().Get("target.branch"); b == "" || p.Target.RefName == b {
				out = append(out, p)
			}
		}
		writeCollectionPage(w, r, out)
	})
	mux.HandleFunc("GET "+repo+"/pipelines/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.pipelineGet++
		if f.onPipelineGet != nil {
			f.onPipelineGet(f.pipelineGet)
		}
		if f.pipelineGetStatus != 0 {
			maps.Copy(w.Header(), f.pipelineGetHeader)
			w.WriteHeader(f.pipelineGetStatus)
			return
		}
		p := f.findRun(r.PathValue("repo"), r.PathValue("id"))
		if p == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, p)
	})
	mux.HandleFunc("GET "+repo+"/pipelines/{id}/steps/{$}", func(w http.ResponseWriter, r *http.Request) {
		page(w, f.steps[r.PathValue("id")])
	})
	serveLog := func(w http.ResponseWriter, r *http.Request) {
		f.logRanges = append(f.logRanges, r.Header.Get("Range"))
		log, ok := f.logs[r.PathValue("step")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n, err := strconv.Atoi(strings.TrimPrefix(r.Header.Get("Range"), "bytes=-"))
		if err != nil {
			_, _ = io.WriteString(w, log)
			return
		}
		if log == "" {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		start := max(len(log)-n, 0)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(log)-1, len(log)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, log[start:])
	}
	mux.HandleFunc("GET "+repo+"/pipelines/{id}/steps/{step}/log", func(w http.ResponseWriter, r *http.Request) {
		if f.logStorage {
			w.Header().Set("Location", "https://storage.example/logs/"+url.PathEscape(r.PathValue("step"))+"?signature=sig")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		serveLog(w, r)
	})
	mux.HandleFunc("GET storage.example/logs/{step}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("credentials were sent to the log storage")
		}
		if r.URL.Query().Get("signature") != "sig" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		serveLog(w, r)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method != http.MethodGet {
			f.mutations = append(f.mutations, r.Method+" "+r.URL.Path)
		}
		mux.ServeHTTP(w, r)
	})
}

// filterPicks reports whether pr meets the branch conditions of a pull request filter, the only
// ones the tools send: `source.branch.name = "…"` and the like, joined by AND.
func filterPicks(t *testing.T, filter string, pr *bitbucket.PullRequest) bool {
	for condition := range strings.SplitSeq(filter, " AND ") {
		if condition == "" {
			continue
		}
		field, quoted, _ := strings.Cut(condition, " = ")
		name, err := strconv.Unquote(quoted)
		if err != nil {
			t.Errorf("filter %q: %v", filter, err)
		}
		switch field {
		case "source.branch.name":
			if pr.Source.Branch.Name != name {
				return false
			}
		case "destination.branch.name":
			if pr.Destination.Branch.Name != name {
				return false
			}
		default:
			t.Errorf("unexpected filter %q", condition)
		}
	}
	return true
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

// writeCollectionPage mirrors Bitbucket's page/size metadata, including offsets past the end.
func writeCollectionPage[T any](w http.ResponseWriter, r *http.Request, values []T) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("pagelen"))
	if limit <= 0 {
		limit = 100
	}
	n, _ := strconv.Atoi(q.Get("page"))
	n = max(n, 1)
	start := len(values)
	if n-1 <= len(values)/limit {
		start = min((n-1)*limit, len(values))
	}
	end := start + min(limit, len(values)-start)
	page := map[string]any{"values": values[start:end], "size": len(values)}
	if end < len(values) {
		q.Set("page", strconv.Itoa(n+1))
		page["next"] = bitbucket.APIURL + r.URL.EscapedPath()[len("/2.0"):] + "?" + q.Encode()
	}
	writeJSON(w, page)
}

func decode(t *testing.T, r *http.Request, v any) {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		t.Errorf("decoding %s %s: %v", r.Method, r.URL.Path, err)
	}
}

// forEachProtocol runs test on the latest MCP version ("") and on 2025-11-25, which differ in
// how input requests reach the client and in how subscriptions work: a long-lived
// subscriptions/listen request from 2026-07-28, resources/subscribe before.
func forEachProtocol(t *testing.T, test func(t *testing.T, version string)) {
	for _, version := range []string{"", "2025-11-25"} {
		t.Run("protocol "+version, func(t *testing.T) { test(t, version) })
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
	return connectClient(t, newTestServer(t, f), opts, version)
}

// newTestServer serves the tools against f.
func newTestServer(t *testing.T, f *fakeBitbucket, policies ...Policy) *mcp.Server {
	bb := bitbucket.New(&http.Client{Transport: handlerTransport{f.handler(t)}}, bitbucket.Config{
		Workspace: "ws", Email: "dev@example.com", Token: "api-secret",
	})
	f.tools = &tools{ctx: t.Context(), bb: bb, log: slog.New(slog.DiscardHandler), serve: f.pages.serve,
		secretWait: 5 * time.Second, pollInterval: 10 * time.Millisecond, watchLifetime: time.Hour}
	if len(policies) != 0 {
		f.tools.policy = policies[0]
	}
	return newServer(&mcp.Implementation{Name: "test-server", Version: "0"}, f.tools)
}

// connectClient opens one more client session to s.
func connectClient(t *testing.T, s *mcp.Server, opts *mcp.ClientOptions, version string) *mcp.ClientSession {
	t.Helper()
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
	return slices.Clone(f.mutations)
}

func (f *fakeBitbucket) keysOf(repo string) []bitbucket.DeployKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deployKeys[repo])
}

func (f *fakeBitbucket) varsOf(key string) []bitbucket.Variable {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.vars[key])
}

// mustCall runs a tool that must succeed and returns its text output.
func mustCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	out, isErr := call(t, cs, name, args)
	if isErr {
		t.Fatalf("%s failed: %s", name, out)
	}
	return out
}

// decodeResult decodes the JSON output of a tool or a resource.
func decodeResult[T any](t *testing.T, out string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("decoding %s: %v", out, err)
	}
	return v
}

// eventually waits up to five seconds for cond to hold.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for start := time.Now(); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			t.Fatal("condition not met in time")
		}
	}
}

// expectPagesClosed fails unless every secret page opened so far, stopped times in all, is
// closed and no longer counts against the limit.
func expectPagesClosed(t *testing.T, f *fakeBitbucket, stopped int) {
	t.Helper()
	f.tools.mu.Lock()
	open := f.tools.openPages
	f.tools.mu.Unlock()
	if open != 0 || f.pages.stopCount() != stopped {
		t.Fatalf("open pages = %d, stopped = %d, want 0 and %d", open, f.pages.stopCount(), stopped)
	}
}
