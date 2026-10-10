package bitbucket

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Pipeline is one run of a pipeline.
type Pipeline struct {
	UUID        string         `json:"uuid"`
	BuildNumber int            `json:"build_number"`
	State       State          `json:"state"`
	Target      PipelineTarget `json:"target"`
	CreatedOn   string         `json:"created_on"`
	CompletedOn string         `json:"completed_on"`
	Creator     struct {
		DisplayName string `json:"display_name"`
	} `json:"creator"`
}

// State is the state of a pipeline or a step: PENDING, READY, IN_PROGRESS with a stage such as
// RUNNING or PAUSED, or COMPLETED with a result such as SUCCESSFUL or FAILED.
type State struct {
	Name   string       `json:"name"`
	Stage  *StateDetail `json:"stage,omitempty"`
	Result *StateDetail `json:"result,omitempty"`
}

type StateDetail struct {
	Name  string `json:"name"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// String is the state with its stage or result, such as "COMPLETED/FAILED".
func (s State) String() string {
	switch {
	case s.Result != nil && s.Result.Name != "":
		return s.Name + "/" + s.Result.Name
	case s.Stage != nil && s.Stage.Name != "":
		return s.Name + "/" + s.Stage.Name
	}
	return s.Name
}

func (s State) Completed() bool { return s.Name == "COMPLETED" }

// Paused means the pipeline waits for someone to run a manual step.
func (s State) Paused() bool {
	return s.Name == "IN_PROGRESS" && s.Stage != nil && s.Stage.Name == "PAUSED"
}

// ErrorMessage is Bitbucket's explanation of an ERROR result, if any.
func (s State) ErrorMessage() string {
	if s.Result != nil && s.Result.Error != nil {
		return s.Result.Error.Message
	}
	return ""
}

// PipelineTarget is what a pipeline builds: a branch or tag, optionally at a commit, with the
// pipeline definition picked by Selector or by the ref.
type PipelineTarget struct {
	Type     string    `json:"type"`
	RefType  string    `json:"ref_type,omitempty"`
	RefName  string    `json:"ref_name,omitempty"`
	Commit   *Commit   `json:"commit,omitempty"`
	Selector *Selector `json:"selector,omitempty"`
}

// RefTarget is a target that builds the branch or tag refName, at commit when one is given.
func RefTarget(refType, refName, commit string) PipelineTarget {
	t := PipelineTarget{Type: "pipeline_ref_target", RefType: refType, RefName: refName}
	if commit != "" {
		t.Commit = &Commit{Type: "commit", Hash: commit}
	}
	return t
}

type Commit struct {
	Type string `json:"type,omitempty"`
	Hash string `json:"hash"`
}

// Selector picks a pipeline definition in bitbucket-pipelines.yml, such as {custom, deploy}.
type Selector struct {
	Type    string `json:"type"`
	Pattern string `json:"pattern,omitempty"`
}

// RunRequest starts a pipeline; Variables are passed to custom pipelines.
type RunRequest struct {
	Target    PipelineTarget `json:"target"`
	Variables []Variable     `json:"variables,omitempty"`
}

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

// pipelinesPath is the pipelines collection; it ends with a slash as in Bitbucket's examples.
func (c *Client) pipelinesPath(repo string) string { return c.repoPath(repo, "pipelines") + "/" }

func (c *Client) RunPipeline(ctx context.Context, repo string, r RunRequest) (Pipeline, error) {
	var p Pipeline
	err := c.call(ctx, http.MethodPost, c.pipelinesPath(repo), r, &p)
	return p, err
}

// ListPipelines returns up to limit latest pipelines of repo, newest first, on branch if set.
func (c *Client) ListPipelines(ctx context.Context, repo, branch string, limit int) ([]Pipeline, error) {
	q := url.Values{"sort": {"-created_on"}, "pagelen": {strconv.Itoa(limit)}}
	if branch != "" {
		q.Set("target.branch", branch)
	}
	var page struct {
		Values []Pipeline `json:"values"`
	}
	err := c.call(ctx, http.MethodGet, c.pipelinesPath(repo)+"?"+q.Encode(), nil, &page)
	return page.Values, err
}

// GetPipeline finds a pipeline of repo by its UUID or its build number.
func (c *Client) GetPipeline(ctx context.Context, repo, id string) (Pipeline, error) {
	var p Pipeline
	err := c.call(ctx, http.MethodGet, c.repoPath(repo, "pipelines", id), nil, &p)
	return p, err
}

// PipelineURL is the page of a pipeline run on the Bitbucket website.
func (c *Client) PipelineURL(repo string, buildNumber int) string {
	return c.repoWebURL(repo, "pipelines", "results", strconv.Itoa(buildNumber))
}
