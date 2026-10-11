package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// retryWaitingForBrowser calls tool again with the approval accepted and returns once the retry
// waits for the browser submission, with a channel for its outcome.
func retryWaitingForBrowser(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, first *mcp.CallToolResult) <-chan *mcp.CallToolResult {
	t.Helper()
	done := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args, RequestState: first.RequestState,
			InputResponses: mcp.InputResponseMap{secretsInputID: &mcp.ElicitResult{Action: "accept"}}})
		if err != nil {
			res = &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
		}
		done <- res
	}()
	eventually(t, func() bool {
		stack := make([]byte, 1<<20)
		n := runtime.Stack(stack, true)
		return strings.Contains(string(stack[:n]), "(*tools).waitSecrets(")
	})
	return done
}

// A schedule edited in Bitbucket while the user reviews its deletion is not deleted on the old
// approval, and one moved to a branch the ref policy does not allow is not deleted at all.
func TestScheduleChangesDuringBrowserWaitAreRefused(t *testing.T) {
	for _, change := range []string{"cron", "branch"} {
		t.Run(change, func(t *testing.T) {
			f := approvalFake()
			cs := connectRetryingItself(t, f)
			f.tools.policy = Policy{Refs: []string{"branch:main"}}
			args := map[string]any{"repo": "repo-a", "schedule": "{schedule}"}
			first, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "delete_schedule", Arguments: args})
			if err != nil || first.IsError {
				t.Fatalf("initial approval: %+v, %v", first, err)
			}
			prompt := first.InputRequests[secretsInputID].(*mcp.ElicitParams)
			done := retryWaitingForBrowser(t, cs, "delete_schedule", args, first)
			f.mu.Lock()
			if change == "cron" {
				f.schedules["repo-a"][0].CronPattern = "0 0 3 * * ? *"
			} else {
				f.schedules["repo-a"][0].Target.RefName = "release"
			}
			f.mu.Unlock()
			if status := f.pages.submit(prompt.URL, nil); status != http.StatusSeeOther {
				t.Fatal(status)
			}
			select {
			case res := <-done:
				if !res.IsError || len(f.mutationList()) != 0 {
					t.Fatalf("changed schedule deleted: %+v, mutations=%v", res, f.mutationList())
				}
			case <-time.After(time.Second):
				t.Fatal("retry did not finish")
			}
			expectPagesClosed(t, f, 1)
		})
	}
}

// Before MCP 2026-07-28 the elicitation happens within the call; a call that ends there, as when
// the user cancels it, must close its page rather than leave it taking values for minutes.
func TestPageClosesWithACallThatNeverComesBack(t *testing.T) {
	f := newFake()
	failing := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return nil, errors.New("the user cancelled")
	}
	cs := connectWith(t, f, clientOptions(failing), "2025-11-25")
	f.tools.secretWait = time.Minute // longer than the test waits, so only the call's end closes the page
	args := map[string]any{"scope": "repository", "repo": "repo-b", "name": "NEW", "value": "plain"}
	if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "set_variable", Arguments: args}); err == nil {
		t.Fatal("the call succeeded")
	}
	// The page closes once the server has finished the request, just after the client saw it end.
	eventually(t, func() bool { return f.pages.stopCount() == 1 })
	expectPagesClosed(t, f, 1)
	if got := f.pages.submit("http://"+stubPagesHost+f.pages.handler.(*secretPage).path, nil); got != http.StatusGone {
		t.Fatalf("the abandoned page took values: %d", got)
	}
	if len(f.mutationList()) != 0 {
		t.Fatalf("mutations = %v", f.mutationList())
	}
}

// A write approved just in time is not cancelled by the approval's deadline once it is sent:
// Bitbucket would still store it, and the tool would report a failure.
func TestApprovedWriteOutlivesTheApprovalDeadline(t *testing.T) {
	f := newFake()
	cs := connectRetryingItself(t, f)
	f.tools.secretWait = 300 * time.Millisecond
	upstream := f.handler(t)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pipelines_config/variables") {
			// The page's time runs out while the write is on its way. Bitbucket stores it
			// all the same; a caller that gave up only sees a failure.
			select {
			case <-r.Context().Done():
				upstream.ServeHTTP(httptest.NewRecorder(), r)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
		upstream.ServeHTTP(w, r)
	})
	f.tools.bb = bitbucket.New(&http.Client{Transport: handlerTransport{h}}, bitbucket.Config{Workspace: "ws"})
	args := map[string]any{"scope": "repository", "repo": "repo-b", "name": "NEW", "value": "plain"}
	first, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "set_variable", Arguments: args})
	if err != nil || first.IsError {
		t.Fatalf("initial approval: %+v, %v", first, err)
	}
	if status := f.pages.submit(first.InputRequests[secretsInputID].(*mcp.ElicitParams).URL, url.Values{}); status != http.StatusSeeOther {
		t.Fatal(status)
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "set_variable", Arguments: args, RequestState: first.RequestState,
		InputResponses: mcp.InputResponseMap{secretsInputID: &mcp.ElicitResult{Action: "accept"}}})
	if err != nil || res.IsError || len(f.mutationList()) != 1 {
		t.Fatalf("write reported as failed: %+v, %v, mutations=%v", res, err, f.mutationList())
	}
}

// Keys rotated or replaced in Bitbucket while the user reviews a link are not changed on the old
// approval: the stale public key is not added and no Access Key is deleted by a stale ID.
func TestKeyChangesDuringBrowserWaitAreRefused(t *testing.T) {
	for _, change := range []string{"source key rotated", "labelled key replaced"} {
		t.Run(change, func(t *testing.T) {
			f := newFake()
			f.deployKeys["repo-b"] = []bitbucket.DeployKey{{ID: 7, Key: "ssh-rsa OLD", Label: "repo-a"}}
			cs := connectRetryingItself(t, f)
			args := keyArgs(true)
			first, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "link_repos_by_key", Arguments: args})
			if err != nil || first.IsError {
				t.Fatalf("initial approval: %+v, %v", first, err)
			}
			prompt := first.InputRequests[secretsInputID].(*mcp.ElicitParams)
			done := retryWaitingForBrowser(t, cs, "link_repos_by_key", args, first)
			f.mu.Lock()
			if change == "source key rotated" {
				f.sshKeys["repo-a"] = "ssh-rsa ROTATED"
			} else {
				f.deployKeys["repo-b"][0].ID = 8
			}
			f.mu.Unlock()
			if status := f.pages.submit(prompt.URL, nil); status != http.StatusSeeOther {
				t.Fatal(status)
			}
			select {
			case res := <-done:
				if !res.IsError || len(f.mutationList()) != 0 {
					t.Fatalf("changed keys linked: %+v, mutations=%v", res, f.mutationList())
				}
			case <-time.After(time.Second):
				t.Fatal("retry did not finish")
			}
			expectPagesClosed(t, f, 1)
		})
	}
}

// A key that appears in the target while the user reviews the link leaves nothing to change:
// the call reports it and, even with force, deletes no labelled key on the old approval.
func TestKeyLinkedDuringBrowserWaitChangesNothing(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint("force=", force), func(t *testing.T) {
			f := newFake()
			if force {
				f.deployKeys["repo-b"] = []bitbucket.DeployKey{{ID: 7, Key: "ssh-rsa OLD", Label: "repo-a"}}
			}
			cs := connectRetryingItself(t, f)
			args := keyArgs(force)
			first, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "link_repos_by_key", Arguments: args})
			if err != nil || first.IsError {
				t.Fatalf("initial approval: %+v, %v", first, err)
			}
			prompt := first.InputRequests[secretsInputID].(*mcp.ElicitParams)
			done := retryWaitingForBrowser(t, cs, "link_repos_by_key", args, first)
			f.mu.Lock()
			f.deployKeys["repo-b"] = append(f.deployKeys["repo-b"], bitbucket.DeployKey{ID: 9, Key: f.sshKeys["repo-a"], Label: "manual"})
			f.mu.Unlock()
			if status := f.pages.submit(prompt.URL, nil); status != http.StatusSeeOther {
				t.Fatal(status)
			}
			select {
			case res := <-done:
				text := res.Content[0].(*mcp.TextContent).Text
				if res.IsError || !strings.Contains(text, `"already_linked"`) || len(f.mutationList()) != 0 {
					t.Fatalf("result = %s, mutations=%v", text, f.mutationList())
				}
			case <-time.After(time.Second):
				t.Fatal("retry did not finish")
			}
		})
	}
}

// The recheck after approval runs under the approval's deadline: one still running when the
// page's time is up changes no Access Key.
func TestKeyLinkApprovalDeadlineCoversFinalCheck(t *testing.T) {
	f := newFake()
	cs := connectRetryingItself(t, f)
	f.tools.secretWait = 500 * time.Millisecond
	upstream := f.handler(t)
	reads := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pipelines_config/ssh/key_pair") {
			if reads++; reads == 3 { // the first call, the retry before the wait, and the final check
				select {
				case <-r.Context().Done():
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				case <-time.After(2 * time.Second):
				}
			}
		}
		upstream.ServeHTTP(w, r)
	})
	f.tools.bb = bitbucket.New(&http.Client{Transport: handlerTransport{h}}, bitbucket.Config{Workspace: "ws"})
	args := keyArgs(false)
	first, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "link_repos_by_key", Arguments: args})
	if err != nil || first.IsError {
		t.Fatalf("initial approval: %+v, %v", first, err)
	}
	if status := f.pages.submit(first.InputRequests[secretsInputID].(*mcp.ElicitParams).URL, nil); status != http.StatusSeeOther {
		t.Fatal(status)
	}
	start := time.Now()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "link_repos_by_key", Arguments: args, RequestState: first.RequestState,
		InputResponses: mcp.InputResponseMap{secretsInputID: &mcp.ElicitResult{Action: "accept"}}})
	if err != nil || !res.IsError || len(f.mutationList()) != 0 || reads != 3 || time.Since(start) > time.Second {
		t.Fatalf("expired final check: %+v, %v, reads=%d, mutations=%v", res, err, reads, f.mutationList())
	}
	expectPagesClosed(t, f, 1)
}

// An approval that runs out during the final recheck, after that recheck's last read succeeded,
// stops every write: the deadline on the reads alone cannot, as nothing is left to cut short.
func TestApprovalExpiringDuringFinalCheckStopsWrites(t *testing.T) {
	for _, tc := range []struct {
		tool  string
		args  map[string]any
		path  string // what the final check reads last
		reads int    // which read of it that is: the first call's, the retry's before the wait, then the final one
	}{
		{"link_repos_by_key", keyArgs(false), "/repositories/ws/repo-b/deploy-keys", 3},
		{"set_variable", map[string]any{"scope": "repository", "repo": "repo-b", "name": "NEW", "value": "plain"}, "/repositories/ws/repo-b/pipelines_config/variables", 3},
		{"delete_variable", map[string]any{"scope": "repository", "repo": "repo-b", "name": "EXISTING"}, "/repositories/ws/repo-b/pipelines_config/variables", 3},
		{"link_repos_by_token", tokenArgs("repository", "", false), "/repositories/ws/repo-b/pipelines_config/variables", 3},
		{"delete_schedule", map[string]any{"repo": "repo-a", "schedule": "{schedule}"}, "/repositories/ws/repo-a/pipelines_config/schedules", 3},
		// The duplicate check before a new schedule is read only after approval.
		{"add_schedule", map[string]any{"repo": "repo-a", "branch": "main", "pipeline": "default", "cron": "0 0 4 * * ? *"}, "/repositories/ws/repo-a/pipelines_config/schedules", 2},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			f := approvalFake()
			cs := connectRetryingItself(t, f)
			upstream := f.handler(t)
			reads := 0
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstream.ServeHTTP(w, r)
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, tc.path) {
					if reads++; reads == tc.reads {
						f.pages.mu.Lock()
						f.pages.handler.(*secretPage).info.Until = time.Now().Add(-time.Second)
						f.pages.mu.Unlock()
					}
				}
			})
			f.tools.bb = bitbucket.New(&http.Client{Transport: handlerTransport{h}}, bitbucket.Config{Workspace: "ws"})
			first, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			if err != nil || first.IsError {
				t.Fatalf("initial approval: %+v, %v", first, err)
			}
			values := url.Values{}
			if tc.tool == "link_repos_by_token" {
				values = url.Values{tokenField: {f.validToken}}
			}
			if status := f.pages.submit(first.InputRequests[secretsInputID].(*mcp.ElicitParams).URL, values); status != http.StatusSeeOther {
				t.Fatal(status)
			}
			res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args, RequestState: first.RequestState,
				InputResponses: mcp.InputResponseMap{secretsInputID: &mcp.ElicitResult{Action: "accept"}}})
			if err != nil || !res.IsError || len(f.mutationList()) != 0 || reads != tc.reads {
				t.Fatalf("write on an expired approval: %+v, %v, reads=%d, mutations=%v", res, err, reads, f.mutationList())
			}
			if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "expired") {
				t.Fatalf("error = %s", text)
			}
		})
	}
}

// Replacing a key takes two writes; an approval that runs out after the first one does not
// cover the second, which deletes the old Access Key.
func TestKeyReplacementStopsWhenApprovalExpiresAfterAdding(t *testing.T) {
	f := newFake()
	f.deployKeys["repo-b"] = []bitbucket.DeployKey{{ID: 7, Key: "ssh-rsa OLD", Label: "repo-a"}}
	cs := connectRetryingItself(t, f)
	upstream := f.handler(t)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.ServeHTTP(w, r)
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repositories/ws/repo-b/deploy-keys") {
			f.pages.mu.Lock()
			f.pages.handler.(*secretPage).info.Until = time.Now().Add(-time.Second)
			f.pages.mu.Unlock()
		}
	})
	f.tools.bb = bitbucket.New(&http.Client{Transport: handlerTransport{h}}, bitbucket.Config{Workspace: "ws"})
	args := keyArgs(true)
	first, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "link_repos_by_key", Arguments: args})
	if err != nil || first.IsError {
		t.Fatalf("initial approval: %+v, %v", first, err)
	}
	if status := f.pages.submit(first.InputRequests[secretsInputID].(*mcp.ElicitParams).URL, nil); status != http.StatusSeeOther {
		t.Fatal(status)
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "link_repos_by_key", Arguments: args, RequestState: first.RequestState,
		InputResponses: mcp.InputResponseMap{secretsInputID: &mcp.ElicitResult{Action: "accept"}}})
	// Calling again would only find the new key linked, so the old one is left to the user.
	text := res.Content[0].(*mcp.TextContent).Text
	if err != nil || !res.IsError || !strings.Contains(text, "the new key was added to repo-b, but the approval expired") ||
		!strings.Contains(text, `Access Key "repo-a" (id 7)`) || !strings.Contains(text, "Repository settings > Access keys of repo-b") {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if m := f.mutationList(); len(m) != 1 || !strings.HasPrefix(m[0], "POST ") {
		t.Fatalf("mutations = %v, want only the added key", m)
	}
}
