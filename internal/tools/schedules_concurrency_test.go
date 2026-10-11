package tools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConcurrentApprovalsCreateOnlyOneSchedule(t *testing.T) {
	f := newFake()
	cs := connectRetryingItself(t, f)
	args := map[string]any{"repo": "repo-a", "branch": "main", "pipeline": "default", "cron": "0 0 2 * * ? *"}
	initial := make([]*mcp.CallToolResult, 2)
	for i := range initial {
		var err error
		initial[i], err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "add_schedule", Arguments: args})
		if err != nil || initial[i].IsError {
			t.Fatalf("approval %d: %v, %+v", i, err, initial[i])
		}
		if status := f.pages.submit(initial[i].InputRequests[secretsInputID].(*mcp.ElicitParams).URL, nil); status != http.StatusSeeOther {
			t.Fatal(status)
		}
	}
	upstream := f.handler(t)
	listed, release := make(chan struct{}), make(chan struct{})
	preflight := make(chan struct{}, 2)
	var reads atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/schedules") {
			n := reads.Add(1)
			rec := httptest.NewRecorder()
			upstream.ServeHTTP(rec, r)
			if n == 1 {
				// Hold the empty preflight response while a second approved call starts.
				close(listed)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		upstream.ServeHTTP(w, r)
		if strings.HasSuffix(r.URL.Path, "/pipelines_config") {
			preflight <- struct{}{}
		}
	})
	f.tools.bb = bitbucket.New(&http.Client{Transport: handlerTransport{h}}, bitbucket.Config{Workspace: "ws"})
	results := make(chan struct {
		res *mcp.CallToolResult
		err error
	}, 2)
	retry := func(first *mcp.CallToolResult) {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "add_schedule", Arguments: args, RequestState: first.RequestState,
			InputResponses: mcp.InputResponseMap{secretsInputID: &mcp.ElicitResult{Action: "accept"}}})
		results <- struct {
			res *mcp.CallToolResult
			err error
		}{res, err}
	}
	go retry(initial[0])
	select {
	case <-listed:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("first preflight did not start")
	}
	go retry(initial[1])
	for range 2 {
		select {
		case <-preflight:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("both approved calls did not start")
		}
	}
	// The other call must not read an empty list while the first create is pending.
	time.Sleep(100 * time.Millisecond)
	if n := reads.Load(); n != 1 {
		t.Errorf("concurrent duplicate checks: %d", n)
	}
	close(release)
	statuses := map[string]int{}
	for range 2 {
		got := <-results
		if got.err != nil || got.res.IsError {
			t.Fatalf("schedule result=%+v, err=%v", got.res, got.err)
		}
		out := decodeResult[scheduleResult](t, got.res.Content[0].(*mcp.TextContent).Text)
		statuses[out.Status]++
	}
	if statuses["created"] != 1 || statuses["already_exists"] != 1 || len(f.schedulesOf("repo-a")) != 1 {
		t.Fatalf("duplicate schedules: statuses=%v, schedules=%+v", statuses, f.schedulesOf("repo-a"))
	}
	if mutations := f.mutationList(); len(mutations) != 1 {
		t.Fatalf("extra schedule writes: %v", mutations)
	}
}
