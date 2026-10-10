package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

// A result goes to the model whole, and Claude Code cuts tool results over 25,000 tokens by
// default. A page of files is bounded by count and a part of the diff in bytes, and escaping
// makes JSON larger, so they leave room: a page of paths of about 85 characters, or a part of
// code-like content, takes about 55 KB, typically under 25,000 tokens; longer paths or content
// dense in tokens can still exceed it.
const (
	maxFiles     = 200
	maxDiffBytes = 40 << 10
)

type getPullRequestInput struct {
	Repo        string   `json:"repo" jsonschema:"repository slug"`
	PullRequest int      `json:"pull_request" jsonschema:"ID of the pull request, such as 42"`
	Paths       []string `json:"paths,omitempty" jsonschema:"files to show the diff of, by their path in files; without paths the result describes the pull request and lists its files instead"`
	DiffFrom    string   `json:"diff_from,omitempty" jsonschema:"the diff_next of the previous part, with the same paths, to read the next part of a long diff"`
	FilesFrom   string   `json:"files_from,omitempty" jsonschema:"the files_next of the previous page, without paths, to list the next files of a pull request that changes many"`
}

func (in getPullRequestInput) validate() error {
	if in.Repo == "" {
		return errors.New("repo is required")
	}
	if in.PullRequest < 1 {
		return errors.New("pull_request must be the ID of a pull request, such as 42")
	}
	if slices.Contains(in.Paths, "") {
		return errors.New("paths must not contain empty paths")
	}
	if in.DiffFrom != "" && len(in.Paths) == 0 {
		return errors.New("diff_from goes with the paths of the part it was returned with")
	}
	if in.FilesFrom != "" && len(in.Paths) > 0 {
		return errors.New("files_from lists files, so it goes without paths")
	}
	return nil
}

type pullRequestEndView struct {
	Branch     string `json:"branch"`
	Commit     string `json:"commit,omitempty"`
	Repository string `json:"repository" jsonschema:"workspace/slug; another repository than repo for a pull request from a fork"`
}

type reviewerView struct {
	Name  string `json:"name"`
	Role  string `json:"role" jsonschema:"'reviewer', or 'participant' for someone who reviewed without being asked to"`
	State string `json:"state" jsonschema:"'approved', 'changes_requested', or 'pending' while a reviewer has not decided"`
}

type buildView struct {
	Name        string `json:"name"`
	State       string `json:"state" jsonschema:"SUCCESSFUL, FAILED, INPROGRESS or STOPPED"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
}

type changedFileView struct {
	Path         string `json:"path"`
	OldPath      string `json:"old_path,omitempty" jsonschema:"the path before the file was renamed"`
	Status       string `json:"status" jsonschema:"added, removed, modified, renamed, or a merge conflict"`
	LinesAdded   int    `json:"lines_added"`
	LinesRemoved int    `json:"lines_removed"`
}

// pullRequestSummary is what is shown of a pull request, listed or read.
type pullRequestSummary struct {
	ID           int                `json:"id"`
	Title        string             `json:"title"`
	State        string             `json:"state" jsonschema:"OPEN, MERGED, DECLINED or SUPERSEDED"`
	Draft        bool               `json:"draft"`
	Author       string             `json:"author"`
	Source       pullRequestEndView `json:"source" jsonschema:"the branch with the changes"`
	Destination  pullRequestEndView `json:"destination" jsonschema:"the branch the changes are merged into"`
	CreatedOn    string             `json:"created_on"`
	UpdatedOn    string             `json:"updated_on"`
	CommentCount int                `json:"comment_count"`
	OpenTasks    int                `json:"open_tasks"`
	URL          string             `json:"url" jsonschema:"page of the pull request on bitbucket.org"`
}

func (t *tools) summarizePullRequest(repo string, pr bitbucket.PullRequest) pullRequestSummary {
	return pullRequestSummary{
		ID:           pr.ID,
		Title:        pr.Title,
		State:        pr.State,
		Draft:        pr.Draft,
		Author:       pr.Author.DisplayName,
		Source:       viewPullRequestEnd(pr.Source),
		Destination:  viewPullRequestEnd(pr.Destination),
		CreatedOn:    pr.CreatedOn,
		UpdatedOn:    pr.UpdatedOn,
		CommentCount: pr.CommentCount,
		OpenTasks:    pr.TaskCount,
		URL:          t.bb.PullRequestURL(repo, pr.ID),
	}
}

type pullRequestView struct {
	pullRequestSummary
	Description       string            `json:"description,omitempty" jsonschema:"only without paths and files_from, as are reviewers and builds"`
	CloseSourceBranch bool              `json:"close_source_branch" jsonschema:"the source branch is deleted when the pull request is merged"`
	MergeCommit       string            `json:"merge_commit,omitempty"`
	ClosedBy          string            `json:"closed_by,omitempty"`
	DeclineReason     string            `json:"decline_reason,omitempty"`
	Reviewers         []reviewerView    `json:"reviewers,omitempty"`
	Builds            []buildView       `json:"builds,omitempty" jsonschema:"results reported for the pull request, such as pipeline runs"`
	Files             []changedFileView `json:"files,omitempty" jsonschema:"without paths: the files the pull request changes, 200 a page; pass some as paths to see their diff"`
	FilesNext         string            `json:"files_next,omitempty" jsonschema:"set when more files follow: pass it as files_from to list the next page"`
	FilesTotal        int               `json:"files_total,omitempty" jsonschema:"how many files the pull request changes"`
	FilesError        string            `json:"files_error,omitempty" jsonschema:"why the changed files could not be listed, such as Bitbucket timing out on a very large pull request"`
	Diff              *string           `json:"diff,omitempty" jsonschema:"with paths: their git-style diff, of the source branch against the commit it forked from the destination at, empty when they did not change; at most 40 KB, cut at a line end unless a line is longer"`
	DiffNext          string            `json:"diff_next,omitempty" jsonschema:"set when the diff goes on: pass it as diff_from, with the same paths, to read the next part"`
	DiffError         string            `json:"diff_error,omitempty" jsonschema:"why the diff could not be read, such as Bitbucket timing out on a very large diff; fewer paths ask for a smaller one"`
}

func (t *tools) getPullRequest(ctx context.Context, _ *mcp.CallToolRequest, in getPullRequestInput) (*mcp.CallToolResult, pullRequestView, error) {
	if err := in.validate(); err != nil {
		return nil, pullRequestView{}, err
	}
	pr, err := t.bb.GetPullRequest(ctx, in.Repo, in.PullRequest)
	if err != nil {
		return nil, pullRequestView{}, err
	}
	v := pullRequestView{
		pullRequestSummary: t.summarizePullRequest(in.Repo, pr),
		CloseSourceBranch:  pr.CloseSourceBranch,
		DeclineReason:      pr.Reason,
	}
	if pr.MergeCommit != nil {
		v.MergeCommit = pr.MergeCommit.Hash
	}
	if pr.ClosedBy != nil {
		v.ClosedBy = pr.ClosedBy.DisplayName
	}
	// The whole diff of a pull request is often too long to read at once; its files show
	// which diffs to ask for.
	if len(in.Paths) == 0 {
		if err = t.describePullRequest(ctx, in, pr, &v); err != nil {
			return nil, pullRequestView{}, err
		}
		return nil, v, nil
	}

	version := diffVersion(v, in.Paths)
	offset, err := cursorStart("diff", in.DiffFrom, version)
	if err != nil {
		return nil, pullRequestView{}, err
	}
	diff, more, err := t.bb.PullRequestDiff(ctx, in.Repo, in.PullRequest, in.Paths, offset, maxDiffBytes)
	if err != nil {
		v.DiffError = err.Error()
		return nil, v, nil
	}
	if more {
		diff = wholeLines(diff)
		v.DiffNext = version + "@" + strconv.Itoa(offset+len(diff))
	}
	text := string(diff)
	v.Diff = &text
	return nil, v, nil
}

// describePullRequest adds what a call without paths tells: the description, reviewers and
// builds, and a page of the changed files. Later pages hold only the files.
func (t *tools) describePullRequest(ctx context.Context, in getPullRequestInput, pr bitbucket.PullRequest, v *pullRequestView) error {
	version := diffVersion(*v, nil)
	offset, err := cursorStart("files", in.FilesFrom, version)
	if err != nil {
		return err
	}
	if in.FilesFrom == "" {
		v.Description = pr.Summary.Raw
		v.Reviewers = viewReviewers(pr.Participants)
		var statuses []bitbucket.CommitStatus
		if statuses, err = t.bb.PullRequestStatuses(ctx, in.Repo, in.PullRequest); err != nil {
			return err
		}
		for _, s := range statuses {
			v.Builds = append(v.Builds, buildView{Name: s.Name, State: s.State, Description: s.Description, URL: s.URL})
		}
	}
	// Bitbucket gives up on the files of a very large pull request; the rest of the result still
	// helps.
	stats, err := t.bb.PullRequestDiffStat(ctx, in.Repo, in.PullRequest)
	if err != nil {
		v.FilesError = err.Error()
		return nil
	}
	v.FilesTotal = len(stats)
	// The offset comes from the caller; added to before it is bounded, it could overflow.
	offset = min(offset, len(stats))
	end := min(offset+maxFiles, len(stats))
	for _, s := range stats[offset:end] {
		v.Files = append(v.Files, viewChangedFile(s))
	}
	if end < len(stats) {
		v.FilesNext = version + "@" + strconv.Itoa(end)
	}
	return nil
}

// diffVersion names the diff of a pull request, or its list of files, by the commits it
// compares and the paths it is limited to. A push between two parts or pages changes it, and
// the next one is then refused rather than read from another diff.
func diffVersion(v pullRequestView, paths []string) string {
	h := fnv.New32a()
	for _, p := range paths {
		_, _ = io.WriteString(h, p+"\x00")
	}
	return fmt.Sprintf("%s..%s/%08x", v.Source.Commit, v.Destination.Commit, h.Sum32())
}

// cursorStart returns where the part of the diff, or the page of files, that from names starts:
// from is a diff_next or a files_next, "<version>@<offset>".
func cursorStart(kind, from, version string) (int, error) {
	if from == "" {
		return 0, nil
	}
	fromVersion, offset, _ := strings.Cut(from, "@")
	n, err := strconv.Atoi(offset)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s_from must be a %s_next this tool returned, got %q", kind, kind, from)
	}
	if fromVersion != version {
		return 0, fmt.Errorf("the pull request has changed since %s_from was returned, or paths differ; start again without %s_from", kind, kind)
	}
	return n, nil
}

// wholeLines cuts a part of a diff after its last line end, so the next part starts a line. A
// line longer than a part is cut too, but not inside a character.
func wholeLines(diff []byte) []byte {
	if i := bytes.LastIndexByte(diff, '\n'); i >= 0 {
		return diff[:i+1]
	}
	for i := len(diff) - 1; i >= 0 && i >= len(diff)-utf8.UTFMax; i-- {
		if utf8.RuneStart(diff[i]) {
			if !utf8.FullRune(diff[i:]) {
				return diff[:i]
			}
			break
		}
	}
	return diff
}

func viewPullRequestEnd(e bitbucket.PullRequestEnd) pullRequestEndView {
	v := pullRequestEndView{Branch: e.Branch.Name, Repository: e.Repository.FullName}
	if e.Commit != nil {
		v.Commit = e.Commit.Hash
	}
	return v
}

// viewReviewers lists the reviewers, and the other participants who approved or asked for
// changes; those who only commented are left out.
func viewReviewers(ps []bitbucket.Participant) []reviewerView {
	var out []reviewerView
	for _, p := range ps {
		state := p.State
		if state == "" && p.Approved {
			state = "approved"
		}
		if p.Role != "REVIEWER" && state == "" {
			continue
		}
		if state == "" {
			state = "pending"
		}
		out = append(out, reviewerView{Name: p.User.DisplayName, Role: strings.ToLower(p.Role), State: state})
	}
	return out
}

func viewChangedFile(s bitbucket.DiffStat) changedFileView {
	v := changedFileView{Status: s.Status, LinesAdded: s.LinesAdded, LinesRemoved: s.LinesRemoved}
	if s.New != nil {
		v.Path = s.New.Path
	}
	if s.Old != nil {
		if v.Path == "" {
			v.Path = s.Old.Path
		} else if s.Old.Path != v.Path {
			v.OldPath = s.Old.Path
		}
	}
	return v
}
