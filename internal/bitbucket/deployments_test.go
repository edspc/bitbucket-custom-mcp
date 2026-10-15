package bitbucket

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestCreateDeploymentEnvironment(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/2.0/repositories/ws/repo-a/environments" {
			t.Errorf("request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["type"] != "deployment_environment" || body["name"] != "Preview" || body["environment_type"].(map[string]any)["name"] != "Test" {
			t.Errorf("body: %v", body)
		}
		_, _ = w.Write([]byte(`{"uuid":"{environment-id}","name":"Preview"}`))
	})
	env, err := c.CreateEnvironment(context.Background(), "repo-a", "Preview", "Test")
	if err != nil || env.UUID != "{environment-id}" {
		t.Fatalf("environment: %+v, %v", env, err)
	}
}

func TestDeploymentCollectionFiltersLocallyAndPreservesStatus(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pagelen") != "50" || r.URL.Path != "/2.0/repositories/ws/repo-a/deployments" {
			t.Errorf("query: %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"values":[{"uuid":"other", "environment":{"uuid":"{other}"}},{"uuid":"wanted","environment":{"uuid":"{env-id}"},"state":{"name":"COMPLETED","status":{"name":"SUCCESSFUL"}}}]}`))
	})
	ds, err := c.ListDeployments(context.Background(), "repo-a", "{env-id}", 50)
	if err != nil || len(ds.Values) != 1 || ds.Values[0].UUID != "wanted" || ds.Values[0].State.Status.Name != "SUCCESSFUL" {
		t.Fatalf("deployments: %+v %v", ds, err)
	}
}
