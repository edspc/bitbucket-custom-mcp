package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Policy is operator configuration, never a tool argument. Nil lists allow all within the
// configured workspace; non-nil empty lists deny all. Ref limits apply to runs and schedules.
type Policy struct {
	Repositories []string
	Tools        []string
	Refs         []string // exact "branch:<name>" or "tag:<name>" entries
}

func (p Policy) checkRepo(repo string) error {
	if repo == "." || repo == ".." || repo == "" || strings.Trim(repo, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") != "" {
		return errors.New("repo must be a repository slug, without path segments")
	}
	if p.Repositories != nil && !slices.Contains(p.Repositories, repo) {
		return errors.New("repository is not allowed by the server policy")
	}
	return nil
}

func (p Policy) checkRef(kind, name string) error {
	if p.Refs != nil && !slices.Contains(p.Refs, kind+":"+name) {
		return errors.New("ref is not allowed by the server policy")
	}
	return nil
}

func (p Policy) check(name string, in any) error {
	if p.Tools != nil && !slices.Contains(p.Tools, name) {
		return errors.New("tool is not allowed by the server policy")
	}
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	var args struct {
		Repo   string `json:"repo"`
		Source string `json:"source_repo"`
		Target string `json:"target_repo"`
		Scope  string `json:"scope"`
		Branch string `json:"branch"`
		Tag    string `json:"tag"`
	}
	if err = json.Unmarshal(data, &args); err != nil {
		return err
	}
	for _, repo := range []string{args.Repo, args.Source, args.Target} {
		if repo != "" {
			if err = p.checkRepo(repo); err != nil {
				return err
			}
		}
	}
	if args.Scope == scopeWorkspace && p.Repositories != nil {
		return errors.New("workspace variables are not allowed by the server policy when repository access is restricted")
	}
	if name == "run_pipeline" || name == "add_schedule" {
		if args.Branch != "" {
			return p.checkRef("branch", args.Branch)
		}
		if args.Tag != "" {
			return p.checkRef("tag", args.Tag)
		}
	}
	return nil
}

func guarded[In, Out any](t *tools, handler mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		if err := t.policy.check(req.Params.Name, in); err != nil {
			var out Out
			return nil, out, fmt.Errorf("%w; nothing changed", err)
		}
		return handler(ctx, req, in)
	}
}
