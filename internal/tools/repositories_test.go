package tools

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/edspc/bitbucket-custom-mcp/internal/bitbucket"
)

// addRepositories lists api and web in APP, ops in OPS and tools in no project.
func addRepositories(f *fakeBitbucket) {
	for _, r := range []struct{ slug, project string }{{"api", "APP"}, {"ops", "OPS"}, {"tools", ""}, {"web", "APP"}} {
		repo := bitbucket.Repository{Slug: r.slug, Name: strings.ToUpper(r.slug[:1]) + r.slug[1:], Description: "the " + r.slug}
		if r.project != "" {
			repo.Project.Key, repo.Project.UUID, repo.Project.Name = r.project, "{"+strings.ToLower(r.project)+"-id}", r.project+" project"
		}
		f.repositories = append(f.repositories, repo)
	}
}

func searchedSlugs(t *testing.T, out string) []string {
	t.Helper()
	var slugs []string
	for _, r := range decodeResult[searchRepositoriesResult](t, out).Repositories {
		slugs = append(slugs, r.Slug)
	}
	return slugs
}

func TestSearchRepositoriesKeepsAllowedOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy Policy
		want   []string
		query  string // the filter that narrows the request, or none
	}{
		{"no limits", Policy{}, []string{"api", "ops", "tools", "web"}, ""},
		{"listed repositories", Policy{Repositories: []string{"ops", "tools"}}, []string{"ops", "tools"}, `(slug = "ops" OR slug = "tools")`},
		{"project or listed", Policy{Projects: []string{"APP"}, Repositories: []string{"tools"}}, []string{"api", "tools", "web"},
			`(project.key = "APP" OR slug = "tools")`},
		{"project by UUID", Policy{Projects: []string{"{ops-id}"}}, []string{"ops"}, `(project.uuid = "{ops-id}")`},
		{"no projects but listed", Policy{Projects: []string{}, Repositories: []string{"ops"}}, []string{"ops"}, `(slug = "ops")`},
		{"projectless repository", Policy{Projects: []string{"APP", "{ops-id}"}}, []string{"api", "ops", "web"}, `(project.key = "APP" OR project.uuid = "{ops-id}")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			addRepositories(f)
			out := mustCall(t, connectClient(t, newTestServer(t, f, tc.policy), clientOptions(nil), ""), "search_repositories", map[string]any{})
			if got := searchedSlugs(t, out); !slices.Equal(got, tc.want) {
				t.Fatalf("repositories = %v, want %v", got, tc.want)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.repoListQueries) != 1 || !sameQuery(f.repoListQueries[0], queryWith("1", "10", tc.query)) {
				t.Fatalf("queries = %q", f.repoListQueries)
			}
		})
	}
}

// queryWith is the query of a repository listing of page with pagelen and the filter q.
func queryWith(page, pagelen, q string) string {
	query := url.Values{"sort": {"slug"}, "page": {page}, "pagelen": {pagelen}}
	if q != "" {
		query.Set("q", q)
	}
	return query.Encode()
}

// A wider answer than asked for names no repository the policy forbids, and does not count them,
// even from another page.
func TestSearchRepositoriesDropsForbiddenAndTheirCount(t *testing.T) {
	f := newFake()
	addRepositories(f)
	f.repositories = append(f.repositories, bitbucket.Repository{Slug: "../x"})
	cs := connectClient(t, newTestServer(t, f, Policy{Projects: []string{"APP"}}), clientOptions(nil), "")
	out := mustCall(t, cs, "search_repositories", map[string]any{})
	if got, total := searchedSlugs(t, out), decodeResult[searchRepositoriesResult](t, out).Total; !slices.Equal(got, []string{"api", "web"}) || total != nil {
		t.Fatalf("repositories = %v, total %v", got, total)
	}
	out = mustCall(t, cs, "search_repositories", map[string]any{"limit": 1})
	if res := decodeResult[searchRepositoriesResult](t, out); len(res.Repositories) != 1 || res.Total != nil {
		t.Fatalf("a page without forbidden repositories = %+v", res)
	}
	unlimited := connect(t, f, nil)
	if got := searchedSlugs(t, mustCall(t, unlimited, "search_repositories", map[string]any{})); slices.Contains(got, "../x") {
		t.Fatalf("an invalid slug was listed: %v", got)
	}
}

func TestSearchRepositoriesNameProjectAndPages(t *testing.T) {
	f := newFake()
	addRepositories(f)
	cs := connect(t, f, nil)
	res := decodeResult[searchRepositoriesResult](t, mustCall(t, cs, "search_repositories", map[string]any{"query": "a", "project": "APP", "limit": 2}))
	if len(res.Repositories) != 2 || res.NextPage != 2 || res.Total == nil || *res.Total != 4 || res.ContentTrust != "untrusted" {
		t.Fatalf("first page = %+v", res)
	}
	api := res.Repositories[0]
	if api != (repositoryView{Slug: "api", Name: "Api", Project: "APP", ProjectName: "APP project", Description: "the api", URL: "https://bitbucket.org/ws/api"}) {
		t.Fatalf("repository = %+v", api)
	}
	res = decodeResult[searchRepositoriesResult](t, mustCall(t, cs, "search_repositories", map[string]any{"query": "a", "project": "APP", "limit": 2, "page": 2}))
	if got := []string{res.Repositories[0].Slug, res.Repositories[1].Slug}; !slices.Equal(got, []string{"tools", "web"}) || res.NextPage != 0 {
		t.Fatalf("second page = %+v", res)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if want := queryWith("2", "2", `name ~ "a" AND project.key = "APP"`); len(f.repoListQueries) != 2 || !sameQuery(f.repoListQueries[1], want) {
		t.Fatalf("queries = %q, want the second %q", f.repoListQueries, want)
	}
}

func TestSearchRepositoriesWithNothingAllowed(t *testing.T) {
	for _, policy := range []Policy{{Repositories: []string{}}, {Repositories: []string{}, Projects: []string{}}} {
		f := newFake()
		addRepositories(f)
		out := mustCall(t, connectClient(t, newTestServer(t, f, policy), clientOptions(nil), ""), "search_repositories", map[string]any{"query": "api"})
		if !strings.Contains(out, `"repositories":[]`) {
			t.Fatalf("result = %s", out)
		}
		f.mu.Lock()
		if len(f.repoListQueries) != 0 {
			t.Fatalf("listed although nothing is allowed: %q", f.repoListQueries)
		}
		f.mu.Unlock()
	}
}

func TestSearchRepositoriesRefusesBadInput(t *testing.T) {
	for want, args := range map[string]map[string]any{
		"limit must be between 1 and 50": {"limit": 51},
		"page must be 1 or more":         {"page": -1},
	} {
		f := newFake()
		if out, isErr := call(t, connect(t, f, nil), "search_repositories", args); !isErr || !strings.Contains(out, want) {
			t.Fatalf("result = %s (error %v), want %q", out, isErr, want)
		}
		f.mu.Lock()
		if len(f.repoListQueries) != 0 {
			t.Fatalf("listed although the input was refused: %q", f.repoListQueries)
		}
		f.mu.Unlock()
	}
}
