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
	ctx          context.Context // background work such as polling for subscribers stops with it
	bb           *bitbucket.Client
	log          *slog.Logger
	server       *mcp.Server
	serve        func(http.Handler) (baseURL string, stop func(), err error) // serves a secret page
	pageTemplate *template.Template                                          // parsed once for every secret page
	secretWait   time.Duration
	pollInterval time.Duration
	// watchLifetime bounds how long a subscribed pipeline is polled.
	watchLifetime time.Duration

	mu         sync.Mutex
	pending    map[string]*pendingSecrets // by elicitation ID, until the call comes back
	openPages  int                        // pending pages plus pages a call is waiting on
	watches    map[string]*pipelineWatch  // by resource URI
	watchCalls int                        // watch_pipeline calls in progress
}

// statusResult is the outcome of a tool that changes something.
type statusResult struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// NewServer returns the MCP server with all tools; its background work stops with ctx.
func NewServer(ctx context.Context, impl *mcp.Implementation, bb *bitbucket.Client, log *slog.Logger) *mcp.Server {
	serve := func(h http.Handler) (string, func(), error) { return serveLocal(log, h) }
	return newServer(impl, &tools{ctx: ctx, bb: bb, log: log, serve: serve, secretWait: secretWait,
		pollInterval: pollInterval, watchLifetime: watchLifetime})
}

func newServer(impl *mcp.Implementation, t *tools) *mcp.Server {
	t.pageTemplate = template.Must(template.New("page").Parse(secretPageHTML))
	t.pending = map[string]*pendingSecrets{}
	t.watches = map[string]*pipelineWatch{}
	s := mcp.NewServer(impl, &mcp.ServerOptions{SubscribeHandler: t.subscribe, UnsubscribeHandler: t.unsubscribe})
	t.server = s
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(s, &mcp.Tool{
		Name:  "link_repos_by_key",
		Title: "Link repositories by SSH key",
		Description: "Let pipelines of source_repo reach target_repo over SSH: the public SSH key of " +
			"source_repo pipelines is added to the Access Keys of target_repo, labelled with the " +
			"source_repo slug. Both repositories must have pipelines enabled.",
	}, t.linkByKey)
	mcp.AddTool(s, &mcp.Tool{
		Name:  "link_repos_by_token",
		Title: "Link repositories by access token",
		Description: "Give pipelines of target_repo a repository access token of source_repo as a " +
			"secured pipeline variable, at repository level or for one deployment environment. " +
			"Bitbucket has no API to create the token, so the user creates it in the browser and " +
			"pastes it into a local page this server opens through URL elicitation; the token " +
			"never passes through the MCP client or the model. Both repositories must have " +
			"pipelines enabled.",
	}, t.linkByToken)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_variables",
		Title:       "List pipeline variables",
		Description: "List pipeline variables of the workspace, of a repository or of one deployment environment. Values of secured variables are never returned.",
		Annotations: readOnly,
	}, t.listVariables)
	mcp.AddTool(s, &mcp.Tool{
		Name:  "set_variable",
		Title: "Set a pipeline variable",
		Description: "Create a pipeline variable of the workspace, of a repository or of one deployment environment, " +
			"or overwrite it with force=true. A plain variable takes its value from the call. A secured variable " +
			"never does: the user enters its value on a local page this server opens through URL elicitation, " +
			"so it never passes through the MCP client or the model.",
	}, t.setVariable)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_variable",
		Title:       "Delete a pipeline variable",
		Description: "Delete a pipeline variable of the workspace, of a repository or of one deployment environment.",
	}, t.deleteVariable)

	mcp.AddTool(s, &mcp.Tool{
		Name:  "run_pipeline",
		Title: "Run a pipeline",
		Description: "Run a pipeline on a branch or tag, optionally at a given commit: the pipeline bitbucket-pipelines.yml " +
			"defines for the ref, or a custom pipeline (pipelines.custom) with variables. Secured variables are " +
			"named in secret_variables and the user enters their values on a local page this server opens " +
			"through URL elicitation. Returns the started pipeline; follow it with watch_pipeline.",
	}, t.runPipeline)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_pipelines",
		Title:       "List pipelines",
		Description: "List the latest pipelines of a repository, newest first, optionally on one branch.",
		Annotations: readOnly,
	}, t.listPipelines)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_pipeline",
		Title:       "Get pipeline status",
		Description: "Get the status of a pipeline and its steps, by build number or UUID.",
		Annotations: readOnly,
	}, t.getPipeline)
	mcp.AddTool(s, &mcp.Tool{
		Name:  "watch_pipeline",
		Title: "Watch a pipeline",
		Description: "Wait until a pipeline completes, pauses for a manual step or the timeout passes, and " +
			"return every status change seen on the way. While waiting, each poll is sent as a progress " +
			"notification. Clients that support resource subscriptions can subscribe to the pipeline's " +
			"resource_uri instead. Bitbucket's rate limit allows only two pipelines to be polled at a time, " +
			"by watches and subscriptions together.",
		Annotations: readOnly,
	}, t.watchPipeline)
	mcp.AddTool(s, &mcp.Tool{
		Name:  "get_pipeline_log",
		Title: "Get a pipeline step log",
		Description: "Return the last lines of the log of a pipeline step: the step asked for, or else the " +
			"first failed step, or else the last step that started.",
		Annotations: readOnly,
	}, t.pipelineLog)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_schedules",
		Title:       "List pipeline schedules",
		Description: "List the schedules of a repository: which pipeline of which branch runs when, by a cron pattern in UTC.",
		Annotations: readOnly,
	}, t.listSchedules)
	mcp.AddTool(s, &mcp.Tool{
		Name:  "add_schedule",
		Title: "Schedule a pipeline",
		Description: "Run a pipeline of a branch on a schedule: a custom pipeline, a branch pipeline or the default " +
			"pipeline of bitbucket-pipelines.yml, by a 7-field cron pattern with seconds, in UTC. An identical " +
			"schedule is not added twice.",
	}, t.addSchedule)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_schedule",
		Title:       "Delete a pipeline schedule",
		Description: "Delete a schedule of a repository by its UUID from list_schedules.",
	}, t.deleteSchedule)

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "pipeline",
		Title:       "Pipeline status",
		URITemplate: pipelineURITemplate,
		MIMEType:    "application/json",
		Description: "Status of a pipeline run and its steps, as returned by get_pipeline. Subscribe to it to " +
			"get notifications/resources/updated on every change, until the pipeline completes or pauses " +
			"for a manual step.",
	}, t.readPipeline)
	return s
}
