package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

func (f *fakeBitbucket) schedulesOf(repo string) []bitbucket.Schedule {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.schedules[repo])
}

func TestAddSchedule(t *testing.T) {
	for pipeline, want := range map[string]bitbucket.Selector{
		"custom: nightly":     {Type: "custom", Pattern: "nightly"},
		"branches:release/*":  {Type: "branches", Pattern: "release/*"},
		" default ":           {Type: "default"},
		"custom:   db backup": {Type: "custom", Pattern: "db backup"},
	} {
		t.Run(pipeline, func(t *testing.T) {
			f := newFake()
			out, isErr := call(t, connectApproving(t, f), "add_schedule",
				map[string]any{"repo": "repo-a", "branch": "main", "pipeline": pipeline, "cron": " 0 0 2  * * ? * "})
			if isErr || !strings.Contains(out, `"created"`) {
				t.Fatalf("result = %s (error %v)", out, isErr)
			}
			got := f.schedulesOf("repo-a")
			if len(got) != 1 || !got[0].Enabled || got[0].CronPattern != "0 0 2 * * ? *" || got[0].Target.RefType != "branch" ||
				got[0].Target.RefName != "main" || got[0].Target.Selector == nil || *got[0].Target.Selector != want {
				t.Fatalf("schedules = %+v", got)
			}
		})
	}
}

func TestAddScheduleKeepsExisting(t *testing.T) {
	f := newFake()
	cs := connectApproving(t, f)
	args := map[string]any{"repo": "repo-a", "branch": "main", "pipeline": "custom: nightly", "cron": "0 0 2 * * ? *"}
	mustCall(t, cs, "add_schedule", args)
	if out := mustCall(t, cs, "add_schedule", args); !strings.Contains(out, `"already_exists"`) {
		t.Fatalf("second add = %s", out)
	}
	args["cron"] = "0 0 3 * * ? *"
	mustCall(t, cs, "add_schedule", args)
	if got := f.schedulesOf("repo-a"); len(got) != 2 {
		t.Fatalf("schedules = %+v", got)
	}
}

func TestAddScheduleValidatesInput(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(map[string]any)
		want string
	}{
		"empty branch":       {func(a map[string]any) { a["branch"] = "" }, "repo and branch are required"},
		"tag pipeline":       {func(a map[string]any) { a["pipeline"] = "tags: v*" }, "pipeline must be"},
		"bare name":          {func(a map[string]any) { a["pipeline"] = "nightly" }, "pipeline must be"},
		"empty custom":       {func(a map[string]any) { a["pipeline"] = "custom:" }, "pipeline must be"},
		"unix cron":          {func(a map[string]any) { a["cron"] = "0 2 * * *" }, "cron must have 7 fields"},
		"pipelines disabled": {func(a map[string]any) { a["repo"] = "repo-c" }, "not enabled in repo-c"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			args := map[string]any{"repo": "repo-a", "branch": "main", "pipeline": "custom: nightly", "cron": "0 0 2 * * ? *"}
			tc.edit(args)
			out, isErr := call(t, connectApproving(t, f), "add_schedule", args)
			if !isErr || !strings.Contains(out, tc.want) {
				t.Fatalf("result = %s (error %v), want %q", out, isErr, tc.want)
			}
			if m := f.mutationList(); len(m) != 0 {
				t.Fatalf("mutations = %v", m)
			}
		})
	}
}

func TestListAndDeleteSchedules(t *testing.T) {
	f := newFake()
	target := func(pattern string) bitbucket.PipelineTarget {
		return bitbucket.PipelineTarget{Type: "pipeline_ref_target", RefType: "branch", RefName: "main", Selector: &bitbucket.Selector{Type: "custom", Pattern: pattern}}
	}
	f.schedules["repo-a"] = []bitbucket.Schedule{
		{UUID: "{sch-1}", Enabled: true, Target: target("nightly"), CronPattern: "0 0 2 * * ? *", CreatedOn: "2026-10-01T08:00:00Z", UpdatedOn: "2026-10-02T08:00:00Z"},
		{UUID: "{sch-2}", Enabled: false, Target: target("weekly"), CronPattern: "0 0 4 ? * SUN *"},
	}
	cs := connectApproving(t, f)
	res := decodeResult[listSchedulesResult](t, mustCall(t, cs, "list_schedules", map[string]any{"repo": "repo-a"}))
	if len(res.Schedules) != 2 || res.Schedules[0].Pipeline != "custom: nightly" || res.Schedules[0].Branch != "main" ||
		res.Schedules[0].CreatedOn != "2026-10-01T08:00:00Z" || res.Schedules[0].UpdatedOn != "2026-10-02T08:00:00Z" ||
		res.Schedules[1].Enabled || res.Schedules[1].Cron != "0 0 4 ? * SUN *" {
		t.Fatalf("schedules = %+v", res.Schedules)
	}

	// The UUID may lose its braces on the way.
	out := mustCall(t, cs, "delete_schedule", map[string]any{"repo": "repo-a", "schedule": "sch-2"})
	if !strings.Contains(out, `"deleted"`) || !strings.Contains(out, "custom: weekly") {
		t.Fatalf("result = %s", out)
	}
	if got := f.schedulesOf("repo-a"); len(got) != 1 || got[0].UUID != "{sch-1}" {
		t.Fatalf("schedules = %+v", got)
	}
	if out, isErr := call(t, cs, "delete_schedule", map[string]any{"repo": "repo-a", "schedule": "{sch-2}"}); !isErr || !strings.Contains(out, "not found") {
		t.Fatalf("deleting a missing schedule = %s (error %v)", out, isErr)
	}
	res = decodeResult[listSchedulesResult](t, mustCall(t, cs, "list_schedules", map[string]any{"repo": "repo-b"}))
	if res.Schedules == nil || len(res.Schedules) != 0 {
		t.Fatalf("schedules of a repository without any = %+v", res.Schedules)
	}
}

// Schedules take the definitions a pipeline can be run by: a custom or branch pipeline by name or
// pattern, or the default one, written the way results show them.
func TestParseDefinition(t *testing.T) {
	for s, want := range map[string]*bitbucket.Selector{
		"custom: nightly":  {Type: "custom", Pattern: "nightly"},
		" branches : main": {Type: "branches", Pattern: "main"},
		"default":          {Type: "default"},
		" default ":        {Type: "default"},
	} {
		if got, err := parseDefinition(s); err != nil || *got != *want {
			t.Errorf("parseDefinition(%q) = %+v, %v; want %+v", s, got, err, want)
		}
	}
	for _, s := range []string{"custom", "custom:", "branches", "tags: v*", "pull-requests: **", "default:", "default: x", "nightly", ""} {
		if got, err := parseDefinition(s); err == nil {
			t.Errorf("parseDefinition(%q) = %+v, want an error", s, got)
		}
	}
}
