package bitbucket

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Deployment records a pipeline's deployment to an environment.
type Deployment struct {
	UUID        string          `json:"uuid"`
	State       DeploymentState `json:"state"`
	Environment Environment     `json:"environment"`
	Release     struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"release"`
	LastUpdateTime string `json:"last_update_time"`
}

type EnvironmentType struct {
	Name string `json:"name"`
}

func (c *Client) CreateEnvironment(ctx context.Context, repo, name, kind string) (Environment, error) {
	var env Environment
	body := struct {
		Type            string          `json:"type"`
		Name            string          `json:"name"`
		EnvironmentType EnvironmentType `json:"environment_type"`
	}{"deployment_environment", name, EnvironmentType{kind}}
	err := c.call(ctx, http.MethodPost, c.repoPath(repo, "environments"), body, &env)
	return env, err
}

func (c *Client) ListDeployments(ctx context.Context, repo, environment string, limit int) (Page[Deployment], error) {
	q := url.Values{"sort": {"-release.created_on"}, "pagelen": {strconv.Itoa(limit)}}
	page, err := readPage[Deployment](ctx, c, APIURL+c.repoPath(repo, "deployments")+"?"+q.Encode(), limit)
	if err != nil {
		return Page[Deployment]{}, err
	}
	out := make([]Deployment, 0, len(page.Values))
	for _, d := range page.Values {
		if strings.EqualFold(strings.Trim(d.Environment.UUID, "{}"), strings.Trim(environment, "{}")) {
			out = append(out, d)
		}
	}
	return Page[Deployment]{Values: out, More: page.Next != ""}, nil
}

// DeploymentState carries the deployment result under status, unlike a pipeline's result.
type DeploymentState struct {
	Name           string       `json:"name"`
	Status         *StateDetail `json:"status,omitempty"`
	StartDate      string       `json:"start_date,omitempty"`
	CompletionDate string       `json:"completion_date,omitempty"`
}
