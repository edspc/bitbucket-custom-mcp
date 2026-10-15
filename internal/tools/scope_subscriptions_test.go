package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBranchSubscriptionDiscoversFutureRuns(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, version string) {
		f := newFake()
		opts, updated := notifiedOptions()
		cs := connectWith(t, f, opts, version)
		const uri = scopeURIPrefix + "repo-a/branch/main"
		if err := subscribe(t, cs, uri); err != nil {
			t.Fatal(err)
		}
		eventually(t, func() bool { return f.watchCount() == 1 })
		p := f.addRun("repo-a", running())
		expectUpdate(t, updated, uri)
		f.mu.Lock()
		f.setState(p, completed("SUCCESSFUL"))
		f.mu.Unlock()
		expectUpdate(t, updated, uri)
		f.addRun("repo-a", running())
		expectUpdate(t, updated, uri)
		result, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
		if err != nil || !strings.Contains(result.Contents[0].Text, `"build_number":2`) {
			t.Fatalf("resource: %v %v", result, err)
		}
		if err = cs.Unsubscribe(context.Background(), &mcp.UnsubscribeParams{URI: uri}); err != nil {
			t.Fatal(err)
		}
		eventually(t, func() bool { return f.watchCount() == 0 })
	})
}

func TestScopeURIsAndPolicy(t *testing.T) {
	for _, uri := range []string{scopeURIPrefix + "repo-a/branch/feature%2Fnext", scopeURIPrefix + "repo-a/pullrequest/42", scopeURIPrefix + "repo-a/deployment/%7B01234567-89ab-cdef-0123-456789abcdef%7D"} {
		if _, _, _, ok := parseScopeURI(uri); !ok {
			t.Errorf("valid URI rejected: %s", uri)
		}
	}
	for _, uri := range []string{scopeURIPrefix + "repo-a/branch/a/b", scopeURIPrefix + "repo-a/pullrequest/-1", scopeURIPrefix + "repo-a/deployment/nope", scopeURIPrefix + "repo-a/branch/%zz"} {
		if _, _, _, ok := parseScopeURI(uri); ok {
			t.Errorf("invalid URI accepted: %s", uri)
		}
	}
	f := newFake()
	cs := connectClient(t, newTestServer(t, f, Policy{Tools: []string{}}), clientOptions(nil), "")
	if _, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: scopeURIPrefix + "repo-a/branch/main"}); err == nil {
		t.Fatal("scope resource bypassed tool policy")
	}
}

func TestPullRequestAndDeploymentSubscriptions(t *testing.T) {
	for _, kind := range []string{"pullrequest", "deployment"} {
		t.Run(kind, func(t *testing.T) {
			f := newFake()
			opts, updated := notifiedOptions()
			cs := connectWith(t, f, opts, "2025-11-25")
			selector := "42"
			if kind == "deployment" {
				selector = "01234567-89ab-cdef-0123-456789abcdef"
			}
			uri := scopeURIPrefix + "repo-a/" + kind + "/" + selector
			if err := subscribe(t, cs, uri); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			if kind == "pullrequest" {
				f.prStatuses["repo-a/42"] = []bitbucket.CommitStatus{{Name: "pipeline", State: "INPROGRESS", URL: "https://bitbucket.org/ws/repo-a/pipelines/results/1"}}
			} else {
				f.deployments["repo-a"] = []bitbucket.Deployment{{UUID: "deploy-1", Environment: bitbucket.Environment{UUID: braced(selector)}, State: bitbucket.DeploymentState{Name: "IN_PROGRESS"}}}
			}
			f.mu.Unlock()
			expectUpdate(t, updated, uri)
			f.mu.Lock()
			if kind == "pullrequest" {
				f.prStatuses["repo-a/42"][0].State = "SUCCESSFUL"
			} else {
				f.deployments["repo-a"][0].State = bitbucket.DeploymentState{Name: "COMPLETED", Status: &bitbucket.StateDetail{Name: "SUCCESSFUL"}}
			}
			f.mu.Unlock()
			expectUpdate(t, updated, uri)
			res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
			if err != nil || !strings.Contains(res.Contents[0].Text, "SUCCESSFUL") {
				t.Fatalf("resource: %v %v", res, err)
			}
		})
	}
}
