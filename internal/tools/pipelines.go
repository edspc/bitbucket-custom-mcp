package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

const (
	defaultListLimit = 10
	maxListLimit     = 50
)

type pipelineView struct {
	BuildNumber int        `json:"build_number"`
	UUID        string     `json:"uuid"`
	State       string     `json:"state" jsonschema:"PENDING, IN_PROGRESS/RUNNING, IN_PROGRESS/PAUSED (waits for someone to run a manual step) or COMPLETED/<result>, the result being SUCCESSFUL, FAILED, ERROR, STOPPED or EXPIRED"`
	Error       string     `json:"error,omitempty"`
	Finished    bool       `json:"finished"`
	Ref         string     `json:"ref,omitempty" jsonschema:"what was built, such as 'branch main'"`
	Commit      string     `json:"commit,omitempty"`
	Definition  string     `json:"definition,omitempty" jsonschema:"the pipeline definition from bitbucket-pipelines.yml, such as 'custom: deploy' or 'branches: main'"`
	Creator     string     `json:"creator,omitempty"`
	CreatedOn   string     `json:"created_on,omitempty"`
	CompletedOn string     `json:"completed_on,omitempty"`
	URL         string     `json:"url" jsonschema:"page of the pipeline on bitbucket.org"`
	ResourceURI string     `json:"resource_uri" jsonschema:"MCP resource with the status of the pipeline; subscribe to it to be notified of every change"`
	Steps       []stepView `json:"steps,omitempty"`
}

type stepView struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name,omitempty"`
	State       string `json:"state"`
	Error       string `json:"error,omitempty"`
	StartedOn   string `json:"started_on,omitempty"`
	CompletedOn string `json:"completed_on,omitempty"`
}

func (t *tools) viewPipeline(repo string, p bitbucket.Pipeline, steps []bitbucket.Step) pipelineView {
	v := pipelineView{
		BuildNumber: p.BuildNumber,
		UUID:        p.UUID,
		State:       p.State.String(),
		Error:       p.State.ErrorMessage(),
		Finished:    p.State.Completed(),
		Creator:     p.Creator.DisplayName,
		CreatedOn:   p.CreatedOn,
		CompletedOn: p.CompletedOn,
		URL:         t.bb.PipelineURL(repo, p.BuildNumber),
		ResourceURI: pipelineURI(repo, p.BuildNumber),
	}
	if p.Target.RefName != "" {
		v.Ref = p.Target.RefType + " " + p.Target.RefName
	}
	if p.Target.Commit != nil {
		v.Commit = p.Target.Commit.Hash
	}
	v.Definition = definition(p.Target.Selector)
	for _, s := range steps {
		v.Steps = append(v.Steps, stepView{
			UUID: s.UUID, Name: s.Name, State: s.State.String(), Error: s.State.ErrorMessage(),
			StartedOn: s.StartedOn, CompletedOn: s.CompletedOn,
		})
	}
	return v
}

// definition names the pipeline definition of bitbucket-pipelines.yml a selector picks, such
// as "custom: deploy" or "default".
func definition(s *bitbucket.Selector) string {
	switch {
	case s == nil:
		return ""
	case s.Pattern == "":
		return s.Type
	}
	return s.Type + ": " + s.Pattern
}

// pipelineStatus reads a pipeline, found by build number or UUID, with its steps.
func (t *tools) pipelineStatus(ctx context.Context, repo, id string) (bitbucket.Pipeline, pipelineView, error) {
	p, err := t.bb.GetPipeline(ctx, repo, id)
	if err != nil {
		return p, pipelineView{}, err
	}
	steps, err := t.bb.ListSteps(ctx, repo, p.UUID)
	if err != nil {
		return p, pipelineView{}, err
	}
	return p, t.viewPipeline(repo, p, steps), nil
}

type pipelineRef struct {
	Repo     string `json:"repo" jsonschema:"repository slug"`
	Pipeline string `json:"pipeline" jsonschema:"build number of the pipeline, such as '42', or its UUID"`
}

func (in pipelineRef) validate() error {
	if in.Repo == "" || in.Pipeline == "" {
		return errors.New("repo and pipeline are required")
	}
	return nil
}

// optionalCount returns n, or def when n is omitted; any other n must be between 1 and most.
func optionalCount(field string, n, def, most int) (int, error) {
	if n == 0 {
		return def, nil
	}
	if n < 1 || n > most {
		return 0, fmt.Errorf("%s must be between 1 and %d", field, most)
	}
	return n, nil
}

func (t *tools) getPipeline(ctx context.Context, _ *mcp.CallToolRequest, in pipelineRef) (*mcp.CallToolResult, pipelineView, error) {
	if err := in.validate(); err != nil {
		return nil, pipelineView{}, err
	}
	_, view, err := t.pipelineStatus(ctx, in.Repo, in.Pipeline)
	return nil, view, err
}

type listPipelinesInput struct {
	Repo   string `json:"repo" jsonschema:"repository slug"`
	Branch string `json:"branch,omitempty" jsonschema:"only pipelines that built this branch"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many of the latest pipelines to return; 10 when omitted, at most 50"`
}

type listPipelinesResult struct {
	Pipelines []pipelineView `json:"pipelines" jsonschema:"newest first, without steps"`
}

func (t *tools) listPipelines(ctx context.Context, _ *mcp.CallToolRequest, in listPipelinesInput) (*mcp.CallToolResult, listPipelinesResult, error) {
	if in.Repo == "" {
		return nil, listPipelinesResult{}, errors.New("repo is required")
	}
	limit, err := optionalCount("limit", in.Limit, defaultListLimit, maxListLimit)
	if err != nil {
		return nil, listPipelinesResult{}, err
	}
	ps, err := t.bb.ListPipelines(ctx, in.Repo, in.Branch, limit)
	if err != nil {
		return nil, listPipelinesResult{}, err
	}
	views := make([]pipelineView, 0, len(ps))
	for _, p := range ps {
		views = append(views, t.viewPipeline(in.Repo, p, nil))
	}
	return nil, listPipelinesResult{Pipelines: views}, nil
}

type runVariable struct {
	Name  string `json:"name"`
	Value string `json:"value" jsonschema:"plain value; secrets go to secret_variables instead"`
}

type runPipelineInput struct {
	Repo            string        `json:"repo" jsonschema:"repository slug"`
	Branch          string        `json:"branch,omitempty" jsonschema:"branch to build; set either branch or tag"`
	Tag             string        `json:"tag,omitempty" jsonschema:"tag to build; set either branch or tag"`
	Commit          string        `json:"commit,omitempty" jsonschema:"commit hash to build in the context of the branch or tag; its latest commit when omitted"`
	Custom          string        `json:"custom,omitempty" jsonschema:"name of a custom pipeline under pipelines.custom in bitbucket-pipelines.yml; when omitted, the pipeline the file defines for the branch or tag runs"`
	Variables       []runVariable `json:"variables,omitempty" jsonschema:"plain variables of the custom pipeline"`
	SecretVariables []string      `json:"secret_variables,omitempty" jsonschema:"names of secured variables of the custom pipeline; never pass their values: the user enters them on a local page this server opens"`
}

func (in runPipelineInput) validate() error {
	if in.Repo == "" {
		return errors.New("repo is required")
	}
	if (in.Branch == "") == (in.Tag == "") {
		return errors.New("set exactly one of branch and tag")
	}
	if (len(in.Variables) > 0 || len(in.SecretVariables) > 0) && in.Custom == "" {
		return errors.New("variables are passed to custom pipelines only; set custom")
	}
	seen := map[string]bool{}
	names := slices.Clone(in.SecretVariables)
	for _, v := range in.Variables {
		names = append(names, v.Name)
	}
	for _, name := range names {
		if err := checkVariableName("variable name", name); err != nil {
			return err
		}
		if seen[name] {
			return fmt.Errorf("variable %s is given more than once", name)
		}
		seen[name] = true
	}
	return nil
}

func (in runPipelineInput) request() bitbucket.RunRequest {
	target := bitbucket.RefTarget("branch", in.Branch, in.Commit)
	if in.Tag != "" {
		target = bitbucket.RefTarget("tag", in.Tag, in.Commit)
	}
	if in.Custom != "" {
		target.Selector = &bitbucket.Selector{Type: "custom", Pattern: in.Custom}
	}
	r := bitbucket.RunRequest{Target: target}
	for _, v := range in.Variables {
		r.Variables = append(r.Variables, bitbucket.Variable{Key: v.Name, Value: v.Value})
	}
	return r
}

// what names the pipeline to run, for messages.
func (in runPipelineInput) what() string {
	pipeline := "the pipeline"
	if in.Custom != "" {
		pipeline = fmt.Sprintf("the custom pipeline %q", in.Custom)
	}
	ref := "branch " + in.Branch
	if in.Tag != "" {
		ref = "tag " + in.Tag
	}
	return fmt.Sprintf("%s of %s on %s", pipeline, in.Repo, ref)
}

type runPipelineResult struct {
	Status   string        `json:"status" jsonschema:"'started', or 'cancelled' when the user did not open the secret page"`
	Message  string        `json:"message"`
	Pipeline *pipelineView `json:"pipeline,omitempty"`
}

func (t *tools) runPipeline(ctx context.Context, req *mcp.CallToolRequest, in runPipelineInput) (*mcp.CallToolResult, runPipelineResult, error) {
	page, err := t.claimSecrets(req, in)
	if err != nil {
		return nil, runPipelineResult{}, err
	}
	defer page.close()

	if err = in.validate(); err != nil {
		return nil, runPipelineResult{}, err
	}
	if err = t.checkPipelines(ctx, in.Repo); err != nil {
		return nil, runPipelineResult{}, err
	}
	r := in.request()
	if len(in.SecretVariables) > 0 {
		if page == nil {
			fields := make([]secretField, 0, len(in.SecretVariables))
			for _, name := range in.SecretVariables {
				fields = append(fields, secretField{Name: name, Label: name})
			}
			var res *mcp.CallToolResult
			res, err = t.askSecrets(req, in, secretPageInfo{
				Title:   "Run a pipeline",
				Need:    fmt.Sprintf("Enter the secured variables to run %s.", in.what()),
				Outcome: "The values are passed as secured variables to this one pipeline run and are not stored in the repository settings.",
				Fields:  fields,
			})
			return res, runPipelineResult{}, err
		}
		if !accepted(req) {
			return nil, runPipelineResult{Status: "cancelled", Message: "the user did not open the secret page; no pipeline was started"}, nil
		}
		var values map[string]string
		if values, err = t.waitSecrets(ctx, req, page); err != nil {
			return nil, runPipelineResult{}, err
		}
		for _, name := range in.SecretVariables {
			r.Variables = append(r.Variables, bitbucket.Variable{Key: name, Value: values[name], Secured: true})
		}
	}

	p, err := t.bb.RunPipeline(ctx, in.Repo, r)
	if err != nil {
		return nil, runPipelineResult{}, err
	}
	t.log.InfoContext(ctx, "started pipeline", "repo", in.Repo, "build", p.BuildNumber, "branch", in.Branch, "tag", in.Tag,
		"custom", in.Custom, "variables", len(r.Variables))
	view := t.viewPipeline(in.Repo, p, nil)
	return nil, runPipelineResult{
		Status:   "started",
		Message:  fmt.Sprintf("started %s as pipeline #%d; follow it with watch_pipeline", in.what(), p.BuildNumber),
		Pipeline: &view,
	}, nil
}

// stepName names a step in messages; steps without a name in bitbucket-pipelines.yml have none.
func stepName(s stepView) string {
	if s.Name != "" {
		return s.Name
	}
	return s.UUID
}
