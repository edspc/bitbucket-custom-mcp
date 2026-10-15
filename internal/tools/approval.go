package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// confirmChange requires a browser submission, not merely an MCP client's claim of consent.
// Derived details are compared on retry so changes during review require a new approval.
func (t *tools) confirmChange(ctx context.Context, req *mcp.CallToolRequest, in any, page *pendingSecrets, details any) (*mcp.CallToolResult, bool, error) {
	args, err := json.MarshalIndent(details, "", "  ")
	if err != nil {
		return nil, false, err
	}
	if page == nil {
		res, err := t.askSecrets(ctx, req, in, secretPageInfo{
			Title:     "Confirm a Bitbucket change",
			Need:      "Review the arguments and confirm this operation only if you requested it.",
			Outcome:   "Confirmation authorizes only this one operation with the displayed arguments.",
			Arguments: string(args),
		})
		return res, false, err
	}
	if !page.automatic && page.page.info.Arguments != string(args) {
		return nil, false, errors.New("the operation changed while awaiting approval; call the tool again; nothing changed")
	}
	if !accepted(req, page) {
		return nil, false, nil
	}
	_, err = t.waitSecrets(ctx, req, page)
	return nil, err == nil, err
}

// approvalLive checks, right before a write, that the approval on page p has not run out while
// its target was rechecked. The write itself runs with the call's context, not the approval's:
// once sent, cancelling it cannot undo it on Bitbucket, only report a failure for a change that
// was made.
func approvalLive(p *pendingSecrets) error {
	if !time.Now().Before(p.page.info.Until) {
		return fmt.Errorf("the approval expired (%w); nothing changed", context.DeadlineExceeded)
	}
	return nil
}
