package bitbucket

import (
	"context"
	"fmt"
	"net/http"
)

func (c *Client) PipelinesEnabled(ctx context.Context, repo string) (bool, error) {
	var cfg struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.call(ctx, http.MethodGet, c.repoPath(repo, "pipelines_config"), nil, &cfg); err != nil {
		return false, err
	}
	return cfg.Enabled, nil
}

// PipelineSSHPublicKey returns the public half of the key pair pipelines of repo use for SSH.
func (c *Client) PipelineSSHPublicKey(ctx context.Context, repo string) (string, error) {
	var pair struct {
		PublicKey string `json:"public_key"`
	}
	err := c.call(ctx, http.MethodGet, c.repoPath(repo, "pipelines_config", "ssh", "key_pair"), nil, &pair)
	if IsStatus(err, http.StatusNotFound) || err == nil && pair.PublicKey == "" {
		return "", fmt.Errorf("repository %s has no pipelines SSH key; generate one in Repository settings > Pipelines > SSH keys", repo)
	}
	if err != nil {
		return "", err
	}
	return pair.PublicKey, nil
}
