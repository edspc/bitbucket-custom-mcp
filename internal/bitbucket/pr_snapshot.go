package bitbucket

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// DiffSnapshot pins the checked redirect, including its comparison mode, before a cursor is
// validated. Its target is private so callers cannot bypass the metadata check.
type DiffSnapshot struct {
	target  *url.URL
	path    string
	Version string
}

// ResolvePullRequestDiff checks the redirect against the metadata and resolves abbreviated
// hashes to full commits. Merged PRs may compare the merge commit instead of the source head.
func (c *Client) ResolvePullRequestDiff(ctx context.Context, repo string, pr PullRequest, kind string) (DiffSnapshot, error) {
	if pr.Source.Commit == nil || pr.Destination.Commit == nil {
		return DiffSnapshot{}, fmt.Errorf("bitbucket returned no commits for the pull request diff")
	}
	if !validDiffHash(pr.Source.Commit.Hash) || !validDiffHash(pr.Destination.Commit.Hash) ||
		pr.State == "MERGED" && pr.MergeCommit != nil && !validDiffHash(pr.MergeCommit.Hash) {
		return DiffSnapshot{}, fmt.Errorf("bitbucket returned an invalid commit SHA for the pull request diff")
	}
	path := c.pullRequestPath(repo, pr.ID, kind)
	target, err := c.redirectTarget(ctx, path)
	if err != nil {
		return DiffSnapshot{}, err
	}
	prefix := "/2.0" + c.repoPath(repo, kind) + "/"
	spec, ok := strings.CutPrefix(target.Path, prefix)
	separator := "\r"
	left, right, pair := strings.Cut(spec, separator)
	if !pair {
		separator = ".."
		left, right, pair = strings.Cut(spec, separator)
	}
	changed := fmt.Errorf("the pull request changed while resolving its %s; start again without a continuation", kind)
	if !ok || !pair {
		return DiffSnapshot{}, changed
	}
	base := c.cfg.Workspace + "/" + repo
	// Most metadata and redirect revisions are identical, so resolve each once per request.
	resolved := map[string]string{}
	resolve := func(repository, hash string) (string, error) {
		key := repository + ":" + hash
		if full, ok := resolved[key]; ok {
			return full, nil
		}
		full, err := c.pullRequestCommit(ctx, repository, hash)
		if err == nil {
			resolved[key] = full
		}
		return full, err
	}
	leftRepo, leftHash := diffRevision(left, base)
	rightRepo, rightHash := diffRevision(right, base)
	source, err := resolve(leftRepo, leftHash)
	if err != nil {
		return DiffSnapshot{}, err
	}
	destination, err := resolve(rightRepo, rightHash)
	if err != nil {
		return DiffSnapshot{}, err
	}
	expected, err := resolve(base, pr.Destination.Commit.Hash)
	if err != nil {
		return DiffSnapshot{}, err
	}
	if destination != expected {
		return DiffSnapshot{}, changed
	}
	// A merged pull request may compare its merge commit instead of the source head, which
	// is tried first then: a merged fork may have been deleted since the PR closed. Only a
	// redirect that names the merge commit costs a lookup of it.
	matched := false
	if pr.State == "MERGED" && pr.MergeCommit != nil && samePrefix(leftHash, pr.MergeCommit.Hash) {
		expected, err = resolve(base, pr.MergeCommit.Hash)
		if err != nil {
			return DiffSnapshot{}, err
		}
		matched = source == expected
	}
	if !matched {
		repository := pr.Source.Repository.FullName
		if repository == "" {
			repository = base
		}
		expected, err = resolve(repository, pr.Source.Commit.Hash)
		if err != nil {
			return DiffSnapshot{}, err
		}
		matched = source == expected
	}
	if !matched {
		return DiffSnapshot{}, changed
	}
	// Keep qualifiers for fork commits, but never request an abbreviated or mutable revision.
	if strings.Contains(left, ":") {
		source = leftRepo + ":" + source
	}
	if strings.Contains(right, ":") {
		destination = rightRepo + ":" + destination
	}
	target.Path, target.RawPath = prefix+source+separator+destination, ""
	target.RawQuery = target.Query().Encode()
	return DiffSnapshot{target: target, path: path, Version: target.String()}, nil
}

func diffRevision(spec, repository string) (string, string) {
	if i := strings.LastIndexByte(spec, ':'); i >= 0 {
		return spec[:i], spec[i+1:]
	}
	return repository, spec
}

func (c *Client) pullRequestCommit(ctx context.Context, repository, hash string) (string, error) {
	workspace, repo, ok := strings.Cut(repository, "/")
	if !ok || workspace == "" || repo == "" || workspace == "." || workspace == ".." || repo == "." || repo == ".." || strings.Contains(repo, "/") {
		return "", fmt.Errorf("bitbucket returned an invalid repository for the pull request diff")
	}
	if !validDiffHash(hash) {
		return "", fmt.Errorf("bitbucket returned an invalid commit SHA for the pull request diff")
	}
	if len(hash) == 40 {
		return fullSHA(hash)
	}
	var commit Commit
	if err := c.call(ctx, http.MethodGet, escapedPath("repositories", workspace, repo, "commit", hash), nil, &commit); err != nil {
		return "", err
	}
	full, err := fullSHA(commit.Hash)
	if err != nil || !strings.HasPrefix(full, strings.ToLower(hash)) {
		return "", fmt.Errorf("bitbucket returned no matching full commit SHA for the pull request diff")
	}
	return full, nil
}

// samePrefix reports whether two hashes, one perhaps abbreviated, may name the same commit.
func samePrefix(a, b string) bool {
	n := min(len(a), len(b))
	return strings.EqualFold(a[:n], b[:n])
}

func validDiffHash(hash string) bool {
	return len(hash) >= 12 && len(hash) <= 40 && strings.Trim(hash, "0123456789abcdefABCDEF") == ""
}
