package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

func keyArgs(force bool) map[string]any {
	return map[string]any{"source_repo": "repo-a", "target_repo": "repo-b", "force": force}
}

func TestLinkByKeyAddsKeyLabelledWithSource(t *testing.T) {
	f := newFake()
	out, isErr := call(t, connect(t, f, nil), "link_repos_by_key", keyArgs(false))
	if isErr || !strings.Contains(out, `"added"`) {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	want := []bitbucket.DeployKey{{ID: 101, Key: "ssh-rsa KEYA", Label: "repo-a"}}
	if got := f.keysOf("repo-b"); !slices.Equal(got, want) {
		t.Fatalf("deploy keys = %+v", got)
	}
}

func TestLinkByKeyNeedsPipelinesInBoth(t *testing.T) {
	f := newFake()
	f.pipelines["repo-b"] = false
	out, isErr := call(t, connect(t, f, nil), "link_repos_by_key", keyArgs(false))
	if !isErr || !strings.Contains(out, "not enabled in repo-b") {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if m := f.mutationList(); len(m) != 0 {
		t.Fatalf("mutations = %v", m)
	}
}

func TestLinkByKeyWithoutPipelinesSSHKey(t *testing.T) {
	f := newFake()
	delete(f.sshKeys, "repo-a")
	out, isErr := call(t, connect(t, f, nil), "link_repos_by_key", keyArgs(false))
	if !isErr || !strings.Contains(out, "no pipelines SSH key") {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
}

func TestLinkByKeySameKeyIsNoop(t *testing.T) {
	f := newFake()
	f.deployKeys["repo-b"] = []bitbucket.DeployKey{{ID: 1, Key: "ssh-rsa KEYA some-comment", Label: "manual"}}
	out, isErr := call(t, connect(t, f, nil), "link_repos_by_key", keyArgs(false))
	if isErr || !strings.Contains(out, `"already_linked"`) {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if m := f.mutationList(); len(m) != 0 {
		t.Fatalf("mutations = %v", m)
	}
}

func TestLinkByKeyLabelConflict(t *testing.T) {
	f := newFake()
	f.deployKeys["repo-b"] = []bitbucket.DeployKey{{ID: 7, Key: "ssh-rsa OLD", Label: "repo-a"}}
	cs := connect(t, f, nil)

	out, isErr := call(t, cs, "link_repos_by_key", keyArgs(false))
	if !isErr || !strings.Contains(out, "force=true") {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if m := f.mutationList(); len(m) != 0 {
		t.Fatalf("mutations without force = %v", m)
	}

	out, isErr = call(t, cs, "link_repos_by_key", keyArgs(true))
	if isErr || !strings.Contains(out, `"replaced"`) {
		t.Fatalf("result with force = %s (error %v)", out, isErr)
	}
	wantMut := []string{"POST /2.0/repositories/ws/repo-b/deploy-keys", "DELETE /2.0/repositories/ws/repo-b/deploy-keys/7"}
	if m := f.mutationList(); !slices.Equal(m, wantMut) {
		t.Fatalf("mutations = %v", m)
	}
	if keys := f.keysOf("repo-b"); len(keys) != 1 || keys[0].Key != "ssh-rsa KEYA" {
		t.Fatalf("deploy keys = %+v", keys)
	}
}

func TestLinkByKeyForceKeepsOldKeyWhenAddFails(t *testing.T) {
	f := newFake()
	f.failAddKey = true
	old := bitbucket.DeployKey{ID: 7, Key: "ssh-rsa OLD", Label: "repo-a"}
	f.deployKeys["repo-b"] = []bitbucket.DeployKey{old}
	out, isErr := call(t, connect(t, f, nil), "link_repos_by_key", keyArgs(true))
	if !isErr {
		t.Fatalf("result = %s, want error", out)
	}
	if got := f.keysOf("repo-b"); !slices.Equal(got, []bitbucket.DeployKey{old}) {
		t.Fatalf("deploy keys = %+v, the old key must stay", got)
	}
}

func TestLinkByKeyRejectsSameRepo(t *testing.T) {
	out, isErr := call(t, connect(t, newFake(), nil), "link_repos_by_key", map[string]any{"source_repo": "repo-a", "target_repo": "repo-a"})
	if !isErr || !strings.Contains(out, "must be different") {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
}
