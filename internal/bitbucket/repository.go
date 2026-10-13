package bitbucket

import (
	"context"
	"errors"
	"net/http"
)

func (c *Client) RepositoryUUID(ctx context.Context, repo string) (string, error) {
	var metadata struct {
		UUID string `json:"uuid"`
	}
	if err := c.call(ctx, http.MethodGet, c.repoPath(repo)+"?fields=uuid", nil, &metadata); err != nil {
		return "", err
	}
	if metadata.UUID == "" {
		return "", errors.New("bitbucket returned no repository UUID")
	}
	return metadata.UUID, nil
}
