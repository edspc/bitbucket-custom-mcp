package bitbucket

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestPullRequestStatusesDoesNotFollowNext(t *testing.T) {
	for _, knownTotal := range []bool{false, true} {
		t.Run(fmt.Sprint(knownTotal), func(t *testing.T) {
			requests := 0
			c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Query().Get("pagelen") != "50" {
					t.Error("request did not limit upstream records")
				}
				// Bitbucket lists the oldest first; the page must hold the newest.
				if r.URL.Query().Get("sort") != "-created_on" {
					t.Error("request did not ask for the newest results first")
				}
				page := map[string]any{"values": make([]CommitStatus, 50), "next": APIURL + r.URL.EscapedPath()[len("/2.0"):] + "?page=2"}
				if knownTotal {
					page["size"] = 500_000
				}
				_ = json.NewEncoder(w).Encode(page)
			})
			page, err := c.PullRequestStatuses(t.Context(), "repo-a", 7, 50)
			if err != nil || len(page.Values) != 50 || !page.More || requests != 1 {
				t.Fatalf("page=%+v, requests=%d, err=%v", page, requests, err)
			}
			if knownTotal && (page.Total == nil || *page.Total != 500_000) || !knownTotal && page.Total != nil {
				t.Fatal("incorrect reported total")
			}
		})
	}
}

func TestPullRequestDiffStatSkipsUnrequestedPages(t *testing.T) {
	var pages []int
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/pullrequests/") {
			w.Header().Set("Location", strings.Replace(diffLocation, "/diff/", "/diffstat/", 1))
			w.WriteHeader(http.StatusFound)
			return
		}
		n, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil || r.URL.Query().Get("pagelen") != "100" || r.URL.Query().Get("topic") != "true" {
			t.Fatalf("incorrect upstream page: %s", r.URL.RawQuery)
		}
		pages = append(pages, n)
		values := make([]DiffStat, 100)
		for i := range values {
			values[i].New = &CommitFile{Path: fmt.Sprint((n-1)*100 + i)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"values": values, "size": 100_000, "next": APIURL + r.URL.EscapedPath()[len("/2.0"):] + "?pagelen=100&topic=true&page=" + strconv.Itoa(n+1),
		})
	})
	got, err := c.PullRequestDiffStat(t.Context(), testDiffSnapshot(t, c, "diffstat"), 20_000, 200)
	if err != nil || len(got.Values) != 200 || !got.More || got.Total == nil || *got.Total != 100_000 || !reflect.DeepEqual(pages, []int{201, 202}) {
		t.Fatalf("pages=%v, result=%+v, err=%v", pages, got, err)
	}
	if got.Values[0].New.Path != "20000" || got.Values[199].New.Path != "20199" {
		t.Fatal("incorrect requested range")
	}
}

func TestCollectionPagesRejectOversizedOrEmptyNext(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"too many records", `{"values":[` + strings.Repeat(`{},`, 50) + `{}]}`},
		{"too many bytes", `{"values":[{"name":"` + strings.Repeat("x", maxPageBytes) + `"}]}`},
		{"empty continuing page", `{"values":[],"next":"` + APIURL + `/repeated"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, tc.body) })
			if _, err := c.PullRequestStatuses(t.Context(), "repo-a", 7, 50); err == nil {
				t.Fatal("unbounded or non-progressing response accepted")
			}
		})
	}
}

func TestDiffStatCannotTraverseUnboundedSmallPages(t *testing.T) {
	reads := 0
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/pullrequests/") {
			w.Header().Set("Location", strings.Replace(diffLocation, "/diff/", "/diffstat/", 1))
			w.WriteHeader(http.StatusFound)
			return
		}
		reads++
		_ = json.NewEncoder(w).Encode(map[string]any{"values": []DiffStat{{Status: "added"}}, "next": APIURL + r.URL.EscapedPath()[len("/2.0"):] + "?page=next"})
	})
	if _, err := c.PullRequestDiffStat(t.Context(), testDiffSnapshot(t, c, "diffstat"), 0, 200); err == nil || reads != 3 {
		t.Fatalf("unbounded traversal: reads=%d err=%v", reads, err)
	}
}
