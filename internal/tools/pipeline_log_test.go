package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

// failedRun adds a completed pipeline of repo-a whose second of three steps failed.
func failedRun(f *fakeBitbucket) {
	f.addRun("repo-a", completed("FAILED"),
		bitbucket.Step{UUID: "{s1}", Name: "Build", State: completed("SUCCESSFUL"), StartedOn: "t1"},
		bitbucket.Step{UUID: "{s2}", Name: "Test", State: completed("FAILED"), StartedOn: "t2"},
		bitbucket.Step{UUID: "{s3}", Name: "Deploy", State: completed("NOT_RUN")},
	)
	f.logs["{s1}"] = "build ok\n"
	f.logs["{s2}"] = "running tests\nFAIL: TestThing\n"
}

func TestPipelineLogOfFailedStep(t *testing.T) {
	for _, storage := range []bool{false, true} {
		t.Run(fmt.Sprintf("storage %v", storage), func(t *testing.T) {
			f := newFake()
			failedRun(f)
			f.logStorage = storage
			res := decodeResult[pipelineLogResult](t, mustCall(t, connect(t, f, nil), "get_pipeline_log", map[string]any{"repo": "repo-a", "pipeline": "1"}))
			if res.Step.Name != "Test" || res.Log != "running tests\nFAIL: TestThing" || res.Truncated || len(res.Steps) != 3 ||
				res.URL != "https://bitbucket.org/ws/repo-a/pipelines/results/1/steps/%7Bs2%7D" {
				t.Fatalf("result = %+v", res)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if want := fmt.Sprintf("bytes=-%d", maxLogBytes); len(f.logRanges) != 1 || f.logRanges[0] != want {
				t.Fatalf("ranges = %v", f.logRanges)
			}
		})
	}
}

func TestPipelineLogTail(t *testing.T) {
	f := newFake()
	failedRun(f)
	var log strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&log, "line %d\n", i)
	}
	f.logs["{s1}"] = log.String()
	cs := connect(t, f, nil)
	res := decodeResult[pipelineLogResult](t, mustCall(t, cs, "get_pipeline_log", map[string]any{"repo": "repo-a", "pipeline": "1", "step": "Build", "tail_lines": 3}))
	if res.Step.UUID != "{s1}" || res.Log != "line 48\nline 49\nline 50" || !res.Truncated {
		t.Fatalf("result = %+v", res)
	}
	for _, lines := range []int{2001, -1} {
		if out, isErr := call(t, cs, "get_pipeline_log", map[string]any{"repo": "repo-a", "pipeline": "1", "tail_lines": lines}); !isErr || !strings.Contains(out, "between 1 and 2000") {
			t.Fatalf("tail_lines %d = %s (error %v)", lines, out, isErr)
		}
	}
}

func TestPipelineLogPicksStep(t *testing.T) {
	for name, tc := range map[string]struct {
		steps []bitbucket.Step
		step  string
		want  string // UUID of the step, or the error
	}{
		"by UUID": {[]bitbucket.Step{{UUID: "{a}", Name: "A", StartedOn: "t"}, {UUID: "{b}", Name: "B", StartedOn: "t"}}, "{a}", "{a}"},
		"last started": {[]bitbucket.Step{
			{UUID: "{a}", Name: "A", State: completed("SUCCESSFUL"), StartedOn: "t"},
			{UUID: "{b}", Name: "B", State: bitbucket.State{Name: "IN_PROGRESS"}, StartedOn: "t"},
			{UUID: "{c}", Name: "C", State: bitbucket.State{Name: "PENDING"}},
		}, "", "{b}"},
		"error result": {[]bitbucket.Step{
			{UUID: "{a}", Name: "A", State: completed("ERROR"), StartedOn: "t"},
			{UUID: "{b}", Name: "B", State: completed("FAILED"), StartedOn: "t"},
		}, "", "{a}"},
		"unknown step":    {[]bitbucket.Step{{UUID: "{a}", Name: "A"}, {UUID: "{b}"}}, "Z", `step "Z" not found in pipeline #1; its steps: A, {b}`},
		"nothing started": {[]bitbucket.Step{{UUID: "{a}", Name: "A", State: bitbucket.State{Name: "PENDING"}}}, "", "no step of pipeline #1 has started yet"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.addRun("repo-a", running(), tc.steps...)
			for _, s := range tc.steps {
				f.logs[s.UUID] = "log of " + s.UUID
			}
			out, isErr := call(t, connect(t, f, nil), "get_pipeline_log", map[string]any{"repo": "repo-a", "pipeline": "1", "step": tc.step})
			if isErr {
				if !strings.Contains(out, tc.want) {
					t.Fatalf("error = %s, want %q", out, tc.want)
				}
				return
			}
			if res := decodeResult[pipelineLogResult](t, out); res.Step.UUID != tc.want || res.Log != "log of "+tc.want {
				t.Fatalf("result = %+v, want step %s", res, tc.want)
			}
		})
	}
}

func TestPipelineLogNotAvailable(t *testing.T) {
	f := newFake()
	f.addRun("repo-a", running(), bitbucket.Step{UUID: "{a}", Name: "Build", State: bitbucket.State{Name: "IN_PROGRESS"}, StartedOn: "t"})
	out, isErr := call(t, connect(t, f, nil), "get_pipeline_log", map[string]any{"repo": "repo-a", "pipeline": "1"})
	if !isErr || !strings.Contains(out, "log of step Build in pipeline #1 is not available yet") {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
}

func TestLastLines(t *testing.T) {
	for _, tc := range []struct {
		text        string
		cut         bool
		n           int
		want        string
		wantDropped bool
	}{
		{"a\nb\nc\n", false, 5, "a\nb\nc", false},
		{"a\nb\nc\n", false, 2, "b\nc", true},
		{"partial\nb\nc\n", true, 5, "b\nc", false},
		{"partial", true, 5, "", false},
		{"", false, 5, "", false},
	} {
		if got, dropped := lastLines(tc.text, tc.cut, tc.n); got != tc.want || dropped != tc.wantDropped {
			t.Errorf("lastLines(%q, %v, %d) = %q, %v", tc.text, tc.cut, tc.n, got, dropped)
		}
	}
}
