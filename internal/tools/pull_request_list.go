package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

type listPullRequestsInput struct {
	Repo              string `json:"repo" jsonschema:"repository slug"`
	State             string `json:"state,omitempty" jsonschema:"OPEN when omitted, MERGED, DECLINED, SUPERSEDED, or ALL for every state"`
	SourceBranch      string `json:"source_branch,omitempty" jsonschema:"only pull requests from this branch"`
	DestinationBranch string `json:"destination_branch,omitempty" jsonschema:"only pull requests into this branch"`
	Limit             int    `json:"limit,omitempty" jsonschema:"how many pull requests a page holds; 10 when omitted, at most 50"`
	Page              int    `json:"page,omitempty" jsonschema:"page to return, from 1 for the most recently updated pull requests; 1 when omitted. Pass next_page of the previous result, with the same filters and limit"`
}

// states names the states to list as Bitbucket does.
func (in listPullRequestsInput) states() ([]string, error) {
	switch state := strings.ToUpper(in.State); state {
	case "":
		return []string{"OPEN"}, nil
	case "ALL":
		return []string{"OPEN", "MERGED", "DECLINED", "SUPERSEDED"}, nil
	case "OPEN", "MERGED", "DECLINED", "SUPERSEDED":
		return []string{state}, nil
	}
	return nil, fmt.Errorf("state must be OPEN, MERGED, DECLINED, SUPERSEDED or ALL, got %q", in.State)
}

type listPullRequestsResult struct {
	PullRequests []pullRequestSummary `json:"pull_requests" jsonschema:"the most recently updated first; get_pull_request shows the description, reviewers, builds and changed files of one, and their diffs"`
	NextPage     int                  `json:"next_page,omitempty" jsonschema:"page with less recently updated pull requests, passed back as page; omitted on the last page"`
	Total        *int                 `json:"total,omitempty" jsonschema:"how many pull requests the filters pick in all, when Bitbucket reports it"`
}

func (t *tools) listPullRequests(ctx context.Context, _ *mcp.CallToolRequest, in listPullRequestsInput) (*mcp.CallToolResult, listPullRequestsResult, error) {
	if in.Repo == "" {
		return nil, listPullRequestsResult{}, errors.New("repo is required")
	}
	states, err := in.states()
	if err != nil {
		return nil, listPullRequestsResult{}, err
	}
	limit, err := optionalCount("limit", in.Limit, defaultListLimit, maxListLimit)
	if err != nil {
		return nil, listPullRequestsResult{}, err
	}
	page, err := optionalPage(in.Page)
	if err != nil {
		return nil, listPullRequestsResult{}, err
	}
	prs, err := t.bb.ListPullRequests(ctx, in.Repo, bitbucket.PullRequestFilter{
		States: states, SourceBranch: in.SourceBranch, DestinationBranch: in.DestinationBranch,
	}, page, limit)
	if err != nil {
		return nil, listPullRequestsResult{}, err
	}
	views := make([]pullRequestSummary, 0, len(prs.Values))
	for _, pr := range prs.Values {
		views = append(views, t.summarizePullRequest(in.Repo, pr))
	}
	return nil, listPullRequestsResult{PullRequests: views, NextPage: nextPage(page, prs.More), Total: prs.Total}, nil
}
