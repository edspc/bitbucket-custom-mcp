package bitbucket

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestPullRequestDiffRedirectRequiresExactCommits(t *testing.T) {
	pr := diffPullRequest()
	source, destination := pr.Source.Commit.Hash, pr.Destination.Commit.Hash
	for _, kind := range []string{"diff", "diffstat"} {
		for _, tc := range []struct {
			name, spec string
			valid      bool
		}{
			{"same repo", source + ".." + destination, true},
			{"fork", "fork/repo:" + source + "\rws/repo-a:" + destination, true},
			{"new source", strings.Repeat("c", 40) + ".." + destination, false},
			{"new destination", source + ".." + strings.Repeat("c", 40), false},
			{"branch names", "feature..main", false},
			{"unresolved abbreviated hashes", source[:12] + ".." + destination[:12], false},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.URL.Path, "/commit/") {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					w.Header().Set("Location", APIURL+escapedPath("repositories", "ws", "repo-a", kind, tc.spec)+"?topic=true")
					w.WriteHeader(http.StatusFound)
				})
				_, err := c.ResolvePullRequestDiff(t.Context(), "repo-a", pr, kind)
				if (err == nil) != tc.valid {
					t.Fatalf("redirect accepted=%v, want %v: %v", err == nil, tc.valid, err)
				}
			})
		}
	}
	for _, invalid := range []PullRequest{{ID: 7}, {ID: 7, Source: PullRequestEnd{Commit: &Commit{Hash: "main"}}, Destination: pr.Destination}} {
		c := newTestClient(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid metadata reached the API") })
		if _, err := c.ResolvePullRequestDiff(t.Context(), "repo-a", invalid, "diff"); err == nil {
			t.Fatal("missing or mutable commits accepted")
		}
	}
}

func TestFilePagesStayOnCheckedCommits(t *testing.T) {
	pages := 0
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/pullrequests/") {
			w.Header().Set("Location", strings.Replace(diffLocation, "/diff/", "/diffstat/", 1))
			w.WriteHeader(http.StatusFound)
			return
		}
		pages++
		if !strings.HasSuffix(r.URL.Path, strings.Repeat("a", 40)+"\r"+strings.Repeat("b", 40)) || r.URL.Query().Get("page") != fmt.Sprint(pages) {
			t.Fatalf("another version was requested: %s", r.URL)
		}
		_, _ = fmt.Fprint(w, `{"values":[`+strings.Repeat(`{},`, 99)+`{}],"next":"https://api.bitbucket.org/2.0/repositories/ws/repo-a/diffstat/main..feature?page=2"}`)
	})
	got, err := c.PullRequestDiffStat(t.Context(), testDiffSnapshot(t, c, "diffstat"), 0, 200)
	if err != nil || pages != 2 || len(got.Values) != 200 || !got.More {
		t.Fatalf("pages=%d, result=%+v, err=%v", pages, got, err)
	}
}
