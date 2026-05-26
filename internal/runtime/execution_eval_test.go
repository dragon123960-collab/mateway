package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/tool"
)

func TestExecutionEvalDetectsMissingEvidence(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.Definition{
		Name:       "fake.empty",
		ArgsSchema: map[string]string{},
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			return tool.Result{OK: true, Output: "ok"}
		},
	})
	rt := Runtime{Tools: registry, ToolCtx: tool.Context{ProjectRoot: t.TempDir()}, MaxSteps: 6}
	results, control := rt.ExecutePlanForEval(context.Background(), "trace", model.Plan{Summary: "empty evidence", Steps: []model.PlanStep{{
		ID:               "s1",
		Tool:             "fake.empty",
		Args:             map[string]string{},
		ExpectedEvidence: []string{"file path"},
	}}}, false, "")
	if control != "" {
		t.Fatalf("expected no control, got %q", control)
	}
	if len(results) != 1 || results[0].OK || results[0].Error != "step_verification_failed" {
		t.Fatalf("expected step verifier failure, got %#v", results)
	}
}

func TestExecutionEvalTerminalSoftFailureBecomesSuspect(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.Definition{
		Name: "terminal.run",
		Metadata: tool.Metadata{
			AcceptanceMode:     tool.AcceptanceCodeLLM,
			SoftFailureSignals: []string{"data not found"},
		},
		ArgsSchema: map[string]string{"command": "command"},
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			return tool.Result{
				OK:     true,
				Output: "data not found",
				Evidence: map[string]any{
					"kind":      "terminal",
					"exit_code": 0,
				},
			}
		},
	})
	rt := Runtime{
		Model:    &fakePlanner{stepAcceptText: `{"status":"suspect","reason":"soft failure output"}`},
		Tools:    registry,
		ToolCtx:  tool.Context{ProjectRoot: t.TempDir()},
		MaxSteps: 6,
	}
	results, control := rt.ExecutePlanForEval(context.Background(), "trace", model.Plan{Summary: "soft fail", Steps: []model.PlanStep{{
		ID: "s1", Tool: "terminal.run", Args: map[string]string{"command": "echo"}, ExpectedEvidence: []string{"exit code"},
	}}}, false, "")
	if control != "" {
		t.Fatalf("expected no control, got %q", control)
	}
	if len(results) != 1 || results[0].Error != "step_acceptance_suspect" {
		t.Fatalf("expected suspect result, got %#v", results)
	}
}

func TestExecutionEvalParallelReadOnlyBatch(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.Definition{
		Name: "fake.read1",
		Metadata: tool.Metadata{
			AcceptanceMode: tool.AcceptanceCodeOnly,
			ParallelMode:   tool.ParallelReadOnlyOK,
			ResourceScope:  "filesystem:path",
		},
		Risk: tool.RiskSafeRead,
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			time.Sleep(30 * time.Millisecond)
			return tool.Result{OK: true, Output: "ok", Evidence: map[string]any{"kind": "file_read", "path": "/tmp/a"}}
		},
	})
	registry.Register(tool.Definition{
		Name: "fake.read2",
		Metadata: tool.Metadata{
			AcceptanceMode: tool.AcceptanceCodeOnly,
			ParallelMode:   tool.ParallelReadOnlyOK,
			ResourceScope:  "web:query",
		},
		Risk: tool.RiskSafeRead,
		Run: func(ctx context.Context, call tool.Call) tool.Result {
			time.Sleep(30 * time.Millisecond)
			return tool.Result{OK: true, Output: "ok", Evidence: map[string]any{"kind": "web_search", "query": "x", "result_count": 1}}
		},
	})
	rt := Runtime{Model: &fakePlanner{}, Tools: registry, ToolCtx: tool.Context{ProjectRoot: t.TempDir()}, MaxSteps: 6}
	results, control := rt.ExecutePlanForEval(context.Background(), "trace", model.Plan{Summary: "parallel", Steps: []model.PlanStep{
		{ID: "s1", Tool: "fake.read1", Args: map[string]string{"path": "a"}, ExpectedEvidence: []string{"file path"}},
		{ID: "s2", Tool: "fake.read2", Args: map[string]string{"query": "x"}, ExpectedEvidence: []string{"query"}},
	}}, false, "")
	if control != "" {
		t.Fatalf("expected no control, got %q", control)
	}
	if len(results) != 2 || !results[0].OK || !results[1].OK {
		t.Fatalf("expected two successful results, got %#v", results)
	}
}

func TestExecutionEvalFileWriteAndScheduleCreateRequireConfirmation(t *testing.T) {
	root := t.TempDir()
	rt := Runtime{Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{Home: root, ProjectRoot: root, Workspace: root}, MaxSteps: 6}
	target := filepath.Join(root, "out.txt")
	plan := model.Plan{Summary: "write and schedule", Steps: []model.PlanStep{
		{ID: "s1", Tool: "file.write", Args: map[string]string{"path": target, "content": "ok"}, ExpectedEvidence: []string{"file path"}},
		{ID: "s2", Tool: "schedule.create", Args: map[string]string{"id": "ai-trends", "title": "AI Trends", "prompt": "Collect AI trends.", "daily_at": "09:00"}, ExpectedEvidence: []string{"schedule task id and path"}},
	}}
	results, control := rt.ExecutePlanForEval(context.Background(), "trace", plan, false, "")
	if control != "await_confirm" || len(results) != 1 || results[0].Error != "await_confirm" {
		t.Fatalf("expected file write confirmation, control=%q results=%#v", control, results)
	}
	results, control = rt.ExecutePlanForEval(context.Background(), "trace", plan, true, "s1")
	if control != "await_confirm" || len(results) != 2 || !results[0].OK || results[1].Error != "await_confirm" {
		t.Fatalf("expected schedule create confirmation after approved file write, control=%q results=%#v", control, results)
	}
	results, control = rt.ExecutePlanForEval(context.Background(), "trace", plan, true, "s2")
	if control != "await_confirm" || len(results) != 1 || results[0].Error != "await_confirm" {
		t.Fatalf("expected file write confirmation on fresh execution, control=%q results=%#v", control, results)
	}
	results, control = rt.ExecutePlanForEval(context.Background(), "trace", plan, true, "s2")
	if control != "await_confirm" || len(results) != 1 || results[0].Error != "await_confirm" {
		t.Fatalf("expected approval to be step-specific, control=%q results=%#v", control, results)
	}
	previous := map[string]session.StepState{
		"s1": {
			ID:            "s1",
			Tool:          "file.write",
			Status:        "passed",
			ResultOK:      true,
			ResultSummary: "wrote file",
			Evidence:      map[string]any{"kind": "file_write", "path": target, "bytes": 2},
		},
		"s2": {
			ID:               "s2",
			Tool:             "schedule.create",
			Status:           "blocked",
			ResultOK:         false,
			ResultError:      "await_confirm",
			AcceptanceStatus: "await_confirm",
		},
	}
	results, control = rt.executePlan(context.Background(), "trace", plan, true, "s2", previous, nil)
	if control != "" || len(results) != 2 || !results[0].OK || !results[1].OK {
		t.Fatalf("expected successful execution after preserving approved step state, control=%q results=%#v", control, results)
	}
	if kind, _ := results[0].Evidence["kind"].(string); kind != "file_write" {
		t.Fatalf("expected file evidence, got %#v", results[0].Evidence)
	}
	if kind, _ := results[1].Evidence["kind"].(string); kind != "schedule_create" {
		t.Fatalf("expected schedule evidence, got %#v", results[1].Evidence)
	}
}

func TestExecutionEvalScheduleDeleteRequiresConfirmation(t *testing.T) {
	root := t.TempDir()
	rt := Runtime{Tools: tool.NewBuiltinRegistry(), ToolCtx: tool.Context{Home: root, ProjectRoot: root, Workspace: root}, MaxSteps: 6}
	create := model.Plan{Summary: "create", Steps: []model.PlanStep{{
		ID: "s1", Tool: "schedule.create", Args: map[string]string{"id": "ai-trends", "title": "AI Trends", "prompt": "Collect AI trends.", "daily_at": "09:00"},
	}}}
	if results, _ := rt.ExecutePlanForEval(context.Background(), "trace", create, true, "s1"); len(results) != 1 || !results[0].OK {
		t.Fatalf("expected setup schedule create, got %#v", results)
	}
	deletePlan := model.Plan{Summary: "delete", Steps: []model.PlanStep{{
		ID: "s1", Tool: "schedule.delete", Args: map[string]string{"id": "ai-trends"},
	}}}
	results, control := rt.ExecutePlanForEval(context.Background(), "trace", deletePlan, false, "")
	if control != "await_confirm" || len(results) != 1 || results[0].Error != "await_confirm" {
		t.Fatalf("expected delete confirmation, control=%q results=%#v", control, results)
	}
	results, control = rt.ExecutePlanForEval(context.Background(), "trace", deletePlan, true, "s1")
	if control != "" || len(results) != 1 || !results[0].OK {
		t.Fatalf("expected confirmed delete success, control=%q results=%#v", control, results)
	}
	if !strings.Contains(results[0].Output, "Deleted schedule task") {
		t.Fatalf("expected delete output, got %q", results[0].Output)
	}
}
