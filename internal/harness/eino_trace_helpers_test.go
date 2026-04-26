package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	einosummarization "github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/schema"

	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/tools"
)

func TestSplitDynamicToolsForGoalCreatesDynamicPool(t *testing.T) {
	list := []tools.Tool{
		namedTool{spec: tools.Spec{Name: "web_search", Description: "Search the web", Kind: tools.KindBuiltin, Tags: []string{"web", "search"}}},
		namedTool{spec: tools.Spec{Name: "browser_fetch", Description: "Read web pages", Kind: tools.KindBuiltin, Tags: []string{"browser", "web"}}},
		namedTool{spec: tools.Spec{Name: "read_file", Description: "Read files", Kind: tools.KindBuiltin, Tags: []string{"filesystem"}}},
		namedTool{spec: tools.Spec{Name: "read_memory", Description: "Read memory", Kind: tools.KindBuiltin, Tags: []string{"memory"}}},
		namedTool{spec: tools.Spec{Name: "search_history", Description: "Search history", Kind: tools.KindBuiltin, Tags: []string{"history"}}},
		namedTool{spec: tools.Spec{Name: "search_scoped_memory", Description: "Scoped memory", Kind: tools.KindBuiltin, Tags: []string{"memory", "search"}}},
		namedTool{spec: tools.Spec{Name: "wiki_query", Description: "Wiki lookup", Kind: tools.KindBuiltin, Tags: []string{"wiki", "search"}}},
		namedTool{spec: tools.Spec{Name: "sandbox_exec", Description: "Run commands in a sandbox", Kind: tools.KindBuiltin, Tags: []string{"exec", "sandbox", "testing"}}},
		namedTool{spec: tools.Spec{Name: "opencli_run", Description: "Run external CLI", Kind: tools.KindCLI, Tags: []string{"cli", "run"}}},
		namedTool{spec: tools.Spec{Name: "build_skill", Description: "Build skill", Kind: tools.KindSkill, Tags: []string{"skill", "build"}}},
		namedTool{spec: tools.Spec{Name: "db_schema_lookup", Description: "Inspect schema", Kind: tools.KindBuiltin, Tags: []string{"database", "schema"}}},
	}

	base, dynamic := splitDynamicToolsForGoal("执行测试命令并验证结果", list)
	if len(dynamic) == 0 {
		t.Fatalf("expected dynamic tools to be created")
	}
	if !containsTool(base, "sandbox_exec") {
		t.Fatalf("expected sandbox_exec to stay in base tools")
	}
	if !containsTool(dynamic, "db_schema_lookup") {
		t.Fatalf("expected unrelated tool to move into dynamic pool")
	}
}

func TestAppendCustomizedActionStepRecordsSummarizationEvents(t *testing.T) {
	root := t.TempDir()
	h := New(root, session.NewStore(root), tools.NewRegistry(), 6)
	run := Run{
		ID:         "run_summary_trace",
		SessionKey: "session:summary",
		AgentName:  "default",
		Status:     "running",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	h.saveRun(run)

	before := &adk.AgentEvent{
		AgentName: "executor",
		Action: &adk.AgentAction{
			CustomizedAction: &einosummarization.CustomizedAction{
				Type: einosummarization.ActionTypeBeforeSummarize,
				Before: &einosummarization.BeforeSummarizeAction{
					Messages: []adk.Message{schema.UserMessage("a"), schema.AssistantMessage("b", nil)},
				},
			},
		},
	}
	generate := &adk.AgentEvent{
		AgentName: "executor",
		Action: &adk.AgentAction{
			CustomizedAction: &einosummarization.CustomizedAction{
				Type: einosummarization.ActionTypeGenerateSummary,
				GenerateSummary: &einosummarization.GenerateSummaryAction{
					Attempt:       1,
					Phase:         einosummarization.GenerateSummaryPhasePrimary,
					ModelResponse: schema.AssistantMessage("<summary>ok</summary>", nil),
				},
			},
		},
	}
	after := &adk.AgentEvent{
		AgentName: "executor",
		Action: &adk.AgentAction{
			CustomizedAction: &einosummarization.CustomizedAction{
				Type: einosummarization.ActionTypeAfterSummarize,
				After: &einosummarization.AfterSummarizeAction{
					Messages: []adk.Message{schema.UserMessage("summary")},
				},
			},
		},
	}

	if !appendCustomizedActionStep(h, run, before) || !appendCustomizedActionStep(h, run, generate) || !appendCustomizedActionStep(h, run, after) {
		t.Fatal("expected summarization actions to be recognized")
	}
	stored := h.mustGetRun(run.ID)
	if !hasRunStep(stored.Steps, "middleware_summarization_prepare") || !hasRunStep(stored.Steps, "middleware_summarization_attempt") || !hasRunStep(stored.Steps, "middleware_summarization_apply") {
		t.Fatalf("expected summarization trace steps, got %#v", stored.Steps)
	}
}

func TestRecordSummarizationArtifactsWritesSummaryFile(t *testing.T) {
	root := t.TempDir()
	h := New(root, session.NewStore(root), tools.NewRegistry(), 6)
	run := Run{
		ID:         "run_summary_artifact",
		SessionKey: "session:artifact",
		AgentName:  "default",
		Route:      "chatmodel",
		Status:     "running",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	h.saveRun(run)

	before := adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.UserMessage("请总结"),
			schema.AssistantMessage("处理中", nil),
		},
	}
	after := adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.SystemMessage("system"),
			&schema.Message{
				Role: schema.User,
				UserInputMultiContent: []schema.MessageInputPart{
					{Type: schema.ChatMessagePartTypeText, Text: "This session is being continued"},
					{Type: schema.ChatMessagePartTypeText, Text: "Continue with the last task"},
				},
			},
		},
	}

	h.recordSummarizationArtifacts(run, "executor", before, after)

	stored := h.mustGetRun(run.ID)
	if !hasRunStep(stored.Steps, "middleware_summarization") {
		t.Fatalf("expected middleware_summarization step, got %#v", stored.Steps)
	}
	entries, err := os.ReadDir(filepath.Join(root, "memory", "summaries"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected summarization artifact file to be written")
	}
	data, err := os.ReadFile(filepath.Join(root, "memory", "summaries", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Eino Summarization") || !strings.Contains(string(data), "Continue with the last task") {
		t.Fatalf("unexpected artifact content: %s", string(data))
	}
}

func TestConsumeEinoEventsPromotesToolSearchAndOffload(t *testing.T) {
	root := t.TempDir()
	h := New(root, session.NewStore(root), tools.NewRegistry(), 6)
	run := Run{
		ID:         "run_event_parse",
		SessionKey: "session:events",
		AgentName:  "default",
		Status:     "running",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	h.saveRun(run)
	req := Request{SessionKey: run.SessionKey, UserText: "继续"}

	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(adk.EventFromMessage(&schema.Message{
		Role:    schema.Tool,
		Content: `{"selectedTools":["db_schema_lookup","db_query"]}`,
	}, nil, schema.Tool, "tool_search"))
	gen.Send(adk.EventFromMessage(&schema.Message{
		Role:    schema.Tool,
		Content: "<persisted-output>Tool result saved to: /tmp/tool-output.txt\nUse read_file to view</persisted-output>",
	}, nil, schema.Tool, "read_file"))
	gen.Send(adk.EventFromMessage(schema.AssistantMessage("done", nil), nil, schema.Assistant, ""))
	gen.Close()

	result, err := h.consumeEinoEvents(iter, run, req)
	if err != nil {
		t.Fatal(err)
	}
	if result != "done" {
		t.Fatalf("unexpected final assistant message: %s", result)
	}
	stored := h.mustGetRun(run.ID)
	if !hasRunStep(stored.Steps, "tool_search") || !hasRunStep(stored.Steps, "tool_offload") {
		t.Fatalf("expected tool_search and tool_offload steps, got %#v", stored.Steps)
	}
}

func containsTool(list []tools.Tool, name string) bool {
	for _, item := range list {
		if item.Spec().Name == name {
			return true
		}
	}
	return false
}
