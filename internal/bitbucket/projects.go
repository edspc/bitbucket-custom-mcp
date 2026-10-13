package bitbucket

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

type writeDeadlineKey struct{}

// WithWriteDeadline limits authorization checks before a write, leaving the write itself
// on the caller's context: cancelling it after sending cannot undo a change on Bitbucket.
func WithWriteDeadline(ctx context.Context, until time.Time) context.Context {
	return context.WithValue(ctx, writeDeadlineKey{}, until)
}

// WithAllowedProjects returns a separate client using the same transport and credentials.
// Nil allows all projects; an empty non-nil list denies all repository access.
func (c *Client) WithAllowedProjects(projects []string) *Client {
	restricted := *c
	restricted.allowedProjects = slices.Clone(projects)
	return &restricted
}

// CheckRepositoryProject uses live metadata: a cached membership could authorize a write
// after a repository has moved to a different project while browser approval was pending.
func (c *Client) CheckRepositoryProject(ctx context.Context, repo string) error {
	if c.allowedProjects == nil {
		return nil
	}
	if len(c.allowedProjects) == 0 {
		return errors.New("repository project is not allowed by the server policy")
	}
	var metadata struct {
		Project struct {
			Key  string `json:"key"`
			UUID string `json:"uuid"`
		} `json:"project"`
	}
	if err := c.sendUnchecked(ctx, http.MethodGet, APIURL+c.repoPath(repo)+"?fields=project.key,project.uuid", c.basicAuth, nil, &metadata); err != nil {
		return fmt.Errorf("checking repository project for %s: %w", repo, err)
	}
	if metadata.Project.Key != "" && slices.Contains(c.allowedProjects, metadata.Project.Key) ||
		metadata.Project.UUID != "" && slices.Contains(c.allowedProjects, metadata.Project.UUID) {
		return nil
	}
	return errors.New("repository project is not allowed by the server policy")
}

func (c *Client) checkProjectURL(ctx context.Context, rawURL string) error {
	if c.allowedProjects == nil || !inAPI(rawURL) {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/2.0/"), "/")
	if slices.Contains(parts, ".") || slices.Contains(parts, "..") {
		return errors.New("request is outside the projects allowed by the server policy")
	}
	// Projects are scoped to the configured workspace. Workspace variables cannot be
	// restricted to one project, and pagination or fork lookups must not escape it.
	if len(parts) < 3 || parts[0] != "repositories" || parts[1] != c.cfg.Workspace || parts[2] == "" {
		return errors.New("request is outside the projects allowed by the server policy")
	}
	return c.CheckRepositoryProject(ctx, parts[2])
}
