package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/memory"
)

func TestBuiltinProviderWikiTools(t *testing.T) {
	root := t.TempDir()
	provider := BuiltinProvider{
		Workspace: root,
		Memory:    memory.Store{Workspace: root},
	}
	toolList, err := provider.Tools(context.Background(), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	index := make(map[string]Tool)
	for _, tool := range toolList {
		index[tool.Spec().Name] = tool
	}

	ingestRes, err := index["wiki_ingest"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{
		"title":    "AI Trends 2026",
		"category": "concepts",
		"summary":  "A compiled AI trends page.",
		"content":  "Links to [[concepts/agents]].",
	})})
	if err != nil {
		t.Fatal(err)
	}
	var ingestPath string
	if err := json.Unmarshal(ingestRes.Output, &ingestPath); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(ingestPath, filepath.Join("concepts", "ai-trends-2026.md")) {
		t.Fatalf("unexpected wiki path: %s", ingestPath)
	}

	_, err = index["wiki_ingest"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{
		"title":    "Agents",
		"category": "concepts",
		"content":  "Agent notes.",
	})})
	if err != nil {
		t.Fatal(err)
	}

	queryRes, err := index["wiki_query"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{
		"query": "trends",
	})})
	if err != nil {
		t.Fatal(err)
	}
	var queryOutput string
	if err := json.Unmarshal(queryRes.Output, &queryOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(queryOutput, "AI Trends 2026") {
		t.Fatalf("unexpected wiki query output: %s", queryOutput)
	}

	lintRes, err := index["wiki_lint"].Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{})})
	if err != nil {
		t.Fatal(err)
	}
	var lintOutput string
	if err := json.Unmarshal(lintRes.Output, &lintOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lintOutput, "missing_summary: 1") {
		t.Fatalf("unexpected wiki lint output: %s", lintOutput)
	}
}
