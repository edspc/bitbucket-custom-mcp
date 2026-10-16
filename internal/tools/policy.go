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

// Policy is operator configuration, never a tool argument. Repository, project and ref
// lists allow all when nil and deny all when empty. With both repository and project lists,
// a repository either list allows is allowed. Tool lists use defaults when empty and accept
// "*" for every tool. Ref limits apply to runs and schedules.
type Policy struct {
	Repositories      []string
	Projects          []string // exact project keys or UUIDs, checked by the client against live repository metadata
	Tools             []string // empty allows all; otherwise exact names or "*"
	ConfirmationTools []string // empty confirms writes; otherwise exact names or "*"
	Refs              []string // exact "branch:<name>" or "tag:<name>" entries
}

func (p Policy) checkRepo(repo string) error {
	if repo == "." || repo == ".." || repo == "" || strings.Trim(repo, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") != "" {
		return errors.New("repo must be a repository slug, without path segments")
	}
	// With project limits, an unlisted repository may still be in an allowed project; the
	// Bitbucket client checks that before every request to it.
	if p.Repositories != nil && p.Projects == nil && !slices.Contains(p.Repositories, repo) {
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
	if len(p.Tools) > 0 && !slices.Contains(p.Tools, "*") && !slices.Contains(p.Tools, name) {
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
	if args.Scope == scopeWorkspace && (p.Repositories != nil || p.Projects != nil) {
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
		if !isWriteTool(req.Params.Name) && t.policy.requiresConfirmation(req.Params.Name) {
			page, err := t.claimSecrets(req, in)
			if err != nil {
				var out Out
				return nil, out, err
			}
			defer page.close()
			res, approved, err := t.confirmChange(ctx, req, in, page, in)
			if err != nil || res != nil {
				var out Out
				return res, out, err
			}
			if !approved {
				var out Out
				return nil, out, errors.New("the operation was not approved")
			}
		}
		return handler(ctx, req, in)
	}
}

func (p Policy) requiresConfirmation(name string) bool {
	if len(p.ConfirmationTools) > 0 {
		return slices.Contains(p.ConfirmationTools, "*") || slices.Contains(p.ConfirmationTools, name)
	}
	return isWriteTool(name)
}

func isWriteTool(name string) bool {
	switch name {
	case "link_repos_by_key", "link_repos_by_token", "set_variable", "delete_variable", "run_pipeline", "add_schedule", "delete_schedule", "create_deployment":
		return true
	}
	return false
}
