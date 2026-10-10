package tools

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

// addPullRequests adds pull requests 8 to 11 of repo-a next to the merged 7 of addPullRequest,
// and one of repo-b.
func addPullRequests(f *fakeBitbucket) {
	addPullRequest(f)
	for _, pr := range []struct {
		id                         int
		state, from, into, updated string
	}{
		{8, "OPEN", "feature/search", "main", "2026-10-05T10:00:00Z"},
		{9, "OPEN", "feature/login", "release", "2026-10-03T10:00:00Z"},
		{10, "DECLINED", "feature/old", "main", "2026-10-04T10:00:00Z"},
		{11, "SUPERSEDED", "feature/older", "main", "2026-10-01T09:00:00Z"},
	} {
		p := &bitbucket.PullRequest{ID: pr.id, Title: "PR " + pr.from, State: pr.state, UpdatedOn: pr.updated}
		p.Source.Branch.Name, p.Destination.Branch.Name = pr.from, pr.into
		f.pullRequests["repo-a/"+strconv.Itoa(pr.id)] = p
	}
	f.pullRequests["repo-b/1"] = &bitbucket.PullRequest{ID: 1, State: "OPEN", UpdatedOn: "2026-10-09T10:00:00Z"}
}

func listedIDs(t *testing.T, out string) []int {
	t.Helper()
	var ids []int
	for _, pr := range decodeResult[listPullRequestsResult](t, out).PullRequests {
		ids = append(ids, pr.ID)
	}
	return ids
}

func TestListPullRequests(t *testing.T) {
	f := newFake()
	addPullRequests(f)
	cs := connect(t, f, nil)
	for name, tc := range map[string]struct {
		args map[string]any
		want []int
	}{
		"open by default":     {map[string]any{}, []int{8, 9}},
		"every state":         {map[string]any{"state": "ALL"}, []int{8, 10, 9, 7, 11}},
		"superseded":          {map[string]any{"state": "SUPERSEDED"}, []int{11}},
		"lower case":          {map[string]any{"state": "merged"}, []int{7}},
		"from a branch":       {map[string]any{"state": "all", "source_branch": "feature/login"}, []int{9, 7}},
		"into a branch":       {map[string]any{"state": "ALL", "destination_branch": "main"}, []int{8, 10, 7, 11}},
		"from and into":       {map[string]any{"state": "ALL", "source_branch": "feature/login", "destination_branch": "main"}, []int{7}},
		"the latest ones":     {map[string]any{"state": "ALL", "limit": 2}, []int{8, 10}},
		"nothing that agrees": {map[string]any{"source_branch": "feature/none"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			tc.args["repo"] = "repo-a"
			if got := listedIDs(t, mustCall(t, cs, "list_pull_requests", tc.args)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("pull requests = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestListPullRequestsShowsSummaries(t *testing.T) {
	f := newFake()
	addPullRequest(f)
	out := mustCall(t, connect(t, f, nil), "list_pull_requests", map[string]any{"repo": "repo-a", "state": "MERGED"})
	got := decodeResult[listPullRequestsResult](t, out)
	if !reflect.DeepEqual(got, listPullRequestsResult{PullRequests: []pullRequestSummary{loginSummary()}}) {
		t.Fatalf("result = %+v", got)
	}
	if !strings.Contains(out, `"pull_requests":[`) {
		t.Fatalf("result = %s", out)
	}
	f.mu.Lock()
	queries := slices.Clone(f.prListQueries)
	f.mu.Unlock()
	if want := []string{"pagelen=10&sort=-updated_on&state=MERGED"}; !slices.Equal(queries, want) {
		t.Fatalf("queries = %q, want the 10 most recently updated by default", queries)
	}
	none := mustCall(t, connect(t, f, nil), "list_pull_requests", map[string]any{"repo": "repo-a"})
	if !strings.Contains(none, `"pull_requests":[]`) {
		t.Fatalf("an empty list = %s", none)
	}
}

func TestListPullRequestsRefusesBadInput(t *testing.T) {
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"no repo":       {map[string]any{"repo": ""}, "repo is required"},
		"unknown state": {map[string]any{"repo": "repo-a", "state": "CLOSED"}, `state must be OPEN, MERGED, DECLINED, SUPERSEDED or ALL, got "CLOSED"`},
		"too many":      {map[string]any{"repo": "repo-a", "limit": 51}, "limit must be between 1 and 50"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			out, isErr := call(t, connect(t, f, nil), "list_pull_requests", tc.args)
			if !isErr || !strings.Contains(out, tc.want) {
				t.Fatalf("result = %s (error %v), want %q", out, isErr, tc.want)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.prListQueries) != 0 {
				t.Fatalf("listed although the input was refused: %q", f.prListQueries)
			}
		})
	}
}
