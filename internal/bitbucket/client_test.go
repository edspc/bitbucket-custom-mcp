package bitbucket

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testEmail = "dev@example.com"
	testToken = "api-secret"
)

// handlerTransport serves requests in-process, so tests need no network listener.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

func newTestClient(h http.HandlerFunc) *Client {
	return New(&http.Client{Transport: handlerTransport{h}}, Config{
		Workspace: "ws", Email: testEmail, Token: testToken,
	})
}

func TestCredentialsOnlyInAuthorizationHeader(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != testEmail || pass != testToken {
			t.Errorf("basic auth = %q, %q, %v", user, pass, ok)
		}
		if strings.Contains(r.URL.String(), testToken) {
			t.Errorf("token leaked into URL %s", r.URL)
		}
		if r.URL.Path != "/2.0/repositories/ws/repo-a/pipelines_config" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"enabled": true}`)
	})
	enabled, err := c.PipelinesEnabled(context.Background(), "repo-a")
	if err != nil || !enabled {
		t.Fatalf("PipelinesEnabled = %v, %v", enabled, err)
	}
}

func TestAPIErrorKeepsOnlyMessage(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"type": "error", "error": {"message": "key is invalid", "detail": "internal detail"}}`)
	})
	err := c.AddDeployKey(context.Background(), "repo-b", "ssh-ed25519 AAAA", "repo-a")
	if !IsStatus(err, http.StatusBadRequest) {
		t.Fatalf("want 400 APIError, got %v", err)
	}
	if !strings.Contains(err.Error(), "key is invalid") || strings.Contains(err.Error(), "internal detail") {
		t.Fatalf("error = %q", err)
	}
}

func TestListFollowsNext(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = fmt.Fprint(w, `{"values": [{"id": 2, "label": "b"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"values": [{"id": 1, "label": "a"}], "next": "https://api.bitbucket.org/2.0/repositories/ws/repo-b/deploy-keys?page=2"}`)
	})
	keys, err := c.ListDeployKeys(context.Background(), "repo-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0].ID != 1 || keys[1].ID != 2 {
		t.Fatalf("keys = %+v", keys)
	}
}

func TestListRefusesForeignNext(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"values": [], "next": "https://evil.example/2.0/steal"}`)
	})
	if _, err := c.ListDeployKeys(context.Background(), "repo-b"); err == nil {
		t.Fatal("want error for a pagination link outside the API")
	}
}

// A followed 307 would resend the body, which may hold a token, to the redirect target.
func TestRedirectsAreNotFollowed(t *testing.T) {
	var requests int
	c := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Location", "https://evil.example/collect")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	err := c.CreateVariable(context.Background(), VariableScope{Repo: "repo-b"}, Variable{Key: "K", Value: "secret", Secured: true})
	if !IsStatus(err, http.StatusTemporaryRedirect) {
		t.Fatalf("want 307 APIError, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, the redirect must not be followed", requests)
	}
}

func TestPipelineSSHPublicKeyMissing(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.PipelineSSHPublicKey(context.Background(), "repo-a")
	if err == nil || !strings.Contains(err.Error(), "has no pipelines SSH key") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckAccessToken(t *testing.T) {
	for _, tc := range []struct {
		permissions []string
		path        string
	}{
		{[]string{"pipeline", "repository"}, "/2.0/repositories/ws/repo-a"},
		{[]string{"pullrequest:write"}, "/2.0/repositories/ws/repo-a"},
		{[]string{"webhook", "pipeline:variable"}, "/2.0/repositories/ws/repo-a/pipelines"},
		{[]string{"webhook"}, "/2.0/repositories/ws/repo-a/hooks"},
	} {
		for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusUnauthorized} {
			t.Run(fmt.Sprintf("%v %d", tc.permissions, status), func(t *testing.T) {
				c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
					if got := r.Header.Get("Authorization"); got != "Bearer repo-token" {
						t.Errorf("Authorization = %q", got)
					}
					if r.URL.Path != tc.path {
						t.Errorf("path = %s, want %s", r.URL.Path, tc.path)
					}
					w.WriteHeader(status)
				})
				err := c.CheckAccessToken(context.Background(), "repo-a", "repo-token", tc.permissions)
				if (err != nil) != (status != http.StatusOK) {
					t.Fatalf("err = %v", err)
				}
			})
		}
	}
}

func TestVariablePaths(t *testing.T) {
	var got []string
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.EscapedPath())
	})
	ctx := context.Background()
	repo := VariableScope{Repo: "repo-b"}
	env := VariableScope{Repo: "repo-b", EnvironmentUUID: "{env-1}"}
	for _, err := range []error{
		c.CreateVariable(ctx, repo, Variable{Key: "K"}),
		c.UpdateVariable(ctx, repo, Variable{UUID: "{var-1}", Key: "K"}),
		c.CreateVariable(ctx, env, Variable{Key: "K"}),
		c.UpdateVariable(ctx, env, Variable{UUID: "{var-2}", Key: "K"}),
		c.CreateVariable(ctx, VariableScope{}, Variable{Key: "K"}),
		c.DeleteVariable(ctx, VariableScope{}, "{var-3}"),
		c.DeleteVariable(ctx, env, "{var-2}"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"POST /2.0/repositories/ws/repo-b/pipelines_config/variables",
		"PUT /2.0/repositories/ws/repo-b/pipelines_config/variables/%7Bvar-1%7D",
		"POST /2.0/repositories/ws/repo-b/deployments_config/environments/%7Benv-1%7D/variables",
		"PUT /2.0/repositories/ws/repo-b/deployments_config/environments/%7Benv-1%7D/variables/%7Bvar-2%7D",
		"POST /2.0/workspaces/ws/pipelines-config/variables",
		"DELETE /2.0/workspaces/ws/pipelines-config/variables/%7Bvar-3%7D",
		"DELETE /2.0/repositories/ws/repo-b/deployments_config/environments/%7Benv-1%7D/variables/%7Bvar-2%7D",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestWebURLs(t *testing.T) {
	c := newTestClient(nil)
	for got, want := range map[string]string{
		c.PipelineURL("repo-a", 42):      "https://bitbucket.org/ws/repo-a/pipelines/results/42",
		c.StepURL("repo-a", 42, "{s-1}"): "https://bitbucket.org/ws/repo-a/pipelines/results/42/steps/%7Bs-1%7D",
		c.AccessTokensPage("repo-a"):     "https://bitbucket.org/ws/repo-a/admin/access-tokens",
	} {
		if got != want {
			t.Errorf("URL = %s, want %s", got, want)
		}
	}
}

func TestSameKey(t *testing.T) {
	if !SameKey("ssh-rsa AAAA one@host", "ssh-rsa AAAA") {
		t.Error("same key with different comment must match")
	}
	if SameKey("ssh-rsa AAAA", "ssh-rsa BBBB") || SameKey("ssh-rsa", "ssh-rsa") {
		t.Error("different or malformed keys must not match")
	}
}
