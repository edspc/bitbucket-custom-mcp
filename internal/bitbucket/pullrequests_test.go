package bitbucket

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

const diffLocation = "https://api.bitbucket.org/2.0/repositories/ws/repo-a/diff/ws/repo-a:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa%0Dbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb?from_pullrequest_id=7&topic=true"

func diffPullRequest() PullRequest {
	return PullRequest{ID: 7, Source: PullRequestEnd{Commit: &Commit{Hash: strings.Repeat("a", 40)}},
		Destination: PullRequestEnd{Commit: &Commit{Hash: strings.Repeat("b", 40)}}}
}

func testDiffSnapshot(t *testing.T, c *Client, kind string) DiffSnapshot {
	t.Helper()
	snapshot, err := c.ResolvePullRequestDiff(t.Context(), "repo-a", diffPullRequest(), kind)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestGetPullRequest(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2.0/repositories/ws/repo-a/pullrequests/7" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id": 7, "title": "Login", "summary": {"raw": "Adds *login*"}, "state": "MERGED",
			"author": {"display_name": "Ann"}, "draft": false, "close_source_branch": true, "task_count": 2,
			"source": {"branch": {"name": "feature/login"}, "commit": {"hash": "aaa"}, "repository": {"full_name": "dev/repo-a-fork"}},
			"destination": {"branch": {"name": "main"}, "commit": {"hash": "bbb"}, "repository": {"full_name": "ws/repo-a"}},
			"merge_commit": {"hash": "ccc"}, "closed_by": {"display_name": "Bob"},
			"participants": [{"user": {"display_name": "Bob"}, "role": "REVIEWER", "approved": true, "state": "approved"}]}`)
	})
	pr, err := c.GetPullRequest(context.Background(), "repo-a", 7)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Summary.Raw != "Adds *login*" || pr.Source.Branch.Name != "feature/login" || pr.Source.Commit.Hash != "aaa" ||
		pr.Source.Repository.FullName != "dev/repo-a-fork" || pr.Destination.Branch.Name != "main" || pr.MergeCommit.Hash != "ccc" ||
		pr.ClosedBy.DisplayName != "Bob" || !pr.CloseSourceBranch || pr.TaskCount != 2 ||
		!reflect.DeepEqual(pr.Participants, []Participant{{User: Account{"Bob"}, Role: "REVIEWER", Approved: true, State: "approved"}}) {
		t.Fatalf("pull request = %+v", pr)
	}
}

// The pull request redirects to the diff of its commits, which is read with the credentials and
// the paths.
func TestPullRequestDiff(t *testing.T) {
	const diff = "diff --git a/a.go b/a.go\n+x\n"
	var requests []string
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Host+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		if user, pass, ok := r.BasicAuth(); !ok || user != testEmail || pass != testToken {
			t.Errorf("%s without credentials", r.URL.Path)
		}
		if r.URL.Path == "/2.0/repositories/ws/repo-a/pullrequests/7/diff" {
			w.Header().Set("Location", diffLocation)
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, diff)
	})
	snapshot := testDiffSnapshot(t, c, "diff")
	got, truncated, err := c.PullRequestDiff(context.Background(), snapshot, []string{"a.go", "dir/b c.go"}, 0, 100)
	if err != nil || string(got) != diff || truncated {
		t.Fatalf("PullRequestDiff = %q, %v, %v", got, truncated, err)
	}
	want := []string{
		"api.bitbucket.org/2.0/repositories/ws/repo-a/pullrequests/7/diff?",
		"api.bitbucket.org/2.0/repositories/ws/repo-a/diff/ws/repo-a:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa%0Dbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb?from_pullrequest_id=7&path=a.go&path=dir%2Fb+c.go&topic=true",
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %q", requests)
	}

	got, truncated, err = c.PullRequestDiff(context.Background(), snapshot, nil, 0, 10)
	if err != nil || string(got) != diff[:10] || !truncated {
		t.Fatalf("cut diff = %q, %v, %v", got, truncated, err)
	}
	if last := requests[len(requests)-1]; !strings.HasSuffix(last, "?from_pullrequest_id=7&topic=true") {
		t.Fatalf("diff without paths requested as %s", last)
	}
	for offset, want := range map[int]string{10: diff[10:20], 25: diff[25:], len(diff) + 5: ""} {
		got, truncated, err = c.PullRequestDiff(context.Background(), snapshot, nil, offset, 10)
		if err != nil || string(got) != want || truncated != (offset == 10) {
			t.Fatalf("diff from %d = %q, %v, %v; want %q", offset, got, truncated, err, want)
		}
	}
}

// The credentials go along with a redirect only within the API.
func TestPullRequestDiffRefusesOtherAnswers(t *testing.T) {
	for name, tc := range map[string]struct {
		status   int
		location string
		want     string
		requests int
	}{
		"other host":     {http.StatusFound, "https://evil.example/2.0/diff", "expected a redirect within the API", 1},
		"plain http":     {http.StatusFound, "http://api.bitbucket.org/2.0/repositories/ws/repo-a/diff/x", "expected a redirect within the API", 1},
		"no redirect":    {http.StatusOK, "", "expected a redirect within the API, got 200", 1},
		"not found":      {http.StatusNotFound, "", "pullrequests/7/diff: 404", 1},
		"target missing": {http.StatusFound, diffLocation, "pullrequests/7/diff: 404", 2},
	} {
		t.Run(name, func(t *testing.T) {
			var requests int
			c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Host != "api.bitbucket.org" {
					t.Errorf("request sent to %s", r.Host)
				}
				if strings.Contains(r.URL.Path, "/pullrequests/") {
					w.Header().Set("Location", tc.location)
					w.WriteHeader(tc.status)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			})
			snapshot, err := c.ResolvePullRequestDiff(t.Context(), "repo-a", diffPullRequest(), "diff")
			if err == nil {
				_, _, err = c.PullRequestDiff(t.Context(), snapshot, nil, 0, 100)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if requests != tc.requests {
				t.Fatalf("%d requests, want %d", requests, tc.requests)
			}
		})
	}
}

func TestPullRequestDiffStatFollowsPages(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/2.0/repositories/ws/repo-a/pullrequests/7/diffstat":
			w.Header().Set("Location", strings.Replace(diffLocation, "/diff/", "/diffstat/", 1))
			w.WriteHeader(http.StatusFound)
		case r.URL.Query().Get("page") == "1":
			_, _ = fmt.Fprintf(w, `{"values": [{"status": "added", "lines_added": 3, "new": {"path": "a.go"}}], "next": %q}`, strings.Replace(diffLocation, "/diff/", "/diffstat/", 1)+"&page=2")
		default:
			_, _ = io.WriteString(w, `{"values": [{"status": "removed", "lines_removed": 1, "old": {"path": "b.go"}}]}`)
		}
	})
	got, err := c.PullRequestDiffStat(context.Background(), testDiffSnapshot(t, c, "diffstat"), 0, 200)
	want := []DiffStat{
		{Status: "added", LinesAdded: 3, New: &CommitFile{Path: "a.go"}},
		{Status: "removed", LinesRemoved: 1, Old: &CommitFile{Path: "b.go"}},
	}
	if err != nil || !reflect.DeepEqual(got.Values, want) || got.More {
		t.Fatalf("PullRequestDiffStat = %+v, %v", got, err)
	}
}

func TestListPullRequests(t *testing.T) {
	var queries []url.Values
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2.0/repositories/ws/repo-a/pullrequests" {
			t.Errorf("path = %s", r.URL.Path)
		}
		queries = append(queries, r.URL.Query())
		_, _ = io.WriteString(w, `{"values": [{"id": 8, "title": "Search", "source": {"branch": {"name": "feature/search"}}}]}`)
	})
	ctx := context.Background()
	prs, err := c.ListPullRequests(ctx, "repo-a", PullRequestFilter{
		States: []string{"OPEN", "MERGED"}, SourceBranch: `fix/"quoted" \ name`, DestinationBranch: "main",
	}, 5)
	if err != nil || len(prs) != 1 || prs[0].ID != 8 || prs[0].Source.Branch.Name != "feature/search" {
		t.Fatalf("ListPullRequests = %+v, %v", prs, err)
	}
	if _, err = c.ListPullRequests(ctx, "repo-a", PullRequestFilter{}, 10); err != nil {
		t.Fatal(err)
	}
	want := []url.Values{
		{"state": {"OPEN", "MERGED"}, "sort": {"-updated_on"}, "pagelen": {"5"},
			"q": {`source.branch.name = "fix/\"quoted\" \\ name" AND destination.branch.name = "main"`}},
		{"sort": {"-updated_on"}, "pagelen": {"10"}},
	}
	if !reflect.DeepEqual(queries, want) {
		t.Fatalf("queries = %v", queries)
	}
}
