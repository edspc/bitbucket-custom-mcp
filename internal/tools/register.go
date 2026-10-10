// Package tools holds the MCP tools; every Bitbucket call goes through package bitbucket.
package tools

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

// tokenWait bounds how long a token page stays open for the user.
const tokenWait = 10 * time.Minute

type tools struct {
	bb        *bitbucket.Client
	log       *slog.Logger
	serve     func(http.Handler) (baseURL string, stop func(), err error) // serves a token page
	tokenWait time.Duration

	mu        sync.Mutex
	pending   map[string]*pendingToken // by elicitation ID, until the call comes back
	openPages int                      // pending pages plus pages a call is waiting on
}

func Register(s *mcp.Server, bb *bitbucket.Client, log *slog.Logger) {
	serve := func(h http.Handler) (string, func(), error) { return serveLocal(log, h) }
	register(s, &tools{bb: bb, log: log, serve: serve, tokenWait: tokenWait})
}

func register(s *mcp.Server, t *tools) {
	t.pending = map[string]*pendingToken{}
	mcp.AddTool(s, &mcp.Tool{
		Name: "link_repos_by_key",
		Description: "Let pipelines of source_repo reach target_repo over SSH: the public SSH key of " +
			"source_repo pipelines is added to the Access Keys of target_repo, labelled with the " +
			"source_repo slug. Both repositories must have pipelines enabled.",
	}, t.linkByKey)
	mcp.AddTool(s, &mcp.Tool{
		Name: "link_repos_by_token",
		Description: "Give pipelines of target_repo a repository access token of source_repo as a " +
			"secured pipeline variable, at repository level or for one deployment environment. " +
			"Bitbucket has no API to create the token, so the user creates it in the browser and " +
			"pastes it into a local page this server opens through URL elicitation; the token " +
			"never passes through the MCP client or the model. Both repositories must have " +
			"pipelines enabled.",
	}, t.linkByToken)
}
