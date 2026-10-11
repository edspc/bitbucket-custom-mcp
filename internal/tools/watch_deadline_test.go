package tools

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWatchDeadlineCancelsSlowAPIRequests(t *testing.T) {
	for _, phase := range []string{"first pipeline", "first steps", "later pipeline", "client cancellation"} {
		t.Run(phase, func(t *testing.T) {
			f := newFake()
			twoSteps(f)
			newTestServer(t, f)
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			upstream := f.handler(t)
			polls := 0
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pipeline := r.URL.Path == "/2.0/repositories/ws/repo-a/pipelines/1"
				if pipeline {
					polls++
				}
				stall := pipeline && (phase == "first pipeline" || phase == "client cancellation" || phase == "later pipeline" && polls == 2) ||
					phase == "first steps" && r.URL.Path == "/2.0/repositories/ws/repo-a/pipelines/{00000000-0000-4000-8000-000000000101}/steps/"
				if stall {
					if _, ok := r.Context().Deadline(); !ok {
						t.Error("API request has no watch deadline")
					}
					if phase == "client cancellation" {
						time.AfterFunc(20*time.Millisecond, cancel)
					}
					select {
					case <-r.Context().Done():
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					case <-time.After(2 * time.Second):
					}
				}
				upstream.ServeHTTP(w, r)
			})
			f.tools.bb = bitbucket.New(&http.Client{Transport: handlerTransport{h}}, bitbucket.Config{Workspace: "ws"})
			start := time.Now()
			_, out, err := f.tools.watchPipeline(parent, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}},
				watchPipelineInput{pipelineRef: pipelineRef{Repo: "repo-a", Pipeline: "1"}, TimeoutSeconds: 1})
			if phase == "client cancellation" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("client cancellation became a timeout: %v", err)
				}
			} else if err != nil || out.Outcome != "timeout" || (out.Pipeline != nil) != (phase == "later pipeline") {
				t.Fatalf("timeout result=%+v, err=%v", out, err)
			}
			if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
				t.Fatalf("one-second watch took %v", elapsed)
			}
			if f.tools.watchCalls != 0 {
				t.Fatal("timed-out watch kept its polling slot")
			}
			if phase == "later pipeline" && out.Pipeline.BuildNumber != 1 {
				t.Fatal("timeout lost the last complete status")
			}
		})
	}
}
