package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

// twoSteps adds a running pipeline of repo-a whose first step runs and second waits.
func twoSteps(f *fakeBitbucket) *bitbucket.Pipeline {
	return f.addRun("repo-a", running(),
		bitbucket.Step{UUID: "{s1}", Name: "Build", State: bitbucket.State{Name: "IN_PROGRESS"}, StartedOn: "t1"},
		bitbucket.Step{UUID: "{s2}", Name: "Test", State: bitbucket.State{Name: "PENDING"}},
	)
}

func ptr[T any](v T) *T { return &v }

func TestWatchPipelineUntilCompleted(t *testing.T) {
	f := newFake()
	p := twoSteps(f)
	f.onPipelineGet = func(n int) {
		switch n {
		case 2:
			f.setState(p, running(), ptr(completed("SUCCESSFUL")), &bitbucket.State{Name: "IN_PROGRESS"})
		case 4:
			f.setState(p, completed("FAILED"), nil, ptr(completed("FAILED")))
		}
	}
	var mu sync.Mutex
	var progress []string
	opts := clientOptions(nil)
	opts.ProgressNotificationHandler = func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
		mu.Lock()
		defer mu.Unlock()
		progress = append(progress, req.Params.Message)
	}
	cs := connectWith(t, f, opts, "")
	params := &mcp.CallToolParams{Name: "watch_pipeline", Arguments: map[string]any{"repo": "repo-a", "pipeline": "1"}}
	params.SetProgressToken("watch-1")
	res, err := cs.CallTool(context.Background(), params)
	if err != nil || res.IsError {
		t.Fatalf("CallTool = %+v, %v", res, err)
	}
	got := decodeResult[watchPipelineResult](t, res.Content[0].(*mcp.TextContent).Text)
	if got.Outcome != "completed" || !got.Pipeline.Finished || got.Pipeline.State != "COMPLETED/FAILED" {
		t.Fatalf("result = %+v", got)
	}
	var updates []string
	for _, u := range got.Updates {
		_, text, _ := strings.Cut(u, " ") // drop the time
		updates = append(updates, text)
	}
	want := []string{"step Build: COMPLETED/SUCCESSFUL", "step Test: IN_PROGRESS", "step Test: COMPLETED/FAILED", "pipeline #1: COMPLETED/FAILED"}
	if strings.Join(updates, "|") != strings.Join(want, "|") {
		t.Fatalf("updates = %q", got.Updates)
	}
	// Notifications are handled apart from the response, so the last ones may still be on the way.
	eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(progress) == 4
	})
	mu.Lock()
	defer mu.Unlock()
	if progress[0] != "pipeline #1: IN_PROGRESS/RUNNING; step 1 of 2, Build: IN_PROGRESS" || progress[3] != "pipeline #1: COMPLETED/FAILED" {
		t.Fatalf("progress = %q", progress)
	}
}

func TestWatchPipelineStopsWhenPaused(t *testing.T) {
	f := newFake()
	f.addRun("repo-a", bitbucket.State{Name: "IN_PROGRESS", Stage: &bitbucket.StateDetail{Name: "PAUSED"}})
	res := decodeResult[watchPipelineResult](t, mustCall(t, connect(t, f, nil), "watch_pipeline", map[string]any{"repo": "repo-a", "pipeline": "1"}))
	if res.Outcome != "paused" || len(res.Updates) != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestWatchPipelineTimeout(t *testing.T) {
	f := newFake()
	twoSteps(f)
	cs := connect(t, f, nil)
	// The watch ends at its timeout even when the next poll would come much later.
	f.tools.pollInterval = time.Hour
	start := time.Now()
	res := decodeResult[watchPipelineResult](t, mustCall(t, cs, "watch_pipeline", map[string]any{"repo": "repo-a", "pipeline": "1", "timeout_seconds": 1}))
	if res.Outcome != "timeout" || res.Pipeline.Finished {
		t.Fatalf("result = %+v", res)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("a 1 s watch took %s", took)
	}
	// 18446744074 s overflows to 290 ms when converted unchecked.
	for _, seconds := range []int64{3601, -5, 18446744074} {
		out, isErr := call(t, cs, "watch_pipeline", map[string]any{"repo": "repo-a", "pipeline": "1", "timeout_seconds": seconds})
		if !isErr || !strings.Contains(out, "between 1 and 3600") {
			t.Fatalf("timeout %d = %s (error %v)", seconds, out, isErr)
		}
	}
}

// A watch takes one of the polling slots for as long as it runs.
func TestWatchPipelineHoldsAPollingSlot(t *testing.T) {
	f := newFake()
	watched := twoSteps(f)
	for range maxPolling {
		twoSteps(f)
	}
	cs := connectWith(t, f, clientOptions(nil), "2025-11-25")
	done := make(chan string, 1)
	go func() {
		out, _ := call(t, cs, "watch_pipeline", map[string]any{"repo": "repo-a", "pipeline": "1"})
		done <- out
	}()
	eventually(t, func() bool {
		f.tools.mu.Lock()
		defer f.tools.mu.Unlock()
		return f.tools.watchCalls == 1
	})
	for i := 2; i <= maxPolling+1; i++ {
		err := subscribe(t, cs, pipelineURI("repo-a", i))
		if (err != nil) != (i > maxPolling) {
			t.Fatalf("subscription %d while a watch runs: err = %v", i, err)
		}
	}
	f.mu.Lock()
	f.setState(watched, completed("SUCCESSFUL"))
	f.mu.Unlock()
	select {
	case out := <-done:
		if !strings.Contains(out, `"completed"`) {
			t.Fatalf("watch = %s", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watch did not end")
	}
	if err := subscribe(t, cs, pipelineURI("repo-a", maxPolling+1)); err != nil {
		t.Fatalf("subscribing after the watch ended: %v", err)
	}
}
