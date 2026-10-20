package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

const (
	watchLogReadBytes  = 64 << 10
	watchLogEventBytes = 32 << 10
	// Bitbucket pipelines have at most 100 steps; keep the per-watch state bounded too.
	maxWatchLogSteps = 100
)

type watchLogChunk struct {
	ContentTrust string   `json:"content_trust"`
	BuildNumber  int      `json:"build_number"`
	Step         stepView `json:"step"`
	Log          string   `json:"log"`
	Truncated    bool     `json:"truncated,omitempty"`
	Unavailable  bool     `json:"unavailable,omitempty"`
	URL          string   `json:"url"`
}

type watchLogState struct {
	end     int64
	seen    bool
	done    bool
	pending string
	chunk   watchLogChunk
}

type watchLogStream struct {
	steps  map[string]*watchLogState
	result *watchPipelineResult
	sizes  []int
	bytes  int
}

func (t *tools) watchLogs(ctx context.Context, in watchPipelineInput, p bitbucket.Pipeline, view pipelineView, stream *watchLogStream, notify func(string) error) (int, error) {
	steps := view.Steps
	if in.Step != "" {
		i, err := pickStep(view, in.Step)
		if err != nil {
			return 0, err
		}
		steps = steps[i : i+1]
	}
	final := p.State.Completed() || p.State.Paused()
	reads := 0
	for _, step := range steps {
		if step.StartedOn == "" && !strings.HasPrefix(step.State, "IN_PROGRESS") &&
			(!strings.HasPrefix(step.State, "COMPLETED") || step.State == "COMPLETED/NOT_RUN") {
			continue
		}
		state := stream.steps[step.UUID]
		if state == nil {
			if len(stream.steps) >= maxWatchLogSteps {
				return reads, errors.New("stream_logs supports at most 100 started steps; select a step to stream")
			}
			state = &watchLogState{}
			stream.steps[step.UUID] = state
		}
		if state.done {
			continue
		}
		state.chunk = watchLogChunk{ContentTrust: "untrusted", BuildNumber: p.BuildNumber, Step: step,
			URL: t.bb.StepURL(in.Repo, p.BuildNumber, step.UUID)}
		reads++
		raw, start, err := t.bb.StepLogTail(ctx, in.Repo, p.UUID, step.UUID, watchLogReadBytes)
		if bitbucket.IsStatus(err, http.StatusNotFound) {
			if final || strings.HasPrefix(step.State, "COMPLETED") {
				state.chunk.Unavailable = true
				state.done = true
				if err = stream.flush(state, true, notify); err != nil {
					return reads, err
				}
			}
			continue // A running step may not have a log yet.
		}
		if err != nil {
			return reads, err
		}
		state.add(raw, start)
		state.done = final || strings.HasPrefix(step.State, "COMPLETED")
		if err = stream.flush(state, state.done, notify); err != nil {
			return reads, err
		}
	}
	return reads, nil
}

func (s *watchLogState) add(raw []byte, start int64) {
	end := start + int64(len(raw))
	if !s.seen || start > s.end || end < s.end {
		s.chunk.Truncated = start > 0 || s.seen
		s.pending = ""
		// The suffix may begin halfway through a line, UTF-8 rune or terminal escape.
		if start > 0 {
			if i := strings.IndexByte(string(raw), '\n'); i >= 0 {
				raw = raw[i+1:]
			} else {
				raw = nil
			}
		}
	} else {
		raw = raw[s.end-start:]
	}
	s.pending += string(raw)
	s.end, s.seen = end, true
	if len(s.pending) > watchLogReadBytes {
		// An unterminated line cannot grow the watcher without bound.
		s.pending = s.pending[len(s.pending)-watchLogReadBytes:]
		s.chunk.Truncated = true
	}
}

// flush waits for whole lines so split UTF-8 characters and escape sequences are
// sanitized together. An ending watch also returns its last unterminated line.
func (s *watchLogStream) flush(state *watchLogState, final bool, notify func(string) error) error {
	n := strings.LastIndexByte(state.pending, '\n') + 1
	if final || len(state.pending) >= watchLogReadBytes {
		n = len(state.pending)
	}
	text := state.pending[:n]
	state.pending = state.pending[n:]
	state.chunk.Log = strings.ToValidUTF8(plainText(text), "\uFFFD")
	newline := strings.HasSuffix(state.chunk.Log, "\n")
	if newline {
		state.chunk.Log = strings.TrimSuffix(state.chunk.Log, "\n")
	}
	var cut bool
	state.chunk.Log, cut = tailWithin(state.chunk.Log, watchLogEventBytes-2)
	if newline {
		state.chunk.Log += "\n"
	}
	state.chunk.Truncated = state.chunk.Truncated || cut
	if state.chunk.Log == "" && !state.chunk.Truncated && !state.chunk.Unavailable {
		return nil
	}
	data, err := json.Marshal(state.chunk)
	if err != nil {
		return err
	}
	size := len(data) + 1 // include the comma between chunks in the result array
	if size > maxLogBytes-2 {
		return fmt.Errorf("step metadata exceeds the log stream size limit")
	}
	for s.bytes+size > maxLogBytes-2 {
		s.bytes -= s.sizes[0]
		s.sizes = s.sizes[1:]
		s.result.Logs = s.result.Logs[1:]
		s.result.LogsTruncated = true
	}
	s.result.Logs = append(s.result.Logs, state.chunk)
	s.sizes = append(s.sizes, size)
	s.bytes += size
	s.result.LogsTruncated = s.result.LogsTruncated || state.chunk.Truncated
	if notify != nil {
		return notify(string(data))
	}
	return nil
}

func (s *watchLogStream) finish() error {
	if s == nil {
		return nil
	}
	for _, state := range s.steps {
		if state.pending != "" {
			state.chunk.Truncated, state.chunk.Unavailable = false, false
			if err := s.flush(state, true, nil); err != nil {
				return err
			}
		}
	}
	return nil
}
