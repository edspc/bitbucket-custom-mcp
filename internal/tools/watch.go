package tools

import (
	"context"
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
	TimeoutSeconds int `json:"timeout_seconds,omitempty" jsonschema:"how long to wait for the pipeline to finish; 600 when omitted, at most 3600"`
}

type watchPipelineResult struct {
	Outcome  string        `json:"outcome" jsonschema:"why the watch ended: 'completed'; 'paused' when the pipeline waits for someone to run a manual step; or 'timeout' when it still runs, then call watch_pipeline again"`
	Updates  []string      `json:"updates" jsonschema:"status changes seen while watching, oldest first, with local time"`
	Pipeline *pipelineView `json:"pipeline,omitempty" jsonschema:"last complete pipeline status; omitted if the first poll did not finish before timeout"`
}

// watchPipeline follows a pipeline until it completes, pauses or the timeout passes. Each poll
// is reported as a progress notification when the client asked for progress, which also keeps
// clients that reset their timeout on progress from giving up on the call.
func (t *tools) watchPipeline(ctx context.Context, req *mcp.CallToolRequest, in watchPipelineInput) (*mcp.CallToolResult, watchPipelineResult, error) {
	if err := in.validate(); err != nil {
		return nil, watchPipelineResult{}, err
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
		if token := req.Params.GetProgressToken(); token != nil {
			err = req.Session.NotifyProgress(watchCtx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: float64(poll), Message: summary(view)})
			if err != nil {
				t.log.WarnContext(ctx, "sending pipeline progress", "error", err)
			}
		}

		switch {
		case p.State.Completed():
			result.Outcome = "completed"
		case p.State.Paused():
			result.Outcome = "paused"
		}
		if result.Outcome != "" {
			return nil, result, nil
		}
		select {
		case <-watchCtx.Done():
			if err = ctx.Err(); err != nil {
				return nil, watchPipelineResult{}, err
			}
			result.Outcome = "timeout"
			return nil, result, nil
		case <-time.After(t.pollInterval):
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
