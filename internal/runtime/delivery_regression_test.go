package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkillFixture creates a real SKILL.md fixture file under the runtime's
// workspace so that discoverSkillsForAgent picks it up.
func writeSkillFixture(t *testing.T, home, agentID, skillName, body string) string {
	t.Helper()
	dir := filepath.Join(home, "workspace", "agents", agentID, "skills", skillName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRuntimeSkillMetadata(t, dir, "execution", "prompt", "subtask")
	return path
}

// readTraceEvents scans the trace file and returns the list of "type" fields
// from each event, so tests can assert the order of important events without
// depending on the file format.
func readTraceEvents(t *testing.T, home, sessionKey string) []map[string]any {
	t.Helper()
	dir := filepath.Join(home, "trace")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read trace dir: %v", err)
	}
	// sessionKey is the cli prefix; trace files are timestamped. We just
	// collect all events across all jsonl files written during the test.
	var events []map[string]any
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var ev map[string]any
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}
			events = append(events, ev)
		}
	}
	return events
}

func hasTraceEvent(events []map[string]any, eventType string) bool {
	for _, ev := range events {
		if ev["type"] == eventType {
			return true
		}
	}
	return false
}

// findToolExecutionEnd returns the first tool_execution_end event whose
// tool_call.name matches the given tool and whose args contain key==value.
// It returns nil if no such event exists.
func findToolExecutionEnd(events []map[string]any, toolName, argKey, argValue string) map[string]any {
	for _, ev := range events {
		if ev["type"] != "tool_execution_end" {
			continue
		}
		tc, ok := ev["tool_call"].(map[string]any)
		if !ok {
			continue
		}
		if name, _ := tc["Name"].(string); name != toolName {
			continue
		}
		if argKey == "" {
			return ev
		}
		args, ok := tc["Args"].(map[string]any)
		if !ok {
			continue
		}
		if v, _ := args[argKey].(string); strings.Contains(v, argValue) {
			return ev
		}
	}
	return nil
}

func countTraceEvent(events []map[string]any, eventType string) int {
	count := 0
	for _, ev := range events {
		if ev["type"] == eventType {
			count++
		}
	}
	return count
}
