package langgraph

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thomasmeadows/hivedispatch/internal/executor"
	exfake "github.com/thomasmeadows/hivedispatch/internal/executor/fake"
	"github.com/thomasmeadows/hivedispatch/internal/trace"
	tracefake "github.com/thomasmeadows/hivedispatch/internal/trace/fake"
)

// fakeGraph writes a hivegraph that saves its stdin to task.json and its
// environment to env.txt, then prints events.
func fakeGraph(t *testing.T, events string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "hivegraph")
	script := "#!/bin/sh\ncat > " + filepath.Join(dir, "task.json") + "\nenv > " + filepath.Join(dir, "env.txt") + "\ncat " + filepath.Join(dir, "events.jsonl") + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

// gitRepo is a workspace with one commit and a policy, so base_ref,
// git_dir, checks and guidance resolve.
func gitRepo(t *testing.T) string {
	t.Helper()
	w := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", w}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(w, ".hive-dispatch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w, ".hive-dispatch", "policy.yaml"), []byte("checks:\n  - go test ./...\nguidance: be nice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return w
}

func newExec(bin string) *Executor {
	return New(Config{Binary: bin, Hivedispatch: "/bin/hivedispatch", WorkerConfig: "/cfg.yaml",
		Model: ModelConfig{Provider: "deepseek", Model: "deepseek-flash", BaseURL: "https://api.deepseek.com/v1", APIKeyEnv: "DEEPSEEK_API_KEY"},
		Inner: map[string]executor.Executor{"fake": exfake.New()}})
}

const okEvents = `{"type":"step","kind":"tool","name":"Bash","input":"{}","output":"ok","start":"2026-10-01T12:00:00Z","end":"2026-10-01T12:00:01Z"}
{"type":"usage","model":"claude-opus-5-5","input_tokens":3,"output_tokens":1}
{"type":"result","status":"completed","summary":"done","resume_token":"th-1","changed_files":["a.go"],"steps_traced":true}
`

func TestRunPassesTaskAndMapsResult(t *testing.T) {
	bin, dir := fakeGraph(t, okEvents)
	w := gitRepo(t)
	res, err := newExec(bin).Run(context.Background(), executor.Task{TicketKey: "HIVE-1", Prompt: "do it", Workspace: w, StepBudget: 50, CodeWith: "codex", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != executor.StatusCompleted || res.Summary != "done" || res.ResumeToken != "th-1" || len(res.Steps) != 1 || !res.StepsTraced || res.Usage.InputTokens != 3 || res.Log == "" {
		t.Fatalf("res %+v", res)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var task map[string]any
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	if task["ticket"] != "HIVE-1" || task["code_with"] != "codex" || task["model"] != "m" || task["guidance"] != "be nice" || task["hivedispatch"] != "/bin/hivedispatch" || task["max_fix_rounds"] != float64(3) {
		t.Fatalf("task %v", task)
	}
	if checks, _ := task["checks"].([]any); len(checks) != 1 {
		t.Fatalf("checks %v", task["checks"])
	}
	if task["base_ref"] == "" || !strings.Contains(task["git_dir"].(string), ".git") {
		t.Fatalf("git refs %v %v", task["base_ref"], task["git_dir"])
	}
	if cm, _ := task["chat_model"].(map[string]any); cm["api_key_env"] != "DEEPSEEK_API_KEY" {
		t.Fatalf("chat model %v", task["chat_model"])
	}
}

func TestRunWithoutResultFails(t *testing.T) {
	bin, _ := fakeGraph(t, "")
	res, err := newExec(bin).Run(context.Background(), executor.Task{Prompt: "x", Workspace: gitRepo(t)})
	if err != nil || res.Status != executor.StatusFailed || res.StopCause != executor.CauseError {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestRunNeedsInput(t *testing.T) {
	bin, _ := fakeGraph(t, `{"type":"result","status":"needs_input","question":"Which DB?","summary":"asked","resume_token":"th-2"}`+"\n")
	res, err := newExec(bin).Run(context.Background(), executor.Task{Prompt: "x", Workspace: gitRepo(t)})
	if err != nil || res.Status != executor.StatusNeedsInput || res.Question != "Which DB?" || res.ResumeToken != "th-2" {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestRunFailedResultKeepsItsCause(t *testing.T) {
	bin, _ := fakeGraph(t, `{"type":"result","status":"failed","stop_cause":"budget","summary":"quota"}`+"\n")
	res, err := newExec(bin).Run(context.Background(), executor.Task{Prompt: "x", Workspace: gitRepo(t)})
	if err != nil || res.Status != executor.StatusFailed || res.StopCause != executor.CauseBudget || res.Summary != "quota" {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestRunSetsTraceParent(t *testing.T) {
	bin, dir := fakeGraph(t, okEvents)
	tr := trace.New(&tracefake.Exporter{}, trace.Options{})
	ctx, span := tr.Start(context.Background(), "run langgraph", trace.KindChain, nil)
	defer span.End(nil, nil)
	if _, err := newExec(bin).Run(ctx, executor.Task{Prompt: "x", Workspace: gitRepo(t)}); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "LANGSMITH_PARENT="+span.DottedOrder()) {
		t.Fatal("LANGSMITH_PARENT not passed")
	}
}

func TestAdviseDelegates(t *testing.T) {
	bin, _ := fakeGraph(t, "")
	raw, err := newExec(bin).Advise(context.Background(), executor.Advice{Kind: executor.AdvicePlan, CodeWith: "fake"})
	if err != nil || !strings.Contains(string(raw), "plan") {
		t.Fatalf("raw %s err %v", raw, err)
	}
	if _, err := newExec(bin).Advise(context.Background(), executor.Advice{CodeWith: "codex"}); err == nil {
		t.Fatal("a missing inner executor must be an error")
	}
}
