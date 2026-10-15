package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type createDeploymentInput struct {
	Repo            string `json:"repo" jsonschema:"repository slug"`
	Name            string `json:"name" jsonschema:"new deployment environment name"`
	EnvironmentType string `json:"environment_type" jsonschema:"Test, Staging or Production"`
}

type createDeploymentResult struct {
	Status       string                 `json:"status"`
	ContentTrust string                 `json:"content_trust"`
	Environment  *bitbucket.Environment `json:"environment,omitempty"`
}

func (t *tools) createDeployment(ctx context.Context, req *mcp.CallToolRequest, in createDeploymentInput) (*mcp.CallToolResult, createDeploymentResult, error) {
	page, err := t.claimSecrets(req, in)
	if err != nil {
		return nil, createDeploymentResult{}, err
	}
	defer page.close()
	if in.Repo == "" || strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Name) != in.Name {
		return nil, createDeploymentResult{}, errors.New("repo and a nonempty name without surrounding whitespace are required")
	}
	switch in.EnvironmentType {
	case "Test", "Staging", "Production":
	default:
		return nil, createDeploymentResult{}, errors.New("environment_type must be Test, Staging or Production")
	}
	if err = t.checkPipelines(ctx, in.Repo); err != nil {
		return nil, createDeploymentResult{}, err
	}
	res, approved, err := t.confirmChange(ctx, req, in, page, in)
	if err != nil || res != nil {
		return res, createDeploymentResult{}, err
	}
	if !approved {
		return nil, createDeploymentResult{Status: "cancelled"}, nil
	}
	// Keep the duplicate check and creation together, after the browser wait.
	t.scheduleMu.Lock()
	defer t.scheduleMu.Unlock()
	checkCtx, cancel := context.WithDeadline(ctx, page.page.info.Until)
	defer cancel()
	envs, err := t.bb.ListEnvironments(checkCtx, in.Repo)
	if err != nil {
		return nil, createDeploymentResult{}, err
	}
	for _, e := range envs {
		if strings.EqualFold(e.Name, in.Name) {
			return nil, createDeploymentResult{}, fmt.Errorf("deployment environment %q already exists; nothing changed", in.Name)
		}
	}
	if err = approvalLive(page); err != nil {
		return nil, createDeploymentResult{}, err
	}
	env, err := t.bb.CreateEnvironment(bitbucket.WithWriteDeadline(ctx, page.page.info.Until), in.Repo, in.Name, in.EnvironmentType)
	if err != nil {
		return nil, createDeploymentResult{}, err
	}
	t.log.InfoContext(ctx, "created deployment environment", "repo", in.Repo, "environment", env.UUID)
	return nil, createDeploymentResult{Status: "created", ContentTrust: "untrusted", Environment: &env}, nil
}
