// Package bitbucket is the only place that talks to the Bitbucket Cloud REST API.
package bitbucket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	APIURL = "https://api.bitbucket.org/2.0"
	WebURL = "https://bitbucket.org"
)

// maxErrorBody caps how much of an error response is read; only its message is kept.
const maxErrorBody = 64 << 10

type Config struct {
	Workspace string
	Email     string
	Token     string
}

type Client struct {
	http *http.Client
	cfg  Config
}

func New(httpClient *http.Client, cfg Config) *Client {
	// A followed 307/308 would resend the request body, which may hold a token, to wherever
	// the redirect points; the API never needs redirects.
	hc := *httpClient
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &hc, cfg: cfg}
}

// Workspace names the authority shown on approval pages, without exposing credentials.
func (c *Client) Workspace() string { return c.cfg.Workspace }

// APIError carries the HTTP status and Bitbucket's own error message, never the response body.
type APIError struct {
	Method  string
	Path    string
	Status  int
	Message string
	// RetryAfter is how long a 429 response said to wait; zero when it did not say.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Status == http.StatusTooManyRequests {
		msg = strings.TrimSuffix(msg, ".") + ". " + rateLimitAdvice(e.RetryAfter)
	}
	return fmt.Sprintf("bitbucket %s %s: %d: %s", e.Method, e.Path, e.Status, msg)
}

func newAPIError(method, path string, resp *http.Response) *APIError {
	apiErr := &APIError{Method: method, Path: path, Status: resp.StatusCode}
	if resp.StatusCode == http.StatusTooManyRequests {
		apiErr.RetryAfter = rateLimitWait(resp.Header, time.Now())
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxErrorBody)).Decode(&payload) == nil {
		apiErr.Message = payload.Error.Message
	}
	return apiErr
}

// IsStatus reports whether err is an APIError with the given HTTP status.
func IsStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == status
}

// escapedPath joins path segments, each escaped, into a URL path.
func escapedPath(segments ...string) string {
	var b strings.Builder
	for _, s := range segments {
		b.WriteString("/")
		b.WriteString(url.PathEscape(s))
	}
	return b.String()
}

func (c *Client) repoPath(repo string, parts ...string) string {
	return escapedPath(append([]string{"repositories", c.cfg.Workspace, repo}, parts...)...)
}

// repoWebURL is a page of repo on the Bitbucket website.
func (c *Client) repoWebURL(repo string, parts ...string) string {
	return WebURL + escapedPath(append([]string{c.cfg.Workspace, repo}, parts...)...)
}

func (c *Client) basicAuth(req *http.Request) { req.SetBasicAuth(c.cfg.Email, c.cfg.Token) }

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	return c.send(ctx, method, APIURL+path, c.basicAuth, in, out)
}

func (c *Client) send(ctx context.Context, method, rawURL string, auth func(*http.Request), in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return err
	}
	auth(req)
	sensitive := strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") || secretPayload(in)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if sensitive {
			// A transport or proxy error can also reflect headers or a request body.
			switch {
			case errors.Is(err, context.Canceled):
				err = context.Canceled
			case errors.Is(err, context.DeadlineExceeded):
				err = context.DeadlineExceeded
			default:
				err = errors.New("request failed; upstream details withheld for a secret-bearing request")
			}
		}
		// The transport error includes the URL, which never holds credentials.
		return fmt.Errorf("bitbucket %s %s: %w", method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		err := newAPIError(method, req.URL.Path, resp)
		if sensitive {
			err.Message = "" // An API validation message may echo a submitted secret.
		}
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if sensitive {
			return fmt.Errorf("bitbucket %s %s: invalid response to a secret-bearing request", method, req.URL.Path)
		}
		return fmt.Errorf("bitbucket %s %s: decoding response: %w", method, req.URL.Path, err)
	}
	return nil
}

func secretPayload(in any) bool {
	switch v := in.(type) {
	case Variable:
		return v.Secured
	case RunRequest:
		for _, variable := range v.Variables {
			if variable.Secured {
				return true
			}
		}
	}
	return false
}

// get sends a GET of rawURL with the given headers and returns the response as it comes.
func (c *Client) get(ctx context.Context, rawURL string, auth func(*http.Request), header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	auth(req)
	maps.Copy(req.Header, header)
	return c.http.Do(req)
}

// inAPI reports whether rawURL is in the API, the only place credentials are sent to.
func inAPI(rawURL string) bool { return strings.HasPrefix(rawURL, APIURL+"/") }

// redirectTarget returns where the API redirects a GET of path, such as the diff of a pull
// request to the diff of its commits.
func (c *Client) redirectTarget(ctx context.Context, path string) (*url.URL, error) {
	resp, err := c.get(ctx, APIURL+path, c.basicAuth, nil)
	if err != nil {
		return nil, fmt.Errorf("bitbucket GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return nil, newAPIError(http.MethodGet, path, resp)
	}
	target, err := resp.Location()
	// The target gets the credentials too.
	if err != nil || !inAPI(target.String()) {
		return nil, fmt.Errorf("bitbucket GET %s: expected a redirect within the API, got %d", path, resp.StatusCode)
	}
	return target, nil
}

// list follows Bitbucket's `next` links and returns all values of a paginated collection.
func list[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var all []T
	for next := APIURL + path + "?pagelen=100"; next != ""; {
		// Credentials go only to the API host, whatever `next` says.
		if !inAPI(next) {
			return nil, fmt.Errorf("bitbucket GET %s: unexpected pagination link outside the API", path)
		}
		var page apiPage[T]
		if err := c.send(ctx, http.MethodGet, next, c.basicAuth, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Values...)
		next = page.Next
	}
	return all, nil
}
