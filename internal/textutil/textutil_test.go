package textutil

import (
	"strings"
	"testing"
)

func TestCleanBlockTrimsByRuneWithoutBreakingChinese(t *testing.T) {
	value := "完成后把结果沉淀到文件、记忆或 wiki。"
	got := CleanBlock(value, 15)
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("expected no replacement rune, got %q", got)
	}
	if !strings.HasPrefix(value, got) {
		t.Fatalf("expected prefix of original string, got %q", got)
	}
}

func TestHumanizeRunErrorSimplifiesNodeRunError(t *testing.T) {
	raw := "[NodeRunError] failed to invoke tool[name:sandbox_exec id:call_x]: sandbox_exec failed: exec: \"lark-cli\": executable file not found in $PATH\n------------------------\nnode path: [node_1, ToolNode]"
	got := HumanizeRunError(raw)
	if strings.Contains(got, "NodeRunError") || strings.Contains(got, "node path") {
		t.Fatalf("expected framework wrapper to be removed, got %q", got)
	}
	if !strings.Contains(got, "lark-cli") || !strings.Contains(got, "PATH") {
		t.Fatalf("expected actionable message, got %q", got)
	}
}
