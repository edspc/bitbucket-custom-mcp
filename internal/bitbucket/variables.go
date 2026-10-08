package bitbucket

import (
	"context"
	"net/http"
)

// Variable is a pipeline variable; Value is empty for secured variables read back from the API.
type Variable struct {
	UUID    string `json:"uuid,omitempty"`
	Key     string `json:"key"`
	Value   string `json:"value"`
	Secured bool   `json:"secured"`
}

type Environment struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// VariableScope selects where a variable lives: the whole repository, or one deployment
// environment when EnvironmentUUID is set.
type VariableScope struct {
	Repo            string
	EnvironmentUUID string
}

func (c *Client) variablesPath(s VariableScope, parts ...string) string {
	if s.EnvironmentUUID == "" {
		return c.repoPath(s.Repo, append([]string{"pipelines_config", "variables"}, parts...)...)
	}
	return c.repoPath(s.Repo, append([]string{"deployments_config", "environments", s.EnvironmentUUID, "variables"}, parts...)...)
}

func (c *Client) ListVariables(ctx context.Context, s VariableScope) ([]Variable, error) {
	return list[Variable](ctx, c, c.variablesPath(s))
}

func (c *Client) CreateVariable(ctx context.Context, s VariableScope, v Variable) error {
	return c.call(ctx, http.MethodPost, c.variablesPath(s), v, nil)
}

func (c *Client) UpdateVariable(ctx context.Context, s VariableScope, v Variable) error {
	return c.call(ctx, http.MethodPut, c.variablesPath(s, v.UUID), v, nil)
}

func (c *Client) ListEnvironments(ctx context.Context, repo string) ([]Environment, error) {
	return list[Environment](ctx, c, c.repoPath(repo, "environments"))
}
