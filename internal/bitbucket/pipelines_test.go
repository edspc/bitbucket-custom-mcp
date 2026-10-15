package bitbucket

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
)

func TestRunPipelineSendsTarget(t *testing.T) {
	var path, body string
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"uuid": "{p-1}", "build_number": 7, "state": {"name": "PENDING"}}`)
	})
	p, err := c.RunPipeline(context.Background(), "repo-a", RunRequest{
		Target: PipelineTarget{
			Type: "pipeline_ref_target", RefType: "branch", RefName: "main",
			Commit: &Commit{Type: "commit", Hash: "abc"}, Selector: &Selector{Type: "custom", Pattern: "deploy"},
		},
		Variables: []Variable{{Key: "ENV", Value: "prod"}, {Key: "PASS", Value: "s", Secured: true}},
	})
	if err != nil || p.BuildNumber != 7 || p.State.String() != "PENDING" {
		t.Fatalf("RunPipeline = %+v, %v", p, err)
	}
	if path != "POST /2.0/repositories/ws/repo-a/pipelines/" {
		t.Errorf("request = %s", path)
	}
	want := `{"target":{"type":"pipeline_ref_target","ref_type":"branch","ref_name":"main","commit":{"type":"commit","hash":"abc"},` +
		`"selector":{"type":"custom","pattern":"deploy"}},"variables":[{"key":"ENV","value":"prod","secured":false},{"key":"PASS","value":"s","secured":true}]}`
	if body != want {
		t.Errorf("body = %s\nwant %s", body, want)
	}
}

func TestListPipelinesQuery(t *testing.T) {
	var got string
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path + "?" + r.URL.RawQuery
		_, _ = fmt.Fprint(w, `{"values": [{"build_number": 2}, {"build_number": 1}], "size": 9, "next": "https://api.bitbucket.org/2.0/x?page=4"}`)
	})
	ps, err := c.ListPipelines(context.Background(), "repo-a", "main", 3, 2)
	if err != nil || len(ps.Values) != 2 || ps.Values[0].BuildNumber != 2 || !ps.More || ps.Total == nil || *ps.Total != 9 {
		t.Fatalf("ListPipelines = %+v, %v", ps, err)
	}
	if want := "/2.0/repositories/ws/repo-a/pipelines/?page=3&pagelen=2&sort=-created_on&target.branch=main"; got != want {
		t.Errorf("request = %s, want %s", got, want)
	}
}

func TestListPipelinesLastPage(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"values": [{"build_number": 1}]}`)
	})
	ps, err := c.ListPipelines(context.Background(), "repo-a", "", 2, 5)
	if err != nil || len(ps.Values) != 1 || ps.More || ps.Total != nil {
		t.Fatalf("ListPipelines = %+v, %v", ps, err)
	}
}

// A page with more pipelines than asked for is refused rather than passed on unbounded.
func TestListPipelinesRefusesOversizedPage(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"values": [{"build_number": 3}, {"build_number": 2}, {"build_number": 1}]}`)
	})
	if ps, err := c.ListPipelines(context.Background(), "repo-a", "", 1, 2); err == nil {
		t.Fatalf("ListPipelines = %+v, want an error", ps)
	}
}

func TestStateString(t *testing.T) {
	for want, s := range map[string]State{
		"PENDING":            {Name: "PENDING"},
		"IN_PROGRESS/PAUSED": {Name: "IN_PROGRESS", Stage: &StateDetail{Name: "PAUSED"}},
		"COMPLETED/FAILED":   {Name: "COMPLETED", Result: &StateDetail{Name: "FAILED"}},
	} {
		if got := s.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
	if !(State{Name: "IN_PROGRESS", Stage: &StateDetail{Name: "PAUSED"}}).Paused() || (State{Name: "IN_PROGRESS"}).Paused() {
		t.Error("Paused is wrong")
	}
}

func TestStepLogRange(t *testing.T) {
	const log = "line 1\nline 2\nline 3\n"
	for name, tc := range map[string]struct {
		serve         func(w http.ResponseWriter, r *http.Request)
		want          string
		wantTruncated bool
	}{
		"partial from the middle": {func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Range", "bytes 7-20/21")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.WriteString(w, log[7:])
		}, log[7:], true},
		"whole log in range": {func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Range", "bytes 0-6/7")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.WriteString(w, "line 3\n")
		}, "line 3\n", false},
		"range ignored": {func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", 300)+log)
		}, log[21-16:], true},
		"empty log": {func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		}, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.EscapedPath() != "/2.0/repositories/ws/repo-a/pipelines/%7Bp-1%7D/steps/%7Bs-1%7D/log" {
					t.Errorf("path = %s", r.URL.EscapedPath())
				}
				if got := r.Header.Get("Range"); got != "bytes=-16" {
					t.Errorf("Range = %q", got)
				}
				tc.serve(w, r)
			})
			got, truncated, err := c.StepLog(context.Background(), "repo-a", "{p-1}", "{s-1}", 16)
			if err != nil || string(got) != tc.want || truncated != tc.wantTruncated {
				t.Fatalf("StepLog = %q, %v, %v; want %q, %v", got, truncated, err, tc.want, tc.wantTruncated)
			}
		})
	}
}

// A finished step's log is redirected to storage with a signed link: the credentials must not
// follow, and the link must not show up in errors.
func TestStepLogFromStorage(t *testing.T) {
	var storageAuth []string
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "api.bitbucket.org" {
			w.Header().Set("Location", "https://storage.example/log?signature=s3cr3t")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		storageAuth = append(storageAuth, r.Header.Get("Authorization"))
		if r.URL.Query().Get("signature") != "s3cr3t" || r.Header.Get("Range") != "bytes=-100" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, "done\n")
	})
	got, truncated, err := c.StepLog(context.Background(), "repo-a", "{p-1}", "{s-1}", 100)
	if err != nil || string(got) != "done\n" || truncated {
		t.Fatalf("StepLog = %q, %v, %v", got, truncated, err)
	}
	if len(storageAuth) != 1 || storageAuth[0] != "" {
		t.Fatalf("storage requests carried Authorization %q", storageAuth)
	}

	failing := New(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.bitbucket.org" {
			h := http.Header{"Location": {"https://storage.example/log?signature=s3cr3t"}}
			return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: h, Body: http.NoBody, Request: r}, nil
		}
		return nil, errors.New("connection reset")
	})}, Config{Workspace: "ws", Email: testEmail, Token: testToken})
	_, _, err = failing.StepLog(context.Background(), "repo-a", "{p-1}", "{s-1}", 100)
	if err == nil || strings.Contains(err.Error(), "s3cr3t") || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("err = %v", err)
	}
}

func TestStepLogRefusesPlainHTTPStorage(t *testing.T) {
	var requests int
	c := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Location", "http://storage.example/log")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	if _, _, err := c.StepLog(context.Background(), "repo-a", "{p-1}", "{s-1}", 100); err == nil {
		t.Fatal("want an error for a redirect to plain HTTP")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, the redirect must not be followed", requests)
	}
}

func TestReadTailKeepsTheEnd(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 1000)
	got, dropped, err := readTail(iotest.OneByteReader(bytes.NewReader(data)), 15)
	if err != nil || string(got) != "567890123456789" || !dropped {
		t.Fatalf("readTail = %q, %v, %v", got, dropped, err)
	}
	got, dropped, err = readTail(bytes.NewReader(data[:10]), 15)
	if err != nil || string(got) != "0123456789" || dropped {
		t.Fatalf("short readTail = %q, %v, %v", got, dropped, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
