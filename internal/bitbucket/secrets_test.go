package bitbucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSecretRequestsWithholdAPIErrorDetails(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429, 500} {
		for _, operation := range []string{"create", "update", "run", "probe"} {
			t.Run(fmt.Sprintf("%s/%d", operation, status), func(t *testing.T) {
				c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"error":{"message":"echo: secret-marker and encoded-secret-marker"}}`)
				})
				err := secretOperation(c, operation)
				if !IsStatus(err, status) || strings.Contains(err.Error(), "secret-marker") {
					t.Fatalf("unsafe error: %v", err)
				}
				if status == 429 {
					wait, limited := RateLimited(err)
					if !limited || wait.Seconds() != 60 {
						t.Fatalf("retry advice was lost: %v", err)
					}
				}
			})
		}
	}
}

func secretOperation(c *Client, operation string) error {
	ctx := context.Background()
	v := Variable{UUID: "{v}", Key: "TOKEN", Value: "secret-marker", Secured: true}
	switch operation {
	case "create":
		return c.CreateVariable(ctx, VariableScope{Repo: "repo-a"}, v)
	case "update":
		return c.UpdateVariable(ctx, VariableScope{Repo: "repo-a"}, v)
	case "run":
		_, err := c.RunPipeline(ctx, "repo-a", RunRequest{Target: RefTarget("branch", "main", ""), Variables: []Variable{v}})
		return err
	default:
		return c.CheckTokenAccess(ctx, "repo-a", "secret-marker", []string{"repository"})
	}
}

func TestSecretRequestsWithholdTransportErrors(t *testing.T) {
	for _, operation := range []string{"create", "update", "run", "probe"} {
		c := New(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("transport reflected secret-marker")
		})}, Config{Workspace: "ws", Email: testEmail, Token: testToken})
		if err := secretOperation(c, operation); err == nil || strings.Contains(err.Error(), "secret-marker") {
			t.Fatalf("%s: unsafe error: %v", operation, err)
		}
	}
}

func TestSecretRequestRetainsCancellation(t *testing.T) {
	c := New(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("reflected secret-marker: %w", context.Canceled)
	})}, Config{Workspace: "ws", Email: testEmail, Token: testToken})
	err := secretOperation(c, "create")
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("unsafe cancellation: %v", err)
	}
}

func TestSecretRunWithholdsResponseDecodingErrors(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"build_number":"secret-marker"}`)
	})
	if err := secretOperation(c, "run"); err == nil || strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("unsafe decoding error: %v", err)
	}
}
