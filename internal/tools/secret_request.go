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
	// Active pages and retained listeners have separate limits: declining a page must not
	// allow unbounded listeners to accumulate during the protection against port takeover.
	maxPendingPages  = 3
	maxPageListeners = 32
	secretsInputID   = "secrets"
	// multiRoundTripVersion is the MCP version from which clients call a tool again themselves.
	multiRoundTripVersion = "2026-07-28"
)

// pendingSecrets is a secret page waiting for the call that asked for it to come back.
type pendingSecrets struct {
	automatic bool // operator policy allows this call without browser confirmation
	in        any  // arguments of the call that opened the page
	page      *secretPage
	stop      func() // closes the page; safe to call more than once
	timer     *time.Timer
	tool      string
	session   *mcp.ServerSession
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
		if !t.policy.requiresConfirmation(req.Params.Name) && !needsSecrets(in) {
			return &pendingSecrets{automatic: true, stop: func() {}, page: &secretPage{info: secretPageInfo{Until: time.Now().Add(t.secretWait)}}}, nil
		}
		return nil, nil
	}
	p := t.takePending(req.Params.RequestState)
	if p == nil {
		return nil, errors.New("the secret page for this call has expired or is unknown; call the tool again; nothing changed")
	}
	if p.tool != req.Params.Name || p.session != req.Session || !reflect.DeepEqual(p.in, in) {
		p.stop()
		return nil, errors.New("the secret page was opened for different arguments; call the tool again; nothing changed")
	}
	return p, nil
}

// askSecrets opens a local secret page and asks the client, through URL elicitation, to send
// the user there. The SDK delivers the request on any protocol version and calls the tool again
// with the user's consent in InputResponses and the elicitation ID in RequestState.
func (t *tools) askSecrets(ctx context.Context, req *mcp.CallToolRequest, in any, info secretPageInfo) (*mcp.CallToolResult, error) {
	if caps := req.ClientCapabilities(); caps == nil || caps.Elicitation == nil || caps.Elicitation.URL == nil {
		return nil, errors.New("the MCP client does not support URL elicitation (MCP 2025-11-25 or later), " +
			"which is needed to confirm changes and enter secrets outside the client and model; nothing changed")
	}
	info.Tool = req.Params.Name
	info.Workspace = t.bb.Workspace()
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
	if t.pageListeners >= maxPageListeners {
		return nil, errors.New("the local approval listener limit has been reached; wait for earlier pages to retire; nothing changed")
	}
	baseURL, closed, err := t.serve(page, info.Until)
	if err != nil {
		return nil, fmt.Errorf("starting the local secret page: %w; nothing changed", err)
	}
	t.pageListeners++
	go func() {
		<-closed
		t.mu.Lock()
		t.pageListeners--
		t.mu.Unlock()
	}()
	// A page counts against the limit until it is closed, also while a call waits on it.
	t.openPages++
	var once sync.Once
	stop := func() {
		once.Do(func() {
			page.close()
			t.mu.Lock()
			t.openPages--
			t.mu.Unlock()
		})
	}
	id := rand.Text()
	// The page closes by itself if the call never comes back.
	t.pending[id] = &pendingSecrets{in: in, page: page, stop: stop, tool: req.Params.Name, session: req.Session,
		timer: time.AfterFunc(t.secretWait, func() {
			if t.takePending(id) != nil {
				stop()
			}
		})}
	// Before MCP 2026-07-28 the SDK asks the client within this same call and then calls the tool
	// once more, which claims the page. A call that ends before that, because the user cancelled
	// it or the client could not show the page, never comes back: its page closes with it.
	if ip := req.Session.InitializeParams(); ip != nil && ip.ProtocolVersion < multiRoundTripVersion {
		context.AfterFunc(ctx, func() {
			if t.takePending(id) != nil {
				stop()
			}
		})
	}

	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{secretsInputID: &mcp.ElicitParams{
			Mode: "url",
			Message: info.Need + " Review and confirm the exact operation on the local page of this MCP server: " +
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
func accepted(req *mcp.CallToolRequest, pages ...*pendingSecrets) bool {
	if len(pages) > 0 && pages[0] != nil && pages[0].automatic {
		return true
	}
	res, ok := req.Params.InputResponses[secretsInputID].(*mcp.ElicitResult)
	return ok && res.Action == "accept"
}

// waitSecrets waits for the user to submit the values on page p, by field name.
func (t *tools) waitSecrets(ctx context.Context, req *mcp.CallToolRequest, p *pendingSecrets) (map[string]string, error) {
	if p.automatic {
		return map[string]string{}, nil
	}
	// The page lives secretWait from its opening, not from the call coming back.
	ctx, cancel := context.WithDeadline(ctx, p.page.info.Until)
	defer cancel()
	select {
	case values := <-p.page.values:
		// A submitted form and an expired context can both be ready. Receiving the form
		// must not extend approval past its deadline while upstream preflight runs.
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("the secret page approval expired or was cancelled (%w); nothing changed", err)
		}
		if !time.Now().Before(p.page.info.Until) {
			return nil, fmt.Errorf("the secret page approval expired (%w); nothing changed", context.DeadlineExceeded)
		}
		err := req.Session.NotifyElicitationComplete(ctx, &mcp.ElicitationCompleteParams{ElicitationID: req.Params.RequestState})
		if err != nil {
			t.log.WarnContext(ctx, "notifying the client that the secret page is done", "error", err)
		}
		if err = ctx.Err(); err != nil {
			return nil, fmt.Errorf("the secret page approval expired or was cancelled (%w); nothing changed", err)
		}
		return values, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("nothing was submitted on the secret page (%w); nothing changed", ctx.Err())
	}
}

func needsSecrets(in any) bool {
	switch in := in.(type) {
	case linkByTokenInput:
		return true
	case setVariableInput:
		return in.Secured
	case runPipelineInput:
		return len(in.SecretVariables) > 0
	}
	return false
}
