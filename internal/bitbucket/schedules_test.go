package bitbucket

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestScheduleRequests(t *testing.T) {
	var got []string
	c := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil { // client requests without a body reach the handler as they are
			body, _ = io.ReadAll(r.Body)
		}
		got = append(got, strings.TrimSpace(r.Method+" "+r.URL.EscapedPath()+" "+string(body)))
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"uuid": "{sch-1}", "enabled": true, "cron_pattern": "0 0 2 * * ? *"}`)
		case http.MethodGet:
			_, _ = fmt.Fprint(w, `{"values": [{"uuid": "{sch-1}", "target": {"ref_name": "main", "selector": {"type": "custom", "pattern": "nightly"}}}]}`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	ctx := context.Background()
	created, err := c.CreateSchedule(ctx, "repo-a", Schedule{
		Enabled:     true,
		Target:      PipelineTarget{Type: "pipeline_ref_target", RefType: "branch", RefName: "main", Selector: &Selector{Type: "custom", Pattern: "nightly"}},
		CronPattern: "0 0 2 * * ? *",
	})
	if err != nil || created.UUID != "{sch-1}" {
		t.Fatalf("CreateSchedule = %+v, %v", created, err)
	}
	schedules, err := c.ListSchedules(ctx, "repo-a")
	if err != nil || len(schedules) != 1 || schedules[0].Target.Selector.Pattern != "nightly" {
		t.Fatalf("ListSchedules = %+v, %v", schedules, err)
	}
	if err = c.DeleteSchedule(ctx, "repo-a", "{sch-1}"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`POST /2.0/repositories/ws/repo-a/pipelines_config/schedules {"type":"pipeline_schedule","enabled":true,` +
			`"target":{"type":"pipeline_ref_target","ref_type":"branch","ref_name":"main","selector":{"type":"custom","pattern":"nightly"}},` +
			`"cron_pattern":"0 0 2 * * ? *"}`,
		"GET /2.0/repositories/ws/repo-a/pipelines_config/schedules",
		"DELETE /2.0/repositories/ws/repo-a/pipelines_config/schedules/%7Bsch-1%7D",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
