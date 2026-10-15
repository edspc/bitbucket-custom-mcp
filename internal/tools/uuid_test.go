package tools

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

func connectUUID(t *testing.T, policy Policy, handler http.HandlerFunc) *mcp.ClientSession {
	t.Helper()
	bb := bitbucket.New(&http.Client{Transport: handlerTransport{handler}}, bitbucket.Config{
		Workspace: "ws", Email: "dev@example.com", Token: "api-secret",
	})
	s := NewServer(t.Context(), &mcp.Implementation{Name: "test-server", Version: "0"}, bb, slog.New(slog.DiscardHandler), policy)
	return connectClient(t, s, clientOptions(nil), "")
}

func TestGetUUIDPreservesBitbucketValue(t *testing.T) {
	for _, uuid := range []string{"{01234567-89AB-cdef-0123-456789abcdef}", "01234567-89AB-cdef-0123-456789abcdef"} {
		for _, deployment := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deployment=%t", uuid, deployment), func(t *testing.T) {
				requests := 0
				cs := connectUUID(t, Policy{}, func(w http.ResponseWriter, r *http.Request) {
					requests++
					if r.Method != http.MethodGet {
						t.Fatalf("unexpected write: %s", r.Method)
					}
					if deployment {
						if r.URL.Path != "/2.0/repositories/ws/repo-a/environments" {
							t.Errorf("unexpected path: %s", r.URL.Path)
						}
						if r.URL.Query().Get("page") != "2" {
							writeJSON(w, map[string]any{"values": []bitbucket.Environment{{Name: "staging", UUID: "{other}"}},
								"next": bitbucket.APIURL + "/repositories/ws/repo-a/environments?page=2"})
						} else {
							writeJSON(w, map[string]any{"values": []bitbucket.Environment{{Name: "Production", UUID: uuid}}})
						}
					} else {
						if r.URL.Path != "/2.0/repositories/ws/repo-a" || r.URL.Query().Get("fields") != "uuid" {
							t.Errorf("unexpected repository request: %s", r.URL)
						}
						writeJSON(w, map[string]string{"uuid": uuid})
					}
				})
				args := map[string]any{"repo": "repo-a"}
				wantRequests := 1
				if deployment {
					args["environment"] = "Production"
					wantRequests = 2
				}
				out := decodeResult[uuidResult](t, mustCall(t, cs, "get_uuid", args))
				if out.UUID != uuid || out.ContentTrust != "untrusted" || requests != wantRequests {
					t.Fatalf("result = %+v, requests = %d; want UUID %q and %d requests", out, requests, uuid, wantRequests)
				}
				list, err := cs.ListTools(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				for _, tool := range list.Tools {
					if tool.Name == "get_uuid" && (tool.Annotations == nil || !tool.Annotations.ReadOnlyHint) {
						t.Fatal("get_uuid must be annotated read-only")
					}
				}
			})
		}
	}
}

func TestGetUUIDRejectsInvalidInputAndPolicyBeforeRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy Policy
		args   map[string]any
	}{
		{"missing repo", Policy{}, map[string]any{}},
		{"empty repo", Policy{}, map[string]any{"repo": ""}},
		{"path escape", Policy{}, map[string]any{"repo": "../other"}},
		{"repository policy", Policy{Repositories: []string{"repo-b"}}, map[string]any{"repo": "repo-a"}},
		{"deployment repository policy", Policy{Repositories: []string{}}, map[string]any{"repo": "repo-a", "environment": "Production"}},
		{"tool policy", Policy{Tools: []string{"run_pipeline"}}, map[string]any{"repo": "repo-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := connectUUID(t, tc.policy, func(http.ResponseWriter, *http.Request) {
				t.Error("denied call reached Bitbucket")
			})
			if out, isErr := call(t, cs, "get_uuid", tc.args); !isErr {
				t.Fatalf("call succeeded: %s", out)
			}
		})
	}
}

func TestGetUUIDReportsLookupErrors(t *testing.T) {
	for _, tc := range []struct {
		name, environment, body, want string
		status                        int
	}{
		{"repository missing UUID", "", `{}`, "no repository UUID", 200},
		{"deployment missing UUID", "Production", `{"values":[{"name":"Production"}]}`, "no deployment environment UUID", 200},
		{"unknown environment", "production", `{"values":[{"name":"Production","uuid":"{env}"}]}`, `not found`, 200},
		{"repository API error", "", `{"error":{"message":"repository unavailable"}}`, "repository unavailable", 404},
		{"environment API error", "Production", `{"error":{"message":"access denied"}}`, "access denied", 403},
		{"invalid response", "", `invalid JSON`, "decoding response", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := connectUUID(t, Policy{}, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			})
			out, isErr := call(t, cs, "get_uuid", map[string]any{"repo": "repo-a", "environment": tc.environment})
			if !isErr || !strings.Contains(out, tc.want) {
				t.Fatalf("result = %s, error = %t; want %q", out, isErr, tc.want)
			}
		})
	}
}

func TestGetUUIDRespectsLiveProjectPolicy(t *testing.T) {
	for _, environment := range []string{"", "Production"} {
		for _, project := range []string{"APP", "OTHER"} {
			t.Run(project+"/"+environment, func(t *testing.T) {
				lookups, checks := 0, 0
				cs := connectUUID(t, Policy{Projects: []string{"APP"}}, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Query().Get("fields") == "project.key,project.uuid" {
						checks++
						writeJSON(w, map[string]any{"project": map[string]string{"key": project}})
						return
					}
					lookups++
					if environment == "" {
						writeJSON(w, map[string]string{"uuid": "{repo}"})
					} else {
						writeJSON(w, map[string]any{"values": []bitbucket.Environment{{Name: environment, UUID: "{env}"}}})
					}
				})
				out, isErr := call(t, cs, "get_uuid", map[string]any{"repo": "repo-a", "environment": environment})
				if checks != 1 || isErr != (project == "OTHER") || (project == "OTHER" && lookups != 0) {
					t.Fatalf("result = %s, error = %t, checks = %d, lookups = %d", out, isErr, checks, lookups)
				}
			})
		}
	}
}
