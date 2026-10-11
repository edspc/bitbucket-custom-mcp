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

// PullRequestStatuses reads only the first page of build results, the newest first: Bitbucket
// lists the oldest first, and on a busy pull request those are long out of date. A page holds
// at most 50.
func (c *Client) PullRequestStatuses(ctx context.Context, repo string, id, limit int) (Page[CommitStatus], error) {
	q := url.Values{"pagelen": {strconv.Itoa(limit)}, "sort": {"-created_on"}}
	page, err := readPage[CommitStatus](ctx, c, APIURL+c.pullRequestPath(repo, id, "statuses")+"?"+q.Encode(), limit)
	return Page[CommitStatus]{Values: page.Values, Total: page.Size, More: page.Next != ""}, err
}

// PullRequestDiffStat reads only the requested range of files, not preceding pages. We request
// 100 records per API page, so a 200-file result needs at most three API reads including
// an unaligned offset; a next link can never turn this into an unbounded traversal.
func (c *Client) PullRequestDiffStat(ctx context.Context, snapshot DiffSnapshot, offset, limit int) (Page[DiffStat], error) {
	var result Page[DiffStat]
	if offset < 0 || limit < 1 || limit > 200 {
		return result, fmt.Errorf("file offset must be nonnegative and limit between 1 and 200")
	}
	if snapshot.target == nil {
		return result, errors.New("a validated pull request diff snapshot is required")
	}
	target := *snapshot.target
	const pageSize = 100
	q := target.Query()
	q.Set("pagelen", strconv.Itoa(pageSize))
	q.Set("page", strconv.Itoa(offset/pageSize+1))
	target.RawQuery = q.Encode()
	next, skip := target.String(), offset%pageSize
	for reads := 0; next != "" && reads < 3; reads++ {
		page, err := readPage[DiffStat](ctx, c, next, pageSize)
		if err != nil {
			return Page[DiffStat]{}, err
		}
		if reads == 0 {
			result.Total = page.Size
		}
		values := page.Values[min(skip, len(page.Values)):]
		skip = 0
		keep := min(len(values), limit-len(result.Values))
		result.Values = append(result.Values, values[:keep]...)
		result.More = keep < len(values) || page.Next != ""
		if len(result.Values) == limit {
			break
		}
		// Keep every page on the validated commit pair, even if a next link selects another diff.
		next = ""
		if page.Next != "" {
			q.Set("page", strconv.Itoa(offset/pageSize+reads+2))
			target.RawQuery = q.Encode()
			next = target.String()
		}
	}
	if result.More && len(result.Values) < limit {
		return Page[DiffStat]{}, fmt.Errorf("bitbucket pagination exceeded the bounded file request")
	}
	return result, nil
}

// PullRequestDiff returns the git-style diff of a pull request, limited to paths when any are
// given. It returns at most maxBytes from offset on, and whether more followed.
func (c *Client) PullRequestDiff(ctx context.Context, snapshot DiffSnapshot, paths []string, offset, maxBytes int) ([]byte, bool, error) {
	if snapshot.target == nil {
		return nil, false, errors.New("a validated pull request diff snapshot is required")
	}
	path, target := snapshot.path, *snapshot.target
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
