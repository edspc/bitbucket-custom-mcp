package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

type linkByKeyInput struct {
	SourceRepo string `json:"source_repo" jsonschema:"slug of the repository whose pipelines SSH key is used"`
	TargetRepo string `json:"target_repo" jsonschema:"slug of the repository that gets the key in its Access Keys"`
	Force      bool   `json:"force,omitempty" jsonschema:"replace an Access Key that already carries the source_repo label but holds a different key"`
}

type linkResult struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (t *tools) linkByKey(ctx context.Context, _ *mcp.CallToolRequest, in linkByKeyInput) (*mcp.CallToolResult, linkResult, error) {
	if err := checkRepoPair(in.SourceRepo, in.TargetRepo); err != nil {
		return nil, linkResult{}, err
	}
	if err := t.checkPipelines(ctx, in.SourceRepo, in.TargetRepo); err != nil {
		return nil, linkResult{}, err
	}
	pub, err := t.bb.PipelineSSHPublicKey(ctx, in.SourceRepo)
	if err != nil {
		return nil, linkResult{}, err
	}
	keys, err := t.bb.ListDeployKeys(ctx, in.TargetRepo)
	if err != nil {
		return nil, linkResult{}, err
	}

	var labelled *bitbucket.DeployKey
	for i, k := range keys {
		if bitbucket.SameKey(k.Key, pub) {
			return nil, linkResult{
				Status:  "already_linked",
				Message: fmt.Sprintf("%s already has the pipelines SSH key of %s (Access Key %q); nothing changed", in.TargetRepo, in.SourceRepo, k.Label),
			}, nil
		}
		if k.Label == in.SourceRepo {
			labelled = &keys[i]
		}
	}

	if labelled != nil && !in.Force {
		return nil, linkResult{}, fmt.Errorf("%s already has an Access Key labelled %q with a different key; nothing changed. Pass force=true to replace it",
			in.TargetRepo, in.SourceRepo)
	}
	// Add before deleting, so a failure never leaves the target without a key for the source.
	if err = t.bb.AddDeployKey(ctx, in.TargetRepo, pub, in.SourceRepo); err != nil {
		return nil, linkResult{}, err
	}
	status := "added"
	if labelled != nil {
		if err = t.bb.DeleteDeployKey(ctx, in.TargetRepo, labelled.ID); err != nil {
			return nil, linkResult{}, fmt.Errorf("the new key was added to %s, but removing the old Access Key %q (id %d) failed: %w",
				in.TargetRepo, in.SourceRepo, labelled.ID, err)
		}
		status = "replaced"
	}
	t.log.InfoContext(ctx, "linked repositories by key", "source", in.SourceRepo, "target", in.TargetRepo, "status", status)
	return nil, linkResult{
		Status:  status,
		Message: fmt.Sprintf("pipelines SSH key of %s %s in Access Keys of %s with label %q", in.SourceRepo, status, in.TargetRepo, in.SourceRepo),
	}, nil
}
