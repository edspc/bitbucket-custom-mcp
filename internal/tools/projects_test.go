package tools

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProjectsRestrictToolsAndBothLinkRepositories(t *testing.T) {
	for _, tc := range []struct {
		name    string
		policy  Policy
		tool    string
		args    map[string]any
		allowed bool
	}{
		{"allowed", Policy{Projects: []string{"APP"}}, "list_variables", map[string]any{"scope": "repository", "repo": "repo-a"}, true},
		{"wrong project", Policy{Projects: []string{"OTHER"}}, "list_variables", map[string]any{"scope": "repository", "repo": "repo-a"}, false},
		{"empty projects", Policy{Projects: []string{}}, "list_variables", map[string]any{"scope": "repository", "repo": "repo-a"}, false},
		{"repo intersection", Policy{Projects: []string{"APP"}, Repositories: []string{"repo-b"}}, "list_variables", map[string]any{"scope": "repository", "repo": "repo-a"}, false},
		{"workspace", Policy{Projects: []string{"APP"}}, "list_variables", map[string]any{"scope": "workspace"}, false},
		{"key source", Policy{Projects: []string{"OPS"}}, "link_repos_by_key", keyArgs(false), false},
		{"key target", Policy{Projects: []string{"APP"}}, "link_repos_by_key", keyArgs(false), false},
		{"token source", Policy{Projects: []string{"OPS"}}, "link_repos_by_token", tokenArgs("repository", "", false), false},
		{"token target", Policy{Projects: []string{"APP"}}, "link_repos_by_token", tokenArgs("repository", "", false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.projects = map[string]string{"repo-a": "APP", "repo-b": "OPS"}
			u := &fakeUser{pages: f.pages, action: "decline"}
			cs := connectClient(t, newTestServer(t, f, tc.policy), clientOptions(u.elicit), "")
			out, isErr := call(t, cs, tc.tool, tc.args)
			if isErr == tc.allowed || (!tc.allowed && !strings.Contains(out, "server policy")) {
				t.Fatalf("allowed=%v: %s (error %v)", tc.allowed, out, isErr)
			}
			if f.pages.served() || len(f.mutationList()) != 0 {
				t.Fatal("denied call reached approval or a write")
			}
		})
	}
}

func TestProjectsProtectResourcesAndSubscriptions(t *testing.T) {
	f := newFake()
	f.projects = map[string]string{"repo-a": "OTHER"}
	f.addRun("repo-a", running())
	cs := connectClient(t, newTestServer(t, f, Policy{Projects: []string{"APP"}}), clientOptions(nil), "2025-11-25")
	uri := pipelineURI("repo-a", 1)
	if _, err := cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri}); err == nil {
		t.Fatal("resource bypassed project policy")
	}
	if err := cs.Subscribe(t.Context(), &mcp.SubscribeParams{URI: uri}); err == nil {
		t.Fatal("subscription bypassed project policy")
	}
	if f.pipelineGet != 0 {
		t.Fatal("denied resource fetched pipeline data")
	}
}

func TestProjectMoveDuringBrowserWaitBlocksEveryWrite(t *testing.T) {
	for _, tc := range changeCases() {
		repos := []string{"repo-a"}
		if repo, ok := tc.args["repo"].(string); ok {
			repos = []string{repo}
		} else if _, ok := tc.args["source_repo"]; ok {
			repos = []string{"repo-a", "repo-b"}
		}
		for _, repo := range repos {
			t.Run(tc.name+"/"+repo, func(t *testing.T) {
				f := approvalFake()
				f.projects = map[string]string{"repo-a": "APP", "repo-b": "APP"}
				opts := clientOptions((&fakeUser{action: "accept"}).elicit)
				opts.MultiRoundTrip = &mcp.MultiRoundTripOptions{Disabled: true}
				cs := connectClient(t, newTestServer(t, f, Policy{Projects: []string{"APP"}}), opts, "")
				first, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
				if err != nil || first.IsError || first.InputRequests[secretsInputID] == nil {
					t.Fatalf("initial approval: %+v, %v", first, err)
				}
				prompt := first.InputRequests[secretsInputID].(*mcp.ElicitParams)
				done := retryWaitingForBrowser(t, cs, tc.name, tc.args, first)
				f.mu.Lock()
				f.projects[repo] = "OTHER"
				f.mu.Unlock()
				values := url.Values{"NEW": {"secret-marker"}, "TOKEN": {"secret-marker"}, tokenField: {f.validToken}}
				if status := f.pages.submit(prompt.URL, values); status != http.StatusSeeOther {
					t.Fatal(status)
				}
				select {
				case res := <-done:
					if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "server policy") || len(f.mutationList()) != 0 {
						t.Fatalf("project move accepted: %+v, mutations=%v", res, f.mutationList())
					}
				case <-time.After(time.Second):
					t.Fatal("retry did not finish")
				}
				expectPagesClosed(t, f, 1)
			})
		}
	}
}

func TestProjectMoveStopsSubscriptionReads(t *testing.T) {
	f := newFake()
	f.projects = map[string]string{"repo-a": "APP"}
	f.addRun("repo-a", running())
	cs := connectClient(t, newTestServer(t, f, Policy{Projects: []string{"APP"}}), clientOptions(nil), "2025-11-25")
	if f.tools.pollInterval != 20*time.Millisecond {
		t.Fatal("project metadata requests did not adjust polling budget")
	}
	if err := cs.Subscribe(t.Context(), &mcp.SubscribeParams{URI: pipelineURI("repo-a", 1)}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.projects["repo-a"] = "OTHER"
	reads := f.pipelineGet
	f.mu.Unlock()
	eventually(t, func() bool {
		f.tools.mu.Lock()
		defer f.tools.mu.Unlock()
		return len(f.tools.watches) == 0
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pipelineGet != reads {
		t.Fatal("subscription kept reading after project move")
	}
}
