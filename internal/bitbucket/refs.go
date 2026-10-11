package bitbucket

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// PipelineCommit resolves the revision a pipeline is to run, before the user approves the run:
// the head of the branch or tag, or the given commit, which must be on that ref. The ref is
// looked up either way, so a ref that does not exist never reaches the approval page. The
// returned full SHA is sent to Bitbucket even if the ref moves during approval.
func (c *Client) PipelineCommit(ctx context.Context, repo, refType, refName, commit string) (string, error) {
	collection := "branches"
	if refType == "tag" {
		collection = "tags"
	} else if refType != "branch" {
		return "", fmt.Errorf("unsupported ref type %q", refType)
	}
	var ref struct {
		Target Commit `json:"target"`
	}
	if err := c.call(ctx, http.MethodGet, c.repoPath(repo, "refs", collection, refName), nil, &ref); err != nil {
		return "", err
	}
	head, err := fullSHA(ref.Target.Hash)
	if err != nil || commit == "" {
		return head, err
	}
	var revision Commit
	if err = c.call(ctx, http.MethodGet, c.repoPath(repo, "commit", commit), nil, &revision); err != nil {
		return "", err
	}
	hash, err := fullSHA(revision.Hash)
	if err != nil || hash == head {
		return hash, err
	}
	// Bitbucket builds the commit it is given in the context of the ref, whichever branch the
	// commit is on; a commit on another branch would run its own code as if it were the ref's.
	// The commits reachable from the commit but not from the head are none when it is on the ref.
	q := url.Values{"include": {hash}, "exclude": {head}, "pagelen": {"1"}}
	var page struct {
		Values []Commit `json:"values"`
	}
	if err = c.call(ctx, http.MethodGet, c.repoPath(repo, "commits")+"?"+q.Encode(), nil, &page); err != nil {
		return "", err
	}
	if len(page.Values) > 0 {
		return "", fmt.Errorf("commit %s is not on %s %q; nothing changed", hash, refType, refName)
	}
	return hash, nil
}

// fullSHA checks that Bitbucket answered with a full commit hash.
func fullSHA(hash string) (string, error) {
	if len(hash) != 40 || strings.Trim(hash, "0123456789abcdefABCDEF") != "" {
		return "", fmt.Errorf("bitbucket returned no full commit SHA for the pipeline; nothing changed")
	}
	return strings.ToLower(hash), nil
}
