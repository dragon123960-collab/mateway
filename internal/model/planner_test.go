package model

import (
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/tool"
)

func TestParsePlanRepairsUnescapedChineseQuotes(t *testing.T) {
	raw := `{
  "summary": "读取测试文档、添加一行、运行pwd并写入报告",
  "steps": [
    {
      "id": "step-1",
      "goal": "读取测试文档内容，定位"本轮清单"位置",
      "tool": "file.read",
      "args": {
        "path": "/Users/dongping/project/mateway/docs/测试文档.md"
      },
      "risk": "safe_read",
      "requires_confirm": false,
      "expected_evidence": ["文档内容，包含"本轮清单"章节"]
    }
  ]
}`
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected one step, got %#v", plan.Steps)
	}
	if !strings.Contains(plan.Steps[0].Goal, `"本轮清单"`) {
		t.Fatalf("expected repaired inner quotes, got %q", plan.Steps[0].Goal)
	}
	if !strings.Contains(plan.Steps[0].ExpectedEvidence[0], `"本轮清单"`) {
		t.Fatalf("expected repaired evidence quotes, got %#v", plan.Steps[0].ExpectedEvidence)
	}
}

func TestParsePlanRepairsMissingStepObjectClosures(t *testing.T) {
	raw := `{"summary":"搜索2025-2026年AI趋势，聚焦独立开发者可切入的方向","steps":[{"id":"step-1","goal":"搜索2025-2026年AI技术趋势最新动态","tool":"web.search","args":{"max_results":10,"query":"AI trends 2025 2026 latest developments"},{"id":"step-2","goal":"搜索独立开发者做AI应用的方向和机会","tool":"web.search","args":{"max_results":10,"query":"indie developer AI app opportunities 2025"},{"id":"step-3","goal":"搜索AI应用市场现状和变现模式","tool":"web.search","args":{"max_results":8,"query":"AI application market trends monetization 2025"},{"id":"step-4","goal":"搜索中国AI应用趋势","tool":"web.search","args":{"max_results":8,"query":"2025中国AI应用趋势 独立开发者机会"}]}`
	result, err := CheckAndRepairPlanJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	plan := result.Plan
	if len(plan.Steps) != 4 {
		t.Fatalf("expected four repaired steps, got %#v", plan.Steps)
	}
	if plan.Steps[0].Tool != "web.search" || plan.Steps[3].Args["query"] != "2025中国AI应用趋势 独立开发者机会" {
		t.Fatalf("unexpected repaired plan %#v", plan)
	}
	if !result.Fixed || !containsString(result.Warnings, "repaired_missing_step_object_closures") {
		t.Fatalf("expected repair warning, got fixed=%t warnings=%v", result.Fixed, result.Warnings)
	}
}

func TestPlanCheckerRejectsEmptySteps(t *testing.T) {
	_, err := CheckAndRepairPlanJSON(`{"summary":"empty","steps":[]}`)
	if err == nil || !strings.Contains(err.Error(), "steps must not be empty") {
		t.Fatalf("expected empty steps schema error, got %v", err)
	}
}

func TestPlanCheckerRejectsMissingTool(t *testing.T) {
	_, err := CheckAndRepairPlanJSON(`{"summary":"bad","steps":[{"id":"s1","args":{}}]}`)
	if err == nil || !strings.Contains(err.Error(), "tool is required") {
		t.Fatalf("expected missing tool schema error, got %v", err)
	}
}

func TestPlanCheckerNormalizesMissingIDAndArgs(t *testing.T) {
	result, err := CheckAndRepairPlanJSON(`{"summary":"","understanding":{"goal":"check time"},"steps":[{"tool":"time.now"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Plan.Summary != "execute user task" || result.Plan.Steps[0].ID != "step-1" || result.Plan.Steps[0].Args == nil {
		t.Fatalf("expected normalized plan, got %#v", result.Plan)
	}
	if !result.Fixed {
		t.Fatalf("expected normalization to mark result fixed")
	}
}

func TestPlanCheckerParsesUnderstandingBlock(t *testing.T) {
	result, err := CheckAndRepairPlanJSON(`{"summary":"install tool","understanding":{"goal":"install lark cli","subtasks":["find install method","run install"],"tool_needs":["software.search","software.install"],"completion_criteria":["installed cli is verified"],"evidence_expectations":["install command","verify command"],"risk_level":"guarded_mutation"},"steps":[{"id":"s1","tool":"software.search","args":{"query":"lark cli install"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Plan.Understanding.Goal != "install lark cli" {
		t.Fatalf("expected understanding goal, got %#v", result.Plan.Understanding)
	}
	if len(result.Plan.Understanding.Subtasks) != 2 || len(result.Plan.Understanding.ToolNeeds) != 2 {
		t.Fatalf("expected understanding arrays parsed, got %#v", result.Plan.Understanding)
	}
}

func TestPlanCheckerAcceptsSingleStringEvidenceAndCriteria(t *testing.T) {
	result, err := CheckAndRepairPlanJSON(`{"summary":"search","steps":[{"id":"s1","tool":"web.search","args":{"query":"AI trends"},"expected_evidence":"search results with URLs","success_criteria":"results are returned"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	step := result.Plan.Steps[0]
	if len(step.ExpectedEvidence) != 1 || step.ExpectedEvidence[0] != "search results with URLs" {
		t.Fatalf("expected single evidence string to normalize, got %#v", step.ExpectedEvidence)
	}
	if len(step.SuccessCriteria) != 1 || step.SuccessCriteria[0] != "results are returned" {
		t.Fatalf("expected single success criteria string to normalize, got %#v", step.SuccessCriteria)
	}
}

func TestPlanCheckerRepairsStrayBracketAfterStringValue(t *testing.T) {
	result, err := CheckAndRepairPlanJSON(`{"summary":"diagnose","steps":[{"id":"s1","tool":"terminal.run","args":{"command":"ps aux","purpose":"check local status"],"risk":"safe_read","expected_evidence":["process output"],"success_criteria":["output collected"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Plan.Steps[0].Args["purpose"]; got != "check local status" {
		t.Fatalf("expected repaired purpose, got %q", got)
	}
	if !result.Fixed || !containsString(result.Warnings, "repaired_stray_string_value_bracket") {
		t.Fatalf("expected repair warning, got fixed=%t warnings=%v", result.Fixed, result.Warnings)
	}
}

func TestPlanCheckerRepairsMissingStringArrayClosure(t *testing.T) {
	result, err := CheckAndRepairPlanJSON(`{"summary":"test","steps":[{"id":"s1","tool":"terminal.run","args":{"command":"go test ./..."},"expected_evidence":["测试输出结果"],"success_criteria":["测试执行完成，返回明确状态"},"on_failure":"repair"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Plan.Steps[0].SuccessCriteria[0]; got != "测试执行完成，返回明确状态" {
		t.Fatalf("expected repaired success criteria, got %#v", result.Plan.Steps[0].SuccessCriteria)
	}
	if !result.Fixed || !containsString(result.Warnings, "repaired_missing_string_array_closures") {
		t.Fatalf("expected repair warning, got fixed=%t warnings=%v", result.Fixed, result.Warnings)
	}
}

func TestSynthesisResultViewHidesStepIDsAndTruncatesOutput(t *testing.T) {
	results := []ToolResult{{
		StepID: "step-2",
		Tool:   "skill.search",
		OK:     true,
		Output: strings.Repeat("x", 1200),
		Evidence: map[string]any{
			"kind":         "skill_search",
			"query":        "text humanizer",
			"result_count": 0,
		},
	}}
	view := synthesisResultView(results)
	if len(view) != 1 {
		t.Fatalf("expected one view item, got %#v", view)
	}
	if _, ok := view[0]["step_id"]; ok {
		t.Fatalf("expected step id to be hidden, got %#v", view[0])
	}
	summary, _ := view[0]["summary"].(string)
	if len([]rune(summary)) > 910 || !strings.HasSuffix(summary, "...") {
		t.Fatalf("expected truncated summary, got len=%d summary=%q", len([]rune(summary)), summary)
	}
}

func TestPlannerPromptKeepsSkillSearchCapabilityDriven(t *testing.T) {
	prompt := plannerSystemPrompt("")
	if !strings.Contains(prompt, "first understand the capability") || !strings.Contains(prompt, "concise capability keywords") {
		t.Fatalf("expected capability-driven skill search instruction, got %q", prompt)
	}
}

func TestPlannerPromptPutsCriticalRulesAtBeginningAndEnd(t *testing.T) {
	prompt := plannerSystemPrompt("")
	if !strings.Contains(prompt, "Most important rules:") || !strings.Contains(prompt, "Final reminders:") {
		t.Fatalf("expected prompt to have emphasized beginning/end sections, got %q", prompt)
	}
	if !strings.Contains(prompt, "Return ONLY strict JSON.") || !strings.Contains(prompt, "Do not invent unavailable tools") {
		t.Fatalf("expected critical reminder text, got %q", prompt)
	}
	if !strings.Contains(prompt, "understanding.tool_needs to capture the concrete tools") {
		t.Fatalf("expected tool_needs contract to mention concrete tools, got %q", prompt)
	}
}

func TestToolListForPromptIncludesStructuredMetadata(t *testing.T) {
	text := toolListForPrompt([]tool.Definition{{
		Name:        "file.summary",
		Description: "Summarize one file",
		Risk:        tool.RiskSafeRead,
		ArgsSchema:  map[string]string{"path": "file path"},
		Metadata: tool.Metadata{
			WhenToUse:      []string{"before reading full file"},
			WhenNotToUse:   []string{"editing files"},
			OutputContract: []string{"preview lines"},
			AcceptanceMode: tool.AcceptanceCodeLLM,
			ParallelMode:   tool.ParallelReadOnlyOK,
			ResourceScope:  "filesystem:path",
		},
	}})
	for _, want := range []string{
		"when_to_use=[before reading full file]",
		"when_not_to_use=[editing files]",
		"output_contract=[preview lines]",
		"acceptance_mode=code_then_llm",
		"parallel_mode=read_only_ok",
		"resource_scope=filesystem:path",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected prompt tool list to contain %q, got %q", want, text)
		}
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
