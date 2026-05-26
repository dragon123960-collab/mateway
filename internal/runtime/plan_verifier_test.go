package runtime

import (
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/tool"
)

func TestPlanVerifierRejectsMissingDependency(t *testing.T) {
	plan := model.Plan{Summary: "bad dependency", Steps: []model.PlanStep{{
		ID:        "s1",
		Tool:      "time.now",
		Args:      map[string]string{},
		DependsOn: []string{"missing"},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "check time", taskUnderstanding{})
	if !got.Blocking() || !containsVerificationError(got.Errors, "dependency missing") {
		t.Fatalf("expected dependency error, got %#v", got)
	}
}

func TestPlanVerifierRejectsMissingRequiredArg(t *testing.T) {
	plan := model.Plan{Summary: "bad args", Steps: []model.PlanStep{{
		ID:   "s1",
		Tool: "file.read",
		Args: map[string]string{},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "read file", taskUnderstanding{})
	if !got.Blocking() || !containsVerificationError(got.Errors, "missing required arg path") {
		t.Fatalf("expected missing arg error, got %#v", got)
	}
}

func TestPlanVerifierWarnsWhenPlanDoesNotCoverCapability(t *testing.T) {
	plan := model.Plan{Summary: "bad capability coverage", Steps: []model.PlanStep{{
		ID:   "s1",
		Tool: "time.now",
		Args: map[string]string{},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "安装 lark cli", taskUnderstanding{
		Capabilities: []string{"software.search", "software.install"},
	})
	if got.Blocking() {
		t.Fatalf("expected warning only, got blocking %#v", got)
	}
	if !containsVerificationError(got.RepairableWarnings, "do not clearly cover tool_need software.search") {
		t.Fatalf("expected capability warning, got %#v", got)
	}
	if !got.ShouldRepair() {
		t.Fatalf("expected repairable warning to request repair")
	}
}

func TestPlanVerifierWarnsWhenTerminalRunLooksLikeProjectOverview(t *testing.T) {
	plan := model.Plan{Summary: "overview", Steps: []model.PlanStep{{
		ID:   "s1",
		Tool: "terminal.run",
		Goal: "show repository map",
		Args: map[string]string{"command": "find . -maxdepth 2 -type f"},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "看一下项目结构", taskUnderstanding{})
	if !containsVerificationError(got.RepairableWarnings, "prefer project.index") {
		t.Fatalf("expected project.index boundary warning, got %#v", got)
	}
}

func TestPlanVerifierWarnsWhenTerminalRunLooksLikeSingleFileRead(t *testing.T) {
	plan := model.Plan{Summary: "read", Steps: []model.PlanStep{{
		ID:   "s1",
		Tool: "terminal.run",
		Goal: "read README.md",
		Args: map[string]string{"command": "cat README.md"},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "读取 README", taskUnderstanding{})
	if !containsVerificationError(got.RepairableWarnings, "prefer file.read or file.summary") {
		t.Fatalf("expected file.read boundary warning, got %#v", got)
	}
}

func TestPlanVerifierWarnsWhenFileReadLooksLikeSummaryOnlyWork(t *testing.T) {
	plan := model.Plan{Summary: "summary", Steps: []model.PlanStep{{
		ID:   "s1",
		Tool: "file.read",
		Goal: "summarize README quickly",
		Args: map[string]string{"path": "README.md"},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "快速总结 README", taskUnderstanding{})
	if !containsVerificationError(got.RepairableWarnings, "prefer file.summary") {
		t.Fatalf("expected file.summary boundary warning, got %#v", got)
	}
}

func TestPlanVerifierWarnsWhenWebSearchLooksLikeKnownURLRead(t *testing.T) {
	plan := model.Plan{Summary: "known url", Steps: []model.PlanStep{{
		ID:   "s1",
		Tool: "web.search",
		Goal: "read this page",
		Args: map[string]string{"query": "https://example.com/docs"},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "读取这个链接", taskUnderstanding{})
	if !containsVerificationError(got.RepairableWarnings, "prefer web.fetch") {
		t.Fatalf("expected web.fetch boundary warning, got %#v", got)
	}
}

func TestPlanVerifierWarnsWhenWebSearchLooksLikeSoftwareDiscovery(t *testing.T) {
	plan := model.Plan{Summary: "software", Steps: []model.PlanStep{{
		ID:   "s1",
		Tool: "web.search",
		Goal: "find lark cli install command",
		Args: map[string]string{"query": "lark cli install github"},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "看看怎么安装 lark cli", taskUnderstanding{})
	if !containsVerificationError(got.RepairableWarnings, "prefer software.search") {
		t.Fatalf("expected software.search boundary warning, got %#v", got)
	}
}

func TestPlanVerifierWarnsWhenWebFetchLooksLikeSourceDiscovery(t *testing.T) {
	plan := model.Plan{Summary: "discover", Steps: []model.PlanStep{{
		ID:   "s1",
		Tool: "web.fetch",
		Goal: "find the official README first",
		Args: map[string]string{},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "先找官方 README", taskUnderstanding{})
	if !containsVerificationError(got.RepairableWarnings, "prefer web.search or software.search first") {
		t.Fatalf("expected source discovery boundary warning, got %#v", got)
	}
}

func TestPlanVerifierWarnsWhenSoftwareSearchLooksLikeKnownURLRead(t *testing.T) {
	plan := model.Plan{Summary: "read upstream", Steps: []model.PlanStep{{
		ID:   "s1",
		Tool: "software.search",
		Goal: "read README content",
		Args: map[string]string{"query": "https://github.com/larksuite/cli/blob/main/README.md"},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "读取这个 README", taskUnderstanding{})
	if !containsVerificationError(got.RepairableWarnings, "prefer web.fetch") {
		t.Fatalf("expected web.fetch boundary warning for software.search, got %#v", got)
	}
}

func TestPlanVerifierWarnsWhenSoftwareInstallLooksSpeculative(t *testing.T) {
	plan := model.Plan{Summary: "install", Steps: []model.PlanStep{{
		ID:   "s1",
		Tool: "software.install",
		Goal: "install the cli",
		Args: map[string]string{"command": "brew install lark-cli"},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "安装 lark cli", taskUnderstanding{})
	if !containsVerificationError(got.RepairableWarnings, "include explicit upstream install command and verify_command before installing") {
		t.Fatalf("expected speculative install warning, got %#v", got)
	}
}

func TestPlanVerifierRejectsPlaceholderArgs(t *testing.T) {
	plan := model.Plan{Summary: "install", Steps: []model.PlanStep{{
		ID:   "s1",
		Tool: "web.fetch",
		Args: map[string]string{"url": "<需从 step-1 获取具体 URL>"},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "安装 example cli", taskUnderstanding{})
	if !got.Blocking() || !containsVerificationError(got.Errors, "args contain unresolved placeholder values") {
		t.Fatalf("expected placeholder arg error, got %#v", got)
	}
}

func TestPlanVerifierDoesNotTreatTodoInRealFilePathAsPlaceholder(t *testing.T) {
	plan := model.Plan{Summary: "read todo", Steps: []model.PlanStep{{
		ID:   "s1",
		Tool: "file.read",
		Args: map[string]string{"path": "docs/开发TODO.md"},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "读取 TODO", taskUnderstanding{})
	if got.Blocking() {
		t.Fatalf("expected normal TODO file path not to be blocked, got %#v", got)
	}
}

func TestPlanVerifierAllowsScheduleCreateAfterStopStyleVerification(t *testing.T) {
	plan := model.Plan{Summary: "schedule", Steps: []model.PlanStep{
		{
			ID:        "s1",
			Tool:      "terminal.run",
			Goal:      "验证任务能否稳定执行",
			Args:      map[string]string{"command": "mateway run \"AI趋势收集\""},
			OnFailure: "stop",
		},
		{
			ID:        "s2",
			Tool:      "schedule.create",
			Args:      map[string]string{"title": "每日AI趋势收集", "prompt": "执行AI趋势收集任务", "daily_at": "09:00"},
			DependsOn: []string{"s1"},
		},
	}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "创建定时任务", taskUnderstanding{})
	if got.Blocking() {
		t.Fatalf("expected safe verification boundary to pass, got %#v", got)
	}
}

func TestPlanVerifierRejectsScheduleCreateAfterRepairStyleVerification(t *testing.T) {
	plan := model.Plan{Summary: "schedule", Steps: []model.PlanStep{
		{
			ID:        "s1",
			Tool:      "terminal.run",
			Goal:      "验证任务能否稳定执行",
			Args:      map[string]string{"command": "mateway run \"AI趋势收集\""},
			OnFailure: "repair",
		},
		{
			ID:        "s2",
			Tool:      "schedule.create",
			Args:      map[string]string{"title": "每日AI趋势收集", "prompt": "执行AI趋势收集任务", "daily_at": "09:00"},
			DependsOn: []string{"s1"},
		},
	}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "创建定时任务", taskUnderstanding{})
	if !got.Blocking() || !containsVerificationError(got.Errors, "must depend on a verification step") {
		t.Fatalf("expected schedule verification boundary error, got %#v", got)
	}
}

func TestPreserveSuccessfulEvidenceGuidanceIncludesReadSteps(t *testing.T) {
	text := preserveSuccessfulEvidenceGuidance([]model.ToolResult{
		{StepID: "s1", Tool: "project.index", OK: true},
		{StepID: "s2", Tool: "file.summary", OK: true},
		{StepID: "s3", Tool: "terminal.run", OK: true},
	})
	for _, want := range []string{"s1 via project.index", "s2 via file.summary"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected preserve guidance to contain %q, got %q", want, text)
		}
	}
	if strings.Contains(text, "s3 via terminal.run") {
		t.Fatalf("expected preserve guidance to skip terminal.run, got %q", text)
	}
}

func TestPlanVerifierWarnsWhenEvidenceDoesNotAlignWithUnderstandingHints(t *testing.T) {
	plan := model.Plan{Summary: "bad evidence alignment", Steps: []model.PlanStep{{
		ID:               "s1",
		Tool:             "software.install",
		Args:             map[string]string{"command": "brew install x"},
		ExpectedEvidence: []string{"generic confirmation"},
		SuccessCriteria:  []string{"tool ran"},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "安装软件", taskUnderstanding{
		EvidenceHints: []string{"install command and verify command output"},
	})
	if got.Blocking() {
		t.Fatalf("expected warning only, got blocking %#v", got)
	}
	if !containsVerificationError(got.RepairableWarnings, "does not clearly align with understanding evidence hints") {
		t.Fatalf("expected evidence hint warning, got %#v", got)
	}
	if !got.ShouldRepair() {
		t.Fatalf("expected repairable warning to request repair")
	}
}

func TestPlanVerifierWarnsWhenSuccessCriteriaDoNotAlignWithUnderstandingCompletion(t *testing.T) {
	plan := model.Plan{Summary: "bad completion alignment", Steps: []model.PlanStep{{
		ID:               "s1",
		Tool:             "software.install",
		Args:             map[string]string{"command": "brew install x"},
		ExpectedEvidence: []string{"install command"},
		SuccessCriteria:  []string{"tool ran"},
	}}}
	got := verifyPlanContract(plan, tool.NewBuiltinRegistry(), "安装软件", taskUnderstanding{
		CompletionDraft: []string{"verify the install result"},
	})
	if !containsVerificationError(got.RepairableWarnings, "success_criteria do not clearly align with understanding completion criteria") {
		t.Fatalf("expected completion alignment warning, got %#v", got)
	}
}

func TestPlanVerificationRepairGuidanceIncludesWarningsAndErrors(t *testing.T) {
	verification := PlanVerification{
		Warnings:           []string{"step-1: success_criteria is empty"},
		RepairableWarnings: []string{"plan tools do not clearly cover tool_need software.search"},
		Errors:             []string{"step-1: missing required arg command"},
	}
	guidance := verification.RepairGuidance()
	for _, want := range []string{
		"error: step-1: missing required arg command",
		"repairable_warning: plan tools do not clearly cover tool_need software.search",
		"warning: step-1: success_criteria is empty",
	} {
		if !strings.Contains(guidance, want) {
			t.Fatalf("expected guidance to contain %q, got %q", want, guidance)
		}
	}
}

func TestPlanVerificationAdvisoryWarningDoesNotRequireRepair(t *testing.T) {
	verification := PlanVerification{Warnings: []string{"step-1: success_criteria is empty"}}
	if verification.Blocking() {
		t.Fatalf("expected advisory warning not to block")
	}
}

func TestStepVerifierRejectsMissingExpectedEvidence(t *testing.T) {
	step := model.PlanStep{ID: "s1", Tool: "web.search", ExpectedEvidence: []string{"search result URL"}}
	result := model.ToolResult{StepID: "s1", Tool: "web.search", OK: true, Output: "ok"}
	got := verifyStepResult(step, result)
	if !got.Blocking() || !containsVerificationError(got.Errors, "expected evidence") {
		t.Fatalf("expected evidence error, got %#v", got)
	}
}

func TestStepVerifierAcceptsWebFetchDocumentEvidence(t *testing.T) {
	step := model.PlanStep{ID: "s1", Tool: "web.fetch", ExpectedEvidence: []string{"README 页面包含安装命令"}}
	result := model.ToolResult{
		StepID: "s1",
		Tool:   "web.fetch",
		OK:     true,
		Output: "Fetched URL: https://raw.githubusercontent.com/larksuite/cli/main/README.md",
		Evidence: map[string]any{
			"kind":   "web_fetch",
			"url":    "https://raw.githubusercontent.com/larksuite/cli/main/README.md",
			"title":  "README",
			"status": 200,
			"bytes":  16468,
		},
	}
	got := verifyStepResult(step, result)
	if got.Blocking() {
		t.Fatalf("expected web.fetch README evidence to pass, got %#v", got)
	}
}

func containsVerificationError(items []string, fragment string) bool {
	for _, item := range items {
		if strings.Contains(item, fragment) {
			return true
		}
	}
	return false
}
