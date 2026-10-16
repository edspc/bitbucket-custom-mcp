package bitbucket

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

func TestRepositoryFilterQuery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter RepositoryFilter
		want   string
		none   bool
	}{
		{"everything", RepositoryFilter{}, "", false},
		{"name and project", RepositoryFilter{Name: `api "v2"`, Project: "APP"}, `name ~ "api \"v2\"" AND project.key = "APP"`, false},
		{"listed repositories", RepositoryFilter{Slugs: []string{"a", "b"}}, `(slug = "a" OR slug = "b")`, false},
		{"projects or listed", RepositoryFilter{Name: "x", Projects: []string{"APP", "{id}"}, Slugs: []string{"tools"}},
			`name ~ "x" AND (project.key = "APP" OR project.uuid = "{id}" OR slug = "tools")`, false},
		{"projects only", RepositoryFilter{Projects: []string{"APP"}}, `(project.key = "APP")`, false},
		{"no repositories", RepositoryFilter{Slugs: []string{}}, "", true},
		{"nothing allowed", RepositoryFilter{Slugs: []string{}, Projects: []string{}}, "", true},
		{"nil slugs with no projects", RepositoryFilter{Projects: []string{}}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if q, none := tc.filter.query(); q != tc.want || none != tc.none {
				t.Fatalf("query = %q, %v; want %q, %v", q, none, tc.want, tc.none)
			}
		})
	}
}

// The listing names no repository, so no project lookup can vet it; the filter in its query
// narrows it instead.
func TestListRepositoriesSkipsProjectLookup(t *testing.T) {
	var requests []string
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path+"?"+r.URL.RawQuery)
		_, _ = fmt.Fprint(w, `{"values": [{"slug": "api", "project": {"key": "APP"}, "mainbranch": {"name": "main"}}], "size": 3, "next": "https://api.bitbucket.org/2.0/repositories/ws?page=2"}`)
	}).WithAllowedProjects([]string{"APP"}, []string{"tools"})
	repos, err := c.ListRepositories(t.Context(), RepositoryFilter{Name: "a", Projects: []string{"APP"}, Slugs: []string{"tools"}}, 1, 2)
	if err != nil || len(repos.Values) != 1 || repos.Values[0].Project.Key != "APP" || repos.Values[0].MainBranch.Name != "main" || !repos.More || *repos.Total != 3 {
		t.Fatalf("ListRepositories = %+v, %v", repos, err)
	}
	q := url.Values{"sort": {"slug"}, "pagelen": {"2"}, "page": {"1"},
		"q": {`name ~ "a" AND (project.key = "APP" OR slug = "tools")`}}
	if want := "/2.0/repositories/ws?" + q.Encode(); len(requests) != 1 || requests[0] != want {
		t.Fatalf("requests = %q, want %q", requests, want)
	}
	if repos, err = c.ListRepositories(t.Context(), RepositoryFilter{Slugs: []string{}, Projects: []string{}}, 1, 2); err != nil || len(repos.Values) != 0 || len(requests) != 1 {
		t.Fatalf("a filter that allows nothing = %+v, %v after %d requests", repos, err, len(requests))
	}
}
