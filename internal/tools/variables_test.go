package tools

import (
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

func TestListVariablesHidesSecuredValues(t *testing.T) {
	f := newFake()
	f.vars["repo-b"] = []bitbucket.Variable{{UUID: "{v1}", Key: "PLAIN", Value: "visible"}, {UUID: "{v2}", Key: "SECRET", Value: "hidden", Secured: true}}
	got := decodeResult[listVariablesResult](t, mustCall(t, connect(t, f, nil), "list_variables", map[string]any{"scope": "repository", "repo": "repo-b"}))
	want := listVariablesResult{ContentTrust: "untrusted", Place: "repository variables of repo-b", Variables: []variableView{{Name: "PLAIN", Value: "visible"}, {Name: "SECRET", Secured: true}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result = %+v", got)
	}
}

func TestListVariablesOfWorkspaceAndEnvironment(t *testing.T) {
	f := newFake()
	f.vars["workspace"] = []bitbucket.Variable{{UUID: "{v1}", Key: "WS", Value: "1"}}
	f.vars["repo-b/{env-test}"] = []bitbucket.Variable{{UUID: "{v2}", Key: "ENV", Value: "2"}}
	cs := connect(t, f, nil)
	if out, isErr := call(t, cs, "list_variables", map[string]any{"scope": "workspace"}); isErr || !strings.Contains(out, `"name":"WS"`) {
		t.Fatalf("workspace result = %s (error %v)", out, isErr)
	}
	out, isErr := call(t, cs, "list_variables", map[string]any{"scope": "deployment", "repo": "repo-b", "environment": "Test"})
	if isErr || !strings.Contains(out, `"name":"ENV"`) || !strings.Contains(out, `deployment environment \"Test\" of repo-b`) {
		t.Fatalf("deployment result = %s (error %v)", out, isErr)
	}
	if out, isErr = call(t, cs, "list_variables", map[string]any{"scope": "deployment", "repo": "repo-b", "environment": "Staging"}); !isErr || !strings.Contains(out, `available: "Production", "Test"`) {
		t.Fatalf("unknown environment = %s (error %v)", out, isErr)
	}
}

func TestSetPlainVariable(t *testing.T) {
	f := newFake()
	cs := connectApproving(t, f)
	args := map[string]any{"scope": "repository", "repo": "repo-b", "name": "REGION", "value": "eu-west-1"}
	if out, isErr := call(t, cs, "set_variable", args); isErr || !strings.Contains(out, `"created"`) {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if out, isErr := call(t, cs, "set_variable", args); !isErr || !strings.Contains(out, "force=true") {
		t.Fatalf("existing variable without force = %s (error %v)", out, isErr)
	}
	args["value"], args["force"] = "us-east-1", true
	if out, isErr := call(t, cs, "set_variable", args); isErr || !strings.Contains(out, `"updated"`) {
		t.Fatalf("result with force = %s (error %v)", out, isErr)
	}
	want := []bitbucket.Variable{{UUID: "{var-101}", Key: "REGION", Value: "us-east-1"}}
	if got := f.varsOf("repo-b"); !slices.Equal(got, want) {
		t.Fatalf("variables = %+v", got)
	}
}

func TestSetSecuredVariableOnlyThroughThePage(t *testing.T) {
	f := newFake()
	u := &fakeUser{pages: f.pages, action: "accept", values: url.Values{"API_KEY": {" s3cr3t "}}}
	cs := connect(t, f, u.elicit)

	args := map[string]any{"scope": "workspace", "name": "API_KEY", "secured": true, "value": "s3cr3t"}
	if out, isErr := call(t, cs, "set_variable", args); !isErr || !strings.Contains(out, "must not be passed") {
		t.Fatalf("secured value in the call = %s (error %v)", out, isErr)
	}
	if len(u.asked()) != 0 || len(f.mutationList()) != 0 {
		t.Fatal("a secured value in the call must be refused before anything happens")
	}

	delete(args, "value")
	opened := time.Now()
	out, isErr := call(t, cs, "set_variable", args)
	if isErr || !strings.Contains(out, `"created"`) || strings.Contains(out, "s3cr3t") {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	want := []bitbucket.Variable{{UUID: "{var-101}", Key: "API_KEY", Value: "s3cr3t", Secured: true}}
	if got := f.varsOf("workspace"); !slices.Equal(got, want) {
		t.Fatalf("workspace variables = %+v", got)
	}
	asked := u.asked()
	if len(asked) != 1 || asked[0].Mode != "url" || !strings.Contains(asked[0].Message, "API_KEY") {
		t.Fatalf("elicitation requests = %+v", asked)
	}
	body := f.pages.openedPage()
	for _, wantText := range []string{"set_variable", "Enter the value of the secured variable API_KEY for workspace variables.", `name="API_KEY"`} {
		if !strings.Contains(body, wantText) {
			t.Errorf("page does not contain %q", wantText)
		}
	}
	// The page tells when it closes; the minute may turn while the call runs.
	if early, late := opened.Add(f.tools.secretWait).Format("15:04"), time.Now().Add(f.tools.secretWait).Format("15:04"); !strings.Contains(body, "open until "+early) && !strings.Contains(body, "open until "+late) {
		t.Errorf("page does not say it is open until %s", early)
	}
	if f.pages.stopCount() != 1 {
		t.Fatal("the secret page must be stopped")
	}
}

func TestSetSecuredVariableDeclined(t *testing.T) {
	f := newFake()
	u := &fakeUser{pages: f.pages, action: "decline"}
	out, isErr := call(t, connect(t, f, u.elicit), "set_variable",
		map[string]any{"scope": "deployment", "repo": "repo-b", "environment": "Production", "name": "DB_PASSWORD", "secured": true})
	if isErr || !strings.Contains(out, `"cancelled"`) {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if m := f.mutationList(); len(m) != 0 {
		t.Fatalf("mutations = %v", m)
	}
}

func TestSetVariableValidatesInput(t *testing.T) {
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"plain without value": {map[string]any{"scope": "repository", "repo": "repo-b", "name": "X"}, "value is required"},
		"bad name":            {map[string]any{"scope": "repository", "repo": "repo-b", "name": "1X", "value": "v"}, "must not start with a digit"},
		"workspace with repo": {map[string]any{"scope": "workspace", "repo": "repo-b", "name": "X", "value": "v"}, "not used with scope 'workspace'"},
		"repository no repo":  {map[string]any{"scope": "repository", "name": "X", "value": "v"}, "repo is required"},
		"deployment no env":   {map[string]any{"scope": "deployment", "repo": "repo-b", "name": "X", "value": "v"}, "environment is required"},
		"unknown scope":       {map[string]any{"scope": "project", "name": "X", "value": "v"}, "scope must be"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			out, isErr := call(t, connect(t, f, nil), "set_variable", tc.args)
			if !isErr || !strings.Contains(out, tc.want) {
				t.Fatalf("result = %s (error %v), want %q", out, isErr, tc.want)
			}
			if m := f.mutationList(); len(m) != 0 {
				t.Fatalf("mutations = %v", m)
			}
		})
	}
}

func TestDeleteVariable(t *testing.T) {
	f := newFake()
	f.vars["repo-b/{env-prod}"] = []bitbucket.Variable{{UUID: "{v1}", Key: "KEEP"}, {UUID: "{v2}", Key: "DROP", Secured: true}}
	cs := connectApproving(t, f)
	args := map[string]any{"scope": "deployment", "repo": "repo-b", "environment": "Production", "name": "DROP"}
	if out, isErr := call(t, cs, "delete_variable", args); isErr || !strings.Contains(out, `"deleted"`) {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if got := f.varsOf("repo-b/{env-prod}"); len(got) != 1 || got[0].Key != "KEEP" {
		t.Fatalf("variables = %+v", got)
	}
	if out, isErr := call(t, cs, "delete_variable", args); !isErr || !strings.Contains(out, "not found") {
		t.Fatalf("deleting a missing variable = %s (error %v)", out, isErr)
	}
}
