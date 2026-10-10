package bitbucket

import (
	"context"
	"net/http"
)

// Schedule runs a pipeline of a branch by a cron pattern.
type Schedule struct {
	Type        string         `json:"type,omitempty"`
	UUID        string         `json:"uuid,omitempty"`
	Enabled     bool           `json:"enabled"`
	Target      PipelineTarget `json:"target"`
	CronPattern string         `json:"cron_pattern"`
	CreatedOn   string         `json:"created_on,omitempty"`
	UpdatedOn   string         `json:"updated_on,omitempty"`
}

func (c *Client) ListSchedules(ctx context.Context, repo string) ([]Schedule, error) {
	return list[Schedule](ctx, c, c.repoPath(repo, "pipelines_config", "schedules"))
}

func (c *Client) CreateSchedule(ctx context.Context, repo string, s Schedule) (Schedule, error) {
	s.Type = "pipeline_schedule"
	var created Schedule
	err := c.call(ctx, http.MethodPost, c.repoPath(repo, "pipelines_config", "schedules"), s, &created)
	return created, err
}

func (c *Client) DeleteSchedule(ctx context.Context, repo, uuid string) error {
	return c.call(ctx, http.MethodDelete, c.repoPath(repo, "pipelines_config", "schedules", uuid), nil, nil)
}
