package bitbucket

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const (
	testHead   = "abcdef0123456789abcdef0123456789abcdef01"
	testOnRef  = "1111111111111111111111111111111111111111"
	testOffRef = "2222222222222222222222222222222222222222"
)

// refServer answers like Bitbucket for a ref whose head is testHead, testOnRef being an
// earlier commit of it and testOffRef a commit of another branch.
func refServer(t *testing.T, requests *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*requests = append(*requests, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		path := r.URL.EscapedPath()
		switch {
		case r.Method != http.MethodGet:
			t.Errorf("unexpected request: %s %s", r.Method, path)
		case strings.Contains(path, "/refs/"):
			_, _ = fmt.Fprintf(w, `{"target":{"hash":%q}}`, testHead)
		case strings.HasSuffix(path, "/commit/abcdef0"):
			_, _ = fmt.Fprintf(w, `{"hash":%q}`, testHead)
		case strings.Contains(path, "/commit/"):
			_, _ = fmt.Fprintf(w, `{"hash":%q}`, path[strings.LastIndex(path, "/")+1:])
		case strings.HasSuffix(path, "/commits"):
			if r.URL.Query().Get("include") == testOffRef && r.URL.Query().Get("exclude") == testHead {
				_, _ = fmt.Fprintf(w, `{"values":[{"hash":%q}]}`, testOffRef)
				return
			}
			_, _ = fmt.Fprint(w, `{"values":[]}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, path)
		}
	}
}

func TestPipelineCommitResolvesFullSHA(t *testing.T) {
	for _, tc := range []struct {
		kind, ref, revision, want string
		paths                     []string
	}{
		{"branch", "release/next", "", testHead, []string{"/2.0/repositories/ws/repo-a/refs/branches/release%2Fnext?"}},
		{"tag", "v1", "", testHead, []string{"/2.0/repositories/ws/repo-a/refs/tags/v1?"}},
		{"branch", "main", "abcdef0", testHead, []string{
			"/2.0/repositories/ws/repo-a/refs/branches/main?",
			"/2.0/repositories/ws/repo-a/commit/abcdef0?",
		}},
		{"branch", "main", testOnRef, testOnRef, []string{
			"/2.0/repositories/ws/repo-a/refs/branches/main?",
			"/2.0/repositories/ws/repo-a/commit/" + testOnRef + "?",
			"/2.0/repositories/ws/repo-a/commits?exclude=" + testHead + "&include=" + testOnRef + "&pagelen=1",
		}},
	} {
		t.Run(tc.ref+"@"+tc.revision, func(t *testing.T) {
			var requests []string
			c := newTestClient(refServer(t, &requests))
			got, err := c.PipelineCommit(context.Background(), "repo-a", tc.kind, tc.ref, tc.revision)
			if err != nil || got != tc.want {
				t.Fatalf("resolved %q, %v", got, err)
			}
			if strings.Join(requests, "\n") != strings.Join(tc.paths, "\n") {
				t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(requests, "\n"), strings.Join(tc.paths, "\n"))
			}
		})
	}
}

// A commit of another branch would run its own pipeline definition as if it were the ref's.
func TestPipelineCommitRejectsCommitOffTheRef(t *testing.T) {
	var requests []string
	c := newTestClient(refServer(t, &requests))
	_, err := c.PipelineCommit(context.Background(), "repo-a", "branch", "main", testOffRef)
	if err == nil || !strings.Contains(err.Error(), `is not on branch "main"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestPipelineCommitRejectsMissingRef(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/refs/branches/") {
			t.Errorf("the commit was looked up before the ref: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	})
	if _, err := c.PipelineCommit(context.Background(), "repo-a", "branch", "no such branch", testOnRef); !IsStatus(err, http.StatusNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestPipelineCommitRejectsMissingOrInvalidSHA(t *testing.T) {
	for _, hash := range []string{"", "abc123", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"} {
		c := newTestClient(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprintf(w, `{"target":{"hash":%q}}`, hash) })
		if _, err := c.PipelineCommit(context.Background(), "repo-a", "branch", "main", ""); err == nil {
			t.Fatalf("accepted %q", hash)
		}
	}
}
