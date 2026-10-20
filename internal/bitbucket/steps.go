package bitbucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Step is one step of a pipeline run.
type Step struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	State       State  `json:"state"`
	StartedOn   string `json:"started_on"`
	CompletedOn string `json:"completed_on"`
}

func (c *Client) ListSteps(ctx context.Context, repo, pipelineUUID string) ([]Step, error) {
	return list[Step](ctx, c, c.repoPath(repo, "pipelines", pipelineUUID, "steps")+"/")
}

// StepURL is the page of a step on the Bitbucket website, with its full log.
func (c *Client) StepURL(repo string, buildNumber int, stepUUID string) string {
	return c.repoWebURL(repo, "pipelines", "results", strconv.Itoa(buildNumber), "steps", stepUUID)
}

// StepLog returns the last maxBytes of the log of a step, and whether earlier output was left
// out. Logs can be large, so only their end is requested.
func (c *Client) StepLog(ctx context.Context, repo, pipelineUUID, stepUUID string, maxBytes int) ([]byte, bool, error) {
	tail, start, err := c.StepLogTail(ctx, repo, pipelineUUID, stepUUID, maxBytes)
	return tail, start > 0, err
}

// StepLogTail returns a bounded suffix and its absolute byte offset in the raw log.
// Offsets let watchers distinguish new output from repeated lines in overlapping tails.
func (c *Client) StepLogTail(ctx context.Context, repo, pipelineUUID, stepUUID string, maxBytes int) ([]byte, int64, error) {
	if maxBytes < 1 {
		return nil, 0, errors.New("log byte limit must be positive")
	}
	path := c.repoPath(repo, "pipelines", pipelineUUID, "steps", stepUUID, "log")
	resp, err := c.getTail(ctx, APIURL+path, c.basicAuth, maxBytes)
	if err != nil {
		return nil, 0, fmt.Errorf("bitbucket GET %s: %w", path, err)
	}
	// A finished step's log moves to long-term storage, and the API redirects there.
	if resp.StatusCode/100 == 3 && resp.Header.Get("Location") != "" {
		_ = resp.Body.Close()
		var target *url.URL
		target, err = resp.Location()
		if err != nil || target.Scheme != "https" {
			return nil, 0, fmt.Errorf("bitbucket GET %s: unexpected log location", path)
		}
		// The storage link is signed by itself; the credentials stay with the API.
		resp, err = c.getTail(ctx, target.String(), func(*http.Request) {}, maxBytes)
		if err != nil {
			// The error would carry the signed link, which grants access to the log.
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				err = urlErr.Err
			}
			return nil, 0, fmt.Errorf("bitbucket GET %s: reading the log from storage: %w", path, err)
		}
	}
	defer func() { _ = resp.Body.Close() }()

	var tail []byte
	var start int64
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
		tail, start, err = readLogTail(resp.Body, maxBytes)
		if err == nil && resp.StatusCode == http.StatusPartialContent {
			var from, to int64
			var total string
			n, _ := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%s", &from, &to, &total)
			valid := n == 3 && from >= 0 && to >= from && to < 1<<63-1 && to-from == start+int64(len(tail))-1
			if total != "*" {
				size, err := strconv.ParseInt(total, 10, 64)
				valid = valid && err == nil && size > to
			}
			if !valid {
				return nil, 0, fmt.Errorf("bitbucket GET %s: invalid log Content-Range", path)
			}
			start += from
		}
	case http.StatusRequestedRangeNotSatisfiable:
		return nil, 0, nil // an empty log has no last bytes
	default:
		return nil, 0, newAPIError(http.MethodGet, path, resp)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("bitbucket GET %s: reading the log: %w", path, err)
	}
	return tail, start, nil
}

// getTail asks for the last maxBytes of rawURL.
func (c *Client) getTail(ctx context.Context, rawURL string, auth func(*http.Request), maxBytes int) (*http.Response, error) {
	return c.get(ctx, rawURL, auth, http.Header{"Range": {"bytes=-" + strconv.Itoa(maxBytes)}})
}

// readTail reads r to the end and keeps its last n bytes, for servers that ignore the range.
func readTail(r io.Reader, n int) ([]byte, bool, error) {
	buf, start, err := readLogTail(r, n)
	return buf, start > 0, err
}

func readLogTail(r io.Reader, n int) ([]byte, int64, error) {
	var buf []byte
	var read int64
	chunk := make([]byte, 32<<10)
	for {
		k, err := r.Read(chunk)
		read += int64(k)
		buf = append(buf, chunk[:k]...)
		if len(buf) > 2*n {
			buf = append(buf[:0], buf[len(buf)-n:]...)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, err
		}
	}
	if len(buf) > n {
		buf = buf[len(buf)-n:]
	}
	return buf, read - int64(len(buf)), nil
}
