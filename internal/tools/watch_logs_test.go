package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

func TestWatchStreamsNewLinesAndStepTransitions(t *testing.T) {
	f := newFake()
	p := twoSteps(f)
	f.logs["{s1}"] = "first\nrepeat\npar"
	f.onPipelineGet = func(n int) {
		switch n {
		case 2:
			f.logs["{s1}"] += "tial\nrepeat\n"
			f.logs["{s2}"] = "test\x1b[31m red\x1b[0m\n"
			f.setState(p, running(), new(completed("SUCCESSFUL")), &bitbucket.State{Name: "IN_PROGRESS"})
		case 3:
			f.logs["{s2}"] += "last"
			f.setState(p, completed("FAILED"), nil, new(completed("FAILED")))
		}
	}
	var mu sync.Mutex
	var progress []mcp.ProgressNotificationParams
	opts := clientOptions(nil)
	opts.ProgressNotificationHandler = func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
		mu.Lock()
		defer mu.Unlock()
		progress = append(progress, *req.Params)
	}
	cs := connectWith(t, f, opts, "")
	params := &mcp.CallToolParams{Name: "watch_pipeline", Arguments: map[string]any{"repo": "repo-a", "pipeline": "1", "stream_logs": true}}
	params.SetProgressToken("log-watch")
	res, err := cs.CallTool(t.Context(), params)
	if err != nil || res.IsError {
		t.Fatalf("call = %+v, %v", res, err)
	}
	got := decodeResult[watchPipelineResult](t, res.Content[0].(*mcp.TextContent).Text)
	if got.Outcome != "completed" || got.LogsTruncated || len(got.Logs) != 4 {
		t.Fatalf("result = %+v", got)
	}
	want := []string{"first\nrepeat\n", "partial\nrepeat\n", "test red\n", "last"}
	for i, chunk := range got.Logs {
		if chunk.Log != want[i] || chunk.ContentTrust != "untrusted" || !strings.Contains(chunk.URL, "/steps/") {
			t.Fatalf("chunk %d = %+v", i, chunk)
		}
	}
	eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		count := 0
		for _, event := range progress {
			if strings.HasPrefix(event.Message, "{") {
				count++
			}
		}
		return count == 4
	})
	mu.Lock()
	defer mu.Unlock()
	logs := 0
	for i, event := range progress {
		if event.ProgressToken != "log-watch" || event.Progress != float64(i+1) {
			t.Fatalf("progress %d = %+v", i, event)
		}
		if strings.HasPrefix(event.Message, "{") {
			var chunk watchLogChunk
			if err := json.Unmarshal([]byte(event.Message), &chunk); err != nil || chunk.Log != want[logs] {
				t.Fatalf("log event = %s, %v", event.Message, err)
			}
			logs++
		}
	}
	if logs != 4 {
		t.Fatalf("log events = %d", logs)
	}
}

func TestWatchLogOptionsAndStepSelection(t *testing.T) {
	f := newFake()
	failedRun(f)
	cs := connect(t, f, nil)
	for _, args := range []map[string]any{
		{"repo": "repo-a", "pipeline": "1", "stream_logs": true},
		{"repo": "repo-a", "pipeline": "1", "step": "Build"},
	} {
		if out, isErr := call(t, cs, "watch_pipeline", args); !isErr || !strings.Contains(out, "requires") {
			t.Fatalf("call = %s, error=%v", out, isErr)
		}
	}
	if f.pipelineGet != 0 {
		t.Fatal("invalid options reached Bitbucket")
	}
	params := &mcp.CallToolParams{Name: "watch_pipeline", Arguments: map[string]any{"repo": "repo-a", "pipeline": "1", "stream_logs": true, "step": "Build"}}
	params.SetProgressToken(1)
	res, err := cs.CallTool(t.Context(), params)
	if err != nil || res.IsError {
		t.Fatalf("call = %+v, %v", res, err)
	}
	got := decodeResult[watchPipelineResult](t, res.Content[0].(*mcp.TextContent).Text)
	if len(got.Logs) != 1 || got.Logs[0].Step.Name != "Build" || got.Logs[0].Log != "build ok\n" {
		t.Fatalf("result = %+v", got)
	}
}

func TestWatchLogUnavailableAndRetry(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "later available"}[available], func(t *testing.T) {
			f := newFake()
			p := twoSteps(f)
			f.onPipelineGet = func(n int) {
				if n == 2 {
					if available {
						f.logs["{s1}"] = "ready\n"
					}
					f.setState(p, completed("SUCCESSFUL"), new(completed("SUCCESSFUL")), new(completed("NOT_RUN")))
				}
			}
			cs := connect(t, f, nil)
			params := &mcp.CallToolParams{Name: "watch_pipeline", Arguments: map[string]any{"repo": "repo-a", "pipeline": "1", "stream_logs": true}}
			params.SetProgressToken("logs")
			res, err := cs.CallTool(t.Context(), params)
			if err != nil || res.IsError {
				t.Fatalf("call = %+v,%v", res, err)
			}
			got := decodeResult[watchPipelineResult](t, res.Content[0].(*mcp.TextContent).Text)
			if got.Outcome != "completed" || len(got.Logs) != 1 || got.Logs[0].Unavailable == available {
				t.Fatalf("result=%+v", got)
			}
			if available && got.Logs[0].Log != "ready\n" {
				t.Fatalf("log=%q", got.Logs[0].Log)
			}
		})
	}
}

func TestWatchLogUnavailableAtTimeout(t *testing.T) {
	for _, tc := range []struct {
		name        string
		initial     string
		recovered   bool
		unavailable bool
		wantLog     string
	}{
		{name: "missing", unavailable: true},
		{name: "recovered", recovered: true, wantLog: "ready\n"},
		{name: "missing with buffered text", initial: "partial", unavailable: true, wantLog: "partial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			twoSteps(f)
			if tc.initial != "" {
				f.logs["{s1}"] = tc.initial
			}
			f.onPipelineGet = func(n int) {
				if n >= 2 {
					if tc.recovered {
						f.logs["{s1}"] = "ready\n"
					} else {
						delete(f.logs, "{s1}")
					}
				}
			}
			cs := connect(t, f, nil)
			f.tools.pollInterval = 400 * time.Millisecond
			params := &mcp.CallToolParams{Name: "watch_pipeline", Arguments: map[string]any{"repo": "repo-a", "pipeline": "1", "stream_logs": true, "timeout_seconds": 1}}
			params.SetProgressToken("logs")
			res, err := cs.CallTool(t.Context(), params)
			if err != nil || res.IsError {
				t.Fatalf("call = %+v, %v", res, err)
			}
			got := decodeResult[watchPipelineResult](t, res.Content[0].(*mcp.TextContent).Text)
			if got.Outcome != "timeout" || len(got.Logs) != 1 {
				t.Fatalf("result = %+v", got)
			}
			chunk := got.Logs[0]
			if chunk.Unavailable != tc.unavailable || chunk.Log != tc.wantLog || chunk.Step.UUID != "{s1}" || chunk.ContentTrust != "untrusted" {
				t.Fatalf("chunk = %+v", chunk)
			}
		})
	}
}

func TestWatchLogGapsResetsAndBoundedResult(t *testing.T) {
	result := watchPipelineResult{}
	stream := &watchLogStream{result: &result, steps: map[string]*watchLogState{}}
	state := &watchLogState{chunk: watchLogChunk{ContentTrust: "untrusted"}}
	state.add([]byte("same\n"), 0)
	if err := stream.flush(state, false, nil); err != nil {
		t.Fatal(err)
	}
	state.chunk.Truncated = false
	state.add([]byte("same\nsame\n"), 0)
	if err := stream.flush(state, false, nil); err != nil {
		t.Fatal(err)
	}
	if len(result.Logs) != 2 || result.Logs[1].Log != "same\n" {
		t.Fatalf("logs=%+v", result.Logs)
	}
	state.add([]byte("partial\nlatest\n"), 100)
	if err := stream.flush(state, false, nil); err != nil {
		t.Fatal(err)
	}
	if result.Logs[2].Log != "latest\n" || !result.Logs[2].Truncated {
		t.Fatalf("gap=%+v", result.Logs[2])
	}
	state.add([]byte("reset\n"), 0)
	if err := stream.flush(state, false, nil); err != nil {
		t.Fatal(err)
	}
	if result.Logs[3].Log != "reset\n" || !result.Logs[3].Truncated {
		t.Fatalf("reset=%+v", result.Logs[3])
	}
	for range 30 {
		state.pending = strings.Repeat("x", watchLogEventBytes) + "\n"
		if err := stream.flush(state, true, nil); err != nil {
			t.Fatal(err)
		}
	}
	if stream.bytes > maxLogBytes || !result.LogsTruncated {
		t.Fatalf("bytes=%d truncated=%v", stream.bytes, result.LogsTruncated)
	}
}

func TestWatchLogDeadlineAndAPIErrors(t *testing.T) {
	for _, status := range []int{0, http.StatusForbidden} {
		t.Run(map[int]string{0: "deadline", http.StatusForbidden: "forbidden"}[status], func(t *testing.T) {
			f := newFake()
			twoSteps(f)
			cs := connect(t, f, nil)
			upstream := f.handler(t)
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/log") {
					if status != 0 {
						w.WriteHeader(status)
						return
					}
					<-r.Context().Done()
					return
				}
				upstream.ServeHTTP(w, r)
			})
			f.tools.bb = bitbucket.New(&http.Client{Transport: handlerTransport{h}}, bitbucket.Config{Workspace: "ws"})
			params := &mcp.CallToolParams{Name: "watch_pipeline", Arguments: map[string]any{"repo": "repo-a", "pipeline": "1", "stream_logs": true, "timeout_seconds": 1}}
			params.SetProgressToken("logs")
			start := time.Now()
			res, err := cs.CallTool(t.Context(), params)
			if err != nil {
				t.Fatal(err)
			}
			if status != 0 {
				if !res.IsError {
					t.Fatalf("call=%+v", res)
				}
			} else {
				got := decodeResult[watchPipelineResult](t, res.Content[0].(*mcp.TextContent).Text)
				if got.Outcome != "timeout" || got.Pipeline == nil || time.Since(start) > 1500*time.Millisecond {
					t.Fatalf("result=%+v", got)
				}
			}
			if f.tools.watchCalls != 0 {
				t.Fatal("watch kept its slot")
			}
		})
	}
}

func TestWatchLogClientCancellation(t *testing.T) {
	f := newFake()
	twoSteps(f)
	cs := connect(t, f, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	upstream := f.handler(t)
	f.tools.bb = bitbucket.New(&http.Client{Transport: handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/log") {
			close(started)
			<-r.Context().Done()
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		upstream.ServeHTTP(w, r)
	})}}, bitbucket.Config{Workspace: "ws"})
	params := &mcp.CallToolParams{Name: "watch_pipeline", Arguments: map[string]any{"repo": "repo-a", "pipeline": "1", "stream_logs": true}}
	params.SetProgressToken("logs")
	done := make(chan error, 1)
	go func() { _, err := cs.CallTool(ctx, params); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not start reading logs")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	eventually(t, func() bool {
		f.tools.mu.Lock()
		defer f.tools.mu.Unlock()
		return f.tools.watchCalls == 0
	})
}

func TestWatchLogBuffersUTF8EscapesAndFinalPartialLine(t *testing.T) {
	result := watchPipelineResult{}
	stream := &watchLogStream{result: &result}
	state := &watchLogState{chunk: watchLogChunk{ContentTrust: "untrusted"}}
	state.add([]byte("\x1b[31m\xd0"), 0)
	if err := stream.flush(state, false, nil); err != nil {
		t.Fatal(err)
	}
	if len(result.Logs) != 0 {
		t.Fatal("sent an incomplete UTF-8/escape sequence")
	}
	state.add([]byte("\x1b[31m\xd0\xbf\x1b[0m\nlast"), 0)
	if err := stream.flush(state, false, nil); err != nil {
		t.Fatal(err)
	}
	if len(result.Logs) != 1 || result.Logs[0].Log != "п\n" {
		t.Fatalf("logs=%+v", result.Logs)
	}
	stream.steps = map[string]*watchLogState{"step": state}
	if err := stream.finish(); err != nil {
		t.Fatal(err)
	}
	if len(result.Logs) != 2 || result.Logs[1].Log != "last" {
		t.Fatalf("logs=%+v", result.Logs)
	}
}

func TestWatchStreamsParallelStepsWithinPollingBudget(t *testing.T) {
	f := newFake()
	p := twoSteps(f)
	f.setState(p, running(), nil, &bitbucket.State{Name: "IN_PROGRESS"})
	f.logs["{s1}"], f.logs["{s2}"] = "build\n", "test\n"
	f.onPipelineGet = func(n int) {
		if n == 2 {
			f.setState(p, completed("SUCCESSFUL"), new(completed("SUCCESSFUL")), new(completed("SUCCESSFUL")))
		}
	}
	var mu sync.Mutex
	statuses := 0
	opts := clientOptions(nil)
	opts.ProgressNotificationHandler = func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
		if !strings.HasPrefix(req.Params.Message, "{") {
			mu.Lock()
			statuses++
			mu.Unlock()
		}
	}
	cs := connectWith(t, f, opts, "")
	f.tools.pollInterval = 80 * time.Millisecond
	params := &mcp.CallToolParams{Name: "watch_pipeline", Arguments: map[string]any{"repo": "repo-a", "pipeline": "1", "stream_logs": true}}
	params.SetProgressToken("parallel")
	start := time.Now()
	res, err := cs.CallTool(t.Context(), params)
	if err != nil || res.IsError {
		t.Fatalf("call = %+v, %v", res, err)
	}
	got := decodeResult[watchPipelineResult](t, res.Content[0].(*mcp.TextContent).Text)
	if len(got.Logs) != 2 || got.Logs[0].Step.UUID != "{s1}" || got.Logs[1].Step.UUID != "{s2}" {
		t.Fatalf("logs=%+v", got.Logs)
	}
	if time.Since(start) < 160*time.Millisecond {
		t.Fatal("additional log requests did not slow down polling")
	}
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return statuses >= 3 })
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.logRanges) != 4 {
		t.Fatalf("log requests=%v", f.logRanges)
	}
	if f.pipelineGet != 2 {
		t.Fatalf("heartbeats made extra pipeline requests: %d", f.pipelineGet)
	}
}
