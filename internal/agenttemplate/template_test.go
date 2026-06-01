package agenttemplate

import "testing"

func TestCoreFilesIncludeSoulAndUseEnglishBaseline(t *testing.T) {
	files := CoreFiles(Profile{ID: "ops", Name: "Ops Agent"})
	for _, name := range []string{"agent.md", "soul.md", "user.md", "tools.md", "memory.md"} {
		if files[name] == "" {
			t.Fatalf("missing %s in core files", name)
		}
		if containsHan(files[name]) {
			t.Fatalf("expected English baseline template for %s, got:\n%s", name, files[name])
		}
	}
}

func containsHan(text string) bool {
	for _, r := range text {
		if r >= '\u4e00' && r <= '\u9fff' {
			return true
		}
	}
	return false
}
