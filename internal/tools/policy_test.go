package tools

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPolicyRejectsToolsRepositoriesAndRefsBeforeRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy Policy
		tool   string
		args   map[string]any
	}{
		{"tool", Policy{Tools: []string{"get_pipeline"}}, "list_pipelines", map[string]any{"repo": "repo-a"}},
		{"empty tools", Policy{Tools: []string{}}, "get_pipeline", map[string]any{"repo": "repo-a", "pipeline": "1"}},
		{"repo", Policy{Repositories: []string{"repo-a"}}, "list_variables", map[string]any{"scope": "repository", "repo": "repo-b"}},
		{"empty repos", Policy{Repositories: []string{}}, "run_pipeline", map[string]any{"repo": "repo-a", "branch": "main"}},
		{"link target", Policy{Repositories: []string{"repo-a"}}, "link_repos_by_key", keyArgs(false)},
		{"workspace", Policy{Repositories: []string{"repo-a"}}, "list_variables", map[string]any{"scope": "workspace"}},
		{"branch", Policy{Refs: []string{"branch:main"}}, "run_pipeline", map[string]any{"repo": "repo-a", "branch": "feature"}},
		{"tag", Policy{Refs: []string{"branch:main"}}, "run_pipeline", map[string]any{"repo": "repo-a", "tag": "main"}},
		{"schedule", Policy{Refs: []string{"branch:main"}}, "add_schedule", map[string]any{"repo": "repo-a", "branch": "feature", "pipeline": "default", "cron": "0 0 2 * * ? *"}},
		{"path escape", Policy{}, "get_pipeline", map[string]any{"repo": "../other", "pipeline": "1"}},
		{"dot segment", Policy{}, "get_pipeline", map[string]any{"repo": "..", "pipeline": "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			s := newTestServer(t, f)
			f.tools.policy = tc.policy
			cs := connectClient(t, s, clientOptions(nil), "")
			out, isErr := call(t, cs, tc.tool, tc.args)
			if !isErr || (!strings.Contains(out, "server policy") && !strings.Contains(out, "repository slug")) {
				t.Fatalf("policy did not deny call: %s", out)
			}
			if f.pages.served() || len(f.mutationList()) != 0 || f.pipelineGet != 0 {
				t.Fatal("denied call reached the workflow")
			}
		})
	}
}

func TestPolicyAllowsExactRepositoryAndRef(t *testing.T) {
	f := newFake()
	s := newTestServer(t, f)
	f.tools.policy = Policy{Repositories: []string{"repo-a"}, Refs: []string{"branch:main"}, Tools: []string{"run_pipeline"}}
	u := &fakeUser{pages: f.pages, action: "decline"}
	cs := connectClient(t, s, clientOptions(u.elicit), "")
	out, isErr := call(t, cs, "run_pipeline", map[string]any{"repo": "repo-a", "branch": "main"})
	if isErr || !strings.Contains(out, `"cancelled"`) {
		t.Fatalf("allowed target did not reach confirmation: %s", out)
	}
}

func TestResourceAccessCannotBypassPolicy(t *testing.T) {
	for _, policy := range []Policy{{Repositories: []string{"repo-b"}}, {Tools: []string{"list_pipelines"}}} {
		f := newFake()
		f.addRun("repo-a", running())
		s := newTestServer(t, f)
		f.tools.policy = policy
		cs := connectClient(t, s, clientOptions(nil), "2025-11-25")
		uri := pipelineURI("repo-a", 1)
		if _, err := cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri}); err == nil {
			t.Fatal("resource read bypassed policy")
		}
		if err := cs.Subscribe(t.Context(), &mcp.SubscribeParams{URI: uri}); err == nil {
			t.Fatal("subscription bypassed policy")
		}
		if f.pipelineGet != 0 {
			t.Fatal("denied resource fetched Bitbucket data")
		}
	}
}

func TestDeleteScheduleRespectsRefPolicy(t *testing.T) {
	f := approvalFake()
	s := newTestServer(t, f)
	f.tools.policy = Policy{Refs: []string{"branch:release"}}
	out, isErr := call(t, connectClient(t, s, clientOptions(nil), ""), "delete_schedule", map[string]any{"repo": "repo-a", "schedule": "{schedule}"})
	if !isErr || !strings.Contains(out, "ref is not allowed") || f.pages.served() || len(f.mutationList()) != 0 {
		t.Fatalf("schedule bypassed ref policy: %s", out)
	}
}

// An allowed branch with a commit of another branch would run that commit's code as if it
// were the branch's, past the ref policy and with a branch name the approval page vouches for.
func TestRunRefusesCommitOffTheRef(t *testing.T) {
	const offRef = "2222222222222222222222222222222222222222"
	f := newFake()
	f.offRefCommits = map[string]bool{offRef: true}
	s := newTestServer(t, f)
	f.tools.policy = Policy{Refs: []string{"branch:main"}}
	cs := connectClient(t, s, clientOptions(nil), "")
	out, isErr := call(t, cs, "run_pipeline", map[string]any{"repo": "repo-a", "branch": "main", "commit": offRef})
	if !isErr || !strings.Contains(out, `is not on branch "main"`) {
		t.Fatalf("commit off the ref was accepted: %s", out)
	}
	if f.pages.served() || len(f.runBodyList()) != 0 {
		t.Fatal("refused run reached the approval page")
	}
}
