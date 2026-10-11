package bitbucket

import (
	"errors"
	"net/http"
	"strconv"
	"time"
)

// maxRateLimitWait bounds the wait a 429 response may ask for. Bitbucket counts requests over a
// rolling hour, so a longer wait is a misread header, such as a reset given as a moment rather
// than in seconds.
const maxRateLimitWait = time.Hour

// RateLimited reports whether err is Bitbucket refusing a request over its rate limit, and how
// long the response said to wait; zero when it did not say.
func RateLimited(err error) (time.Duration, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		return 0, false
	}
	return apiErr.RetryAfter, true
}

// rateLimitWait is how long a 429 response says to wait: by Retry-After first, in seconds or as
// a date, and else by X-RateLimit-Reset, the seconds until the limit resets, which Bitbucket
// sends without documenting it. It is zero when neither says a wait within maxRateLimitWait.
func rateLimitWait(h http.Header, now time.Time) time.Duration {
	retryAfter := h.Get("Retry-After")
	if when, err := http.ParseTime(retryAfter); err == nil {
		// The date has whole seconds; against the start of the current second the wait is
		// rounded up to them.
		if wait := when.Sub(now.Truncate(time.Second)); wait > 0 && wait <= maxRateLimitWait {
			return wait
		}
	} else if wait := waitSeconds(retryAfter); wait > 0 {
		return wait
	}
	return waitSeconds(h.Get("X-RateLimit-Reset"))
}

// waitSeconds reads a wait given in seconds; zero when v is not one within maxRateLimitWait.
func waitSeconds(v string) time.Duration {
	s, err := strconv.Atoi(v)
	// Bounded before the conversion, which would overflow for huge values.
	if err != nil || s <= 0 || s > int(maxRateLimitWait/time.Second) {
		return 0
	}
	return time.Duration(s) * time.Second
}

// rateLimitAdvice tells what to do about a 429 response that said to wait for wait, or did not
// say when it is zero.
func rateLimitAdvice(wait time.Duration) string {
	when := "in a few minutes, as earlier requests leave the hour"
	if wait > 0 {
		when = "in " + wait.String()
	}
	return "Bitbucket counts requests over a rolling hour, those of everything that uses the same Atlassian account or access token; try again " + when
}
