// Package bitbucket is the only place that talks to the Bitbucket Cloud REST API.
package bitbucket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

// APIError carries the HTTP status and Bitbucket's own error message, never the response body.
type APIError struct {
	Method  string
	Path    string
	Status  int
	Message string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("bitbucket %s %s: %d: %s", e.Method, e.Path, e.Status, msg)
}

// IsStatus reports whether err is an APIError with the given HTTP status.
func IsStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == status
}

func (c *Client) repoPath(repo string, parts ...string) string {
	p := "/repositories/" + url.PathEscape(c.cfg.Workspace) + "/" + url.PathEscape(repo)
	for _, part := range parts {
		p += "/" + url.PathEscape(part)
	}
	return p
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
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The transport error includes the URL, which never holds credentials.
		return fmt.Errorf("bitbucket %s %s: %w", method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		apiErr := &APIError{Method: method, Path: req.URL.Path, Status: resp.StatusCode}
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
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("bitbucket %s %s: decoding response: %w", method, req.URL.Path, err)
	}
	return nil
}

// list follows Bitbucket's `next` links and returns all values of a paginated collection.
func list[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var all []T
	next := APIURL + path + "?pagelen=100"
	for next != "" {
		// Credentials go only to the API host, whatever `next` says.
		if !strings.HasPrefix(next, APIURL+"/") {
			return nil, fmt.Errorf("bitbucket GET %s: unexpected pagination link outside the API", path)
		}
		var page struct {
			Values []T    `json:"values"`
			Next   string `json:"next"`
		}
		if err := c.send(ctx, http.MethodGet, next, c.basicAuth, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Values...)
		next = page.Next
	}
	return all, nil
}
