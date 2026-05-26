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
		ID:              "s1",
		Tool:            "software.install",
		Args:            map[string]string{"command": "brew install x"},
		ExpectedEvidence: []string{"install command"},
		SuccessCriteria: []string{"tool ran"},
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
