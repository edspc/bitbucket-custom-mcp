package tools

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

const (
	testPRSource      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testPRDestination = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testPRMerge       = "cccccccccccccccccccccccccccccccccccccccc"
	loginDiff         = "diff --git a/app/login.go b/app/login.go\n--- a/app/login.go\n+++ b/app/login.go\n@@ -1 +1,2 @@\n-old\n+new\n+more\n"
	renameDiff        = "diff --git a/docs/old.md b/docs/new.md\nsimilarity index 90%\nrename from docs/old.md\nrename to docs/new.md\n"
	removeDiff        = "diff --git a/legacy.txt b/legacy.txt\ndeleted file mode 100644\n"
)

// addPullRequest adds pull request 7 of repo-a: from a fork, merged, with three changed files.
func addPullRequest(f *fakeBitbucket) {
	pr := &bitbucket.PullRequest{
		ID: 7, Title: "Add login", State: "MERGED", Author: bitbucket.Account{DisplayName: "Ann"}, CloseSourceBranch: true,
		MergeCommit: &bitbucket.Commit{Hash: testPRMerge}, ClosedBy: &bitbucket.Account{DisplayName: "Bob"},
		CreatedOn: "2026-10-01T10:00:00Z", UpdatedOn: "2026-10-02T10:00:00Z", CommentCount: 4, TaskCount: 1,
		Participants: []bitbucket.Participant{
			{User: bitbucket.Account{DisplayName: "Bob"}, Role: "REVIEWER", Approved: true, State: "approved"},
			{User: bitbucket.Account{DisplayName: "Cid"}, Role: "REVIEWER", State: "changes_requested"},
			{User: bitbucket.Account{DisplayName: "Dee"}, Role: "REVIEWER"},
			{User: bitbucket.Account{DisplayName: "Eve"}, Role: "PARTICIPANT", Approved: true},
			{User: bitbucket.Account{DisplayName: "Fay"}, Role: "PARTICIPANT"},
		},
	}
	pr.Summary.Raw = "Adds the *login* page."
	pr.Source.Branch.Name, pr.Source.Commit, pr.Source.Repository.FullName = "feature/login", &bitbucket.Commit{Hash: testPRSource}, "dev/repo-a-fork"
	pr.Destination.Branch.Name, pr.Destination.Commit, pr.Destination.Repository.FullName = "main", &bitbucket.Commit{Hash: testPRDestination}, "ws/repo-a"
	f.pullRequests["repo-a/7"] = pr
	f.prStatuses["repo-a/7"] = []bitbucket.CommitStatus{
		{Name: "Pipeline #12 for feature/login", State: "SUCCESSFUL", URL: "https://bitbucket.org/ws/repo-a/pipelines/results/12"},
	}
	f.prChanges["repo-a/7"] = []fakeChange{
		{"app/login.go", bitbucket.DiffStat{Status: "modified", LinesAdded: 2, LinesRemoved: 1,
			Old: &bitbucket.CommitFile{Path: "app/login.go"}, New: &bitbucket.CommitFile{Path: "app/login.go"}}, loginDiff},
		{"docs/new.md", bitbucket.DiffStat{Status: "renamed",
			Old: &bitbucket.CommitFile{Path: "docs/old.md"}, New: &bitbucket.CommitFile{Path: "docs/new.md"}}, renameDiff},
		{"legacy.txt", bitbucket.DiffStat{Status: "removed", LinesRemoved: 9, Old: &bitbucket.CommitFile{Path: "legacy.txt"}}, removeDiff},
	}
}

// loginSummary is how pull request 7 of addPullRequest is shown.
func loginSummary() pullRequestSummary {
	return pullRequestSummary{
		ContentTrust: "untrusted",
		ID:           7, Title: "Add login", State: "MERGED", Author: "Ann",
		Source:      pullRequestEndView{Branch: "feature/login", Commit: testPRSource, Repository: "dev/repo-a-fork"},
		Destination: pullRequestEndView{Branch: "main", Commit: testPRDestination, Repository: "ws/repo-a"},
		CreatedOn:   "2026-10-01T10:00:00Z", UpdatedOn: "2026-10-02T10:00:00Z", CommentCount: 4, OpenTasks: 1,
		URL: "https://bitbucket.org/ws/repo-a/pull-requests/7",
	}
}

// prRequestsOf runs a call and returns what it asked Bitbucket about the pull request.
func prRequestsOf(f *fakeBitbucket, call func()) []string {
	f.mu.Lock()
	before := len(f.prRequests)
	f.mu.Unlock()
	call()
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.prRequests[before:])
}

// Without paths the pull request is described with its files, and no diff is read.
func TestGetPullRequest(t *testing.T) {
	f := newFake()
	addPullRequest(f)
	cs := connect(t, f, nil)
	var got pullRequestView
	requests := prRequestsOf(f, func() {
		got = decodeResult[pullRequestView](t, mustCall(t, cs, "get_pull_request", map[string]any{"repo": "repo-a", "pull_request": 7}))
	})
	want := pullRequestView{
		pullRequestSummary: loginSummary(),
		Description:        "Adds the *login* page.",
		CloseSourceBranch:  true, MergeCommit: testPRMerge, ClosedBy: "Bob",
		Reviewers: []reviewerView{
			{Name: "Bob", Role: "reviewer", State: "approved"},
			{Name: "Cid", Role: "reviewer", State: "changes_requested"},
			{Name: "Dee", Role: "reviewer", State: "pending"},
			{Name: "Eve", Role: "participant", State: "approved"},
		},
		Builds: []buildView{{Name: "Pipeline #12 for feature/login", State: "SUCCESSFUL", URL: "https://bitbucket.org/ws/repo-a/pipelines/results/12"}},
		Files: []changedFileView{
			{Path: "app/login.go", Status: "modified", LinesAdded: 2, LinesRemoved: 1},
			{Path: "docs/new.md", OldPath: "docs/old.md", Status: "renamed"},
			{Path: "legacy.txt", Status: "removed", LinesRemoved: 9},
		},
		FilesTotal: 3,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pull request =\n%+v\nwant\n%+v", got, want)
	}
	if want := []string{"pull request", "statuses", "diffstat redirect", "diffstat"}; !slices.Equal(requests, want) {
		t.Fatalf("requests = %q", requests)
	}
}

// With paths the result holds their diff, and what names the pull request, but not the rest.
func TestGetPullRequestDiffOfSomeFiles(t *testing.T) {
	f := newFake()
	addPullRequest(f)
	cs := connect(t, f, nil)
	var got pullRequestView
	requests := prRequestsOf(f, func() {
		got = decodeResult[pullRequestView](t, mustCall(t, cs, "get_pull_request",
			map[string]any{"repo": "repo-a", "pull_request": 7, "paths": []string{"docs/new.md", "legacy.txt"}}))
	})
	want := pullRequestView{
		pullRequestSummary: loginSummary(),
		CloseSourceBranch:  true, MergeCommit: testPRMerge, ClosedBy: "Bob",
		Diff: new(renameDiff + removeDiff),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pull request =\n%+v\nwant\n%+v", got, want)
	}
	if want := []string{"pull request", "diff redirect", "diff"}; !slices.Equal(requests, want) {
		t.Fatalf("requests = %q", requests)
	}
	f.mu.Lock()
	queries := slices.Clone(f.diffQueries)
	f.mu.Unlock()
	if want := []string{"from_pullrequest_id=7&path=docs%2Fnew.md&path=legacy.txt&topic=true"}; !slices.Equal(queries, want) {
		t.Fatalf("diff queries = %q", queries)
	}

	// A file the pull request does not change has an empty diff, which is still shown.
	out := mustCall(t, cs, "get_pull_request", map[string]any{"repo": "repo-a", "pull_request": 7, "paths": []string{"README.md"}})
	if got = decodeResult[pullRequestView](t, out); got.Diff == nil || *got.Diff != "" || !strings.Contains(out, `"diff":""`) {
		t.Fatalf("diff of an unchanged file = %s", out)
	}
}

// bigDiff adds a file to pull request 7 whose diff takes six parts: lines for two and a half
// parts, then a line of two-byte characters longer than two parts.
func bigDiff(f *fakeBitbucket) string {
	var big strings.Builder
	big.WriteString("diff --git a/big.txt b/big.txt\n")
	for i := 0; big.Len() <= 2*maxDiffBytes; i++ {
		_, _ = fmt.Fprintf(&big, "+line %d: ünïcode keeps whole characters\n", i)
	}
	big.WriteString("+" + strings.Repeat("é", maxDiffBytes) + "\n+last\n")
	f.prChanges["repo-a/7"] = append(f.prChanges["repo-a/7"], fakeChange{"big.txt", bitbucket.DiffStat{Status: "added"}, big.String()})
	return big.String()
}

// A diff longer than a part, even of a single file, is read part by part; parts end at a line
// end, except within a line longer than a part, which is cut between characters.
func TestGetPullRequestReadsLongDiffInParts(t *testing.T) {
	f := newFake()
	addPullRequest(f)
	big := bigDiff(f)
	cs := connect(t, f, nil)

	var parts []string
	args := map[string]any{"repo": "repo-a", "pull_request": 7, "paths": []string{"big.txt"}}
	for len(parts) < 10 {
		var got pullRequestView
		requests := prRequestsOf(f, func() { got = decodeResult[pullRequestView](t, mustCall(t, cs, "get_pull_request", args)) })
		if got.Diff == nil || len(*got.Diff) > maxDiffBytes || got.Files != nil || !slices.Equal(requests, []string{"pull request", "diff redirect", "diff"}) {
			t.Fatalf("part %d, requested %q: %+v", len(parts)+1, requests, got)
		}
		parts = append(parts, *got.Diff)
		if got.DiffNext == "" {
			break
		}
		args["diff_from"] = got.DiffNext
	}
	if strings.Join(parts, "") != big {
		t.Fatalf("%d parts do not add up to the diff", len(parts))
	}
	// The long line starts with "+", so a cut after a whole number of parts' bytes would split
	// a character.
	if len(parts) != 6 || !strings.HasSuffix(parts[0], "\n") || len(parts[0]) < maxDiffBytes-100 || len(parts[3]) != maxDiffBytes-1 {
		t.Fatalf("%d parts; the first ends %q, the fourth has %d bytes", len(parts), parts[0][len(parts[0])-20:], len(parts[3]))
	}
}

// A part is read only from the diff the previous one came from.
func TestGetPullRequestRefusesPartOfAnotherDiff(t *testing.T) {
	f := newFake()
	addPullRequest(f)
	bigDiff(f)
	cs := connect(t, f, nil)
	big := []string{"big.txt"}
	first := decodeResult[pullRequestView](t, mustCall(t, cs, "get_pull_request", map[string]any{"repo": "repo-a", "pull_request": 7, "paths": big}))
	if first.DiffNext == "" {
		t.Fatal("the diff must go on")
	}
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"other paths": {map[string]any{"paths": []string{"big.txt", "legacy.txt"}, "diff_from": first.DiffNext}, "paths differ"},
		"no paths":    {map[string]any{"diff_from": first.DiffNext}, "diff_from goes with the paths"},
		"not a next":  {map[string]any{"paths": big, "diff_from": "65536"}, "must be a diff_next"},
		"negative":    {map[string]any{"paths": big, "diff_from": strings.Replace(first.DiffNext, "@", "@-", 1)}, "must be a diff_next"},
	} {
		t.Run(name, func(t *testing.T) {
			tc.args["repo"], tc.args["pull_request"] = "repo-a", 7
			if out, isErr := call(t, cs, "get_pull_request", tc.args); !isErr || !strings.Contains(out, tc.want) {
				t.Fatalf("result = %s (error %v), want %q", out, isErr, tc.want)
			}
		})
	}
	// A push to the source branch makes another diff.
	f.mu.Lock()
	f.pullRequests["repo-a/7"].Source.Commit = &bitbucket.Commit{Hash: strings.Repeat("d", 40)}
	f.mu.Unlock()
	out, isErr := call(t, cs, "get_pull_request", map[string]any{"repo": "repo-a", "pull_request": 7, "paths": big, "diff_from": first.DiffNext})
	if !isErr || !strings.Contains(out, "has changed") {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
}

// addGeneratedFiles adds n added files to pull request 7, after its three, and returns the
// paths of all of them.
func addGeneratedFiles(f *fakeBitbucket, n int) []string {
	paths := []string{"app/login.go", "docs/new.md", "legacy.txt"}
	for i := range n {
		path := fmt.Sprint("gen/", i)
		f.prChanges["repo-a/7"] = append(f.prChanges["repo-a/7"], fakeChange{path, bitbucket.DiffStat{Status: "added", New: &bitbucket.CommitFile{Path: path}}, ""})
		paths = append(paths, path)
	}
	return paths
}

// The files of a pull request that changes many come in pages; only the first describes it.
func TestGetPullRequestListsFilesInPages(t *testing.T) {
	f := newFake()
	addPullRequest(f)
	all := addGeneratedFiles(f, maxFiles+50)
	cs := connect(t, f, nil)

	var listed []string
	args := map[string]any{"repo": "repo-a", "pull_request": 7}
	for page := 1; page <= 3; page++ {
		var got pullRequestView
		requests := prRequestsOf(f, func() { got = decodeResult[pullRequestView](t, mustCall(t, cs, "get_pull_request", args)) })
		want := []string{"pull request", "diffstat redirect", "diffstat"}
		described := got.Description != "" && got.Reviewers != nil && got.Builds != nil
		if page == 1 {
			want = []string{"pull request", "statuses", "diffstat redirect", "diffstat", "diffstat"}
		}
		if described != (page == 1) || got.FilesTotal != len(all) || len(got.Files) > maxFiles || !slices.Equal(requests, want) {
			t.Fatalf("page %d, requested %q: %d files of %d, described %v", page, requests, len(got.Files), got.FilesTotal, described)
		}
		for _, file := range got.Files {
			listed = append(listed, file.Path)
		}
		if got.FilesNext == "" {
			break
		}
		args["files_from"] = got.FilesNext
	}
	if !slices.Equal(listed, all) {
		t.Fatalf("listed %d files, want all %d in order", len(listed), len(all))
	}

	// An offset past the end, even the largest there is, gives an empty last page.
	version, _, _ := strings.Cut(args["files_from"].(string), "@")
	for _, offset := range []string{strconv.Itoa(len(all)), strconv.Itoa(len(all) + 1), strconv.Itoa(math.MaxInt)} {
		args["files_from"] = version + "@" + offset
		got := decodeResult[pullRequestView](t, mustCall(t, cs, "get_pull_request", args))
		if got.Files != nil || got.FilesNext != "" || got.FilesTotal != len(all) {
			t.Fatalf("files from %s: %d files, next %q, total %d", offset, len(got.Files), got.FilesNext, got.FilesTotal)
		}
	}
}

// A page of files is read only from the list the previous one came from.
func TestGetPullRequestRefusesPageOfAnotherList(t *testing.T) {
	f := newFake()
	addPullRequest(f)
	addGeneratedFiles(f, maxFiles)
	bigDiff(f)
	cs := connect(t, f, nil)
	first := decodeResult[pullRequestView](t, mustCall(t, cs, "get_pull_request", map[string]any{"repo": "repo-a", "pull_request": 7}))
	diff := decodeResult[pullRequestView](t, mustCall(t, cs, "get_pull_request", map[string]any{"repo": "repo-a", "pull_request": 7, "paths": []string{"big.txt"}}))
	if first.FilesNext == "" || diff.DiffNext == "" {
		t.Fatal("the files and the diff must go on")
	}
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"with paths":   {map[string]any{"files_from": first.FilesNext, "paths": []string{"big.txt"}}, "files_from lists files, so it goes without paths"},
		"not a next":   {map[string]any{"files_from": "200"}, "files_from must be a files_next"},
		"a diff_next":  {map[string]any{"files_from": diff.DiffNext}, "has changed since files_from"},
		"a files_next": {map[string]any{"paths": []string{"big.txt"}, "diff_from": first.FilesNext}, "has changed since diff_from"},
	} {
		t.Run(name, func(t *testing.T) {
			tc.args["repo"], tc.args["pull_request"] = "repo-a", 7
			if out, isErr := call(t, cs, "get_pull_request", tc.args); !isErr || !strings.Contains(out, tc.want) {
				t.Fatalf("result = %s (error %v), want %q", out, isErr, tc.want)
			}
		})
	}
	// A push to the destination branch makes another list.
	f.mu.Lock()
	f.pullRequests["repo-a/7"].Destination.Commit = &bitbucket.Commit{Hash: strings.Repeat("e", 40)}
	f.mu.Unlock()
	out, isErr := call(t, cs, "get_pull_request", map[string]any{"repo": "repo-a", "pull_request": 7, "files_from": first.FilesNext})
	if !isErr || !strings.Contains(out, "has changed") {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
}

// Bitbucket gives up on the files or the diff of a very large pull request; the call still
// returns the rest and says why that part is missing.
func TestGetPullRequestWhenBitbucketGivesUp(t *testing.T) {
	f := newFake()
	addPullRequest(f)
	cs := connect(t, f, nil)
	f.mu.Lock()
	f.diffstatStatus, f.diffStatus = 555, 555
	f.mu.Unlock()
	got := decodeResult[pullRequestView](t, mustCall(t, cs, "get_pull_request", map[string]any{"repo": "repo-a", "pull_request": 7}))
	if !strings.Contains(got.FilesError, "555") || got.Files != nil || got.FilesTotal != 0 || len(got.Builds) != 1 || got.Description == "" {
		t.Fatalf("result without files = %+v", got)
	}
	got = decodeResult[pullRequestView](t, mustCall(t, cs, "get_pull_request",
		map[string]any{"repo": "repo-a", "pull_request": 7, "paths": []string{"app/login.go"}}))
	if !strings.Contains(got.DiffError, "555") || got.Diff != nil || got.Source.Branch != "feature/login" {
		t.Fatalf("result without a diff = %+v", got)
	}
}

func TestGetPullRequestRefusesBadInput(t *testing.T) {
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"no repo":      {map[string]any{"repo": "", "pull_request": 7}, "repo is required"},
		"no id":        {map[string]any{"repo": "repo-a", "pull_request": 0}, "pull_request must be the ID"},
		"negative id":  {map[string]any{"repo": "repo-a", "pull_request": -7}, "pull_request must be the ID"},
		"empty path":   {map[string]any{"repo": "repo-a", "pull_request": 7, "paths": []string{"a.go", ""}}, "empty paths"},
		"unknown pull": {map[string]any{"repo": "repo-a", "pull_request": 99}, "404"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			addPullRequest(f)
			out, isErr := call(t, connect(t, f, nil), "get_pull_request", tc.args)
			if !isErr || !strings.Contains(out, tc.want) {
				t.Fatalf("result = %s (error %v), want %q", out, isErr, tc.want)
			}
		})
	}
}

// codeDiff is a diff of code full of characters that JSON escapes.
func codeDiff(path string) string {
	var code strings.Builder
	code.WriteString("diff --git a/" + path + " b/" + path + "\n")
	for code.Len() <= maxDiffBytes {
		code.WriteString("+\t\t\"key\": \"value with \\\"quotes\\\" and \\\\n\",\n")
	}
	return code.String()
}

// A result with the most files of long paths, and one with a part of a diff of code full of
// characters that JSON escapes, each stay within 25,000 tokens at 2.5 bytes a token.
func TestGetPullRequestResultSize(t *testing.T) {
	f := newFake()
	addPullRequest(f)
	f.pullRequests["repo-a/7"].Summary.Raw = strings.Repeat("Explains the change in a paragraph or two. ", 25)
	for i := range 5 {
		f.prStatuses["repo-a/7"] = append(f.prStatuses["repo-a/7"], bitbucket.CommitStatus{
			Name: fmt.Sprint("Pipeline #", i, " for feature/login"), State: "SUCCESSFUL", URL: "https://bitbucket.org/ws/repo-a/pipelines/results/12",
		})
	}
	for i := range maxFiles + 50 {
		path := fmt.Sprintf("services/payments/internal/adapters/providers/settlement/reconciliation_%03d_test.go", i)
		f.prChanges["repo-a/7"] = append(f.prChanges["repo-a/7"], fakeChange{path,
			bitbucket.DiffStat{Status: "renamed", LinesAdded: 120, LinesRemoved: 45,
				Old: &bitbucket.CommitFile{Path: path + ".old"}, New: &bitbucket.CommitFile{Path: path}},
			codeDiff(path)})
	}
	cs := connect(t, f, nil)

	files := mustCall(t, cs, "get_pull_request", map[string]any{"repo": "repo-a", "pull_request": 7})
	if got := decodeResult[pullRequestView](t, files); len(got.Files) != maxFiles {
		t.Fatalf("%d files", len(got.Files))
	}
	path := "services/payments/internal/adapters/providers/settlement/reconciliation_000_test.go"
	diff := mustCall(t, cs, "get_pull_request", map[string]any{"repo": "repo-a", "pull_request": 7, "paths": []string{path}})
	if got := decodeResult[pullRequestView](t, diff); got.DiffNext == "" {
		t.Fatal("not a whole part")
	}
	t.Logf("files: %d bytes, a part of a diff: %d bytes", len(files), len(diff))
	for name, out := range map[string]string{"files": files, "a part of a diff": diff} {
		if len(out) > 25_000*5/2 {
			t.Errorf("%s take %d bytes", name, len(out))
		}
	}
}

// Anyone who can push may report build results, so only the first ones are shown, with a count.
func TestGetPullRequestBoundsBuilds(t *testing.T) {
	f := newFake()
	addPullRequest(f)
	f.prStatuses["repo-a/7"] = nil
	for i := range maxBuilds + 7 {
		f.prStatuses["repo-a/7"] = append(f.prStatuses["repo-a/7"], bitbucket.CommitStatus{Name: fmt.Sprint("build ", i), State: "SUCCESSFUL"})
	}
	got := decodeResult[pullRequestView](t, mustCall(t, connect(t, f, nil), "get_pull_request", map[string]any{"repo": "repo-a", "pull_request": 7}))
	if len(got.Builds) != maxBuilds || got.BuildsTotal != maxBuilds+7 || !got.BuildsTruncated || got.Builds[0].Name != "build 0" {
		t.Fatalf("%d builds of %d", len(got.Builds), got.BuildsTotal)
	}
}
