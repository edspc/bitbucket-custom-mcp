package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

const (
	// A poll costs two requests, and Bitbucket allows a user 1,000 requests an hour to
	// repository data. Polling at most maxPolling pipelines at a time, subscriptions and
	// watch_pipeline calls together, every pollInterval takes 480 of them at most and leaves
	// the rest to the other tools. Project restrictions can double the requests, unless a
	// repository is allowed by name, so they always double the interval.
	pollInterval        = 30 * time.Second
	maxPolling          = 2
	defaultWatchSeconds = 600
	maxWatchSeconds     = 3600
)

type watchPipelineInput struct {
	pipelineRef
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"how long to wait for the pipeline to finish; 600 when omitted, at most 3600"`
	StreamLogs     bool   `json:"stream_logs,omitempty" jsonschema:"stream new log lines through progress notifications; requires a client progress token. False by default"`
	Step           string `json:"step,omitempty" jsonschema:"with stream_logs, follow only this step name or UUID; otherwise all started steps"`
}

type watchPipelineResult struct {
	Outcome       string          `json:"outcome" jsonschema:"why the watch ended: 'completed'; 'paused' when the pipeline waits for someone to run a manual step; or 'timeout' when it still runs, then call watch_pipeline again"`
	Updates       []string        `json:"updates" jsonschema:"status changes seen while watching, oldest first, with local time"`
	Pipeline      *pipelineView   `json:"pipeline,omitempty" jsonschema:"last complete pipeline status; omitted if the first poll did not finish before timeout"`
	Logs          []watchLogChunk `json:"logs,omitempty" jsonschema:"recent untrusted log chunks; also sent as JSON in progress messages while streaming"`
	LogsTruncated bool            `json:"logs_truncated,omitempty" jsonschema:"earlier log output was omitted from a bounded read, notification or final result; use get_pipeline_log or the step URL for more"`
}

// watchPipeline follows a pipeline until it completes, pauses or the timeout passes. Each poll
// is reported as a progress notification when the client asked for progress, which also keeps
// clients that reset their timeout on progress from giving up on the call.
func (t *tools) watchPipeline(ctx context.Context, req *mcp.CallToolRequest, in watchPipelineInput) (*mcp.CallToolResult, watchPipelineResult, error) {
	if err := in.validate(); err != nil {
		return nil, watchPipelineResult{}, err
	}
	token := req.Params.GetProgressToken()
	if in.StreamLogs && (token == nil || req.Session == nil) {
		return nil, watchPipelineResult{}, errors.New("stream_logs requires the client to supply a progress token")
	}
	if in.Step != "" && !in.StreamLogs {
		return nil, watchPipelineResult{}, errors.New("step requires stream_logs=true")
	}
	// Checked before the conversion to a duration, which would overflow for huge values.
	seconds, err := optionalCount("timeout_seconds", in.TimeoutSeconds, defaultWatchSeconds, maxWatchSeconds)
	if err != nil {
		return nil, watchPipelineResult{}, err
	}
	timeout := time.Duration(seconds) * time.Second
	t.mu.Lock()
	err = t.checkPollingSlot()
	if err == nil {
		t.watchCalls++
	}
	t.mu.Unlock()
	if err != nil {
		return nil, watchPipelineResult{}, err
	}
	defer func() {
		t.mu.Lock()
		t.watchCalls--
		t.mu.Unlock()
	}()

	watchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := watchPipelineResult{Updates: []string{}}
	var stream *watchLogStream
	if in.StreamLogs {
		stream = &watchLogStream{steps: map[string]*watchLogState{}, result: &result}
	}
	progress := 0
	notify := func(message string) error {
		progress++
		return req.Session.NotifyProgress(watchCtx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: float64(progress), Message: message})
	}
	var prev pipelineView
	for poll := 1; ; poll++ {
		var p bitbucket.Pipeline
		var view pipelineView
		if p, view, err = t.pipelineStatus(watchCtx, in.Repo, in.Pipeline); err != nil {
			if ctx.Err() != nil {
				return nil, watchPipelineResult{}, ctx.Err()
			}
			if watchCtx.Err() != nil {
				result.Outcome = "timeout"
				if err = stream.finish(); err != nil {
					return nil, watchPipelineResult{}, err
				}
				return nil, result, nil
			}
			return nil, watchPipelineResult{}, err
		}
		if poll > 1 {
			now := time.Now().Format("15:04:05")
			for _, c := range changes(prev, view) {
				result.Updates = append(result.Updates, now+" "+c)
			}
		}
		prev = view
		result.Pipeline = &view
		switch {
		case p.State.Completed():
			result.Outcome = "completed"
		case p.State.Paused():
			result.Outcome = "paused"
		}
		if token != nil {
			err = notify(summary(view))
			if err != nil {
				t.log.WarnContext(ctx, "sending pipeline progress", "error", err)
			}
		}
		logReads := 0
		if stream != nil {
			if err == nil {
				logReads, err = t.watchLogs(watchCtx, in, p, view, stream, notify)
			}
			if err != nil {
				if ctx.Err() != nil {
					return nil, watchPipelineResult{}, ctx.Err()
				}
				if watchCtx.Err() == nil {
					return nil, watchPipelineResult{}, err
				}
				if result.Outcome == "" {
					result.Outcome = "timeout"
				}
				if err = stream.finish(); err != nil {
					return nil, watchPipelineResult{}, err
				}
				return nil, result, nil
			}
		}

		if result.Outcome != "" {
			return nil, result, nil
		}
		heartbeat := time.Duration(0)
		if stream != nil {
			heartbeat = t.pollInterval
		}
		// Log reads slow API polling without slowing the progress heartbeat.
		err = waitForWatchPoll(watchCtx, t.pollInterval*time.Duration(2+logReads)/2, heartbeat,
			func() error { return notify(summary(view)) })
		if err != nil {
			if ctx.Err() != nil {
				return nil, watchPipelineResult{}, ctx.Err()
			}
			if watchCtx.Err() == nil {
				return nil, watchPipelineResult{}, err
			}
			result.Outcome = "timeout"
			if err = stream.finish(); err != nil {
				return nil, watchPipelineResult{}, err
			}
			return nil, result, nil
		}
	}
}

func waitForWatchPoll(ctx context.Context, delay, heartbeat time.Duration, notify func() error) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	var ticks <-chan time.Time
	if heartbeat > 0 && heartbeat < delay {
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case <-ticks:
			if err := notify(); err != nil {
				return err
			}
		}
	}
}

// checkPollingSlot fails when maxPolling pipelines are already polled; t.mu must be held.
func (t *tools) checkPollingSlot() error {
	if len(t.watches)+t.watchCalls >= maxPolling {
		return fmt.Errorf("%d pipelines are already polled for their status, by subscriptions or watch_pipeline calls, "+
			"which is as many as Bitbucket's rate limit allows; wait for one to finish, unsubscribe, or use get_pipeline", maxPolling)
	}
	return nil
}

// changes lists what differs between two looks at the same pipeline.
func changes(prev, cur pipelineView) []string {
	var out []string
	before := map[string]string{}
	for _, s := range prev.Steps {
		before[s.UUID] = s.State
	}
	for _, s := range cur.Steps {
		if before[s.UUID] != s.State {
			out = append(out, fmt.Sprintf("step %s: %s", stepName(s), s.State))
		}
	}
	if prev.State != cur.State {
		out = append(out, fmt.Sprintf("pipeline #%d: %s", cur.BuildNumber, cur.State))
	}
	return out
}

// summary is a one-line status: the pipeline and the step it is at.
func summary(v pipelineView) string {
	s := fmt.Sprintf("pipeline #%d: %s", v.BuildNumber, v.State)
	for i, st := range v.Steps {
		if !strings.HasPrefix(st.State, "COMPLETED") {
			return s + fmt.Sprintf("; step %d of %d, %s: %s", i+1, len(v.Steps), stepName(st), st.State)
		}
	}
	return s
}
