package tools

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

func TestPullRequestShortHashCursorsUseResolvedComparison(t *testing.T) {
	for _, change := range []string{"merge", "resolved commit"} {
		for _, kind := range []string{"diff", "files"} {
			t.Run(change+"/"+kind, func(t *testing.T) {
				f := newFake()
				addPullRequest(f)
				pr := f.pullRequests["repo-a/7"]
				pr.State, pr.MergeCommit = "OPEN", nil
				pr.Source.Commit.Hash, pr.Destination.Commit.Hash = testPRSource[:12], testPRDestination[:12]
				f.commits["dev/repo-a-fork:"+testPRSource[:12]] = testPRSource
				f.commits["ws/repo-a:"+testPRDestination[:12]] = testPRDestination
				bigDiff(f)
				addGeneratedFiles(f, maxFiles)
				cs := connect(t, f, nil)
				args := map[string]any{"repo": "repo-a", "pull_request": 7}
				if kind == "diff" {
					args["paths"] = []string{"big.txt"}
				}
				first := decodeResult[pullRequestView](t, mustCall(t, cs, "get_pull_request", args))
				cursor := first.FilesNext
				if kind == "diff" {
					cursor = first.DiffNext
					args["diff_from"] = cursor
				} else {
					args["files_from"] = cursor
				}
				if cursor == "" || first.FilesError != "" || first.DiffError != "" {
					t.Fatalf("short hashes produced no continuation: %+v", first)
				}
				f.mu.Lock()
				if change == "merge" {
					pr.State, pr.MergeCommit = "MERGED", &bitbucket.Commit{Hash: testPRMerge[:12]}
					f.commits["ws/repo-a:"+testPRMerge[:12]] = testPRMerge
					f.mergeDiff = true
				} else {
					// The metadata text is unchanged; comparing only its abbreviated IDs would
					// accept a different full commit that shares the same prefix.
					f.commits["dev/repo-a-fork:"+testPRSource[:12]] = testPRSource[:12] + strings.Repeat("d", 28)
				}
				f.mu.Unlock()
				if out, isErr := call(t, cs, "get_pull_request", args); !isErr || !strings.Contains(out, "has changed") {
					t.Fatalf("continued another comparison: %s", out)
				}
				delete(args, "diff_from")
				delete(args, "files_from")
				fresh := decodeResult[pullRequestView](t, mustCall(t, cs, "get_pull_request", args))
				if fresh.FilesError != "" || fresh.DiffError != "" || kind == "diff" && fresh.Diff == nil || kind == "files" && len(fresh.Files) == 0 {
					t.Fatalf("fresh comparison failed: %+v", fresh)
				}
			})
		}
	}
}

func TestPullRequestPushDuringRedirectRefusesMixedVersions(t *testing.T) {
	for _, kind := range []string{"diff", "files"} {
		for _, continuation := range []bool{false, true} {
			t.Run(kind+"/continuation="+fmt.Sprint(continuation), func(t *testing.T) {
				f := newFake()
				addPullRequest(f)
				f.pullRequests["repo-a/7"].State = "OPEN"
				f.pullRequests["repo-a/7"].MergeCommit = nil
				bigDiff(f)
				addGeneratedFiles(f, maxFiles)
				cs := connect(t, f, nil)
				args := map[string]any{"repo": "repo-a", "pull_request": 7}
				if kind == "diff" {
					args["paths"] = []string{"big.txt"}
				}
				if continuation {
					first := decodeResult[pullRequestView](t, mustCall(t, cs, "get_pull_request", args))
					if kind == "diff" {
						args["diff_from"] = first.DiffNext
					} else {
						args["files_from"] = first.FilesNext
					}
				}
				upstream := f.handler(t)
				h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstream.ServeHTTP(w, r)
					if r.URL.Path == "/2.0/repositories/ws/repo-a/pullrequests/7" {
						// A push lands after the old metadata response, before the diff redirect.
						f.mu.Lock()
						f.pullRequests["repo-a/7"].Source.Commit.Hash = strings.Repeat("c", 40)
						f.mu.Unlock()
					}
				})
				f.tools.bb = bitbucket.New(&http.Client{Transport: handlerTransport{h}}, bitbucket.Config{Workspace: "ws"})
				var got pullRequestView
				requests := prRequestsOf(f, func() {
					got = decodeResult[pullRequestView](t, mustCall(t, cs, "get_pull_request", args))
				})
				if !strings.Contains(got.DiffError+got.FilesError, "changed") || got.Diff != nil || len(got.Files) != 0 ||
					got.DiffNext != "" || got.FilesNext != "" || slices.Contains(requests, "diff") || slices.Contains(requests, "diffstat") {
					t.Fatalf("mixed versions were read: requests=%v, result=%+v", requests, got)
				}
			})
		}
	}
}
