package tools

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

// fakeUser answers URL elicitation like a person: agrees to open the page and submits token
// there, or values for a page with other fields, unless both are empty.
type fakeUser struct {
	pages  *stubPages
	action string
	token  string
	values url.Values

	mu       sync.Mutex
	requests []*mcp.ElicitParams
	status   int
}

func (u *fakeUser) elicit(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.requests = append(u.requests, req.Params)
	values := u.values
	if u.token != "" {
		values = url.Values{tokenField: {u.token}}
	}
	if u.action == "accept" && values != nil {
		u.status = u.pages.submit(req.Params.URL, values)
	}
	return &mcp.ElicitResult{Action: u.action}, nil
}

func (u *fakeUser) asked() []*mcp.ElicitParams {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.requests)
}

// connectRetryingItself connects a client that leaves input requests to the test, which
// answers them by calling the tool again itself.
func connectRetryingItself(t *testing.T, f *fakeBitbucket) *mcp.ClientSession {
	t.Helper()
	opts := clientOptions((&fakeUser{action: "accept"}).elicit)
	opts.MultiRoundTrip = &mcp.MultiRoundTripOptions{Disabled: true}
	return connectWith(t, f, opts, "")
}

func tokenArgs(scope, env string, force bool) map[string]any {
	args := map[string]any{
		"source_repo": "repo-a", "target_repo": "repo-b", "variable_name": "REPO_A_TOKEN",
		"scope": scope, "permissions": []string{"repository", "pipeline"}, "force": force,
	}
	if env != "" {
		args["environment"] = env
	}
	return args
}

// The page request reaches the user both as an input request of the current protocol and as
// a direct elicitation request on the earlier version that has URL elicitation.
func TestLinkByTokenRepositoryScope(t *testing.T) {
	forEachProtocol(t, testRepositoryScope)
}

func testRepositoryScope(t *testing.T, version string) {
	f := newFake()
	u := &fakeUser{pages: f.pages, action: "accept", token: " repo-a-token\n"}
	cs := connectWith(t, f, clientOptions(u.elicit), version)
	if version != "" && cs.InitializeResult().ProtocolVersion != version {
		t.Fatalf("negotiated protocol %s, want %s", cs.InitializeResult().ProtocolVersion, version)
	}
	out, isErr := call(t, cs, "link_repos_by_token", tokenArgs("repository", "", false))
	if isErr || !strings.Contains(out, `"created"`) {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if strings.Contains(out, "repo-a-token") {
		t.Fatal("token leaked into the tool result")
	}
	want := []bitbucket.Variable{{UUID: "{var-101}", Key: "REPO_A_TOKEN", Value: "repo-a-token", Secured: true}}
	if got := f.varsOf("repo-b"); !slices.Equal(got, want) {
		t.Fatalf("variables = %+v", got)
	}
	asked := u.asked()
	if len(asked) != 1 || asked[0].Mode != "url" || !strings.HasPrefix(asked[0].URL, "http://"+stubPagesHost+"/") || asked[0].ElicitationID == "" {
		t.Fatalf("elicitation requests = %+v", asked)
	}
	if f.pages.stopCount() != 1 {
		t.Fatalf("token page stopped %d times, want 1", f.pages.stopCount())
	}
}

func TestLinkByTokenPageShowsInstructions(t *testing.T) {
	f := newFake()
	u := &fakeUser{pages: f.pages, action: "accept", token: "repo-a-token"}
	call(t, connect(t, f, u.elicit), "link_repos_by_token", tokenArgs("repository", "", false))
	asked := u.asked()
	if len(asked) != 1 {
		t.Fatalf("elicitation requests = %+v", asked)
	}
	body := f.pages.openedPage()
	for _, want := range []string{
		`href="https://bitbucket.org/ws/repo-a/admin/access-tokens"`, `<span class="chip">repository</span><span class="chip">pipeline</span>`,
		"repo-b-pipelines", "REPO_A_TOKEN", "link_repos_by_token", "Create a repository access token in repo-a",
		"checks read access only", "cannot verify the token type, repository-only scope or selected permissions",
		"Do not paste a workspace, project or user token",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not mention %q", want)
		}
	}
}

func TestLinkByTokenReportsReadAccessOnly(t *testing.T) {
	f := newFake()
	u := &fakeUser{pages: f.pages, action: "accept", token: f.validToken}
	args := tokenArgs("repository", "", false)
	args["permissions"] = []string{"repository:write"}
	out := mustCall(t, connect(t, f, u.elicit), "link_repos_by_token", args)
	got := decodeResult[linkByTokenResult](t, out)
	if got.TokenValidation != "read_access_only" || !strings.Contains(got.Message, "permissions were not verified") ||
		strings.Contains(out, f.validToken) {
		t.Fatalf("token validation was misrepresented: %s", out)
	}
}

func TestLinkByTokenDeploymentScope(t *testing.T) {
	f := newFake()
	u := &fakeUser{pages: f.pages, action: "accept", token: "repo-a-token"}
	out, isErr := call(t, connect(t, f, u.elicit), "link_repos_by_token", tokenArgs("deployment", "Production", false))
	if isErr || !strings.Contains(out, `"created"`) {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if got := f.varsOf("repo-b/{env-prod}"); len(got) != 1 || !got[0].Secured || got[0].Value != "repo-a-token" {
		t.Fatalf("deployment variables = %+v", got)
	}
	if got := f.varsOf("repo-b"); len(got) != 0 {
		t.Fatalf("repository variables = %+v", got)
	}
}

func TestLinkByTokenExistingVariable(t *testing.T) {
	f := newFake()
	f.vars["repo-b"] = []bitbucket.Variable{{UUID: "{var-1}", Key: "REPO_A_TOKEN", Value: "old", Secured: false}}
	u := &fakeUser{pages: f.pages, action: "accept", token: "repo-a-token"}
	cs := connect(t, f, u.elicit)

	out, isErr := call(t, cs, "link_repos_by_token", tokenArgs("repository", "", false))
	if !isErr || !strings.Contains(out, "force=true") {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if len(u.asked()) != 0 || len(f.mutationList()) != 0 || f.pages.served() {
		t.Fatal("the conflict must be reported before asking for the token or changing anything")
	}

	out, isErr = call(t, cs, "link_repos_by_token", tokenArgs("repository", "", true))
	if isErr || !strings.Contains(out, `"updated"`) {
		t.Fatalf("result with force = %s (error %v)", out, isErr)
	}
	want := []bitbucket.Variable{{UUID: "{var-1}", Key: "REPO_A_TOKEN", Value: "repo-a-token", Secured: true}}
	if got := f.varsOf("repo-b"); !slices.Equal(got, want) {
		t.Fatalf("variables = %+v", got)
	}
}

func TestLinkByTokenUnknownEnvironment(t *testing.T) {
	f := newFake()
	u := &fakeUser{pages: f.pages, action: "accept", token: "repo-a-token"}
	out, isErr := call(t, connect(t, f, u.elicit), "link_repos_by_token", tokenArgs("deployment", "Staging", false))
	if !isErr || !strings.Contains(out, `available: "Production", "Test"`) {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if len(u.asked()) != 0 {
		t.Fatal("must not ask for the token")
	}
}

func TestLinkByTokenUserBacksOut(t *testing.T) {
	for _, action := range []string{"decline", "cancel"} {
		t.Run(action, func(t *testing.T) {
			f := newFake()
			u := &fakeUser{pages: f.pages, action: action}
			out, isErr := call(t, connect(t, f, u.elicit), "link_repos_by_token", tokenArgs("repository", "", false))
			if isErr || !strings.Contains(out, `"cancelled"`) {
				t.Fatalf("result = %s (error %v)", out, isErr)
			}
			if m := f.mutationList(); len(m) != 0 {
				t.Fatalf("mutations = %v", m)
			}
			if f.pages.stopCount() != 1 {
				t.Fatal("the token page must be stopped")
			}
		})
	}
}

func TestLinkByTokenNoTokenSubmitted(t *testing.T) {
	f := newFake()
	u := &fakeUser{pages: f.pages, action: "accept"}
	cs := connect(t, f, u.elicit)
	f.tools.secretWait = 50 * time.Millisecond
	out, isErr := call(t, cs, "link_repos_by_token", tokenArgs("repository", "", false))
	if !isErr || !strings.Contains(out, "nothing was submitted") {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if len(f.mutationList()) != 0 || f.pages.stopCount() != 1 {
		t.Fatal("nothing may change and the token page must be stopped")
	}
}

func TestLinkByTokenNeedsURLElicitation(t *testing.T) {
	for name, opts := range map[string]*mcp.ClientOptions{
		"no elicitation": clientOptions(nil),
		"form only": {
			ElicitationHandler: (&fakeUser{action: "accept"}).elicit,
			Capabilities:       &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			out, isErr := call(t, connectWith(t, f, opts, ""), "link_repos_by_token", tokenArgs("repository", "", false))
			if !isErr || !strings.Contains(out, "URL elicitation") {
				t.Fatalf("result = %s (error %v)", out, isErr)
			}
			if f.pages.served() || len(f.mutationList()) != 0 {
				t.Fatal("must neither open the token page nor change anything")
			}
		})
	}
}

// A client that answers the input request itself must come back with the same arguments.
func TestLinkByTokenRetryMustMatchRequest(t *testing.T) {
	f := newFake()
	cs := connectRetryingItself(t, f)
	ctx := context.Background()

	first, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "link_repos_by_token", Arguments: tokenArgs("repository", "", false)})
	if err != nil || first.RequestState == "" || first.InputRequests[secretsInputID] == nil {
		t.Fatalf("first call = %+v, %v", first, err)
	}
	accept := mcp.InputResponseMap{secretsInputID: &mcp.ElicitResult{Action: "accept"}}

	other := tokenArgs("repository", "", false)
	other["variable_name"] = "OTHER"
	for state, want := range map[string]string{"unknown-state": "expired or is unknown", first.RequestState: "different arguments"} {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "link_repos_by_token", Arguments: other, InputResponses: accept, RequestState: state})
		if err != nil {
			t.Fatal(err)
		}
		if text := res.Content[0].(*mcp.TextContent).Text; !res.IsError || !strings.Contains(text, want) {
			t.Fatalf("retry with state %q = %s", state, text)
		}
	}
	if len(f.mutationList()) != 0 || f.pages.stopCount() != 1 {
		t.Fatal("nothing may change and the token page must be stopped")
	}
}

func TestLinkByTokenOfAnotherRepository(t *testing.T) {
	f := newFake()
	u := &fakeUser{pages: f.pages, action: "accept", token: "repo-c-token"}
	out, isErr := call(t, connect(t, f, u.elicit), "link_repos_by_token", tokenArgs("repository", "", false))
	if !isErr || !strings.Contains(out, "another repository") || strings.Contains(out, "repo-c-token") {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if m := f.mutationList(); len(m) != 0 {
		t.Fatalf("mutations = %v", m)
	}
}

// Without repository read permission the token is checked on what it may read instead.
func TestLinkByTokenWithoutRepositoryPermission(t *testing.T) {
	for token, wantCreated := range map[string]bool{"repo-a-token": true, "repo-c-token": false} {
		t.Run(token, func(t *testing.T) {
			f := newFake()
			u := &fakeUser{pages: f.pages, action: "accept", token: token}
			args := tokenArgs("repository", "", false)
			args["permissions"] = []string{"pipeline", "webhook"}
			out, isErr := call(t, connect(t, f, u.elicit), "link_repos_by_token", args)
			if created := !isErr && strings.Contains(out, `"created"`); created != wantCreated {
				t.Fatalf("result = %s (error %v), want created %v", out, isErr, wantCreated)
			}
		})
	}
}

// Pages that calls are already waiting on still count, and they close at their deadline.
func TestLinkByTokenLimitCountsWaitingPages(t *testing.T) {
	f := newFake()
	cs := connectRetryingItself(t, f)
	f.tools.secretWait = time.Second
	ctx := context.Background()
	callTool := func(p *mcp.CallToolParams) *mcp.CallToolResult {
		res, err := cs.CallTool(ctx, p)
		if err != nil {
			t.Error(err)
			return &mcp.CallToolResult{IsError: true}
		}
		return res
	}
	// Fresh params per call: the SDK writes request metadata into them.
	fresh := func() *mcp.CallToolParams {
		return &mcp.CallToolParams{Name: "link_repos_by_token", Arguments: tokenArgs("repository", "", false)}
	}

	var wg sync.WaitGroup
	for range maxPendingPages {
		state := callTool(fresh()).RequestState
		wg.Go(func() {
			retry := fresh()
			retry.InputResponses = mcp.InputResponseMap{secretsInputID: &mcp.ElicitResult{Action: "accept"}}
			retry.RequestState = state
			callTool(retry)
		})
	}
	// The retries pick up their pages.
	eventually(t, func() bool {
		f.tools.mu.Lock()
		defer f.tools.mu.Unlock()
		return len(f.tools.pending) == 0
	})
	if res := callTool(fresh()); !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "already open") {
		t.Fatalf("a call beyond the limit must be refused, got %+v", res)
	}
	wg.Wait()
	expectPagesClosed(t, f, maxPendingPages)
}

// A call that comes back and then fails its checks must close its page, or the page would
// take a token nobody reads and hold a slot until its deadline.
func TestLinkByTokenFailedRetryClosesPage(t *testing.T) {
	f := newFake()
	cs := connectRetryingItself(t, f)
	ctx := context.Background()
	first, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "link_repos_by_token", Arguments: tokenArgs("repository", "", false)})
	if err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	f.vars["repo-b"] = []bitbucket.Variable{{UUID: "{var-1}", Key: "REPO_A_TOKEN"}}
	f.mu.Unlock()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "link_repos_by_token", Arguments: tokenArgs("repository", "", false),
		InputResponses: mcp.InputResponseMap{secretsInputID: &mcp.ElicitResult{Action: "accept"}}, RequestState: first.RequestState,
	})
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; !res.IsError || !strings.Contains(text, "already exists") {
		t.Fatalf("retry = %s", text)
	}
	expectPagesClosed(t, f, 1)
}

func TestLinkByTokenLimitsOpenPages(t *testing.T) {
	f := newFake()
	cs := connectRetryingItself(t, f)
	for i := range maxPendingPages + 1 {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "link_repos_by_token", Arguments: tokenArgs("repository", "", false)})
		if err != nil {
			t.Fatal(err)
		}
		if refused := res.IsError; refused != (i == maxPendingPages) {
			t.Fatalf("call %d: refused = %v", i+1, refused)
		}
	}
}

func TestLinkByTokenRejectedToken(t *testing.T) {
	f := newFake()
	u := &fakeUser{pages: f.pages, action: "accept", token: "wrong-token"}
	out, isErr := call(t, connect(t, f, u.elicit), "link_repos_by_token", tokenArgs("repository", "", false))
	if !isErr || !strings.Contains(out, "rejected") || strings.Contains(out, "wrong-token") {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if m := f.mutationList(); len(m) != 0 {
		t.Fatalf("mutations = %v", m)
	}
}

func TestLinkByTokenNeedsPipelinesInBoth(t *testing.T) {
	f := newFake()
	f.pipelines["repo-a"] = false
	f.pipelines["repo-b"] = false
	u := &fakeUser{pages: f.pages, action: "accept", token: "repo-a-token"}
	out, isErr := call(t, connect(t, f, u.elicit), "link_repos_by_token", tokenArgs("repository", "", false))
	if !isErr || !strings.Contains(out, "not enabled in repo-a, repo-b") {
		t.Fatalf("result = %s (error %v)", out, isErr)
	}
	if len(u.asked()) != 0 {
		t.Fatal("must not ask for the token")
	}
}

func TestLinkByTokenValidatesInput(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(map[string]any)
		want string
	}{
		"bad scope":            {func(a map[string]any) { a["scope"] = "workspace" }, "scope must be"},
		"deployment no env":    {func(a map[string]any) { a["scope"] = "deployment" }, "environment is required"},
		"repository with env":  {func(a map[string]any) { a["environment"] = "Production" }, "only used with scope"},
		"unknown permission":   {func(a map[string]any) { a["permissions"] = []string{"repository", "admin"} }, `unknown permission "admin"`},
		"no permissions":       {func(a map[string]any) { a["permissions"] = []string{} }, "permissions are required"},
		"runner permission":    {func(a map[string]any) { a["permissions"] = []string{"pipeline", "runner"} }, `unknown permission "runner"`},
		"empty variable name":  {func(a map[string]any) { a["variable_name"] = "" }, "only letters, digits and underscores"},
		"dash in variable":     {func(a map[string]any) { a["variable_name"] = "REPO-A" }, "only letters, digits and underscores"},
		"leading digit":        {func(a map[string]any) { a["variable_name"] = "1TOKEN" }, "only letters, digits and underscores"},
		"same source & target": {func(a map[string]any) { a["target_repo"] = "repo-a" }, "must be different"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			args := tokenArgs("repository", "", false)
			tc.edit(args)
			out, isErr := call(t, connect(t, f, (&fakeUser{pages: f.pages, action: "accept"}).elicit), "link_repos_by_token", args)
			if !isErr || !strings.Contains(out, tc.want) {
				t.Fatalf("result = %s (error %v), want %q", out, isErr, tc.want)
			}
		})
	}
}
