package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bitbucket-custom-mcp/internal/bitbucket"
)

const (
	scopeRepository = "repository"
	scopeDeployment = "deployment"
	tokenInputID    = "token"
)

type linkByTokenInput struct {
	SourceRepo   string   `json:"source_repo" jsonschema:"slug of the repository the access token is created in"`
	TargetRepo   string   `json:"target_repo" jsonschema:"slug of the repository whose pipelines get the token"`
	VariableName string   `json:"variable_name" jsonschema:"name of the secured pipeline variable in target_repo"`
	Scope        string   `json:"scope" jsonschema:"where the variable lives in target_repo: 'repository' (all pipelines) or 'deployment' (one deployment environment)"`
	Environment  string   `json:"environment,omitempty" jsonschema:"deployment environment name in target_repo; required when scope is 'deployment'"`
	Permissions  []string `json:"permissions" jsonschema:"permissions of the token, any of: repository, repository:write, repository:admin, repository:delete, pullrequest, pullrequest:write, webhook, pipeline, pipeline:write, pipeline:variable"`
	Force        bool     `json:"force,omitempty" jsonschema:"overwrite the variable if it already exists"`
}

func validPermission(p string) bool {
	switch p {
	case "repository", "repository:write", "repository:admin", "repository:delete",
		"pullrequest", "pullrequest:write", "webhook",
		"pipeline", "pipeline:write", "pipeline:variable":
		return true
	}
	return false
}

// validVariableName mirrors Bitbucket's rule for variable names, checked up front so the user
// does not create a token that then cannot be stored.
func validVariableName(name string) bool {
	for i, r := range name {
		letter := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !letter && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return name != ""
}

func (in linkByTokenInput) validate() error {
	if err := checkRepoPair(in.SourceRepo, in.TargetRepo); err != nil {
		return err
	}
	if !validVariableName(in.VariableName) {
		return fmt.Errorf("variable_name %q must contain only letters, digits and underscores and must not start with a digit", in.VariableName)
	}
	switch in.Scope {
	case scopeRepository:
		if in.Environment != "" {
			return errors.New("environment is only used with scope 'deployment'")
		}
	case scopeDeployment:
		if in.Environment == "" {
			return errors.New("environment is required with scope 'deployment'")
		}
	default:
		return fmt.Errorf("scope must be %q or %q, got %q", scopeRepository, scopeDeployment, in.Scope)
	}
	if len(in.Permissions) == 0 {
		return errors.New("permissions are required")
	}
	for _, p := range in.Permissions {
		if !validPermission(p) {
			return fmt.Errorf("unknown permission %q", p)
		}
	}
	return nil
}

func (in linkByTokenInput) place() string {
	if in.Scope == scopeDeployment {
		return fmt.Sprintf("deployment environment %q of %s", in.Environment, in.TargetRepo)
	}
	return "repository variables of " + in.TargetRepo
}

func (t *tools) linkByToken(ctx context.Context, req *mcp.CallToolRequest, in linkByTokenInput) (*mcp.CallToolResult, linkResult, error) {
	// A call coming back from the token page claims the page before anything can fail.
	resp, cameBack := req.Params.InputResponses[tokenInputID]
	var page *pendingToken
	if cameBack {
		p, err := t.claimPage(req.Params.RequestState, in)
		if err != nil {
			return nil, linkResult{}, err
		}
		defer p.stop()
		page = p
	}

	if err := in.validate(); err != nil {
		return nil, linkResult{}, err
	}
	if err := t.checkPipelines(ctx, in.SourceRepo, in.TargetRepo); err != nil {
		return nil, linkResult{}, err
	}
	scope, err := t.variableScope(ctx, in)
	if err != nil {
		return nil, linkResult{}, err
	}
	vars, err := t.bb.ListVariables(ctx, scope)
	if err != nil {
		return nil, linkResult{}, err
	}
	var existing *bitbucket.Variable
	for i, v := range vars {
		if v.Key == in.VariableName {
			existing = &vars[i]
		}
	}
	if existing != nil && !in.Force {
		return nil, linkResult{}, fmt.Errorf("variable %s already exists in %s; nothing changed. Pass force=true to overwrite it",
			in.VariableName, in.place())
	}

	if !cameBack {
		return t.askToken(req, in)
	}
	if !accepted(resp) {
		return nil, linkResult{Status: "cancelled", Message: "the user did not open the token page; nothing changed"}, nil
	}
	token, err := t.waitToken(ctx, req, page)
	if err != nil {
		return nil, linkResult{}, err
	}
	if err = t.bb.CheckAccessToken(ctx, in.SourceRepo, token, in.Permissions); err != nil {
		if bitbucket.IsStatus(err, http.StatusUnauthorized) {
			return nil, linkResult{}, errors.New("bitbucket rejected the provided token as invalid; nothing changed")
		}
		if bitbucket.IsStatus(err, http.StatusForbidden) || bitbucket.IsStatus(err, http.StatusNotFound) {
			return nil, linkResult{}, fmt.Errorf("the provided token cannot access %s with the requested permissions, so it was likely created in another repository or with other permissions; nothing changed", in.SourceRepo)
		}
		return nil, linkResult{}, fmt.Errorf("checking the provided token against %s: %w; nothing changed", in.SourceRepo, err)
	}

	v := bitbucket.Variable{Key: in.VariableName, Value: token, Secured: true}
	status := "created"
	if existing != nil {
		v.UUID = existing.UUID
		status = "updated"
		err = t.bb.UpdateVariable(ctx, scope, v)
	} else {
		err = t.bb.CreateVariable(ctx, scope, v)
	}
	if err != nil {
		return nil, linkResult{}, err
	}
	t.log.InfoContext(ctx, "linked repositories by token", "source", in.SourceRepo, "target", in.TargetRepo,
		"variable", in.VariableName, "scope", in.Scope, "environment", in.Environment, "status", status)
	return nil, linkResult{
		Status:  status,
		Message: fmt.Sprintf("secured variable %s %s in %s with an access token of %s", in.VariableName, status, in.place(), in.SourceRepo),
	}, nil
}

func (t *tools) variableScope(ctx context.Context, in linkByTokenInput) (bitbucket.VariableScope, error) {
	scope := bitbucket.VariableScope{Repo: in.TargetRepo}
	if in.Scope != scopeDeployment {
		return scope, nil
	}
	envs, err := t.bb.ListEnvironments(ctx, in.TargetRepo)
	if err != nil {
		return scope, err
	}
	names := make([]string, 0, len(envs))
	for _, e := range envs {
		if e.Name == in.Environment {
			scope.EnvironmentUUID = e.UUID
			return scope, nil
		}
		names = append(names, e.Name)
	}
	return scope, fmt.Errorf("deployment environment %q not found in %s; available: %s",
		in.Environment, in.TargetRepo, strings.Join(names, ", "))
}
