package bitbucket

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestRateLimitWait(t *testing.T) {
	now := time.Date(2026, 10, 11, 5, 11, 23, 400_000_000, time.UTC)
	date := func(d time.Duration) string { return now.Truncate(time.Second).Add(d).Format(http.TimeFormat) }
	for name, tc := range map[string]struct {
		header http.Header
		want   time.Duration
	}{
		"nothing said":            {http.Header{}, 0},
		"retry after seconds":     {http.Header{"Retry-After": {"90"}}, 90 * time.Second},
		"retry after a date":      {http.Header{"Retry-After": {date(2 * time.Minute)}}, 2 * time.Minute},
		"retry after first":       {http.Header{"Retry-After": {"90"}, "X-Ratelimit-Reset": {"2917"}}, 90 * time.Second},
		"the reset otherwise":     {http.Header{"X-Ratelimit-Reset": {"2917"}}, 2917 * time.Second},
		"a whole hour":            {http.Header{"Retry-After": {"3600"}}, time.Hour},
		"over an hour":            {http.Header{"Retry-After": {"3601"}}, 0},
		"a date over an hour":     {http.Header{"Retry-After": {date(time.Hour + time.Second)}}, 0},
		"a past date":             {http.Header{"Retry-After": {date(-time.Minute)}}, 0},
		"right now":               {http.Header{"Retry-After": {"0"}}, 0},
		"negative":                {http.Header{"Retry-After": {"-5"}}, 0},
		"garbage":                 {http.Header{"Retry-After": {"soon"}}, 0},
		"huge":                    {http.Header{"Retry-After": {"9223372036854775807"}}, 0},
		"a reset as a moment":     {http.Header{"X-Ratelimit-Reset": {"1791695483"}}, 0},
		"bad retry after, reset":  {http.Header{"Retry-After": {"soon"}, "X-Ratelimit-Reset": {"60"}}, time.Minute},
		"long retry after, reset": {http.Header{"Retry-After": {"7200"}, "X-Ratelimit-Reset": {"60"}}, time.Minute},
		"past date, reset":        {http.Header{"Retry-After": {date(-time.Minute)}, "X-Ratelimit-Reset": {"60"}}, time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			if got := rateLimitWait(tc.header, now); got != tc.want {
				t.Fatalf("rateLimitWait(%v) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

func TestRateLimitedError(t *testing.T) {
	for name, tc := range map[string]struct {
		header   http.Header
		body     string
		wantWait time.Duration
		want     string
	}{
		"a wait said": {
			http.Header{"Retry-After": {"90"}},
			`{"type": "error", "error": {"message": "Rate limit for this resource has been exceeded."}}`,
			90 * time.Second,
			"bitbucket GET /2.0/repositories/ws/repo-a/pipelines/7: 429: Rate limit for this resource has been exceeded. " +
				"Bitbucket counts requests over a rolling hour, those of everything that uses the same Atlassian account or access token; try again in 1m30s",
		},
		"no wait said": {
			http.Header{},
			"Rate limit for this resource has been exceeded",
			0,
			"bitbucket GET /2.0/repositories/ws/repo-a/pipelines/7: 429: Too Many Requests. " +
				"Bitbucket counts requests over a rolling hour, those of everything that uses the same Atlassian account or access token; " +
				"try again in a few minutes, as earlier requests leave the hour",
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.header {
					w.Header()[k] = v
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = fmt.Fprint(w, tc.body)
			})
			_, err := c.GetPipeline(context.Background(), "repo-a", "7")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v\nwant %s", err, tc.want)
			}
			if wait, limited := RateLimited(fmt.Errorf("polling: %w", err)); !limited || wait != tc.wantWait {
				t.Fatalf("RateLimited = %v, %v; want %v, true", wait, limited, tc.wantWait)
			}
		})
	}
}

func TestOtherErrorsAreNotRateLimited(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	_, err := c.GetPipeline(context.Background(), "repo-a", "7")
	if !IsStatus(err, http.StatusServiceUnavailable) || err.Error() != "bitbucket GET /2.0/repositories/ws/repo-a/pipelines/7: 503: Service Unavailable" {
		t.Fatalf("err = %v", err)
	}
	for _, err := range []error{err, errors.New("connection reset"), nil} {
		if wait, limited := RateLimited(err); limited || wait != 0 {
			t.Fatalf("RateLimited(%v) = %v, %v", err, wait, limited)
		}
	}
}
