package bitbucket

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestPullRequestSnapshotMatchesRealBitbucketHashes(t *testing.T) {
	// Metadata and redirects captured from public PRs atlassian-plugins #1133 and
	// atlassian-event #20. Embedded commit IDs have 12 characters, including merge_commit.
	for _, merged := range []bool{false, true} {
		for _, expanded := range []bool{false, true} {
			for _, kind := range []string{"diff", "diffstat"} {
				t.Run(fmt.Sprintf("merged=%v/expanded=%v/%s", merged, expanded, kind), func(t *testing.T) {
					repo, id := "atlassian-plugins", 1133
					source := "fa7782de032166981a6ba0644b623b9b031a7cb6"
					destination := "212bf36c93df542fc49148d3d284734ccea5b17c"
					pr := PullRequest{ID: id, State: "DECLINED"}
					if merged {
						repo, id = "atlassian-event", 20
						source = "30c4710cbc0bda5882a5cc61fefd6a6c6700773f"
						destination = "e6bf7c63b7aff6456d3a9809b9769bb91e2b125e"
						pr.ID, pr.State = id, "MERGED"
						pr.MergeCommit = &Commit{Hash: source[:12]}
					}
					pr.Source.Commit, pr.Destination.Commit = &Commit{Hash: source[:12]}, &Commit{Hash: destination[:12]}
					pr.Source.Repository.FullName = "atlassian/" + repo
					if merged {
						pr.Source.Commit.Hash = "db89d0c8e7d7"
						pr.Source.Repository.FullName = "dev/deleted-fork"
					}
					if expanded {
						pr.Destination.Commit.Hash = destination
						if merged {
							pr.MergeCommit.Hash = source
						} else {
							pr.Source.Commit.Hash = source
						}
					}
					lookups, reads := 0, 0
					c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
						switch {
						case strings.Contains(r.URL.Path, "/commit/"):
							lookups++
							switch r.URL.Path {
							case "/2.0/repositories/atlassian/" + repo + "/commit/" + source[:12]:
								_ = json.NewEncoder(w).Encode(Commit{Hash: source})
							case "/2.0/repositories/atlassian/" + repo + "/commit/" + destination[:12]:
								_ = json.NewEncoder(w).Encode(Commit{Hash: destination})
							default:
								t.Fatalf("unexpected commit lookup: %s", r.URL.Path)
							}
						case strings.Contains(r.URL.Path, "/pullrequests/"):
							w.Header().Set("Location", fmt.Sprintf("%s/repositories/atlassian/%s/%s/atlassian/%s:%s%%0D%s?from_pullrequest_id=%d", APIURL, repo, kind, repo, source[:12], destination[:12], id))
							w.WriteHeader(http.StatusFound)
						default:
							reads++
							if !strings.HasSuffix(r.URL.Path, "atlassian/"+repo+":"+source+"\r"+destination) {
								t.Fatalf("diff was not pinned to full commits: %s", r.URL.Path)
							}
							if kind == "diff" {
								_, _ = fmt.Fprint(w, "+change\n")
							} else {
								_, _ = fmt.Fprint(w, `{"values":[{"status":"added","new":{"path":"a.go"}}]}`)
							}
						}
					})
					c.cfg.Workspace = "atlassian"
					snapshot, err := c.ResolvePullRequestDiff(t.Context(), repo, pr, kind)
					if err != nil || lookups != 2 {
						t.Fatalf("snapshot=%+v, lookups=%d, err=%v", snapshot, lookups, err)
					}
					if kind == "diff" {
						diff, _, err := c.PullRequestDiff(t.Context(), snapshot, []string{"a.go"}, 0, 100)
						if err != nil || string(diff) != "+change\n" {
							t.Fatalf("diff=%q, err=%v", diff, err)
						}
					} else {
						files, err := c.PullRequestDiffStat(t.Context(), snapshot, 0, 200)
						if err != nil || len(files.Values) != 1 {
							t.Fatalf("files=%+v, err=%v", files, err)
						}
					}
					if reads != 1 {
						t.Fatalf("diff reads=%d", reads)
					}
				})
			}
		}
	}
}

func TestPullRequestSnapshotRejectsWrongResolvedCommit(t *testing.T) {
	pr := diffPullRequest()
	for _, answer := range []string{strings.Repeat("a", 12), strings.Repeat("c", 40), strings.Repeat("a", 12) + strings.Repeat("c", 28)} {
		t.Run(answer, func(t *testing.T) {
			c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/pullrequests/") {
					w.Header().Set("Location", APIURL+"/repositories/ws/repo-a/diff/"+pr.Source.Commit.Hash[:12]+".."+pr.Destination.Commit.Hash)
					w.WriteHeader(http.StatusFound)
					return
				}
				if !strings.Contains(r.URL.Path, "/commit/") {
					t.Fatal("read an unchecked diff")
				}
				_ = json.NewEncoder(w).Encode(Commit{Hash: answer})
			})
			if _, err := c.ResolvePullRequestDiff(t.Context(), "repo-a", pr, "diff"); err == nil {
				t.Fatal("a short, unrelated or prefix-colliding commit was accepted")
			}
		})
	}
}

// A merged pull request whose redirect compares its source head, as Bitbucket's does for one from
// the same repository, costs no lookup of the merge commit.
func TestMergedPullRequestSkipsUnrelatedMergeCommit(t *testing.T) {
	const (
		source      = "d0a06076aec9a1b2c3d4e5f60718293a4b5c6d7e"
		destination = "b58dfbb281c81122334455667788990011223344"
		merge       = "28144ba87de0ffeeddccbbaa0099887766554433"
	)
	pr := PullRequest{ID: 547, State: "MERGED", MergeCommit: &Commit{Hash: merge[:12]}}
	pr.Source.Commit, pr.Destination.Commit = &Commit{Hash: source[:12]}, &Commit{Hash: destination[:12]}
	pr.Source.Repository.FullName = "ws/repo-a"
	var lookups []string
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/commit/"):
			lookups = append(lookups, r.URL.Path)
			for _, full := range []string{source, destination, merge} {
				if strings.HasSuffix(r.URL.Path, "/"+full[:12]) {
					_ = json.NewEncoder(w).Encode(Commit{Hash: full})
				}
			}
		default:
			w.Header().Set("Location", fmt.Sprintf("%s/repositories/ws/repo-a/diff/%s%%0D%s?from_pullrequest_id=547", APIURL, source[:12], destination[:12]))
			w.WriteHeader(http.StatusFound)
		}
	})
	if _, err := c.ResolvePullRequestDiff(t.Context(), "repo-a", pr, "diff"); err != nil || len(lookups) != 2 {
		t.Fatalf("lookups=%v, err=%v", lookups, err)
	}
}
