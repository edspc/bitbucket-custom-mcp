package bitbucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAllowedProjectsMatchExactKeyOrUUIDAndFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		projects []string
		metadata string
		status   int
		allowed  bool
		lookups  int
	}{
		{"unset", nil, "", 200, true, 0},
		{"deny all", []string{}, "", 200, false, 0},
		{"key", []string{"APP"}, `{"project":{"key":"APP","uuid":"{id}"}}`, 200, true, 1},
		{"either project", []string{"OPS", "APP"}, `{"project":{"key":"APP"}}`, 200, true, 1},
		{"uuid", []string{"{id}"}, `{"project":{"key":"RENAMED","uuid":"{id}"}}`, 200, true, 1},
		{"case sensitive", []string{"app"}, `{"project":{"key":"APP"}}`, 200, false, 1},
		{"no wildcard", []string{"*"}, `{"project":{"key":"APP"}}`, 200, false, 1},
		{"display name", []string{"Application"}, `{"project":{"key":"APP","name":"Application"}}`, 200, false, 1},
		{"other project", []string{"APP"}, `{"project":{"key":"OPS"}}`, 200, false, 1},
		{"missing", []string{"APP"}, `{}`, 200, false, 1},
		{"null", []string{"APP"}, `{"project":null}`, 200, false, 1},
		{"invalid", []string{"APP"}, `{"project":`, 200, false, 1},
		{"not found", []string{"APP"}, `{"error":{"message":"not found"}}`, 404, false, 1},
		{"unauthorized", []string{"APP"}, `{}`, 403, false, 1},
		{"rate limited", []string{"APP"}, `{}`, 429, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookups, requests := 0, 0
			c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/2.0/repositories/ws/repo-a" {
					lookups++
					if r.URL.Query().Get("fields") != "project.key,project.uuid" {
						t.Errorf("metadata fields = %q", r.URL.RawQuery)
					}
					w.WriteHeader(tc.status)
					_, _ = fmt.Fprint(w, tc.metadata)
					return
				}
				requests++
				_, _ = fmt.Fprint(w, `{"enabled":true}`)
			}).WithAllowedProjects(tc.projects)
			enabled, err := c.PipelinesEnabled(t.Context(), "repo-a")
			if (err == nil) != tc.allowed || enabled != tc.allowed || lookups != tc.lookups || tc.allowed && requests != 1 || !tc.allowed && requests != 0 {
				t.Fatalf("enabled=%v, err=%v, lookups=%d, requests=%d", enabled, err, lookups, requests)
			}
		})
	}
}

func TestProjectsRecheckedBeforeEachRequestAndWrite(t *testing.T) {
	project, metadata, reads, writes := "APP", 0, 0, 0
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("fields") {
			metadata++
			_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]string{"key": project}})
			return
		}
		if r.Method == http.MethodGet {
			reads++
			_, _ = fmt.Fprint(w, `{"enabled":true}`)
			return
		}
		writes++
	}).WithAllowedProjects([]string{"APP"})
	if _, err := c.PipelinesEnabled(t.Context(), "repo-a"); err != nil {
		t.Fatal(err)
	}
	project = "OPS"
	if _, err := c.PipelinesEnabled(t.Context(), "repo-a"); err == nil {
		t.Fatal("cached membership authorized a read")
	}
	if err := c.CreateVariable(t.Context(), VariableScope{Repo: "repo-a"}, Variable{Key: "TOKEN", Value: "secret", Secured: true}); err == nil {
		t.Fatal("cached membership authorized a write")
	}
	if reads != 1 || writes != 0 || metadata != 3 {
		t.Fatalf("reads=%d writes=%d metadata=%d", reads, writes, metadata)
	}
}

func TestProjectsProtectGETRedirectsAndPagination(t *testing.T) {
	for _, kind := range []string{"diff redirect", "diff data", "log", "pagination", "foreign workspace", "workspace variables", "dot segment", "encoded dot segment"} {
		t.Run(kind, func(t *testing.T) {
			requests := 0
			c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Has("fields") {
					key := "APP"
					if strings.Contains(r.URL.Path, "repo-b") {
						key = "OPS"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]string{"key": key}})
					return
				}
				requests++
				_, _ = fmt.Fprint(w, `{"values":[],"next":"https://api.bitbucket.org/2.0/repositories/ws/repo-b/deploy-keys?page=2"}`)
			}).WithAllowedProjects([]string{"APP"})
			var err error
			switch kind {
			case "diff redirect":
				_, err = c.redirectTarget(t.Context(), c.repoPath("repo-b", "pullrequests", "1", "diff"))
			case "diff data":
				_, err = c.get(t.Context(), APIURL+c.repoPath("repo-b", "diff", "aaaa..bbbb"), c.basicAuth, nil)
			case "log":
				_, err = c.get(t.Context(), APIURL+c.repoPath("repo-b", "pipelines", "1", "steps", "1", "log"), c.basicAuth, nil)
			case "pagination":
				_, err = c.ListDeployKeys(t.Context(), "repo-a")
			case "foreign workspace":
				err = c.call(t.Context(), http.MethodGet, "/repositories/other/repo-a/commit/abcdefabcdef", nil, nil)
			case "workspace variables":
				_, err = c.ListVariables(t.Context(), VariableScope{})
			case "dot segment":
				err = c.call(t.Context(), http.MethodGet, "/repositories/ws/repo-a/../repo-b/deploy-keys", nil, nil)
			case "encoded dot segment":
				err = c.call(t.Context(), http.MethodGet, "/repositories/ws/repo-a/%2e%2e/repo-b/deploy-keys", nil, nil)
			}
			want := 0
			if kind == "pagination" {
				want = 1
			}
			if err == nil || !strings.Contains(err.Error(), "server policy") || requests != want {
				t.Fatalf("err=%v requests=%d want=%d", err, requests, want)
			}
		})
	}
}

func TestProjectCheckUsesServerCredentialsBeforeTokenProbe(t *testing.T) {
	requests := 0
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Has("fields") {
			if user, token, ok := r.BasicAuth(); !ok || user != testEmail || token != testToken {
				t.Fatal("project lookup did not use server credentials")
			}
			_, _ = fmt.Fprint(w, `{"project":{"key":"APP"}}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer provided-token" {
			t.Fatal("probe did not use supplied token")
		}
	}).WithAllowedProjects([]string{"APP"})
	if err := c.CheckTokenAccess(t.Context(), "repo-a", "provided-token", []string{"repository"}); err != nil || requests != 2 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
}

func TestProjectRestrictionDoesNotChangeOriginalClient(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, `{"enabled":true}`) })
	projects := []string{"APP"}
	restricted := c.WithAllowedProjects(projects)
	projects[0] = "OPS"
	if restricted.allowedProjects[0] != "APP" {
		t.Fatal("caller changed configured projects")
	}
	if _, err := c.PipelinesEnabled(t.Context(), "repo-a"); err != nil {
		t.Fatalf("original client acquired project restrictions: %v", err)
	}
}

func TestWriteDeadlineCoversProjectChecksButNotSentWrites(t *testing.T) {
	for _, kind := range []string{"expired", "slow project check", "late project response", "slow write"} {
		t.Run(kind, func(t *testing.T) {
			lookups, writes := 0, 0
			c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Has("fields") {
					lookups++
					switch kind {
					case "slow project check":
						select {
						case <-r.Context().Done():
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						case <-time.After(300 * time.Millisecond):
						}
					case "late project response":
						// A transport may return success despite the context expiring.
						<-time.After(200 * time.Millisecond)
					}
					_, _ = fmt.Fprint(w, `{"project":{"key":"APP"}}`)
					return
				}
				writes++
				if kind == "slow write" {
					select {
					case <-r.Context().Done():
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					case <-time.After(200 * time.Millisecond):
					}
				}
			}).WithAllowedProjects([]string{"APP"})
			until := time.Now().Add(100 * time.Millisecond)
			if kind == "expired" {
				until = time.Now().Add(-time.Second)
			}
			ctx := WithWriteDeadline(t.Context(), until)
			err := c.CreateVariable(ctx, VariableScope{Repo: "repo-a"}, Variable{Key: "TOKEN", Value: "secret", Secured: true})
			if kind == "slow write" {
				if err != nil || writes != 1 {
					t.Fatalf("sent write was cancelled: %v, writes=%d", err, writes)
				}
			} else if err == nil || writes != 0 {
				t.Fatalf("expired authorization sent a write: %v, writes=%d", err, writes)
			}
			if kind == "late project response" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("late successful response hid the deadline: %v", err)
			}
			if kind == "expired" && lookups != 0 {
				t.Fatalf("expired approval issued %d project lookups", lookups)
			}
		})
	}
}
