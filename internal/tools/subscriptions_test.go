package tools

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

// subscribe subscribes to uri; under subscriptions/listen the SDK does not wait for the
// server, so errors show only under resources/subscribe.
func subscribe(t *testing.T, cs *mcp.ClientSession, uri string) error {
	t.Helper()
	return cs.Subscribe(context.Background(), &mcp.SubscribeParams{URI: uri})
}

func (f *fakeBitbucket) watchCount() int {
	f.tools.mu.Lock()
	defer f.tools.mu.Unlock()
	return len(f.tools.watches)
}

func (f *fakeBitbucket) gets() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pipelineGet
}

// notifiedOptions are client options whose session puts the URI of every resources/updated
// notification it gets on the returned channel.
func notifiedOptions() (*mcp.ClientOptions, chan string) {
	updated := make(chan string, 10)
	opts := clientOptions(nil)
	opts.ResourceUpdatedHandler = func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
		updated <- req.Params.URI
	}
	return opts, updated
}

// expectUpdate waits for a resources/updated notification of uri.
func expectUpdate(t *testing.T, updated chan string, uri string) {
	t.Helper()
	select {
	case got := <-updated:
		if got != uri {
			t.Fatalf("updated %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no resources/updated notification")
	}
}

func TestPipelineSubscriptionNotifiesChanges(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, version string) {
		f := newFake()
		p := twoSteps(f)
		opts, updated := notifiedOptions()
		cs := connectWith(t, f, opts, version)
		const uri = "bitbucket://pipelines/repo-a/1"
		if err := subscribe(t, cs, uri); err != nil {
			t.Fatal(err)
		}
		eventually(t, func() bool { return f.watchCount() == 1 })
		// Polls that find nothing new notify nobody.
		expectQuiet := func() {
			t.Helper()
			polled := f.gets()
			eventually(t, func() bool { return f.gets() >= polled+3 })
			select {
			case got := <-updated:
				t.Fatalf("updated %s though nothing changed", got)
			default:
			}
		}
		expectQuiet()

		f.mu.Lock()
		f.setState(p, running(), ptr(completed("SUCCESSFUL")), &bitbucket.State{Name: "IN_PROGRESS"})
		f.mu.Unlock()
		expectUpdate(t, updated, uri)
		// Later polls compare with the new state, so the change is reported once.
		expectQuiet()
		f.mu.Lock()
		f.setState(p, completed("SUCCESSFUL"), nil, ptr(completed("SUCCESSFUL")))
		f.mu.Unlock()
		expectUpdate(t, updated, uri)

		// A completed pipeline never changes again, so polling stops.
		eventually(t, func() bool { return f.watchCount() == 0 })
		before := f.gets()
		time.Sleep(50 * time.Millisecond)
		if f.gets() != before {
			t.Fatal("polling continued after the pipeline completed")
		}
	})
}

func TestPipelineUnsubscribeStopsPolling(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, version string) {
		f := newFake()
		twoSteps(f)
		cs := connectWith(t, f, clientOptions(nil), version)
		const uri = "bitbucket://pipelines/repo-a/1"
		if err := subscribe(t, cs, uri); err != nil {
			t.Fatal(err)
		}
		eventually(t, func() bool { return f.watchCount() == 1 })
		if err := cs.Unsubscribe(context.Background(), &mcp.UnsubscribeParams{URI: uri}); err != nil {
			t.Fatal(err)
		}
		eventually(t, func() bool { return f.watchCount() == 0 })
	})
}

func TestPipelineSubscriptionRefusesUnknownPipelines(t *testing.T) {
	f := newFake()
	f.addRun("repo-a", completed("SUCCESSFUL"))
	cs := connectWith(t, f, clientOptions(nil), "2025-11-25")
	for _, uri := range []string{"bitbucket://pipelines/repo-a/2", "bitbucket://pipelines/repo-a/x", "https://example.com/1"} {
		if err := subscribe(t, cs, uri); err == nil {
			t.Errorf("subscribing to %s must fail", uri)
		}
	}
	// A completed pipeline is accepted, but there is nothing to poll.
	if err := subscribe(t, cs, "bitbucket://pipelines/repo-a/1"); err != nil {
		t.Fatal(err)
	}
	if f.watchCount() != 0 {
		t.Fatal("a completed pipeline must not be polled")
	}
}

// Subscriptions and watch_pipeline calls share the polling budget.
func TestPollingLimitIsShared(t *testing.T) {
	f := newFake()
	for range maxPolling + 1 {
		twoSteps(f)
	}
	f.addRun("repo-a", completed("SUCCESSFUL"))
	done := strconv.Itoa(maxPolling + 2)
	cs := connectWith(t, f, clientOptions(nil), "2025-11-25")
	for i := 1; i <= maxPolling+1; i++ {
		err := subscribe(t, cs, pipelineURI("repo-a", i))
		if (err != nil) != (i > maxPolling) {
			t.Fatalf("subscription %d: err = %v", i, err)
		}
	}
	if out, isErr := call(t, cs, "watch_pipeline", map[string]any{"repo": "repo-a", "pipeline": done}); !isErr || !strings.Contains(out, "already polled") {
		t.Fatalf("watch beyond the limit = %s (error %v)", out, isErr)
	}
	if err := cs.Unsubscribe(context.Background(), &mcp.UnsubscribeParams{URI: pipelineURI("repo-a", 1)}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return f.watchCount() == maxPolling-1 })
	// A finished watch gives its slot back.
	for range 2 {
		mustCall(t, cs, "watch_pipeline", map[string]any{"repo": "repo-a", "pipeline": done})
	}
	if err := subscribe(t, cs, pipelineURI("repo-a", 1)); err != nil {
		t.Fatalf("subscribing within the limit again: %v", err)
	}
}

func TestPipelineSubscriptionStopsWhenPaused(t *testing.T) {
	f := newFake()
	p := twoSteps(f)
	cs := connectWith(t, f, clientOptions(nil), "2025-11-25")
	if err := subscribe(t, cs, pipelineURI("repo-a", 1)); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return f.watchCount() == 1 })
	f.mu.Lock()
	f.setState(p, bitbucket.State{Name: "IN_PROGRESS", Stage: &bitbucket.StateDetail{Name: "PAUSED"}})
	f.mu.Unlock()
	eventually(t, func() bool { return f.watchCount() == 0 })
}

// Polling that keeps failing stops, and tells the subscribers once more, so they read the
// pipeline and see the failure instead of waiting for updates; subscribing anew polls again.
func TestPipelineSubscriptionStopsAfterFailures(t *testing.T) {
	f := newFake()
	twoSteps(f)
	opts, updated := notifiedOptions()
	cs := connectWith(t, f, opts, "2025-11-25")
	uri := pipelineURI("repo-a", 1)
	if err := subscribe(t, cs, uri); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return f.watchCount() == 1 })
	f.mu.Lock()
	f.pipelineGetStatus = http.StatusInternalServerError
	failedFrom := f.pipelineGet
	f.mu.Unlock()
	eventually(t, func() bool { return f.watchCount() == 0 })
	if polls := f.gets() - failedFrom; polls != maxPollFailures {
		t.Fatalf("polled %d times after the failures began, want %d", polls, maxPollFailures)
	}
	expectUpdate(t, updated, uri)
	_, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("reading the pipeline: err = %v, want the failure", err)
	}
	f.mu.Lock()
	f.pipelineGetStatus = 0
	f.mu.Unlock()
	if err = cs.Unsubscribe(context.Background(), &mcp.UnsubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	if err = subscribe(t, cs, uri); err != nil {
		t.Fatal(err)
	}
	if f.watchCount() != 1 {
		t.Fatal("subscribing anew did not restart polling")
	}
}

// Refusals over the rate limit are not failures: polling rests and goes on, however many come.
func TestPipelineSubscriptionRestsOverRateLimit(t *testing.T) {
	f := newFake()
	p := twoSteps(f)
	opts, updated := notifiedOptions()
	cs := connectWith(t, f, opts, "2025-11-25")
	// Rests are counted in poll intervals; short ones keep the test fast.
	f.tools.pollInterval = time.Millisecond
	uri := pipelineURI("repo-a", 1)
	if err := subscribe(t, cs, uri); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return f.watchCount() == 1 })
	f.mu.Lock()
	f.pipelineGetStatus = http.StatusTooManyRequests
	limitedFrom := f.pipelineGet
	f.mu.Unlock()
	eventually(t, func() bool { return f.gets() >= limitedFrom+2*maxPollFailures })
	if f.watchCount() != 1 {
		t.Fatal("polling stopped over the rate limit")
	}
	select {
	case got := <-updated:
		t.Fatalf("updated %s while polling rested", got)
	default:
	}
	f.mu.Lock()
	f.pipelineGetStatus = 0
	f.setState(p, running(), ptr(completed("SUCCESSFUL")))
	f.mu.Unlock()
	expectUpdate(t, updated, uri)
}

// Polling rests as long as Bitbucket says rather than its own guess.
func TestPipelineSubscriptionRestsAsBitbucketSays(t *testing.T) {
	f := newFake()
	twoSteps(f)
	cs := connectWith(t, f, clientOptions(nil), "2025-11-25")
	if err := subscribe(t, cs, pipelineURI("repo-a", 1)); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return f.watchCount() == 1 })
	var limitedAt, nextAt time.Time
	f.mu.Lock()
	f.onPipelineGet = func(int) {
		switch {
		case limitedAt.IsZero():
			limitedAt = time.Now()
			f.pipelineGetStatus, f.pipelineGetHeader = http.StatusTooManyRequests, http.Header{"Retry-After": {"1"}}
		case nextAt.IsZero():
			nextAt = time.Now()
			f.pipelineGetStatus = 0
		}
	}
	f.mu.Unlock()
	eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return !nextAt.IsZero()
	})
	// Without the header polling would rest for two poll intervals, 20ms here.
	if rest := nextAt.Sub(limitedAt); rest < time.Second {
		t.Fatalf("polled again after %v, want the second Bitbucket asked for", rest)
	}
	if f.watchCount() != 1 {
		t.Fatal("polling stopped over the rate limit")
	}
}

// Rests for the rate limit start short again once a poll gets through.
func TestPipelineSubscriptionRateLimitRestsStartOver(t *testing.T) {
	f := newFake()
	twoSteps(f)
	cs := connectWith(t, f, clientOptions(nil), "2025-11-25")
	f.tools.pollInterval = 5 * time.Millisecond
	if err := subscribe(t, cs, pipelineURI("repo-a", 1)); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return f.watchCount() == 1 })
	const refused = 6 // enough to reach the longest rest, 150ms here
	var polls []time.Time
	f.mu.Lock()
	f.onPipelineGet = func(int) {
		polls = append(polls, time.Now())
		f.pipelineGetStatus = http.StatusTooManyRequests
		if len(polls) == refused+1 {
			f.pipelineGetStatus = 0
		}
	}
	f.mu.Unlock()
	eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(polls) >= refused+4
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	// Refused again after the poll that got through, polling rests 10ms, then 20ms.
	if rests := polls[refused+3].Sub(polls[refused+1]); rests > 150*time.Millisecond {
		t.Fatalf("the first two rests after a poll got through took %v, want about 30ms", rests)
	}
}

func TestRateLimitPause(t *testing.T) {
	tl := &tools{pollInterval: 30 * time.Second}
	for _, tc := range []struct {
		said, last, want time.Duration
	}{
		{0, 0, time.Minute},
		{0, time.Minute, 2 * time.Minute},
		{0, 8 * time.Minute, 15 * time.Minute},
		{0, 15 * time.Minute, 15 * time.Minute},
		{0, 48 * time.Minute, 15 * time.Minute},
		{4 * time.Minute, 0, 4 * time.Minute},
		{4 * time.Minute, 15 * time.Minute, 4 * time.Minute},
		{48 * time.Minute, 0, 48 * time.Minute},
		{2 * time.Second, 0, 30 * time.Second},
	} {
		if got := tl.rateLimitPause(tc.said, tc.last); got != tc.want {
			t.Errorf("rateLimitPause(%v, %v) = %v, want %v", tc.said, tc.last, got, tc.want)
		}
	}
}

// Failures between successful polls do not add up to the limit.
func TestPipelineSubscriptionToleratesSporadicFailures(t *testing.T) {
	f := newFake()
	twoSteps(f)
	cs := connectWith(t, f, clientOptions(nil), "2025-11-25")
	if err := subscribe(t, cs, pipelineURI("repo-a", 1)); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return f.watchCount() == 1 })
	f.mu.Lock()
	f.onPipelineGet = func(n int) {
		f.pipelineGetStatus = 0
		if n%2 == 0 {
			f.pipelineGetStatus = http.StatusInternalServerError
		}
	}
	start := f.pipelineGet
	f.mu.Unlock()
	eventually(t, func() bool { return f.gets() >= start+4*maxPollFailures })
	if f.watchCount() != 1 {
		t.Fatal("polling stopped although no failures came in a row")
	}
}

// Sessions subscribed to one pipeline share its poller: one session leaving must not stop the
// updates of the other, and polling stops once both have left.
func TestPipelineSubscriptionOfTwoSessions(t *testing.T) {
	f := newFake()
	p := twoSteps(f)
	s := newTestServer(t, f)
	opts, updated := notifiedOptions()
	first := connectClient(t, s, clientOptions(nil), "2025-11-25")
	second := connectClient(t, s, opts, "2025-11-25")
	uri := pipelineURI("repo-a", 1)
	for _, cs := range []*mcp.ClientSession{first, second} {
		if err := subscribe(t, cs, uri); err != nil {
			t.Fatal(err)
		}
	}
	if err := first.Unsubscribe(context.Background(), &mcp.UnsubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	if f.watchCount() != 1 {
		t.Fatal("the pipeline must stay polled for the other session")
	}
	f.mu.Lock()
	f.setState(p, running(), ptr(completed("SUCCESSFUL")))
	f.mu.Unlock()
	// The remaining session is still told.
	expectUpdate(t, updated, uri)

	if err := second.Unsubscribe(context.Background(), &mcp.UnsubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	if f.watchCount() != 0 {
		t.Fatal("the pipeline must not be polled once every session left")
	}
	// A poll already under way may still finish; after that nothing polls the pipeline.
	time.Sleep(20 * time.Millisecond)
	before := f.gets()
	time.Sleep(100 * time.Millisecond)
	if f.gets() != before {
		t.Fatal("the pipeline is still polled after every session left")
	}
}

// Polling a pipeline that never settles ends with the watch's lifetime.
func TestPipelineSubscriptionLifetime(t *testing.T) {
	f := newFake()
	twoSteps(f)
	cs := connectWith(t, f, clientOptions(nil), "2025-11-25")
	f.tools.watchLifetime = 100 * time.Millisecond
	if err := subscribe(t, cs, pipelineURI("repo-a", 1)); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return f.watchCount() == 0 })
}

func TestReadPipelineResource(t *testing.T) {
	f := newFake()
	twoSteps(f)
	cs := connect(t, f, nil)
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "bitbucket://pipelines/repo-a/1"})
	if err != nil {
		t.Fatal(err)
	}
	if v := decodeResult[pipelineView](t, res.Contents[0].Text); v.BuildNumber != 1 || len(v.Steps) != 2 || res.Contents[0].MIMEType != "application/json" {
		t.Fatalf("resource = %+v", res.Contents[0])
	}
	_, err = cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "bitbucket://pipelines/repo-a/7"})
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams || rpcErr.Message != "Resource not found" {
		t.Fatalf("reading an unknown pipeline: err = %v, want resource not found", err)
	}
	templates, err := cs.ListResourceTemplates(context.Background(), nil)
	if err != nil || len(templates.ResourceTemplates) != 1 || templates.ResourceTemplates[0].URITemplate != pipelineURITemplate {
		t.Fatalf("templates = %+v, %v", templates, err)
	}
	if caps := cs.InitializeResult().Capabilities; caps.Resources == nil || !caps.Resources.Subscribe {
		t.Fatalf("resource capabilities = %+v", caps.Resources)
	}
}

// A poller that ends after its pipeline was subscribed again must not drop the newer watch:
// unsubscribe could not stop that one, and it would poll outside the budget.
func TestEndedPollerKeepsNewerWatch(t *testing.T) {
	const uri = "bitbucket://pipelines/repo-a/1"
	newer := &pipelineWatch{cancel: func() {}, sessions: map[*mcp.ServerSession]bool{}}
	tl := &tools{log: slog.New(slog.DiscardHandler), pollInterval: time.Hour, watches: map[string]*pipelineWatch{uri: newer}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tl.poll(ctx, uri, &pipelineWatch{cancel: cancel}, pipelineView{})
	if tl.watches[uri] != newer {
		t.Fatal("the ended poller removed the watch that replaced it")
	}
}

func TestParsePipelineURI(t *testing.T) {
	if repo, build, ok := parsePipelineURI("bitbucket://pipelines/repo-a/42"); !ok || repo != "repo-a" || build != "42" {
		t.Fatalf("parsePipelineURI = %q, %q, %v", repo, build, ok)
	}
	for _, uri := range []string{"bitbucket://pipelines//42", "bitbucket://pipelines/repo-a/", "bitbucket://pipelines/repo-a/4x", "bitbucket://pipelines/repo-a", "file:///repo-a/42"} {
		if _, _, ok := parsePipelineURI(uri); ok {
			t.Errorf("%s must not parse", uri)
		}
	}
}
