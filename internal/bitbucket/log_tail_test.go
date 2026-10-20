package bitbucket

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestStepLogTailByteOffsets(t *testing.T) {
	for _, tc := range []struct {
		name, contentRange, body, want string
		status                         int
		start                          int64
	}{
		{"range", "bytes 10-15/16", "hello\n", "hello\n", 206, 10},
		{"ignored range", "", "first\nhello\n", "hello\n", 200, 6},
		{"larger range", "bytes 10-21/22", "first\nhello\n", "hello\n", 206, 16},
		{"empty", "", "", "", 416, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "bytes=-6" {
					t.Errorf("range = %q", r.Header.Get("Range"))
				}
				w.Header().Set("Content-Range", tc.contentRange)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			data, start, err := c.StepLogTail(context.Background(), "repo-a", "{p}", "{s}", 6)
			if err != nil || string(data) != tc.want || start != tc.start {
				t.Fatalf("tail = %q, %d, %v; want %q, %d", data, start, err, tc.want, tc.start)
			}
		})
	}
}

func TestStepLogTailRefusesUnusableOffsets(t *testing.T) {
	for _, header := range []string{"", "bytes -1-4/5", "bytes 3-1/5", "bytes 0-9/10", "bytes 0-2/2", "bytes 0-2/nope", "bytes 9223372036854775805-9223372036854775807/*"} {
		t.Run(header, func(t *testing.T) {
			c := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Range", header)
				w.WriteHeader(206)
				_, _ = io.WriteString(w, "hi\n")
			})
			if _, _, err := c.StepLogTail(context.Background(), "repo-a", "{p}", "{s}", 6); err == nil || !strings.Contains(err.Error(), "invalid log Content-Range") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
