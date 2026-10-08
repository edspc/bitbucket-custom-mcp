package bitbucket

import (
	"context"
	"net/http"
	"strconv"
	"strings"
)

// DeployKey is a repository Access Key.
type DeployKey struct {
	ID    int    `json:"id"`
	Key   string `json:"key"`
	Label string `json:"label"`
}

func (c *Client) ListDeployKeys(ctx context.Context, repo string) ([]DeployKey, error) {
	return list[DeployKey](ctx, c, c.repoPath(repo, "deploy-keys"))
}

func (c *Client) AddDeployKey(ctx context.Context, repo, key, label string) error {
	return c.call(ctx, http.MethodPost, c.repoPath(repo, "deploy-keys"), map[string]string{"key": key, "label": label}, nil)
}

func (c *Client) DeleteDeployKey(ctx context.Context, repo string, id int) error {
	return c.call(ctx, http.MethodDelete, c.repoPath(repo, "deploy-keys", strconv.Itoa(id)), nil, nil)
}

// SameKey compares two OpenSSH public keys by type and body, ignoring the comment.
func SameKey(a, b string) bool {
	fa, fb := strings.Fields(a), strings.Fields(b)
	return len(fa) >= 2 && len(fb) >= 2 && fa[0] == fb[0] && fa[1] == fb[1]
}
