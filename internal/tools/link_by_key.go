package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

type linkByKeyInput struct {
	SourceRepo string `json:"source_repo" jsonschema:"slug of the repository whose pipelines SSH key is used"`
	TargetRepo string `json:"target_repo" jsonschema:"slug of the repository that gets the key in its Access Keys"`
	Force      bool   `json:"force,omitempty" jsonschema:"replace an Access Key that already carries the source_repo label but holds a different key"`
}

func (t *tools) linkByKey(ctx context.Context, req *mcp.CallToolRequest, in linkByKeyInput) (*mcp.CallToolResult, statusResult, error) {
	page, err := t.claimSecrets(req, in)
	if err != nil {
		return nil, statusResult{}, err
	}
	defer page.close()
	if err = checkRepoPair(in.SourceRepo, in.TargetRepo); err != nil {
		return nil, statusResult{}, err
	}
	if err = t.checkPipelines(ctx, in.SourceRepo, in.TargetRepo); err != nil {
		return nil, statusResult{}, err
	}
	link, err := t.planKeyLink(ctx, in)
	if err != nil || link.linked != nil {
		return nil, link.alreadyLinked(in), err
	}
	res, approved, err := t.confirmChange(ctx, req, in, page, struct {
		Arguments linkByKeyInput `json:"arguments"`
		keyLink
	}{in, link})
	if err != nil || res != nil {
		return res, statusResult{}, err
	}
	if !approved {
		return nil, statusResult{Status: "cancelled", Message: "the operation was not approved; nothing changed"}, nil
	}
	// The browser wait may have taken minutes: the source key may have been rotated, or the
	// labelled target key replaced, so both are read again and must be as approved.
	checkCtx, cancel := context.WithDeadline(ctx, page.page.info.Until)
	defer cancel()
	now, err := t.planKeyLink(checkCtx, in)
	if err != nil || now.linked != nil {
		return nil, now.alreadyLinked(in), err
	}
	if !now.same(link) {
		return nil, statusResult{}, errors.New("the keys changed while awaiting approval; call the tool again; nothing changed")
	}
	if err = approvalLive(page); err != nil {
		return nil, statusResult{}, err
	}
	// Add before deleting, so a failure never leaves the target without a key for the source.
	if err = t.bb.AddDeployKey(ctx, in.TargetRepo, link.PublicKey, in.SourceRepo); err != nil {
		return nil, statusResult{}, err
	}
	status := "added"
	if old := link.ReplacedKey; old != nil {
		// A second write, so the approval must still hold for it: only a write already sent
		// may outlive the deadline.
		if err = approvalLive(page); err != nil {
			// A new call finds the key linked and changes nothing, so the old key is the user's to remove.
			return nil, statusResult{}, fmt.Errorf("the new key was added to %s, but the approval expired before the old Access Key %q (id %d) was removed (%w); "+
				"remove it in Bitbucket under Repository settings > Access keys of %s",
				in.TargetRepo, in.SourceRepo, old.ID, context.DeadlineExceeded, in.TargetRepo)
		}
		if err = t.bb.DeleteDeployKey(ctx, in.TargetRepo, old.ID); err != nil {
			return nil, statusResult{}, fmt.Errorf("the new key was added to %s, but removing the old Access Key %q (id %d) failed: %w",
				in.TargetRepo, in.SourceRepo, old.ID, err)
		}
		status = "replaced"
	}
	t.log.InfoContext(ctx, "linked repositories by key", "source", in.SourceRepo, "target", in.TargetRepo, "status", status)
	return nil, statusResult{
		Status:  status,
		Message: fmt.Sprintf("pipelines SSH key of %s %s in Access Keys of %s with label %q", in.SourceRepo, status, in.TargetRepo, in.SourceRepo),
	}, nil
}

// keyLink is what linking the repositories by key changes: the public key of the source
// pipelines to add, and the Access Key labelled with the source that it replaces, if any.
type keyLink struct {
	PublicKey   string               `json:"public_key"`
	ReplacedKey *bitbucket.DeployKey `json:"replaced_key,omitempty"`
	linked      *bitbucket.DeployKey // an Access Key of the target that already holds the key
}

// planKeyLink reads both repositories and tells what linking them changes.
func (t *tools) planKeyLink(ctx context.Context, in linkByKeyInput) (keyLink, error) {
	pub, err := t.bb.PipelineSSHPublicKey(ctx, in.SourceRepo)
	if err != nil {
		return keyLink{}, err
	}
	keys, err := t.bb.ListDeployKeys(ctx, in.TargetRepo)
	if err != nil {
		return keyLink{}, err
	}
	link := keyLink{PublicKey: pub}
	for i, k := range keys {
		if bitbucket.SameKey(k.Key, pub) {
			return keyLink{linked: &keys[i]}, nil
		}
		if k.Label == in.SourceRepo {
			link.ReplacedKey = &keys[i]
		}
	}
	if link.ReplacedKey != nil && !in.Force {
		return keyLink{}, fmt.Errorf("%s already has an Access Key labelled %q with a different key; nothing changed. Pass force=true to replace it",
			in.TargetRepo, in.SourceRepo)
	}
	return link, nil
}

func (l keyLink) same(o keyLink) bool {
	return l.PublicKey == o.PublicKey && (l.ReplacedKey == nil) == (o.ReplacedKey == nil) &&
		(l.ReplacedKey == nil || *l.ReplacedKey == *o.ReplacedKey)
}

// alreadyLinked is the result when the target already holds the key; zero otherwise.
func (l keyLink) alreadyLinked(in linkByKeyInput) statusResult {
	if l.linked == nil {
		return statusResult{}
	}
	return statusResult{
		Status:  "already_linked",
		Message: fmt.Sprintf("%s already has the pipelines SSH key of %s (Access Key %q); nothing changed", in.TargetRepo, in.SourceRepo, l.linked.Label),
	}
}
