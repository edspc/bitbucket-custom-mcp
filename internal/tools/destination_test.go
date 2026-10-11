package tools

import (
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestVariableChangesDuringBrowserWaitAreRefused(t *testing.T) {
	for _, tool := range []string{"plain", "secured", "token", "delete"} {
		for _, change := range []string{"destination", "replacement", "secured flag"} {
			t.Run(tool+"/"+change, func(t *testing.T) {
				f := newFake()
				cs := connectRetryingItself(t, f)
				name := "set_variable"
				key := "TOKEN"
				args := map[string]any{"scope": "deployment", "repo": "repo-b", "environment": "Production", "name": key, "secured": true, "force": true}
				values := url.Values{key: {"secret-marker"}}
				switch tool {
				case "plain":
					args["secured"], args["value"] = false, "plain"
					values = nil
				case "token":
					name, args = "link_repos_by_token", tokenArgs("deployment", "Production", true)
					key, values = "REPO_A_TOKEN", url.Values{tokenField: {f.validToken}}
				case "delete":
					name = "delete_variable"
					delete(args, "secured")
					delete(args, "force")
					values = nil
				}
				f.vars["repo-b/{env-prod}"] = []bitbucket.Variable{{UUID: "{old}", Key: key, Secured: true}}
				first, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
				if err != nil || first.IsError {
					t.Fatalf("initial approval: %+v, %v", first, err)
				}
				prompt := first.InputRequests[secretsInputID].(*mcp.ElicitParams)
				type result struct {
					res *mcp.CallToolResult
					err error
				}
				done := make(chan result, 1)
				ctx := t.Context()
				go func() {
					res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args, RequestState: first.RequestState,
						InputResponses: mcp.InputResponseMap{secretsInputID: &mcp.ElicitResult{Action: "accept"}}})
					done <- result{res, err}
				}()
				// The edit must happen after preflight, while the retry actually waits for
				// the browser. A change before starting the retry misses this regression.
				eventually(t, func() bool {
					stack := make([]byte, 1<<20)
					n := runtime.Stack(stack, true)
					return strings.Contains(string(stack[:n]), "(*tools).waitSecrets(")
				})
				f.mu.Lock()
				switch change {
				case "destination":
					f.envs["repo-b"] = []bitbucket.Environment{{UUID: "{env-prod}", Name: "Archive"}, {UUID: "{env-test}", Name: "Production"}}
				case "replacement":
					f.vars["repo-b/{env-prod}"][0].UUID = "{replacement}"
				case "secured flag":
					f.vars["repo-b/{env-prod}"][0].Secured = false
				}
				f.mu.Unlock()
				if status := f.pages.submit(prompt.URL, values); status != http.StatusSeeOther {
					t.Fatal(status)
				}
				select {
				case got := <-done:
					if got.err != nil || !got.res.IsError || len(f.mutationList()) != 0 {
						t.Fatalf("changed target accepted: %+v, %v, mutations=%v", got.res, got.err, f.mutationList())
					}
				case <-time.After(time.Second):
					t.Fatal("retry did not finish")
				}
				expectPagesClosed(t, f, 1)
			})
		}
	}
}

func TestVariableApprovalDeadlineCoversFinalDestinationCheck(t *testing.T) {
	for _, tool := range []string{"set_variable", "link_repos_by_token", "delete_variable"} {
		t.Run(tool, func(t *testing.T) {
			f := newFake()
			cs := connectRetryingItself(t, f)
			f.tools.secretWait = 500 * time.Millisecond
			args := map[string]any{"scope": "repository", "repo": "repo-b", "name": "TOKEN", "secured": true, "force": true}
			key, values := "TOKEN", url.Values{"TOKEN": {"secret-marker"}}
			switch tool {
			case "link_repos_by_token":
				args = tokenArgs("repository", "", true)
				key, values = "REPO_A_TOKEN", url.Values{tokenField: {f.validToken}}
			case "delete_variable":
				delete(args, "secured")
				delete(args, "force")
				values = nil
			}
			f.vars["repo-b"] = []bitbucket.Variable{{UUID: "{old}", Key: key, Secured: true}}
			upstream := f.handler(t)
			reads := 0
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/2.0/repositories/ws/repo-b/pipelines_config/variables" {
					reads++
					if reads == 3 {
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
			first, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
			if err != nil || first.IsError {
				t.Fatalf("initial approval: %+v, %v", first, err)
			}
			if status := f.pages.submit(first.InputRequests[secretsInputID].(*mcp.ElicitParams).URL, values); status != http.StatusSeeOther {
				t.Fatal(status)
			}
			start := time.Now()
			res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args, RequestState: first.RequestState,
				InputResponses: mcp.InputResponseMap{secretsInputID: &mcp.ElicitResult{Action: "accept"}}})
			if err != nil || !res.IsError || len(f.mutationList()) != 0 || reads != 3 || time.Since(start) > time.Second {
				t.Fatalf("expired final check: %+v, %v, reads=%d, mutations=%v", res, err, reads, f.mutationList())
			}
			expectPagesClosed(t, f, 1)
		})
	}
}

func TestVariableApprovalBindsDeploymentUUID(t *testing.T) {
	for _, kind := range []string{"plain", "secured", "token"} {
		t.Run(kind, func(t *testing.T) {
			f := newFake()
			cs := connectRetryingItself(t, f)
			name := "set_variable"
			args := map[string]any{"scope": "deployment", "repo": "repo-b", "environment": "Production", "name": "TOKEN", "secured": true}
			values := url.Values{"TOKEN": {"secret-marker"}}
			switch kind {
			case "plain":
				args["secured"], args["value"] = false, "plain"
				values = nil
			case "token":
				name, args = "link_repos_by_token", tokenArgs("deployment", "Production", false)
				values = url.Values{tokenField: {f.validToken}}
			}
			first, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
			if err != nil || first.IsError {
				t.Fatalf("initial approval: %+v, %v", first, err)
			}
			pageURL := first.InputRequests[secretsInputID].(*mcp.ElicitParams).URL
			if status := f.pages.submit(pageURL, values); status != http.StatusSeeOther {
				t.Fatal(status)
			}
			if !strings.Contains(f.pages.openedPage(), "{env-prod}") {
				t.Fatal("approval did not show the deployment UUID")
			}
			f.mu.Lock()
			f.envs["repo-b"] = []bitbucket.Environment{{UUID: "{env-prod}", Name: "Former Production"}, {UUID: "{env-test}", Name: "Production"}}
			f.mu.Unlock()
			res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args, RequestState: first.RequestState, InputResponses: mcp.InputResponseMap{secretsInputID: &mcp.ElicitResult{Action: "accept"}}})
			if err != nil || !res.IsError || len(f.mutationList()) != 0 {
				t.Fatalf("changed destination accepted approval: %+v, %v", res, err)
			}
			expectPagesClosed(t, f, 1)
		})
	}
}

func TestVariableApprovalBindsReplacementUUID(t *testing.T) {
	for _, tool := range []string{"set_variable", "link_repos_by_token"} {
		t.Run(tool, func(t *testing.T) {
			f := newFake()
			cs := connectRetryingItself(t, f)
			args := map[string]any{"scope": "repository", "repo": "repo-b", "name": "TOKEN", "secured": true, "force": true}
			key, values := "TOKEN", url.Values{"TOKEN": {"secret-marker"}}
			if tool == "link_repos_by_token" {
				args = tokenArgs("repository", "", true)
				key, values = "REPO_A_TOKEN", url.Values{tokenField: {f.validToken}}
			}
			f.vars["repo-b"] = []bitbucket.Variable{{UUID: "{old}", Key: key, Secured: true}}
			first, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
			if err != nil || first.IsError {
				t.Fatalf("initial approval: %+v, %v", first, err)
			}
			if status := f.pages.submit(first.InputRequests[secretsInputID].(*mcp.ElicitParams).URL, values); status != http.StatusSeeOther {
				t.Fatal(status)
			}
			f.mu.Lock()
			f.vars["repo-b"][0].UUID = "{replacement}"
			f.mu.Unlock()
			res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args, RequestState: first.RequestState, InputResponses: mcp.InputResponseMap{secretsInputID: &mcp.ElicitResult{Action: "accept"}}})
			if err != nil || !res.IsError || len(f.mutationList()) != 0 {
				t.Fatalf("changed replacement accepted approval: %+v, %v", res, err)
			}
		})
	}
}
