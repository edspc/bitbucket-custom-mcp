package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

const (
	scopeWorkspace  = "workspace"
	scopeRepository = "repository"
	scopeDeployment = "deployment"
)

// variablePlace is where a pipeline variable lives.
type variablePlace struct {
	Scope       string `json:"scope" jsonschema:"where the variable lives: 'workspace' (pipelines of every repository in the workspace), 'repository' (all pipelines of repo) or 'deployment' (one deployment environment of repo)"`
	Repo        string `json:"repo,omitempty" jsonschema:"repository slug; required unless scope is 'workspace'"`
	Environment string `json:"environment,omitempty" jsonschema:"deployment environment name in repo; required when scope is 'deployment'"`
}

func (p variablePlace) validate() error {
	switch p.Scope {
	case scopeWorkspace:
		if p.Repo != "" || p.Environment != "" {
			return errors.New("repo and environment are not used with scope 'workspace'")
		}
		return nil
	case scopeRepository:
		if p.Environment != "" {
			return errors.New("environment is only used with scope 'deployment'")
		}
	case scopeDeployment:
		if p.Environment == "" {
			return errors.New("environment is required with scope 'deployment'")
		}
	default:
		return fmt.Errorf("scope must be %q, %q or %q, got %q", scopeWorkspace, scopeRepository, scopeDeployment, p.Scope)
	}
	if p.Repo == "" {
		return fmt.Errorf("repo is required with scope %q", p.Scope)
	}
	return nil
}

func (p variablePlace) String() string {
	switch p.Scope {
	case scopeWorkspace:
		return "workspace variables"
	case scopeDeployment:
		return fmt.Sprintf("deployment environment %q of %s", p.Environment, p.Repo)
	}
	return "repository variables of " + p.Repo
}

func (t *tools) variableScope(ctx context.Context, p variablePlace) (bitbucket.VariableScope, error) {
	switch p.Scope {
	case scopeWorkspace:
		return bitbucket.VariableScope{}, nil
	case scopeRepository:
		return bitbucket.VariableScope{Repo: p.Repo}, nil
	}
	envs, err := t.bb.ListEnvironments(ctx, p.Repo)
	if err != nil {
		return bitbucket.VariableScope{}, err
	}
	names := make([]string, 0, len(envs))
	for _, e := range envs {
		if e.Name == p.Environment {
			return bitbucket.VariableScope{Repo: p.Repo, EnvironmentUUID: e.UUID}, nil
		}
		names = append(names, e.Name)
	}
	return bitbucket.VariableScope{}, fmt.Errorf("deployment environment %q not found in %s; available: %s",
		p.Environment, p.Repo, strings.Join(names, ", "))
}

// findVariable returns the variable called name in scope, or nil.
func (t *tools) findVariable(ctx context.Context, scope bitbucket.VariableScope, name string) (*bitbucket.Variable, error) {
	vars, err := t.bb.ListVariables(ctx, scope)
	if err != nil {
		return nil, err
	}
	if i := slices.IndexFunc(vars, func(v bitbucket.Variable) bool { return v.Key == name }); i >= 0 {
		return &vars[i], nil
	}
	return nil, nil
}

// variableToSet finds the scope of place and the variable called name in it, if any, and
// refuses to overwrite that variable unless force is set.
func (t *tools) variableToSet(ctx context.Context, place variablePlace, name string, force bool) (bitbucket.VariableScope, *bitbucket.Variable, error) {
	scope, err := t.variableScope(ctx, place)
	if err != nil {
		return scope, nil, err
	}
	existing, err := t.findVariable(ctx, scope, name)
	if err != nil {
		return scope, nil, err
	}
	if existing != nil && !force {
		return scope, nil, fmt.Errorf("variable %s already exists in %s; nothing changed. Pass force=true to overwrite it", name, place)
	}
	return scope, existing, nil
}

// putVariable creates v in scope, or overwrites existing when set, and tells which it did.
func (t *tools) putVariable(ctx context.Context, scope bitbucket.VariableScope, existing *bitbucket.Variable, v bitbucket.Variable) (string, error) {
	if existing == nil {
		return "created", t.bb.CreateVariable(ctx, scope, v)
	}
	v.UUID = existing.UUID
	return "updated", t.bb.UpdateVariable(ctx, scope, v)
}

// validVariableName mirrors Bitbucket's rule for variable names, checked up front so the user
// does not enter a secret that then cannot be stored.
func validVariableName(name string) bool {
	for i, r := range name {
		letter := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !letter && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return name != ""
}

func checkVariableName(field, name string) error {
	if !validVariableName(name) {
		return fmt.Errorf("%s %q must contain only letters, digits and underscores and must not start with a digit", field, name)
	}
	return nil
}

type variableView struct {
	Name    string `json:"name"`
	Value   string `json:"value,omitempty" jsonschema:"value of a plain variable; never returned for secured variables"`
	Secured bool   `json:"secured"`
}

type listVariablesResult struct {
	Place     string         `json:"place"`
	Variables []variableView `json:"variables"`
}

func (t *tools) listVariables(ctx context.Context, _ *mcp.CallToolRequest, in variablePlace) (*mcp.CallToolResult, listVariablesResult, error) {
	if err := in.validate(); err != nil {
		return nil, listVariablesResult{}, err
	}
	scope, err := t.variableScope(ctx, in)
	if err != nil {
		return nil, listVariablesResult{}, err
	}
	vars, err := t.bb.ListVariables(ctx, scope)
	if err != nil {
		return nil, listVariablesResult{}, err
	}
	views := make([]variableView, 0, len(vars))
	for _, v := range vars {
		view := variableView{Name: v.Key, Secured: v.Secured}
		if !v.Secured {
			view.Value = v.Value
		}
		views = append(views, view)
	}
	return nil, listVariablesResult{Place: in.String(), Variables: views}, nil
}

type setVariableInput struct {
	variablePlace
	Name    string `json:"name" jsonschema:"variable name: letters, digits and underscores, not starting with a digit"`
	Value   string `json:"value,omitempty" jsonschema:"value of a plain variable; never pass a value for a secured variable, the user enters it on a local page"`
	Secured bool   `json:"secured,omitempty" jsonschema:"store the variable secured: its value is masked in logs and never readable again; the user enters it on a local page this server opens"`
	Force   bool   `json:"force,omitempty" jsonschema:"overwrite the variable if it already exists"`
}

func (in setVariableInput) validate() error {
	if err := in.variablePlace.validate(); err != nil {
		return err
	}
	if err := checkVariableName("name", in.Name); err != nil {
		return err
	}
	if in.Secured && in.Value != "" {
		return errors.New("a secured value must not be passed to the tool: the user enters it on a local page this server opens; nothing changed")
	}
	if !in.Secured && in.Value == "" {
		return errors.New("value is required for a plain variable; for a secret set secured=true and the user enters it on a local page")
	}
	return nil
}

func (t *tools) setVariable(ctx context.Context, req *mcp.CallToolRequest, in setVariableInput) (*mcp.CallToolResult, statusResult, error) {
	page, err := t.claimSecrets(req, in)
	if err != nil {
		return nil, statusResult{}, err
	}
	defer page.close()

	if err = in.validate(); err != nil {
		return nil, statusResult{}, err
	}
	scope, existing, err := t.variableToSet(ctx, in.variablePlace, in.Name, in.Force)
	if err != nil {
		return nil, statusResult{}, err
	}

	v := bitbucket.Variable{Key: in.Name, Value: in.Value, Secured: in.Secured}
	if in.Secured {
		if page == nil {
			var res *mcp.CallToolResult
			res, err = t.askSecrets(req, in, secretPageInfo{
				Title:   "Set a secured pipeline variable",
				Need:    fmt.Sprintf("Enter the value of the secured variable %s for %s.", in.Name, in.variablePlace),
				Outcome: fmt.Sprintf("The value is stored as the secured variable %s in %s.", in.Name, in.variablePlace),
				Fields:  []secretField{{Name: in.Name, Label: in.Name, Hint: "Secured: masked in pipeline logs and never readable again."}},
			})
			return res, statusResult{}, err
		}
		if !accepted(req) {
			return nil, statusResult{Status: "cancelled", Message: "the user did not open the secret page; nothing changed"}, nil
		}
		var values map[string]string
		if values, err = t.waitSecrets(ctx, req, page); err != nil {
			return nil, statusResult{}, err
		}
		v.Value = values[in.Name]
	}

	status, err := t.putVariable(ctx, scope, existing, v)
	if err != nil {
		return nil, statusResult{}, err
	}
	t.log.InfoContext(ctx, "set variable", "scope", in.Scope, "repo", in.Repo, "environment", in.Environment,
		"variable", in.Name, "secured", in.Secured, "status", status)
	kind := "plain"
	if in.Secured {
		kind = "secured"
	}
	return nil, statusResult{Status: status, Message: fmt.Sprintf("%s variable %s %s in %s", kind, in.Name, status, in.variablePlace)}, nil
}

type deleteVariableInput struct {
	variablePlace
	Name string `json:"name" jsonschema:"name of the variable to delete"`
}

func (t *tools) deleteVariable(ctx context.Context, _ *mcp.CallToolRequest, in deleteVariableInput) (*mcp.CallToolResult, statusResult, error) {
	if err := in.validate(); err != nil {
		return nil, statusResult{}, err
	}
	if in.Name == "" {
		return nil, statusResult{}, errors.New("name is required")
	}
	scope, err := t.variableScope(ctx, in.variablePlace)
	if err != nil {
		return nil, statusResult{}, err
	}
	existing, err := t.findVariable(ctx, scope, in.Name)
	if err != nil {
		return nil, statusResult{}, err
	}
	if existing == nil {
		return nil, statusResult{}, fmt.Errorf("variable %s not found in %s; nothing changed", in.Name, in.variablePlace)
	}
	if err = t.bb.DeleteVariable(ctx, scope, existing.UUID); err != nil {
		return nil, statusResult{}, err
	}
	t.log.InfoContext(ctx, "deleted variable", "scope", in.Scope, "repo", in.Repo, "environment", in.Environment, "variable", in.Name)
	return nil, statusResult{Status: "deleted", Message: fmt.Sprintf("variable %s deleted from %s", in.Name, in.variablePlace)}, nil
}
