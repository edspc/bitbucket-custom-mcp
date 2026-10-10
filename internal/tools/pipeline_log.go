package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

const (
	defaultLogLines = 200
	maxLogLines     = 2000
	// maxLogBytes is what is read from the end of a log; it holds maxLogLines of usual length.
	maxLogBytes = 256 << 10
)

type pipelineLogInput struct {
	pipelineRef
	Step      string `json:"step,omitempty" jsonschema:"step name or UUID; when omitted, the first failed step, or else the last step that started"`
	TailLines int    `json:"tail_lines,omitempty" jsonschema:"how many last lines of the log to return; 200 when omitted, at most 2000"`
}

type pipelineLogResult struct {
	BuildNumber int        `json:"build_number"`
	Step        stepView   `json:"step"`
	Log         string     `json:"log"`
	Truncated   bool       `json:"truncated" jsonschema:"earlier lines were left out; the whole log is at url"`
	URL         string     `json:"url" jsonschema:"page of the step on bitbucket.org"`
	Steps       []stepView `json:"steps" jsonschema:"all steps of the pipeline, to read another one"`
}

func (t *tools) pipelineLog(ctx context.Context, _ *mcp.CallToolRequest, in pipelineLogInput) (*mcp.CallToolResult, pipelineLogResult, error) {
	if err := in.validate(); err != nil {
		return nil, pipelineLogResult{}, err
	}
	lines, err := optionalCount("tail_lines", in.TailLines, defaultLogLines, maxLogLines)
	if err != nil {
		return nil, pipelineLogResult{}, err
	}
	p, view, err := t.pipelineStatus(ctx, in.Repo, in.Pipeline)
	if err != nil {
		return nil, pipelineLogResult{}, err
	}
	i, err := pickStep(view, in.Step)
	if err != nil {
		return nil, pipelineLogResult{}, err
	}
	step := view.Steps[i]
	raw, truncated, err := t.bb.StepLog(ctx, in.Repo, p.UUID, step.UUID, maxLogBytes)
	if bitbucket.IsStatus(err, http.StatusNotFound) {
		return nil, pipelineLogResult{}, fmt.Errorf("the log of step %s in pipeline #%d is not available yet", stepName(step), p.BuildNumber)
	}
	if err != nil {
		return nil, pipelineLogResult{}, err
	}
	text, cut := lastLines(string(raw), truncated, lines)
	return nil, pipelineLogResult{
		BuildNumber: p.BuildNumber,
		Step:        step,
		Log:         text,
		Truncated:   truncated || cut,
		URL:         t.bb.StepURL(in.Repo, p.BuildNumber, step.UUID),
		Steps:       view.Steps,
	}, nil
}

// pickStep finds the step to read: the one asked for, or else the first failed step, or else
// the last step that started.
func pickStep(v pipelineView, want string) (int, error) {
	names := make([]string, 0, len(v.Steps))
	for i, s := range v.Steps {
		if want != "" && (s.UUID == want || s.Name == want) {
			return i, nil
		}
		names = append(names, stepName(s))
	}
	if want != "" {
		return 0, fmt.Errorf("step %q not found in pipeline #%d; its steps: %s", want, v.BuildNumber, strings.Join(names, ", "))
	}
	for i, s := range v.Steps {
		if s.State == "COMPLETED/FAILED" || s.State == "COMPLETED/ERROR" {
			return i, nil
		}
	}
	for i := len(v.Steps) - 1; i >= 0; i-- {
		if v.Steps[i].StartedOn != "" {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no step of pipeline #%d has started yet", v.BuildNumber)
}

// lastLines keeps the last n lines of a log. When the log was cut at the front, its first line
// is partial and dropped.
func lastLines(text string, cutAtFront bool, n int) (string, bool) {
	text = strings.TrimRight(text, "\n")
	if cutAtFront {
		_, text, _ = strings.Cut(text, "\n")
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= n {
		return text, false
	}
	return strings.Join(lines[len(lines)-n:], "\n"), true
}
