package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

const (
	pipelineURIPrefix   = "bitbucket://pipelines/"
	pipelineURITemplate = pipelineURIPrefix + "{repo}/{build_number}"
	// watchLifetime bounds the polling of a pipeline that runs for too long.
	watchLifetime   = 24 * time.Hour
	maxPollFailures = 5
	// Polling refused over Bitbucket's rate limit rests as long as the response says, but at
	// least a poll interval. Without a word on how long, it rests firstRateLimitPause poll
	// intervals (a minute), then twice the last rest, up to maxRateLimitPause (a quarter of an
	// hour): requests leave Bitbucket's rolling hour one by one.
	firstRateLimitPause = 2
	maxRateLimitPause   = 30
)

// pipelineWatch polls one subscribed pipeline for the sessions that subscribed to it.
type pipelineWatch struct {
	cancel   context.CancelFunc
	sessions map[*mcp.ServerSession]bool
}

func pipelineURI(repo string, buildNumber int) string {
	return pipelineURIPrefix + repo + "/" + strconv.Itoa(buildNumber)
}

func parsePipelineURI(uri string) (repo, build string, ok bool) {
	rest, ok := strings.CutPrefix(uri, pipelineURIPrefix)
	if !ok {
		return "", "", false
	}
	repo, build, ok = strings.Cut(rest, "/")
	if !ok || repo == "" || build == "" || strings.Trim(build, "0123456789") != "" {
		return "", "", false
	}
	return repo, build, true
}

// lookPipeline reads the pipeline a resource URI names.
func (t *tools) lookPipeline(ctx context.Context, uri string) (bitbucket.Pipeline, pipelineView, error) {
	repo, build, ok := parsePipelineURI(uri)
	if !ok {
		return bitbucket.Pipeline{}, pipelineView{}, mcp.ResourceNotFoundError(uri)
	}
	if err := t.policy.check("get_pipeline", pipelineRef{Repo: repo, Pipeline: build}); err != nil {
		return bitbucket.Pipeline{}, pipelineView{}, err
	}
	p, view, err := t.pipelineStatus(ctx, repo, build)
	if bitbucket.IsStatus(err, http.StatusNotFound) {
		return p, view, mcp.ResourceNotFoundError(uri)
	}
	return p, view, err
}

func (t *tools) readPipeline(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	_, view, err := t.lookPipeline(ctx, req.Params.URI)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
		{URI: req.Params.URI, MIMEType: "application/json", Text: string(b)},
	}}, nil
}

// subscribe starts polling a pipeline for a session; the SDK then routes the resulting
// resources/updated notifications to every subscribed session.
func (t *tools) subscribe(ctx context.Context, req *mcp.SubscribeRequest) error {
	uri := req.Params.URI
	done, snapshot, err := t.subscriptionSnapshot(ctx, uri)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if w := t.watches[uri]; w != nil {
		w.sessions[req.Session] = true
		return nil
	}
	if done {
		return nil
	}
	if err = t.checkPollingSlot(); err != nil {
		return err
	}
	pollCtx, cancel := context.WithTimeout(t.ctx, t.watchLifetime)
	w := &pipelineWatch{cancel: cancel, sessions: map[*mcp.ServerSession]bool{req.Session: true}}
	t.watches[uri] = w
	go t.poll(pollCtx, uri, w, snapshot)
	return nil
}

func (t *tools) unsubscribe(_ context.Context, req *mcp.UnsubscribeRequest) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	w := t.watches[req.Params.URI]
	if w == nil {
		return nil
	}
	delete(w.sessions, req.Session)
	if len(w.sessions) == 0 {
		w.cancel()
		delete(t.watches, req.Params.URI)
	}
	return nil
}

// settled means the pipeline does not change by itself: it completed, or it waits for someone
// to run a manual step. Polling it would only spend the rate limit.
func settled(s bitbucket.State) bool { return s.Completed() || s.Paused() }

// poll tells subscribers about every change of the pipeline until it settles, everybody
// unsubscribes, it keeps failing or its lifetime passes. Refusals over Bitbucket's rate limit are
// not failures: polling rests until the limit lets it go on.
func (t *tools) poll(ctx context.Context, uri string, w *pipelineWatch, prev string) {
	defer func() {
		w.cancel()
		t.mu.Lock()
		if t.watches[uri] == w {
			delete(t.watches, uri)
		}
		t.mu.Unlock()
	}()
	failures := 0
	var pause time.Duration // the last rest for the rate limit, until a poll succeeds
	wait := t.pollInterval
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = t.pollInterval
		done, snapshot, err := t.subscriptionSnapshot(ctx, uri)
		if said, limited := bitbucket.RateLimited(err); limited {
			pause = t.rateLimitPause(said, pause)
			wait = pause
			t.log.WarnContext(ctx, "polling a subscribed pipeline rests for Bitbucket's rate limit", "uri", uri, "pause", pause, "error", err)
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			t.log.WarnContext(ctx, "polling a subscribed pipeline", "uri", uri, "failures", failures, "error", err)
			if failures >= maxPollFailures {
				// Subscribers would wait for updates that never come. Reading the pipeline once
				// more shows them the failure while it lasts; subscribing anew polls again.
				if err = t.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: uri}); err != nil {
					t.log.WarnContext(ctx, "notifying subscribers that polling stopped", "uri", uri, "error", err)
				}
				return
			}
			continue
		}
		failures, pause = 0, 0
		if prev != snapshot {
			if err = t.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: uri}); err != nil {
				t.log.WarnContext(ctx, "notifying subscribers of a pipeline", "uri", uri, "error", err)
			}
		}
		prev = snapshot
		if done {
			return
		}
	}
}

// rateLimitPause is how long polling rests after Bitbucket refused a poll over its rate limit,
// saying to wait for said, or not saying when it is zero; last is the rest before, if the poll
// before was refused too.
func (t *tools) rateLimitPause(said, last time.Duration) time.Duration {
	if said > 0 {
		return max(said, t.pollInterval)
	}
	return min(max(2*last, firstRateLimitPause*t.pollInterval), maxRateLimitPause*t.pollInterval)
}
