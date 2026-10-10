package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

const tokenField = "token"

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

func (in linkByTokenInput) place() variablePlace {
	return variablePlace{Scope: in.Scope, Repo: in.TargetRepo, Environment: in.Environment}
}

func (in linkByTokenInput) validate() error {
	if err := checkRepoPair(in.SourceRepo, in.TargetRepo); err != nil {
		return err
	}
	if err := checkVariableName("variable_name", in.VariableName); err != nil {
		return err
	}
	if in.Scope != scopeRepository && in.Scope != scopeDeployment {
		return fmt.Errorf("scope must be %q or %q, got %q", scopeRepository, scopeDeployment, in.Scope)
	}
	if err := in.place().validate(); err != nil {
		return err
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

func (t *tools) linkByToken(ctx context.Context, req *mcp.CallToolRequest, in linkByTokenInput) (*mcp.CallToolResult, statusResult, error) {
	page, err := t.claimSecrets(req, in)
	if err != nil {
		return nil, statusResult{}, err
	}
	defer page.close()

	if err = in.validate(); err != nil {
		return nil, statusResult{}, err
	}
	if err = t.checkPipelines(ctx, in.SourceRepo, in.TargetRepo); err != nil {
		return nil, statusResult{}, err
	}
	scope, existing, err := t.variableToSet(ctx, in.place(), in.VariableName, in.Force)
	if err != nil {
		return nil, statusResult{}, err
	}

	if page == nil {
		var res *mcp.CallToolResult
		res, err = t.askSecrets(req, in, secretPageInfo{
			Title:   "Link repositories by access token",
			Need:    fmt.Sprintf("Create a repository access token in %s and paste it here.", in.SourceRepo),
			Outcome: fmt.Sprintf("The token becomes the secured variable %s in %s, so its pipelines can reach %s.", in.VariableName, in.place(), in.SourceRepo),
			Token: &tokenHelp{
				Repo:        in.SourceRepo,
				CreateURL:   t.bb.AccessTokensPage(in.SourceRepo),
				Name:        in.TargetRepo + "-pipelines",
				Permissions: in.Permissions,
			},
			Fields: []secretField{{Name: tokenField, Label: "Access token of " + in.SourceRepo}},
		})
		return res, statusResult{}, err
	}
	if !accepted(req) {
		return nil, statusResult{Status: "cancelled", Message: "the user did not open the token page; nothing changed"}, nil
	}
	values, err := t.waitSecrets(ctx, req, page)
	if err != nil {
		return nil, statusResult{}, err
	}
	token := values[tokenField]
	if err = t.bb.CheckAccessToken(ctx, in.SourceRepo, token, in.Permissions); err != nil {
		if bitbucket.IsStatus(err, http.StatusUnauthorized) {
			return nil, statusResult{}, errors.New("bitbucket rejected the provided token as invalid; nothing changed")
		}
		if bitbucket.IsStatus(err, http.StatusForbidden) || bitbucket.IsStatus(err, http.StatusNotFound) {
			return nil, statusResult{}, fmt.Errorf("the provided token cannot access %s with the requested permissions, so it was likely created in another repository or with other permissions; nothing changed", in.SourceRepo)
		}
		return nil, statusResult{}, fmt.Errorf("checking the provided token against %s: %w; nothing changed", in.SourceRepo, err)
	}

	status, err := t.putVariable(ctx, scope, existing, bitbucket.Variable{Key: in.VariableName, Value: token, Secured: true})
	if err != nil {
		return nil, statusResult{}, err
	}
	t.log.InfoContext(ctx, "linked repositories by token", "source", in.SourceRepo, "target", in.TargetRepo,
		"variable", in.VariableName, "scope", in.Scope, "environment", in.Environment, "status", status)
	return nil, statusResult{
		Status:  status,
		Message: fmt.Sprintf("secured variable %s %s in %s with an access token of %s", in.VariableName, status, in.place(), in.SourceRepo),
	}, nil
}
