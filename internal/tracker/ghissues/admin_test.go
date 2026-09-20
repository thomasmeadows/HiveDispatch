package ghissues

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func adminMux(labels map[string][]string, created *[]string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"login":"thomas"}`)) })
	mux.HandleFunc("GET /repos/{o}/{r}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("r") == "missing" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"full_name":"` + r.PathValue("o") + "/" + r.PathValue("r") + `","has_issues":true,"permissions":{"push":true}}`))
	})
	mux.HandleFunc("GET /repos/{o}/{r}/labels", func(w http.ResponseWriter, r *http.Request) {
		var out []map[string]string
		for _, n := range labels[r.PathValue("o")+"/"+r.PathValue("r")] {
			out = append(out, map[string]string{"name": n})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /repos/{o}/{r}/labels", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		*created = append(*created, r.PathValue("o")+"/"+r.PathValue("r")+":"+b["name"])
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("GET /repos/{o}/{r}/issues", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"number":1,"title":"x","labels":[{"name":"hive:ready"}],"updated_at":"2026-09-20T10:00:00Z"}]`))
	})
	mux.HandleFunc("GET /repos/{o}/{r}/issues/{n}/comments", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /repos/{o}/{r}/issues/{n}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"number":1,"title":"x","body":"hello","labels":[{"name":"hive:ready"}],"updated_at":"2026-09-20T10:00:00Z"}`))
	})
	mux.HandleFunc("PATCH /repos/{o}/{r}/issues/{n}", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["body"] != "hello" {
			w.WriteHeader(400) // the probe must not change anything
			return
		}
		if readOnlyToken {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by personal access token"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	return mux
}

var readOnlyToken bool

func TestCheckReportsMissingLabels(t *testing.T) {
	var created []string
	labels := map[string][]string{"o/r": {"hive:ready", "hive:in-progress", "bug"}, "o/other": nil}
	c := newTestClient(t, adminMux(labels, &created))
	rep, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.User != "thomas" || rep.SampleTickets != 2 {
		t.Errorf("rep = %+v", rep)
	}
	if len(rep.MissingLabels["o/r"]) != 3 || len(rep.MissingLabels["o/other"]) != 5 {
		t.Errorf("missing = %v", rep.MissingLabels)
	}
	if rep.OK() {
		t.Error("missing labels must fail the check")
	}
	if rep.NotWritable != "" {
		t.Errorf("token writes fine here: %q", rep.NotWritable)
	}
}

func TestCheckDetectsReadOnlyToken(t *testing.T) {
	readOnlyToken = true
	t.Cleanup(func() { readOnlyToken = false })
	var created []string
	labels := map[string][]string{"o/r": {"hive:ready", "hive:in-progress", "hive:needs-info", "hive:in-review", "hive:needs-human"}, "o/other": {"hive:ready", "hive:in-progress", "hive:needs-info", "hive:in-review", "hive:needs-human"}}
	c := newTestClient(t, adminMux(labels, &created))
	rep, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.NotWritable == "" || rep.OK() {
		t.Errorf("read-only token must fail the check: %+v", rep)
	}
}

func TestEnsureLabelsCreatesOnlyMissing(t *testing.T) {
	var created []string
	labels := map[string][]string{"o/r": {"hive:ready", "hive:in-progress", "hive:needs-info", "hive:in-review", "hive:needs-human"}, "o/other": {"hive:ready"}}
	c := newTestClient(t, adminMux(labels, &created))
	n, err := c.EnsureLabels(context.Background())
	if err != nil || n != 4 {
		t.Fatalf("n=%d err=%v created=%v", n, err, created)
	}
	for _, want := range []string{"o/other:hive:in-progress", "o/other:hive:needs-human"} {
		found := false
		for _, c := range created {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Errorf("did not create %s: %v", want, created)
		}
	}
}
