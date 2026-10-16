package tools

import (
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

func (f *fakeBitbucket) runBodyList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runBodies)
}

func TestRunPipelineOnBranch(t *testing.T) {
	f := newFake()
	out, isErr := call(t, connectApproving(t, f), "run_pipeline", map[string]any{"repo": "repo-a", "branch": "main"})
	if isErr {
		t.Fatalf("result = %s", out)
	}
	res := decodeResult[runPipelineResult](t, out)
	p := res.Pipeline
	if res.Status != "started" || p == nil || p.BuildNumber != 1 || p.State != "PENDING" || p.Ref != "branch main" ||
		p.URL != "https://bitbucket.org/ws/repo-a/pipelines/results/1" || p.ResourceURI != "bitbucket://pipelines/repo-a/1" {
		t.Fatalf("result = %+v %+v", res, p)
	}
	want := `{"target":{"type":"pipeline_ref_target","ref_type":"branch","ref_name":"main","commit":{"type":"commit","hash":"` + testPipelineCommit + `"}}}`
	if got := f.runBodyList(); len(got) != 1 || got[0] != want {
		t.Fatalf("run requests = %v", got)
	}
}

func TestRunCustomPipelineOnTagWithVariables(t *testing.T) {
	f := newFake()
	out, isErr := call(t, connectApproving(t, f), "run_pipeline", map[string]any{
		"repo": "repo-a", "tag": "v1.2.0", "commit": "abc123", "custom": "deploy",
		"variables": []map[string]string{{"name": "ENV", "value": "prod"}},
	})
	if isErr || !strings.Contains(out, `"definition":"custom: deploy"`) {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	want := `{"target":{"type":"pipeline_ref_target","ref_type":"tag","ref_name":"v1.2.0","commit":{"type":"commit","hash":"abc1230000000000000000000000000000000000"},` +
		`"selector":{"type":"custom","pattern":"deploy"}},"variables":[{"key":"ENV","value":"prod","secured":false}]}`
	if got := f.runBodyList(); len(got) != 1 || got[0] != want {
		t.Fatalf("run requests = %v\nwant %s", got, want)
	}
}

func TestRunCustomPipelineWithSecretVariables(t *testing.T) {
	f := newFake()
	u := &fakeUser{pages: f.pages, action: "accept", values: url.Values{"DEPLOY_TOKEN": {"t0k3n"}, "DB_PASS": {"p4ss"}}}
	cs := connect(t, f, u.elicit)
	out, isErr := call(t, cs, "run_pipeline", map[string]any{
		"repo": "repo-a", "branch": "main", "custom": "deploy",
		"variables":        []map[string]string{{"name": "ENV", "value": "prod"}},
		"secret_variables": []string{"DEPLOY_TOKEN", "DB_PASS"},
	})
	if isErr || !strings.Contains(out, `"started"`) || strings.Contains(out, "t0k3n") || strings.Contains(out, "p4ss") {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	want := `"variables":[{"key":"ENV","value":"prod","secured":false},{"key":"DEPLOY_TOKEN","value":"t0k3n","secured":true},{"key":"DB_PASS","value":"p4ss","secured":true}]`
	if got := f.runBodyList(); len(got) != 1 || !strings.Contains(got[0], want) {
		t.Fatalf("run requests = %v", got)
	}
	asked := u.asked()
	if len(asked) != 1 {
		t.Fatalf("elicitation requests = %+v", asked)
	}
	body := f.pages.openedPage()
	for _, wantText := range []string{"run_pipeline", `name="DEPLOY_TOKEN"`, `name="DB_PASS"`, "the custom pipeline &#34;deploy&#34; of &#34;repo-a&#34; on branch &#34;main&#34;"} {
		if !strings.Contains(body, wantText) {
			t.Errorf("page does not contain %q", wantText)
		}
	}
	if strings.Contains(body, `name="ENV"`) {
		t.Error("plain variables must not be asked for on the page")
	}
	expectPagesClosed(t, f, 1)
}

func TestRunPipelineSecretsDeclined(t *testing.T) {
	f := newFake()
	u := &fakeUser{pages: f.pages, action: "cancel"}
	out, isErr := call(t, connect(t, f, u.elicit), "run_pipeline",
		map[string]any{"repo": "repo-a", "branch": "main", "custom": "deploy", "secret_variables": []string{"TOKEN"}})
	if isErr || !strings.Contains(out, `"cancelled"`) {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if got := f.runBodyList(); len(got) != 0 {
		t.Fatalf("run requests = %v", got)
	}
	expectPagesClosed(t, f, 1)
}

func TestRunPipelineValidatesInput(t *testing.T) {
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"no ref":                {map[string]any{"repo": "repo-a"}, "exactly one of branch and tag"},
		"branch and tag":        {map[string]any{"repo": "repo-a", "branch": "main", "tag": "v1"}, "exactly one of branch and tag"},
		"variables, no custom":  {map[string]any{"repo": "repo-a", "branch": "main", "variables": []map[string]string{{"name": "A", "value": "1"}}}, "custom pipelines only"},
		"secrets, no custom":    {map[string]any{"repo": "repo-a", "branch": "main", "secret_variables": []string{"A"}}, "custom pipelines only"},
		"duplicate variable":    {map[string]any{"repo": "repo-a", "branch": "main", "custom": "c", "variables": []map[string]string{{"name": "A", "value": "1"}}, "secret_variables": []string{"A"}}, "more than once"},
		"bad variable name":     {map[string]any{"repo": "repo-a", "branch": "main", "custom": "c", "secret_variables": []string{"A-B"}}, "only letters, digits and underscores"},
		"pipelines not enabled": {map[string]any{"repo": "repo-c", "branch": "main"}, "not enabled in repo-c"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			u := &fakeUser{pages: f.pages, action: "accept"}
			out, isErr := call(t, connect(t, f, u.elicit), "run_pipeline", tc.args)
			if !isErr || !strings.Contains(out, tc.want) {
				t.Fatalf("result = %s (error %v), want %q", out, isErr, tc.want)
			}
			if len(f.runBodyList()) != 0 || len(u.asked()) != 0 {
				t.Fatal("nothing may be started or asked for")
			}
		})
	}
}

func TestListPipelinesNewestFirst(t *testing.T) {
	f := newFake()
	f.addRun("repo-a", completed("SUCCESSFUL"))
	f.addRun("repo-a", running())
	f.mu.Lock()
	f.addRunLocked("repo-a", completed("FAILED"), bitbucket.PipelineTarget{Type: "pipeline_ref_target", RefType: "branch", RefName: "dev"}, nil)
	f.mu.Unlock()
	cs := connect(t, f, nil)

	res := decodeResult[listPipelinesResult](t, mustCall(t, cs, "list_pipelines", map[string]any{"repo": "repo-a", "limit": 2}))
	if len(res.Pipelines) != 2 || res.Pipelines[0].BuildNumber != 3 || res.Pipelines[1].BuildNumber != 2 || res.Pipelines[0].State != "COMPLETED/FAILED" {
		t.Fatalf("pipelines = %+v", res.Pipelines)
	}
	res = decodeResult[listPipelinesResult](t, mustCall(t, cs, "list_pipelines", map[string]any{"repo": "repo-a", "branch": "main"}))
	if len(res.Pipelines) != 2 || res.Pipelines[0].BuildNumber != 2 {
		t.Fatalf("pipelines on main = %+v", res.Pipelines)
	}
	f.mu.Lock()
	queries := f.listQueries
	f.mu.Unlock()
	if len(queries) != 2 || queries[0] != "page=1&pagelen=2&sort=-created_on" || queries[1] != "page=1&pagelen=10&sort=-created_on&target.branch=main" {
		t.Fatalf("queries = %v", queries)
	}
	for _, limit := range []int{51, -1} {
		if out, isErr := call(t, cs, "list_pipelines", map[string]any{"repo": "repo-a", "limit": limit}); !isErr || !strings.Contains(out, "between 1 and 50") {
			t.Fatalf("limit %d = %s (error %v)", limit, out, isErr)
		}
	}
	if out, isErr := call(t, cs, "list_pipelines", map[string]any{"repo": "repo-a", "page": -1}); !isErr || !strings.Contains(out, "page must be 1 or more") {
		t.Fatalf("page -1 = %s (error %v)", out, isErr)
	}
}

func TestListPipelinesFilters(t *testing.T) {
	const commit = "ABCDEF0123456789abcdef0123456789abcdef01"
	f := newFake()
	f.mu.Lock()
	for _, run := range []struct {
		refType, ref, section, pattern, trigger, commit string
	}{
		{"branch", "main", "branches", "main", "PUSH", ""},                        // 1
		{"branch", "main", "custom", "deploy", "MANUAL", strings.ToLower(commit)}, // 2
		{"tag", "v1", "tags", "v*", "PUSH", ""},                                   // 3
		{"branch", "dev", "custom", "nightly", "SCHEDULED", ""},                   // 4
		{"branch", "dev", "default", "", "PUSH", ""},                              // 5
		{"annotated_tag", "v2", "tags", "v*", "MANUAL", ""},                       // 6
	} {
		target := bitbucket.RefTarget(run.refType, run.ref, run.commit)
		target.Selector = &bitbucket.Selector{Type: run.section, Pattern: run.pattern}
		p := f.addRunLocked("repo-a", completed("SUCCESSFUL"), target, nil)
		p.Trigger.Name = run.trigger
	}
	f.mu.Unlock()
	cs := connect(t, f, nil)

	for name, tc := range map[string]struct {
		args  map[string]any
		want  []int
		query string
	}{
		"custom section":     {map[string]any{"definition": "custom"}, []int{4, 2}, "target.selector.type=CUSTOM"},
		"custom by name":     {map[string]any{"definition": " custom : deploy "}, []int{2}, "target.selector.pattern=deploy&target.selector.type=CUSTOM"},
		"branch definitions": {map[string]any{"definition": "branches: main"}, []int{1}, "target.selector.pattern=main&target.selector.type=BRANCH"},
		"pull requests":      {map[string]any{"definition": "pull-requests"}, nil, "target.selector.type=PULLREQUESTS"},
		"default":            {map[string]any{"definition": "default"}, []int{5}, "target.selector.type=DEFAULT"},
		"scheduled":          {map[string]any{"trigger": "scheduled"}, []int{4}, "trigger_type=SCHEDULED"},
		"manual":             {map[string]any{"trigger": "manual"}, []int{6, 2}, "trigger_type=MANUAL"},
		"tags":               {map[string]any{"ref_type": "tag"}, []int{3}, "target.ref_type=TAG"},
		"annotated tags":     {map[string]any{"ref_type": "annotated_tag"}, []int{6}, "target.ref_type=ANNOTATED_TAG"},
		"tag definitions":    {map[string]any{"definition": "tags"}, []int{6, 3}, "target.selector.type=TAG"},
		"commit":             {map[string]any{"commit": commit}, []int{2}, "target.commit.hash=" + strings.ToLower(commit)},
		"combined":           {map[string]any{"branch": "dev", "ref_type": "branch", "trigger": "push"}, []int{5}, "target.branch=dev&target.ref_type=BRANCH&trigger_type=PUSH"},
	} {
		t.Run(name, func(t *testing.T) {
			f.mu.Lock()
			f.listQueries = nil
			f.mu.Unlock()
			tc.args["repo"] = "repo-a"
			res := decodeResult[listPipelinesResult](t, mustCall(t, cs, "list_pipelines", tc.args))
			var got []int
			for _, p := range res.Pipelines {
				got = append(got, p.BuildNumber)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("pipelines = %v, want %v", got, tc.want)
			}
			f.mu.Lock()
			queries := slices.Clone(f.listQueries)
			f.mu.Unlock()
			if want := "page=1&pagelen=10&sort=-created_on&" + tc.query; len(queries) != 1 || !sameQuery(queries[0], want) {
				t.Fatalf("query = %q, want %q", queries, want)
			}
		})
	}
	res := decodeResult[listPipelinesResult](t, mustCall(t, cs, "list_pipelines", map[string]any{"repo": "repo-a", "definition": "custom: nightly"}))
	if len(res.Pipelines) != 1 || res.Pipelines[0].Trigger != "scheduled" || res.Pipelines[0].Definition != "custom: nightly" {
		t.Fatalf("pipeline = %+v", res.Pipelines)
	}
}

// sameQuery compares URL queries regardless of the order of their parameters.
func sameQuery(a, b string) bool {
	qa, errA := url.ParseQuery(a)
	qb, errB := url.ParseQuery(b)
	return errA == nil && errB == nil && reflect.DeepEqual(qa, qb)
}

func TestListPipelinesRefusesBadFilters(t *testing.T) {
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"unknown ref type":      {map[string]any{"ref_type": "commit"}, "ref_type must be branch, tag or annotated_tag"},
		"colon without a name":  {map[string]any{"definition": "custom:"}, "definition must be"},
		"short commit":          {map[string]any{"commit": "abcdef0"}, "full 40-character hash"},
		"not a commit":          {map[string]any{"commit": strings.Repeat("z", 40)}, "full 40-character hash"},
		"unknown section":       {map[string]any{"definition": "bookmarks"}, "definition must be"},
		"section in upper case": {map[string]any{"definition": "CUSTOM"}, "definition must be"},
		"default with a name":   {map[string]any{"definition": "default: x"}, "definition must be"},
		"unknown trigger":       {map[string]any{"trigger": "schedule"}, "trigger must be push, manual or scheduled"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			tc.args["repo"] = "repo-a"
			if out, isErr := call(t, connect(t, f, nil), "list_pipelines", tc.args); !isErr || !strings.Contains(out, tc.want) {
				t.Fatalf("result = %s (error %v), want %q", out, isErr, tc.want)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.listQueries) != 0 {
				t.Fatalf("listed although the filter was refused: %q", f.listQueries)
			}
		})
	}
}

func TestListPipelinesPages(t *testing.T) {
	f := newFake()
	for range 5 {
		f.addRun("repo-a", completed("SUCCESSFUL"))
	}
	cs := connect(t, f, nil)

	var builds []int
	args := map[string]any{"repo": "repo-a", "limit": 2}
	for range 3 {
		out := mustCall(t, cs, "list_pipelines", args)
		res := decodeResult[listPipelinesResult](t, out)
		if res.Total == nil || *res.Total != 5 {
			t.Fatalf("total = %v in %s", res.Total, out)
		}
		for _, p := range res.Pipelines {
			builds = append(builds, p.BuildNumber)
		}
		if res.NextPage == 0 {
			if strings.Contains(out, "next_page") {
				t.Fatalf("the last page names a next one: %s", out)
			}
			break
		}
		args["page"] = res.NextPage
	}
	if !slices.Equal(builds, []int{5, 4, 3, 2, 1}) {
		t.Fatalf("builds = %v, want every pipeline once, newest first", builds)
	}
	f.mu.Lock()
	queries := slices.Clone(f.listQueries)
	f.mu.Unlock()
	if want := []string{"page=1&pagelen=2&sort=-created_on", "page=2&pagelen=2&sort=-created_on", "page=3&pagelen=2&sort=-created_on"}; !slices.Equal(queries, want) {
		t.Fatalf("queries = %q, want %q", queries, want)
	}
}

func TestGetPipelineWithSteps(t *testing.T) {
	f := newFake()
	failed := completed("ERROR")
	failed.Result.Error = &struct {
		Message string `json:"message"`
	}{Message: "Container 'docker' exceeded memory limit."}
	p := f.addRun("repo-a", failed,
		bitbucket.Step{UUID: "{s1}", Name: "Build", State: completed("SUCCESSFUL"), StartedOn: "t1", CompletedOn: "t2"},
		bitbucket.Step{UUID: "{s2}", Name: "Test", State: failed, StartedOn: "t2", CompletedOn: "t3"},
	)
	p.Creator.DisplayName = "Dev One"
	p.CompletedOn = "2026-10-10T10:05:00Z"
	p.Target.Commit = &bitbucket.Commit{Type: "commit", Hash: "abc123"}
	cs := connect(t, f, nil)
	// A UUID copied without its braces reaches the pipeline too; Bitbucket refuses it bare.
	for _, id := range []string{"1", p.UUID, strings.Trim(p.UUID, "{}")} {
		v := decodeResult[pipelineView](t, mustCall(t, cs, "get_pipeline", map[string]any{"repo": "repo-a", "pipeline": id}))
		if v.BuildNumber != 1 || !v.Finished || v.State != "COMPLETED/ERROR" || v.Error != "Container 'docker' exceeded memory limit." || len(v.Steps) != 2 ||
			v.Steps[1].State != "COMPLETED/ERROR" || v.Steps[1].Error != "Container 'docker' exceeded memory limit." {
			t.Fatalf("pipeline %s = %+v", id, v)
		}
		if v.Creator != "Dev One" || v.CreatedOn != "2026-10-10T10:00:00Z" || v.CompletedOn != "2026-10-10T10:05:00Z" ||
			v.Commit != "abc123" || v.Ref != "branch main" || v.Steps[0].StartedOn != "t1" || v.Steps[0].CompletedOn != "t2" {
			t.Fatalf("pipeline %s details = %+v", id, v)
		}
	}
	if out, isErr := call(t, cs, "get_pipeline", map[string]any{"repo": "repo-a", "pipeline": "99"}); !isErr || !strings.Contains(out, "404") {
		t.Fatalf("unknown pipeline = %s (error %v)", out, isErr)
	}
}

func TestToolsRefuseMissingFields(t *testing.T) {
	cron := "0 0 2 * * ? *"
	for name, tc := range map[string]struct {
		tool string
		args map[string]any
		want string
	}{
		"get_pipeline, no pipeline":          {"get_pipeline", map[string]any{"repo": "repo-a", "pipeline": ""}, "repo and pipeline are required"},
		"watch_pipeline, no repo":            {"watch_pipeline", map[string]any{"repo": "", "pipeline": "1"}, "repo and pipeline are required"},
		"get_pipeline_log, no pipeline":      {"get_pipeline_log", map[string]any{"repo": "repo-a", "pipeline": ""}, "repo and pipeline are required"},
		"list_pipelines, no repo":            {"list_pipelines", map[string]any{"repo": ""}, "repo is required"},
		"run_pipeline, no repo":              {"run_pipeline", map[string]any{"repo": "", "branch": "main"}, "repo is required"},
		"list_schedules, no repo":            {"list_schedules", map[string]any{"repo": ""}, "repo is required"},
		"add_schedule, no repo":              {"add_schedule", map[string]any{"repo": "", "branch": "main", "pipeline": "default", "cron": cron}, "repo and branch are required"},
		"delete_schedule, no repo":           {"delete_schedule", map[string]any{"repo": "", "schedule": "{sch-1}"}, "repo and schedule are required"},
		"delete_schedule, no schedule":       {"delete_schedule", map[string]any{"repo": "repo-a", "schedule": ""}, "repo and schedule are required"},
		"list_variables, unknown scope":      {"list_variables", map[string]any{"scope": "project"}, "scope must be"},
		"delete_variable, no name":           {"delete_variable", map[string]any{"scope": "workspace", "name": ""}, "name is required"},
		"delete_variable, workspace+repo":    {"delete_variable", map[string]any{"scope": "workspace", "repo": "repo-b", "name": "TOKEN"}, "not used with scope 'workspace'"},
		"delete_variable, repository+env":    {"delete_variable", map[string]any{"scope": "repository", "repo": "repo-b", "environment": "Production", "name": "TOKEN"}, "only used with scope 'deployment'"},
		"delete_variable, deployment no env": {"delete_variable", map[string]any{"scope": "deployment", "repo": "repo-b", "name": "TOKEN"}, "environment is required"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.addRun("repo-a", running())
			f.vars["workspace"] = []bitbucket.Variable{{UUID: "{v1}", Key: "TOKEN"}}
			f.vars["repo-b"] = []bitbucket.Variable{{UUID: "{v2}", Key: "TOKEN"}}
			f.schedules["repo-a"] = []bitbucket.Schedule{{UUID: "{sch-1}"}}
			out, isErr := call(t, connect(t, f, nil), tc.tool, tc.args)
			if !isErr || !strings.Contains(out, tc.want) {
				t.Fatalf("result = %s (error %v), want %q", out, isErr, tc.want)
			}
			if m := f.mutationList(); len(m) != 0 {
				t.Fatalf("mutations = %v", m)
			}
		})
	}
}

// Anything but a build number or a UUID never reaches Bitbucket, which would resolve a dot
// segment to another resource.
func TestPipelineRefMustBeNumberOrUUID(t *testing.T) {
	f := newFake()
	f.addRun("repo-a", running())
	cs := connect(t, f, nil)
	for _, id := range []string{"..", ".", "{}", "1/steps", "main"} {
		if out, isErr := call(t, cs, "get_pipeline", map[string]any{"repo": "repo-a", "pipeline": id}); !isErr || !strings.Contains(out, "build number") {
			t.Errorf("pipeline %q = %s (error %v)", id, out, isErr)
		}
	}
	if f.pipelineGet != 0 {
		t.Fatalf("%d requests reached Bitbucket", f.pipelineGet)
	}
}
