package bitbucket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// maxPageBytes bounds memory even when an upstream page ignores its requested record limit.
const maxPageBytes = 4 << 20

// Page is a bounded part of a collection. Total is unknown when the API omits its size.
type Page[T any] struct {
	Values []T
	Total  *int
	More   bool
}

type apiPage[T any] struct {
	Values []T    `json:"values"`
	Size   *int   `json:"size"`
	Next   string `json:"next"`
}

// readPage never follows next. Callers choose a bounded number of pages to read.
func readPage[T any](ctx context.Context, c *Client, rawURL string, limit int) (apiPage[T], error) {
	var page apiPage[T]
	if !inAPI(rawURL) {
		return page, fmt.Errorf("unexpected pagination link outside the Bitbucket API")
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return page, err
	}
	resp, err := c.get(ctx, rawURL, c.basicAuth, nil)
	if err != nil {
		return page, fmt.Errorf("reading Bitbucket page: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return page, newAPIError(http.MethodGet, target.Path, resp)
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, maxPageBytes)).Decode(&page); err != nil {
		return apiPage[T]{}, fmt.Errorf("reading bounded Bitbucket page: %w", err)
	}
	if len(page.Values) > limit || page.Size != nil && *page.Size < 0 || len(page.Values) == 0 && page.Next != "" {
		return apiPage[T]{}, fmt.Errorf("bitbucket returned an invalid or oversized collection page")
	}
	return page, nil
}
