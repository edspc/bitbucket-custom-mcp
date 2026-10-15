package tools

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const scopeURIPrefix = "bitbucket://pipeline-scopes/"
const scopeURITemplate = scopeURIPrefix + "{repo}/{kind}/{selector}"

func parseScopeURI(uri string) (repo, kind, selector string, ok bool) {
	rest, ok := strings.CutPrefix(uri, scopeURIPrefix)
	if !ok {
		return "", "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return "", "", "", false
	}
	selector, err := url.PathUnescape(parts[2])
	if err != nil || selector == "" {
		return "", "", "", false
	}
	switch parts[1] {
	case "branch":
	case "pullrequest":
		id, err := strconv.Atoi(selector)
		if err != nil || id <= 0 || strconv.Itoa(id) != selector {
			return "", "", "", false
		}
	case "deployment":
		id := strings.Trim(selector, "{}")
		if len(id) != 36 {
			return "", "", "", false
		}
		for i, c := range id {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				if c != '-' {
					return "", "", "", false
				}
			} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return "", "", "", false
			}
		}
	default:
		return "", "", "", false
	}
	return parts[0], parts[1], selector, true
}

// scopeSnapshot is bounded to ten latest records so following a busy branch never downloads
// its entire history. It keeps polling after completion to discover future runs.
func (t *tools) scopeSnapshot(ctx context.Context, uri string) (string, error) {
	repo, kind, selector, ok := parseScopeURI(uri)
	if !ok {
		return "", mcp.ResourceNotFoundError(uri)
	}
	if err := t.policy.checkRepo(repo); err != nil {
		return "", err
	}
	if err := t.policy.check("get_pipeline", pipelineRef{Repo: repo}); err != nil {
		return "", err
	}
	var data any
	switch kind {
	case "branch":
		ps, err := t.bb.ListPipelines(ctx, repo, selector, 1, defaultListLimit)
		if err != nil {
			return "", err
		}
		// The resource is the latest page only: it has no next_page to follow, and its content
		// stays what subscribers already compare.
		result := listPipelinesResult{Pipelines: make([]pipelineView, 0, len(ps.Values))}
		for _, p := range ps.Values {
			result.Pipelines = append(result.Pipelines, t.viewPipeline(repo, p, nil))
		}
		data = result
	case "pullrequest":
		id, _ := strconv.Atoi(selector)
		statuses, err := t.bb.PullRequestStatuses(ctx, repo, id, defaultListLimit)
		if err != nil {
			return "", err
		}
		data = struct {
			ContentTrust string `json:"content_trust"`
			Builds       any    `json:"builds"`
			Truncated    bool   `json:"truncated"`
		}{"untrusted", statuses.Values, statuses.More}
	case "deployment":
		ds, err := t.bb.ListDeployments(ctx, repo, braced(selector), maxListLimit)
		if err != nil {
			return "", err
		}
		data = struct {
			ContentTrust string                 `json:"content_trust"`
			Deployments  []bitbucket.Deployment `json:"deployments"`
			WindowLimit  int                    `json:"window_limit"`
			Truncated    bool                   `json:"truncated"`
		}{"untrusted", ds.Values, maxListLimit, ds.More}
	}
	b, err := json.Marshal(data)
	return string(b), err
}

func (t *tools) readScope(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	text, err := t.scopeSnapshot(ctx, req.Params.URI)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "application/json", Text: text}}}, nil
}

func (t *tools) subscriptionSnapshot(ctx context.Context, uri string) (bool, string, error) {
	if strings.HasPrefix(uri, scopeURIPrefix) {
		text, err := t.scopeSnapshot(ctx, uri)
		return false, text, err
	}
	p, view, err := t.lookPipeline(ctx, uri)
	if err != nil {
		return false, "", err
	}
	b, err := json.Marshal(view)
	return settled(p.State), string(b), err
}
