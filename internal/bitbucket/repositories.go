package bitbucket

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// Repository is a repository as the workspace listing shows it.
type Repository struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	Language    string `json:"language"`
	IsPrivate   bool   `json:"is_private"`
	UpdatedOn   string `json:"updated_on"`
	MainBranch  *struct {
		Name string `json:"name"`
	} `json:"mainbranch"`
	Project struct {
		Key  string `json:"key"`
		UUID string `json:"uuid"`
		Name string `json:"name"`
	} `json:"project"`
}

// RepositoryFilter picks repositories of the workspace. Slugs and Projects limit them the way the
// server policy does: when Projects is set, to repositories in one of those projects, by key or
// UUID, or with one of the slugs; otherwise, when Slugs is set, to those slugs. Nil allows all.
type RepositoryFilter struct {
	Name     string // a part of the name
	Project  string // a project key
	Slugs    []string
	Projects []string
}

// query is the filter in Bitbucket's query language; none means that it picks no repository.
func (f RepositoryFilter) query() (q string, none bool) {
	var all []string
	if f.Name != "" {
		all = append(all, "name ~ "+queryString(f.Name))
	}
	if f.Project != "" {
		all = append(all, "project.key = "+queryString(f.Project))
	}
	if f.Slugs != nil || f.Projects != nil {
		var allowed []string
		// A project is configured by key, or by UUID in braces, which a key cannot hold.
		for _, p := range f.Projects {
			field := "project.key = "
			if strings.HasPrefix(p, "{") {
				field = "project.uuid = "
			}
			allowed = append(allowed, field+queryString(p))
		}
		for _, s := range f.Slugs {
			allowed = append(allowed, "slug = "+queryString(s))
		}
		if len(allowed) == 0 {
			return "", true
		}
		all = append(all, "("+strings.Join(allowed, " OR ")+")")
	}
	return strings.Join(all, " AND "), false
}

// ListRepositories returns page number page, from 1, of the repositories of the workspace that f
// picks, limit a page, by slug. The listing names no repository, so the project check of other
// requests cannot vet it: f narrows the request, and callers keep only allowed repositories.
func (c *Client) ListRepositories(ctx context.Context, f RepositoryFilter, page, limit int) (Page[Repository], error) {
	q, none := f.query()
	if none {
		return Page[Repository]{}, nil
	}
	values := url.Values{"sort": {"slug"}, "pagelen": {strconv.Itoa(limit)}, "page": {strconv.Itoa(page)}}
	if q != "" {
		values.Set("q", q)
	}
	p, err := readWorkspacePage[Repository](ctx, c, APIURL+escapedPath("repositories", c.cfg.Workspace)+"?"+values.Encode(), limit)
	return Page[Repository]{Values: p.Values, Total: p.Size, More: p.Next != ""}, err
}

// RepositoryURL is the page of a repository on the Bitbucket website.
func (c *Client) RepositoryURL(repo string) string { return c.repoWebURL(repo) }
