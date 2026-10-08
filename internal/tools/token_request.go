package tools

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxPendingPages bounds open token pages, each a listener and a goroutine, should a client
// keep calling the tool without coming back.
const maxPendingPages = 3

// pendingToken is a token page waiting for the call that asked for it to come back.
type pendingToken struct {
	in       linkByTokenInput
	page     *tokenPage
	stop     func() // closes the page; safe to call more than once
	deadline time.Time
	timer    *time.Timer
}

func sameRequest(a, b linkByTokenInput) bool {
	return a.SourceRepo == b.SourceRepo && a.TargetRepo == b.TargetRepo && a.VariableName == b.VariableName &&
		a.Scope == b.Scope && a.Environment == b.Environment && a.Force == b.Force &&
		slices.Equal(a.Permissions, b.Permissions)
}

// askToken opens a local token page and asks the client, through URL elicitation, to send the
// user there. The SDK delivers the request on any protocol version and calls the tool again
// with the user's consent in InputResponses and the elicitation ID in RequestState.
func (t *tools) askToken(req *mcp.CallToolRequest, in linkByTokenInput) (*mcp.CallToolResult, linkResult, error) {
	if caps := req.ClientCapabilities(); caps == nil || caps.Elicitation == nil || caps.Elicitation.URL == nil {
		return nil, linkResult{}, errors.New("the MCP client does not support URL elicitation (MCP 2025-11-25 or later), " +
			"which is needed to ask for the token without passing it through the client or the model; nothing changed")
	}
	page := newTokenPage(tokenPageInfo{
		SourceRepo:  in.SourceRepo,
		TokensPage:  t.bb.AccessTokensPage(in.SourceRepo),
		TokenName:   in.TargetRepo + "-pipelines",
		Permissions: strings.Join(in.Permissions, ", "),
		Variable:    in.VariableName,
		Place:       in.place(),
	})
	// The lock covers the limit check, the listener start and the registration, so neither the
	// limit nor the timer below can race with another call.
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.openPages >= maxPendingPages {
		return nil, linkResult{}, fmt.Errorf("%d token pages are already open; finish or decline them, or wait up to %s; nothing changed",
			t.openPages, t.tokenWait)
	}
	baseURL, stopServer, err := t.serve(page)
	if err != nil {
		return nil, linkResult{}, fmt.Errorf("starting the local token page: %w; nothing changed", err)
	}
	// A page counts against the limit until it is closed, also while a call waits on it.
	t.openPages++
	var once sync.Once
	stop := func() {
		once.Do(func() {
			stopServer()
			t.mu.Lock()
			t.openPages--
			t.mu.Unlock()
		})
	}
	id := rand.Text()
	// The page closes by itself if the call never comes back.
	t.pending[id] = &pendingToken{in: in, page: page, stop: stop, deadline: time.Now().Add(t.tokenWait),
		timer: time.AfterFunc(t.tokenWait, func() {
			if t.takePending(id) != nil {
				stop()
			}
		})}

	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{tokenInputID: &mcp.ElicitParams{
			Mode: "url",
			Message: fmt.Sprintf("Open a local page to paste a repository access token of %s for the secured variable %s in %s. "+
				"The token goes only to this MCP server.", in.SourceRepo, in.VariableName, in.place()),
			URL:           baseURL + page.path,
			ElicitationID: id,
		}},
		RequestState: id,
	}, linkResult{}, nil
}

func (t *tools) takePending(id string) *pendingToken {
	t.mu.Lock()
	p := t.pending[id]
	delete(t.pending, id)
	t.mu.Unlock()
	if p != nil {
		p.timer.Stop()
	}
	return p
}

// claimPage takes the page opened for this call when the call comes back. The caller must stop
// it on every path, so a failed call never leaves a page that accepts a token nobody reads.
func (t *tools) claimPage(id string, in linkByTokenInput) (*pendingToken, error) {
	p := t.takePending(id)
	if p == nil {
		return nil, errors.New("the token page for this call has expired or is unknown; call the tool again; nothing changed")
	}
	if !sameRequest(p.in, in) {
		p.stop()
		return nil, errors.New("the token page was opened for different arguments; call the tool again; nothing changed")
	}
	return p, nil
}

func accepted(resp mcp.InputResponse) bool {
	res, ok := resp.(*mcp.ElicitResult)
	return ok && res.Action == "accept"
}

// waitToken waits for the user to submit the token on page p.
func (t *tools) waitToken(ctx context.Context, req *mcp.CallToolRequest, p *pendingToken) (string, error) {
	// The page lives tokenWait from its opening, not from the call coming back.
	ctx, cancel := context.WithDeadline(ctx, p.deadline)
	defer cancel()
	select {
	case token := <-p.page.tokens:
		err := req.Session.NotifyElicitationComplete(ctx, &mcp.ElicitationCompleteParams{ElicitationID: req.Params.RequestState})
		if err != nil {
			t.log.WarnContext(ctx, "notifying the client that the token page is done", "error", err)
		}
		return token, nil
	case <-ctx.Done():
		return "", fmt.Errorf("no token was submitted on the token page (%w); nothing changed", ctx.Err())
	}
}
