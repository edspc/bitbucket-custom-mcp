package bitbucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// PullRequest asks to merge the changes on a source branch into a destination branch.
type PullRequest struct {
	ID      int    `json:"id"`
	Title   string `json:"title"`
	Summary struct {
		Raw string `json:"raw"`
	} `json:"summary"`
	State             string         `json:"state"`
	Draft             bool           `json:"draft"`
	Author            Account        `json:"author"`
	Source            PullRequestEnd `json:"source"`
	Destination       PullRequestEnd `json:"destination"`
	CloseSourceBranch bool           `json:"close_source_branch"`
	MergeCommit       *Commit        `json:"merge_commit"`
	ClosedBy          *Account       `json:"closed_by"`
	Reason            string         `json:"reason"` // why it was declined
	CreatedOn         string         `json:"created_on"`
	UpdatedOn         string         `json:"updated_on"`
	CommentCount      int            `json:"comment_count"`
	TaskCount         int            `json:"task_count"` // open tasks
	Participants      []Participant  `json:"participants"`
}

// PullRequestEnd is the source or the destination of a pull request.
type PullRequestEnd struct {
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
	Commit     *Commit `json:"commit"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// Participant is a reviewer of a pull request, or someone else who took part in it.
type Participant struct {
	User     Account `json:"user"`
	Role     string  `json:"role"` // REVIEWER or PARTICIPANT
	Approved bool    `json:"approved"`
	State    string  `json:"state"` // approved, changes_requested or empty
}

// CommitStatus is a result reported for a commit, such as a pipeline run.
type CommitStatus struct {
	Name        string `json:"name"`
	State       string `json:"state"` // SUCCESSFUL, FAILED, INPROGRESS or STOPPED
	Description string `json:"description"`
	URL         string `json:"url"`
}

// DiffStat is how a change affects one file.
type DiffStat struct {
	Status       string      `json:"status"` // added, removed, modified, renamed, or a conflict
	LinesAdded   int         `json:"lines_added"`
	LinesRemoved int         `json:"lines_removed"`
	Old          *CommitFile `json:"old"` // nil for an added file
	New          *CommitFile `json:"new"` // nil for a removed file
}

type CommitFile struct {
	Path string `json:"path"`
}

func (c *Client) pullRequestPath(repo string, id int, parts ...string) string {
	return c.repoPath(repo, append([]string{"pullrequests", strconv.Itoa(id)}, parts...)...)
}

// PullRequestFilter picks the pull requests to list.
type PullRequestFilter struct {
	States            []string // OPEN, MERGED, DECLINED or SUPERSEDED; Bitbucket lists open ones when empty
	SourceBranch      string
	DestinationBranch string
}

// ListPullRequests returns up to limit pull requests of repo, at most 50, that f picks, the most
// recently updated first. Listed pull requests have no participants.
func (c *Client) ListPullRequests(ctx context.Context, repo string, f PullRequestFilter, limit int) ([]PullRequest, error) {
	q := url.Values{"sort": {"-updated_on"}, "pagelen": {strconv.Itoa(limit)}, "state": f.States}
	var conditions []string
	if f.SourceBranch != "" {
		conditions = append(conditions, "source.branch.name = "+queryString(f.SourceBranch))
	}
	if f.DestinationBranch != "" {
		conditions = append(conditions, "destination.branch.name = "+queryString(f.DestinationBranch))
	}
	if len(conditions) > 0 {
		q.Set("q", strings.Join(conditions, " AND "))
	}
	var page struct {
		Values []PullRequest `json:"values"`
	}
	err := c.call(ctx, http.MethodGet, c.repoPath(repo, "pullrequests")+"?"+q.Encode(), nil, &page)
	return page.Values, err
}

// queryString quotes s for Bitbucket's filter language, so a name cannot end the string early.
func queryString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (c *Client) GetPullRequest(ctx context.Context, repo string, id int) (PullRequest, error) {
	var pr PullRequest
	err := c.call(ctx, http.MethodGet, c.pullRequestPath(repo, id), nil, &pr)
	return pr, err
}

// PullRequestStatuses lists the build results reported for a pull request.
func (c *Client) PullRequestStatuses(ctx context.Context, repo string, id int) ([]CommitStatus, error) {
	return list[CommitStatus](ctx, c, c.pullRequestPath(repo, id, "statuses"))
}

// PullRequestDiffStat lists the files a pull request changes.
func (c *Client) PullRequestDiffStat(ctx context.Context, repo string, id int) ([]DiffStat, error) {
	path := c.pullRequestPath(repo, id, "diffstat")
	target, err := c.redirectTarget(ctx, path)
	if err != nil {
		return nil, err
	}
	return listFrom[DiffStat](ctx, c, path, target.String())
}

// PullRequestDiff returns the git-style diff of a pull request, limited to paths when any are
// given. It returns at most maxBytes from offset on, and whether more followed.
func (c *Client) PullRequestDiff(ctx context.Context, repo string, id int, paths []string, offset, maxBytes int) ([]byte, bool, error) {
	// The pull request only redirects to the diff of its commits, which takes the paths.
	path := c.pullRequestPath(repo, id, "diff")
	target, err := c.redirectTarget(ctx, path)
	if err != nil {
		return nil, false, err
	}
	if len(paths) > 0 {
		q := target.Query()
		q["path"] = paths
		target.RawQuery = q.Encode()
	}
	resp, err := c.get(ctx, target.String(), c.basicAuth, nil)
	if err != nil {
		return nil, false, fmt.Errorf("bitbucket GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, false, newAPIError(http.MethodGet, path, resp)
	}
	// The diff is one document without ranges or pages, so a later part is read past the start.
	if _, err = io.CopyN(io.Discard, resp.Body, int64(offset)); err != nil && !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("bitbucket GET %s: reading the diff: %w", path, err)
	}
	diff, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, false, fmt.Errorf("bitbucket GET %s: reading the diff: %w", path, err)
	}
	if len(diff) > maxBytes {
		return diff[:maxBytes], true, nil
	}
	return diff, false, nil
}

// PullRequestURL is the page of a pull request on the Bitbucket website.
func (c *Client) PullRequestURL(repo string, id int) string {
	return c.repoWebURL(repo, "pull-requests", strconv.Itoa(id))
}
