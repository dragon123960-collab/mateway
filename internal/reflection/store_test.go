package reflection

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAppendBuildsIndexes(t *testing.T) {
	workspace := t.TempDir()
	if err := Append(workspace, Record{Type: "harness_run", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	if err := Append(workspace, Record{Type: "llm_turn", Status: "failed", Failure: "timeout"}); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(workspace, "memory", "reflections", "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var index Index
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	if index.TotalByType["harness_run"] != 1 || index.TotalByType["llm_turn"] != 1 {
		t.Fatalf("unexpected totals: %#v", index)
	}
	if index.FailureByKind["timeout"] != 1 {
		t.Fatalf("unexpected failures: %#v", index)
	}
	failuresPath := filepath.Join(workspace, "memory", "reflections", "failures.json")
	if _, err := os.Stat(failuresPath); err != nil {
		t.Fatalf("expected failures index: %v", err)
	}
}
