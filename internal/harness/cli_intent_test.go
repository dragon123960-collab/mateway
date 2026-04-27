package harness

import (
	"strings"
	"testing"
)

func TestBuildCLIExplorationHintForCLIUsageGoal(t *testing.T) {
	hint := buildCLIExplorationHint("你学习一下这个 lark-cli 的使用可以不", []string{"exec", "sandbox_exec", "web_search"})
	if !strings.Contains(hint, "lark-cli") {
		t.Fatalf("expected command to be mentioned, got %q", hint)
	}
	if !strings.Contains(hint, "Prefer `exec`") {
		t.Fatalf("expected exec guidance, got %q", hint)
	}
	if !strings.Contains(hint, "Only use `sandbox_exec`") {
		t.Fatalf("expected sandbox_exec to be demoted to isolated verification, got %q", hint)
	}
	if !strings.Contains(hint, "Do not ask the user for docs or links before you try local inspection.") {
		t.Fatalf("expected no-docs-first guidance, got %q", hint)
	}
}

func TestMatchingCLIProviderTool(t *testing.T) {
	tool := matchingCLIProviderTool("lark-cli", []string{"lark_cli_list", "sandbox_exec"})
	if tool != "lark_cli_list" {
		t.Fatalf("expected lark_cli_list, got %q", tool)
	}
}
