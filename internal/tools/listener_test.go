package tools

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDeclinesCannotExceedRetainedListenerLimit(t *testing.T) {
	f := newFake()
	cs := connectRetryingItself(t, f)
	var retired []chan struct{}
	f.tools.serve = func(h http.Handler, until time.Time) (string, <-chan struct{}, error) {
		base, _, err := f.pages.serve(h, until)
		closed := make(chan struct{})
		retired = append(retired, closed)
		return base, closed, err
	}
	args := map[string]any{"repo": "repo-a", "branch": "main"}
	for range maxPageListeners {
		first, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "run_pipeline", Arguments: args})
		if err != nil || first.IsError {
			t.Fatalf("initial approval: %+v, %v", first, err)
		}
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "run_pipeline", Arguments: args, RequestState: first.RequestState, InputResponses: mcp.InputResponseMap{secretsInputID: &mcp.ElicitResult{Action: "decline"}}})
		if err != nil || res.IsError {
			t.Fatalf("decline: %+v, %v", res, err)
		}
	}
	out, isErr := call(t, cs, "run_pipeline", args)
	if !isErr || !strings.Contains(out, "listener limit") || len(retired) != maxPageListeners {
		t.Fatalf("retained listener limit bypassed: %s", out)
	}
	expectPagesClosed(t, f, maxPageListeners)
	close(retired[0])
	eventually(t, func() bool {
		f.tools.mu.Lock()
		defer f.tools.mu.Unlock()
		return f.tools.pageListeners == maxPageListeners-1
	})
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "run_pipeline", Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("retired listener did not release its slot: %+v, %v", res, err)
	}
	for _, closed := range retired[1:] {
		close(closed)
	}
}

func TestListenerFailureDoesNotConsumeSlot(t *testing.T) {
	f := newFake()
	cs := connectRetryingItself(t, f)
	f.tools.serve = func(http.Handler, time.Time) (string, <-chan struct{}, error) {
		return "", nil, errors.New("listen failed")
	}
	out, isErr := call(t, cs, "run_pipeline", map[string]any{"repo": "repo-a", "branch": "main"})
	if !isErr || !strings.Contains(out, "listen failed") {
		t.Fatal(out)
	}
	f.tools.mu.Lock()
	defer f.tools.mu.Unlock()
	if f.tools.pageListeners != 0 || f.tools.openPages != 0 || len(f.tools.pending) != 0 {
		t.Fatal("failed listener consumed a slot")
	}
}
