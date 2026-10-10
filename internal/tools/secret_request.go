package tools

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// maxPendingPages bounds open secret pages, each a listener and a goroutine, should a client
	// keep calling tools without coming back.
	maxPendingPages = 3
	secretsInputID  = "secrets"
)

// pendingSecrets is a secret page waiting for the call that asked for it to come back.
type pendingSecrets struct {
	in    any // arguments of the call that opened the page
	page  *secretPage
	stop  func() // closes the page; safe to call more than once
	timer *time.Timer
}

// close closes the page of a call that came back; a first call has none.
func (p *pendingSecrets) close() {
	if p != nil {
		p.stop()
	}
}

// claimSecrets runs first in a tool that takes secrets on a local page. When the call comes
// back from the page, it claims the page opened for it; the caller must close the page on every
// path, so a failed call never leaves a page that accepts values nobody reads. On the first
// call it returns nil.
func (t *tools) claimSecrets(req *mcp.CallToolRequest, in any) (*pendingSecrets, error) {
	if _, cameBack := req.Params.InputResponses[secretsInputID]; !cameBack {
		return nil, nil
	}
	p := t.takePending(req.Params.RequestState)
	if p == nil {
		return nil, errors.New("the secret page for this call has expired or is unknown; call the tool again; nothing changed")
	}
	if !reflect.DeepEqual(p.in, in) {
		p.stop()
		return nil, errors.New("the secret page was opened for different arguments; call the tool again; nothing changed")
	}
	return p, nil
}

// askSecrets opens a local secret page and asks the client, through URL elicitation, to send
// the user there. The SDK delivers the request on any protocol version and calls the tool again
// with the user's consent in InputResponses and the elicitation ID in RequestState.
func (t *tools) askSecrets(req *mcp.CallToolRequest, in any, info secretPageInfo) (*mcp.CallToolResult, error) {
	if caps := req.ClientCapabilities(); caps == nil || caps.Elicitation == nil || caps.Elicitation.URL == nil {
		return nil, errors.New("the MCP client does not support URL elicitation (MCP 2025-11-25 or later), " +
			"which is needed to ask for secrets without passing them through the client or the model; nothing changed")
	}
	info.Tool = req.Params.Name
	info.Until = time.Now().Add(t.secretWait)
	page := newSecretPage(t.pageTemplate, info)
	// The lock covers the limit check, the listener start and the registration, so neither the
	// limit nor the timer below can race with another call.
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.openPages >= maxPendingPages {
		return nil, fmt.Errorf("%d secret pages are already open; finish or decline them, or wait up to %s; nothing changed",
			t.openPages, t.secretWait)
	}
	baseURL, stopServer, err := t.serve(page)
	if err != nil {
		return nil, fmt.Errorf("starting the local secret page: %w; nothing changed", err)
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
	t.pending[id] = &pendingSecrets{in: in, page: page, stop: stop,
		timer: time.AfterFunc(t.secretWait, func() {
			if t.takePending(id) != nil {
				stop()
			}
		})}

	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{secretsInputID: &mcp.ElicitParams{
			Mode: "url",
			Message: info.Need + " Open the local page of this MCP server to do it there: " +
				"what you enter goes only to this server, never to the MCP client or the model.",
			URL:           baseURL + page.path,
			ElicitationID: id,
		}},
		RequestState: id,
	}, nil
}

func (t *tools) takePending(id string) *pendingSecrets {
	t.mu.Lock()
	p := t.pending[id]
	delete(t.pending, id)
	t.mu.Unlock()
	if p != nil {
		p.timer.Stop()
	}
	return p
}

// accepted reports whether the user agreed to open the secret page.
func accepted(req *mcp.CallToolRequest) bool {
	res, ok := req.Params.InputResponses[secretsInputID].(*mcp.ElicitResult)
	return ok && res.Action == "accept"
}

// waitSecrets waits for the user to submit the values on page p, by field name.
func (t *tools) waitSecrets(ctx context.Context, req *mcp.CallToolRequest, p *pendingSecrets) (map[string]string, error) {
	// The page lives secretWait from its opening, not from the call coming back.
	ctx, cancel := context.WithDeadline(ctx, p.page.info.Until)
	defer cancel()
	select {
	case values := <-p.page.values:
		err := req.Session.NotifyElicitationComplete(ctx, &mcp.ElicitationCompleteParams{ElicitationID: req.Params.RequestState})
		if err != nil {
			t.log.WarnContext(ctx, "notifying the client that the secret page is done", "error", err)
		}
		return values, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("nothing was submitted on the secret page (%w); nothing changed", ctx.Err())
	}
}
