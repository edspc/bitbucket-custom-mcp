// Package tools holds the MCP tools; every Bitbucket call goes through package bitbucket.
package tools

import (
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

// secretWait bounds how long a secret page stays open for the user.
const secretWait = 10 * time.Minute

type tools struct {
	policy       Policy
	ctx          context.Context // background work such as polling for subscribers stops with it
	bb           *bitbucket.Client
	log          *slog.Logger
	server       *mcp.Server
	serve        func(http.Handler, time.Time) (baseURL string, closed <-chan struct{}, err error) // closes after its deadline and linger
	pageTemplate *template.Template                                                                // parsed once for every secret page
	secretWait   time.Duration
	pollInterval time.Duration
	// watchLifetime bounds how long a subscribed pipeline is polled.
	watchLifetime time.Duration
	// scheduleMu serializes duplicate checks and creates without holding up pages or pollers.
	scheduleMu sync.Mutex

	mu            sync.Mutex
	pending       map[string]*pendingSecrets // by elicitation ID, until the call comes back
	openPages     int                        // pending pages plus pages a call is waiting on
	pageListeners int                        // includes closed pages until their listener shuts down
	watches       map[string]*pipelineWatch  // by resource URI
	watchCalls    int                        // watch_pipeline calls in progress
}

// statusResult is the outcome of a tool that changes something.
type statusResult struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// NewServer returns the MCP server with all tools; its background work stops with ctx.
func NewServer(ctx context.Context, impl *mcp.Implementation, bb *bitbucket.Client, log *slog.Logger, policy Policy) *mcp.Server {
	serve := func(h http.Handler, until time.Time) (string, <-chan struct{}, error) {
		return serveLocal(log, h, until)
	}
	return newServer(impl, &tools{ctx: ctx, bb: bb, log: log, policy: policy, serve: serve, secretWait: secretWait,
		pollInterval: pollInterval, watchLifetime: watchLifetime})
}

func newServer(impl *mcp.Implementation, t *tools) *mcp.Server {
	t.bb = t.bb.WithAllowedProjects(t.policy.Projects, t.policy.Repositories)
	if t.policy.Projects != nil {
		// Project checks double the requests per poll; keep the same hourly budget.
		t.pollInterval *= 2
	}
	t.pageTemplate = template.Must(template.New("page").Parse(secretPageHTML))
	t.pending = map[string]*pendingSecrets{}
	t.watches = map[string]*pipelineWatch{}
	s := mcp.NewServer(impl, &mcp.ServerOptions{SubscribeHandler: t.subscribe, UnsubscribeHandler: t.unsubscribe})
	t.server = s
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	write := &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: new(true)}

	mcp.AddTool(s, &mcp.Tool{
		Name:  "search_repositories",
		Title: "Search repositories",
		Description: "Find repositories of the workspace by a part of their name, optionally in one project, among " +
			"those the server policy allows, a page at a time: pass next_page back as page for more. Returns " +
			"each repository's slug, which the repo input of the other tools takes.",
		Annotations: readOnly,
	}, guarded(t, t.searchRepositories))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_uuid",
		Title:       "Get a repository or deployment environment UUID",
		Description: "Return the UUID of repo, or of its deployment environment when environment is supplied, exactly as Bitbucket returns it, including braces and letter case.",
		Annotations: readOnly,
	}, guarded(t, t.getUUID))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "link_repos_by_key",
		Annotations: write,
		Title:       "Link repositories by SSH key",
		Description: "Let pipelines of source_repo reach target_repo over SSH: the public SSH key of " +
			"source_repo pipelines is added to the Access Keys of target_repo, labelled with the " +
			"source_repo slug. Both repositories must have pipelines enabled.",
	}, guarded(t, t.linkByKey))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "link_repos_by_token",
		Annotations: write,
		Title:       "Link repositories by access token",
		Description: "Give pipelines of target_repo a repository access token of source_repo as a " +
			"secured pipeline variable, at repository level or for one deployment environment. " +
			"Bitbucket has no API to create the token, so the user creates it in the browser and " +
			"pastes it into a local page this server opens through URL elicitation; the token " +
			"never passes through the MCP client or the model. Only read access is checked; " +
			"token type, repository-only scope and actual permissions are not verified. Both repositories must have " +
			"pipelines enabled.",
	}, guarded(t, t.linkByToken))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_variables",
		Title:       "List pipeline variables",
		Description: "List pipeline variables of the workspace, of a repository or of one deployment environment. Values of secured variables are never returned.",
		Annotations: readOnly,
	}, guarded(t, t.listVariables))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_variable",
		Annotations: write,
		Title:       "Set a pipeline variable",
		Description: "Create a pipeline variable of the workspace, of a repository or of one deployment environment, " +
			"or overwrite it with force=true. A plain variable takes its value from the call. A secured variable " +
			"never does: the user enters its value on a local page this server opens through URL elicitation, " +
			"so it never passes through the MCP client or the model.",
	}, guarded(t, t.setVariable))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_variable",
		Annotations: write,
		Title:       "Delete a pipeline variable",
		Description: "Delete a pipeline variable of the workspace, of a repository or of one deployment environment.",
	}, guarded(t, t.deleteVariable))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "run_pipeline",
		Annotations: write,
		Title:       "Run a pipeline",
		Description: "Run a pipeline on a branch or tag, optionally at a given commit: the pipeline bitbucket-pipelines.yml " +
			"defines for the ref, or a custom pipeline (pipelines.custom) with variables. Secured variables are " +
			"named in secret_variables and the user enters their values on a local page this server opens " +
			"through URL elicitation. Returns the started pipeline; follow it with watch_pipeline.",
	}, guarded(t, t.runPipeline))
	mcp.AddTool(s, &mcp.Tool{
		Name:  "list_pipelines",
		Title: "List pipelines",
		Description: "List the pipelines of a repository, newest first, a page at a time: pass next_page back as " +
			"page, with the same filters and limit, for older ones. Filters pick pipelines of a branch, a ref type " +
			"or a commit, of a part of bitbucket-pipelines.yml such as 'custom: deploy', or started by push, " +
			"manually, by a schedule or by a parent step (child pipelines).",
		Annotations: readOnly,
	}, guarded(t, t.listPipelines))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_pipeline",
		Title:       "Get pipeline status",
		Description: "Get the status of a pipeline and its steps, by build number or UUID.",
		Annotations: readOnly,
	}, guarded(t, t.getPipeline))
	mcp.AddTool(s, &mcp.Tool{
		Name:  "watch_pipeline",
		Title: "Watch a pipeline",
		Description: "Wait until a pipeline completes, pauses for a manual step or the timeout passes, and " +
			"return every status change seen on the way. While waiting, each poll is sent as a progress " +
			"notification. Clients that support resource subscriptions can subscribe to the pipeline's " +
			"resource_uri instead. Bitbucket's rate limit allows only two pipelines to be polled at a time, " +
			"by watches and subscriptions together.",
		Annotations: readOnly,
	}, guarded(t, t.watchPipeline))
	mcp.AddTool(s, &mcp.Tool{
		Name:  "get_pipeline_log",
		Title: "Get a pipeline step log",
		Description: "Return the last lines of the log of a pipeline step: the step asked for, or else the " +
			"first failed step, or else the last step that started.",
		Annotations: readOnly,
	}, guarded(t, t.pipelineLog))

	mcp.AddTool(s, &mcp.Tool{Name: "create_deployment", Title: "Create a deployment environment", Description: "Create a Bitbucket deployment environment of type Test, Staging or Production. This creates the environment, not a pipeline run.", Annotations: write}, guarded(t, t.createDeployment))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_schedules",
		Title:       "List pipeline schedules",
		Description: "List the schedules of a repository: which pipeline of which branch runs when, by a cron pattern in UTC.",
		Annotations: readOnly,
	}, guarded(t, t.listSchedules))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_schedule",
		Annotations: write,
		Title:       "Schedule a pipeline",
		Description: "Run a pipeline of a branch on a schedule: a custom pipeline, a branch pipeline or the default " +
			"pipeline of bitbucket-pipelines.yml, by a 7-field cron pattern with seconds, in UTC. An identical " +
			"schedule is not added twice.",
	}, guarded(t, t.addSchedule))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_schedule",
		Annotations: write,
		Title:       "Delete a pipeline schedule",
		Description: "Delete a schedule of a repository by its UUID from list_schedules.",
	}, guarded(t, t.deleteSchedule))

	mcp.AddTool(s, &mcp.Tool{
		Name:  "get_pull_request",
		Title: "Get a pull request",
		Description: "Get a pull request by its ID: title, description, state, author, the source branch with " +
			"the changes and the destination branch they merge into, reviewers and their decisions, build " +
			"results such as pipeline runs, and the paths of the changed files with line counts, 200 a page: " +
			"pass files_next back as files_from for the next page. With paths, get the diff of those files " +
			"instead; a diff over 40 KB comes in parts: pass diff_next back as diff_from, with the same paths, " +
			"to read the next one.",
		Annotations: readOnly,
	}, guarded(t, t.getPullRequest))
	mcp.AddTool(s, &mcp.Tool{
		Name:  "list_pull_requests",
		Title: "List pull requests",
		Description: "List pull requests of a repository, the most recently updated first: open ones, or those in " +
			"another state, optionally only from a source branch or into a destination branch, a page at a time: " +
			"pass next_page back as page, with the same filters and limit, for the next one. " +
			"get_pull_request shows the description, reviewers, builds and changed files of one, and their diffs.",
		Annotations: readOnly,
	}, guarded(t, t.listPullRequests))

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "pipeline",
		Title:       "Pipeline status",
		URITemplate: pipelineURITemplate,
		MIMEType:    "application/json",
		Description: "Status of a pipeline run and its steps, as returned by get_pipeline. Subscribe to it to " +
			"get notifications/resources/updated on every change, until the pipeline completes or pauses " +
			"for a manual step.",
	}, t.readPipeline)
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name: "pipeline_scope", Title: "Pipelines for a branch, pull request or deployment environment",
		URITemplate: scopeURITemplate, MIMEType: "application/json",
		Description: "Latest 10 branch pipelines, 10 PR build statuses, or matching deployments from the latest 50 repository deployments; kind is branch, pullrequest or deployment. Selector is a percent-encoded branch name, positive PR ID or environment UUID. Subscribe for new runs and status changes for up to 24 hours.",
	}, t.readScope)
	return s
}
