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

type linkByTokenResult struct {
	statusResult
	TokenValidation string `json:"token_validation,omitempty" jsonschema:"read_access_only: token type, repository-only scope and requested permissions were not verified"`
}

type linkByTokenInput struct {
	SourceRepo   string   `json:"source_repo" jsonschema:"slug of the repository the access token is created in"`
	TargetRepo   string   `json:"target_repo" jsonschema:"slug of the repository whose pipelines get the token"`
	VariableName string   `json:"variable_name" jsonschema:"name of the secured pipeline variable in target_repo"`
	Scope        string   `json:"scope" jsonschema:"where the variable lives in target_repo: 'repository' (all pipelines) or 'deployment' (one deployment environment)"`
	Environment  string   `json:"environment,omitempty" jsonschema:"deployment environment name in target_repo; required when scope is 'deployment'"`
	Permissions  []string `json:"permissions" jsonschema:"permissions to select in Bitbucket, not verified by the read-access check; any of: repository, repository:write, repository:admin, repository:delete, pullrequest, pullrequest:write, webhook, pipeline, pipeline:write, pipeline:variable"`
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

func (t *tools) linkByToken(ctx context.Context, req *mcp.CallToolRequest, in linkByTokenInput) (*mcp.CallToolResult, linkByTokenResult, error) {
	page, err := t.claimSecrets(req, in)
	if err != nil {
		return nil, linkByTokenResult{}, err
	}
	defer page.close()

	if err = in.validate(); err != nil {
		return nil, linkByTokenResult{}, err
	}
	if err = t.checkPipelines(ctx, in.SourceRepo, in.TargetRepo); err != nil {
		return nil, linkByTokenResult{}, err
	}
	scope, existing, err := t.variableToSet(ctx, in.place(), in.VariableName, in.Force)
	if err != nil {
		return nil, linkByTokenResult{}, err
	}
	args, err := variableChangeArguments(in, scope, existing, page)
	if err != nil {
		return nil, linkByTokenResult{}, err
	}

	if page == nil {
		var res *mcp.CallToolResult
		res, err = t.askSecrets(ctx, req, in, secretPageInfo{
			Arguments: args,
			Title:     "Link repositories by access token",
			Need:      fmt.Sprintf("Create a repository access token in %s and paste it here.", in.SourceRepo),
			Outcome:   fmt.Sprintf("The token becomes the secured variable %s in %s, so its pipelines can reach %s.", in.VariableName, in.place(), in.SourceRepo),
			Token: &tokenHelp{
				Repo:        in.SourceRepo,
				CreateURL:   t.bb.AccessTokensPage(in.SourceRepo),
				Name:        in.TargetRepo + "-pipelines",
				Permissions: in.Permissions,
			},
			Fields: []secretField{{Name: tokenField, Label: "Access token of " + in.SourceRepo}},
		})
		return res, linkByTokenResult{}, err
	}
	if !accepted(req) {
		return nil, linkByTokenResult{statusResult: statusResult{Status: "cancelled", Message: "the user did not open the token page; nothing changed"}}, nil
	}
	values, err := t.waitSecrets(ctx, req, page)
	if err != nil {
		return nil, linkByTokenResult{}, err
	}
	checkCtx, cancel := context.WithDeadline(ctx, page.page.info.Until)
	defer cancel()
	token := values[tokenField]
	if err = t.bb.CheckTokenAccess(checkCtx, in.SourceRepo, token, in.Permissions); err != nil {
		if bitbucket.IsStatus(err, http.StatusUnauthorized) {
			return nil, linkByTokenResult{}, errors.New("bitbucket rejected the provided token as invalid; nothing changed")
		}
		if bitbucket.IsStatus(err, http.StatusForbidden) || bitbucket.IsStatus(err, http.StatusNotFound) {
			return nil, linkByTokenResult{}, fmt.Errorf("the provided token cannot read the selected endpoint in %s; it may belong to another repository or lack read access; token type and permissions were not verified; nothing changed", in.SourceRepo)
		}
		return nil, linkByTokenResult{}, fmt.Errorf("checking the provided token against %s: %w; nothing changed", in.SourceRepo, err)
	}

	// Recheck after both the browser wait and token probe, immediately before storing it.
	scope, existing, err = t.variableToSet(checkCtx, in.place(), in.VariableName, in.Force)
	if err != nil {
		return nil, linkByTokenResult{}, err
	}
	if _, err = variableChangeArguments(in, scope, existing, page); err != nil {
		return nil, linkByTokenResult{}, err
	}
	if err = approvalLive(page); err != nil {
		return nil, linkByTokenResult{}, err
	}
	status, err := t.putVariable(ctx, scope, existing, bitbucket.Variable{Key: in.VariableName, Value: token, Secured: true})
	if err != nil {
		return nil, linkByTokenResult{}, err
	}
	t.log.InfoContext(ctx, "linked repositories by token", "source", in.SourceRepo, "target", in.TargetRepo,
		"variable", in.VariableName, "scope", in.Scope, "environment", in.Environment, "status", status)
	return nil, linkByTokenResult{TokenValidation: "read_access_only", statusResult: statusResult{
		Status:  status,
		Message: fmt.Sprintf("secured variable %s %s in %s with the provided token; read access to %s was checked, but token type, repository-only scope and permissions were not verified", in.VariableName, status, in.place(), in.SourceRepo),
	}}, nil
}
