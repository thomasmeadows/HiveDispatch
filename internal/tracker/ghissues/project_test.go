package ghissues

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/config"
	"github.com/thomasmeadows/hivedispatch/internal/tracker"
)

func testProjectCfg(url string) config.GitHubConfig {
	cfg := testCfg(url)
	cfg.Project = config.GitHubProject{Owner: "thomasmeadows", Number: 2, Field: "Status", Columns: config.GitHubProjectColumns{
		Ready: "Ready", InProgress: "In Progress", NeedsInfo: "Needs Info", InReview: "In Review", NeedsHuman: "Needs Human",
	}}
	return cfg
}

// fakeBoard is a Projects v2 board served over a fake /graphql endpoint. It
// answers the four operations the client sends and records the mutations.
type fakeBoard struct {
	mu       sync.Mutex
	options  []string          // Status options on the board
	items    map[string]string // issue node id → item id
	lookups  int               // project resolutions
	added    []string          // issue node ids added
	moved    []string          // "item=option"
	scopeErr bool              // answer every mutation with INSUFFICIENT_SCOPES
}

func (b *fakeBoard) handle(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		t.Errorf("graphql auth = %q", r.Header.Get("Authorization"))
	}
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Errorf("graphql body: %v", err)
	}
	write := func(data string) { _, _ = w.Write([]byte(`{"data":` + data + `}`)) }
	switch {
	case strings.Contains(req.Query, "repositoryOwner("):
		b.lookups++
		if req.Variables["owner"] != "thomasmeadows" || req.Variables["number"] != float64(2) {
			t.Errorf("project lookup vars = %v", req.Variables)
		}
		var opts []string
		for i, o := range b.options {
			opts = append(opts, `{"id":"opt`+string(rune('a'+i))+`","name":"`+o+`"}`)
		}
		write(`{"repositoryOwner":{"projectV2":{"id":"PVT_1","title":"Hive Dispatch Issues","fields":{"nodes":[
			{},{"id":"F_title","name":"Title"},{"id":"F_status","name":"Status","options":[` + strings.Join(opts, ",") + `]}]}}}}`)
	case strings.Contains(req.Query, "projectItems("):
		id, _ := req.Variables["id"].(string)
		items := `[]`
		if item, ok := b.items[id]; ok {
			items = `[{"id":"` + item + `","project":{"id":"PVT_1"}},{"id":"other","project":{"id":"PVT_other"}}]`
		}
		write(`{"node":{"projectItems":{"nodes":` + items + `}}}`)
	case strings.Contains(req.Query, "addProjectV2ItemById("):
		if b.scopeErr {
			_, _ = w.Write([]byte(`{"data":null,"errors":[{"type":"INSUFFICIENT_SCOPES","message":"Your token has not been granted the required scopes to execute this query. The 'addProjectV2ItemById' field requires one of the following scopes: ['project'], but your token has only been granted the: ['repo'] scopes."}]}`))
			return
		}
		if req.Variables["project"] != "PVT_1" {
			t.Errorf("add vars = %v", req.Variables)
		}
		id, _ := req.Variables["content"].(string)
		b.added = append(b.added, id)
		b.items[id] = "item_" + id
		write(`{"addProjectV2ItemById":{"item":{"id":"item_` + id + `"}}}`)
	case strings.Contains(req.Query, "updateProjectV2ItemFieldValue("):
		if req.Variables["project"] != "PVT_1" || req.Variables["field"] != "F_status" {
			t.Errorf("update vars = %v", req.Variables)
		}
		b.moved = append(b.moved, req.Variables["item"].(string)+"="+req.Variables["option"].(string))
		write(`{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"` + req.Variables["item"].(string) + `"}}}`)
	default:
		t.Errorf("unexpected graphql query: %s", req.Query)
		w.WriteHeader(400)
	}
}

const allColumns = "Ready,In Progress,Needs Info,In Review,Needs Human"

func labelMux(t *testing.T, board *fakeBoard) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/issues/12", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Replace(issueFixture, `"number": 12,`, `"number": 12, "node_id": "I_12",`, 1)))
	})
	mux.HandleFunc("DELETE /repos/o/r/issues/12/labels/{name}", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	mux.HandleFunc("POST /repos/o/r/issues/12/labels", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) { board.handle(t, w, r) })
	return mux
}

func newProjectClient(t *testing.T, mux *http.ServeMux) *Client {
	t.Helper()
	c := newTestClient(t, mux)
	c.cfg = testProjectCfg(c.cfg.APIURL)
	return c
}

func TestTransitionAddsIssueToBoardAndSetsColumn(t *testing.T) {
	board := &fakeBoard{options: strings.Split(allColumns, ","), items: map[string]string{}}
	c := newProjectClient(t, labelMux(t, board))
	if err := c.Transition(context.Background(), "HD-12", tracker.StateInProgress); err != nil {
		t.Fatal(err)
	}
	if len(board.added) != 1 || board.added[0] != "I_12" {
		t.Errorf("added = %v", board.added)
	}
	if strings.Join(board.moved, ",") != "item_I_12=optb" {
		t.Errorf("moved = %v", board.moved)
	}
	// Already on the board: no second add, project resolved once.
	if err := c.Transition(context.Background(), "HD-12", tracker.StateInReview); err != nil {
		t.Fatal(err)
	}
	if len(board.added) != 1 || strings.Join(board.moved, ",") != "item_I_12=optb,item_I_12=optd" || board.lookups != 1 {
		t.Errorf("added=%v moved=%v lookups=%d", board.added, board.moved, board.lookups)
	}
}

func TestTransitionWithoutProjectNeverCallsGraphQL(t *testing.T) {
	board := &fakeBoard{items: map[string]string{}}
	c := newTestClient(t, labelMux(t, board))
	if err := c.Transition(context.Background(), "HD-12", tracker.StateInProgress); err != nil {
		t.Fatal(err)
	}
	if board.lookups != 0 || len(board.added) != 0 || len(board.moved) != 0 {
		t.Errorf("label-only config must not touch the board: %+v", board)
	}
}

func TestTransitionReportsMissingColumnAfterRefresh(t *testing.T) {
	board := &fakeBoard{options: []string{"Ready", "In Progress"}, items: map[string]string{}}
	c := newProjectClient(t, labelMux(t, board))
	if err := c.Transition(context.Background(), "HD-12", tracker.StateInProgress); err != nil {
		t.Fatal(err)
	}
	err := c.Transition(context.Background(), "HD-12", tracker.StateInReview)
	if err == nil || !strings.Contains(err.Error(), `"In Review"`) || !strings.Contains(err.Error(), "Hive Dispatch Issues") {
		t.Errorf("missing column must name the option and the board: %v", err)
	}
	if board.lookups != 2 {
		t.Errorf("a missing column must re-read the board once, lookups=%d", board.lookups)
	}
	// The column appears (added by the user); the next transition finds it.
	board.mu.Lock()
	board.options = append(board.options, "Needs Info", "In Review")
	board.mu.Unlock()
	if err := c.Transition(context.Background(), "HD-12", tracker.StateInReview); err != nil {
		t.Fatal(err)
	}
}

func TestTransitionExplainsMissingProjectScope(t *testing.T) {
	board := &fakeBoard{options: strings.Split(allColumns, ","), items: map[string]string{}, scopeErr: true}
	c := newProjectClient(t, labelMux(t, board))
	err := c.Transition(context.Background(), "HD-12", tracker.StateInProgress)
	if err == nil || !strings.Contains(err.Error(), "gh auth refresh -s project") {
		t.Errorf("scope error must say how to fix the token: %v", err)
	}
}

func TestCheckReportsBoardAndMissingColumns(t *testing.T) {
	board := &fakeBoard{options: []string{"Ready", "In Progress", "Done"}, items: map[string]string{}}
	var created []string
	labels := map[string][]string{"o/r": strings.Split("hive:ready,hive:in-progress,hive:needs-info,hive:in-review,hive:needs-human", ","), "o/other": strings.Split("hive:ready,hive:in-progress,hive:needs-info,hive:in-review,hive:needs-human", ",")}
	mux := adminMux(labels, &created)
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) { board.handle(t, w, r) })
	c := newProjectClient(t, mux)
	rep, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Project != "Hive Dispatch Issues" || strings.Join(rep.MissingColumns, ",") != "In Review,Needs Human,Needs Info" || rep.OK() {
		t.Errorf("rep = %+v", rep)
	}
	board.mu.Lock()
	board.options = strings.Split(allColumns, ",")
	board.mu.Unlock()
	c = newProjectClient(t, mux)
	rep, err = c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || len(rep.MissingColumns) != 0 {
		t.Errorf("rep = %+v", rep)
	}
}

func TestCheckReportsUnreachableBoard(t *testing.T) {
	var created []string
	labels := map[string][]string{"o/r": strings.Split("hive:ready,hive:in-progress,hive:needs-info,hive:in-review,hive:needs-human", ","), "o/other": strings.Split("hive:ready,hive:in-progress,hive:needs-info,hive:in-review,hive:needs-human", ",")}
	mux := adminMux(labels, &created)
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"repositoryOwner":{"projectV2":null}},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a ProjectV2 with the number 2."}]}`))
	})
	c := newProjectClient(t, mux)
	rep, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.ProjectError == "" || !strings.Contains(rep.ProjectError, "Could not resolve") || rep.OK() {
		t.Errorf("rep = %+v", rep)
	}
}
