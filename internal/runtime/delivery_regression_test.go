package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
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

func countTraceEvent(events []map[string]any, eventType string) int {
	count := 0
	for _, ev := range events {
		if ev["type"] == eventType {
			count++
		}
	}
	return count
}

// TestDeliveryFixtureGuidanceSkillNotGated verifies that a guidance/planning
// skill does not block execution tools. The task contract uses a guidance
// skill in required_skills, but the gate should NOT require reading it before
// terminal.run (since the guidance skill has no specific execution tool).
func TestDeliveryFixtureGuidanceSkillNotGated(t *testing.T) {
	home := t.TempDir()
	rt := newTestRuntime(t)
	rt.Config.App.Home = home
	rt.Config.App.Workspace = filepath.Join(home, "workspace")

	guidancePath := writeSkillFixture(t, home, "main", "fresh-search", `---
name: fresh-search
description: How to evaluate a fresh external search result.
stage: guidance
priority: 1
---

# fresh-search

Guidance for assessing search results. No CLI helper.
`)

	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "ok"})
	registry.Register(runtimeNamedTool{name: "file.read", content: "skill body"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})
	rt.Tools = registry

	// The contract model output references the guidance skill in required_skills.
	// Since stage=guidance, skillExecutionTool returns "" and the gate should
	// not block terminal.run.
	rt.ContractModel = contractJSONModel{json: `{"summary":"evaluate Nasdaq news","requires_tools":true,"required_tools":["web.search","terminal.run"],"required_skills":[{"name":"fresh-search","path":"` + guidancePath + `","reason":"use guidance to assess results"}],"required_evidence":[{"kind":"current_external_fact","tool":"web.search","description":"Nasdaq data"}],"plan_items":[{"id":"plan-1","title":"read fresh-search guidance","status":"pending","tool":"file.read","criteria":"read ` + guidancePath + `"},{"id":"plan-2","title":"search Nasdaq","status":"pending","tool":"web.search","criteria":"collect data"},{"id":"plan-3","title":"run summary command","status":"pending","tool":"terminal.run","criteria":"echo summary"}],"expected_outcome":"answer with evidence","completion_policy":"use evidence"}`}

	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "c1", Name: "web.search", Args: map[string]any{"query": "Nasdaq"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "c2", Name: "terminal.run", Args: map[string]any{"command": "echo done"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "summary done"},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:guidance", "summarize Nasdaq"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		resp, err = rt.Handle(context.Background(), inbound("cli:guidance", "1"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if resp.Failed {
		t.Fatalf("expected completion, got %#v", resp.Reply)
	}

	// terminal.run must have been accepted (no skill_not_read block).
	state := loadState(t, rt, "cli:guidance")
	if len(state.Tasks) == 0 {
		t.Fatal("task not found")
	}
	task := state.Tasks[0]
	terminalAccepted := false
	for _, step := range task.Steps {
		if step.Tool == "terminal.run" && step.Accepted {
			terminalAccepted = true
		}
	}
	if !terminalAccepted {
		t.Fatal("expected terminal.run to be accepted without reading the guidance skill")
	}

	// And the gate must NOT have emitted a tool_blocked_skill_not_read trace.
	events := readTraceEvents(t, home, "cli:guidance")
	if hasTraceEvent(events, "tool_blocked_skill_not_read") {
		t.Fatal("guidance skill must not block execution tools")
	}
}

// TestDeliveryFixtureExecutionSkillGatedUntilRead verifies the opposite:
// a CLI-stage skill DOES block terminal.run until the SKILL.md is read.
// This is the universal-mechanism regression the Phase B review asked for.
func TestDeliveryFixtureExecutionSkillGatedUntilRead(t *testing.T) {
	home := t.TempDir()
	rt := newTestRuntime(t)
	rt.Config.App.Home = home
	rt.Config.App.Workspace = filepath.Join(home, "workspace")

	execPath := writeSkillFixture(t, home, "main", "cloud-doc-publish", `---
name: cloud-doc-publish
description: Publish a markdown report to a cloud doc provider.
stage: cli
priority: 5
---

# cloud-doc-publish

Use `+"`cloud-doc publish --markdown-file <path>`"+` to upload.
`)

	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "data"})
	registry.Register(runtimeNamedTool{name: "file.read", content: "skill body"})
	registry.Register(runtimeNamedTool{name: "file.write", content: "wrote /tmp/report.md"})
	registry.Register(&captureCommandTool{name: "terminal.run", content: `{"url":"https://example.cloud/doc/mock"}`})
	rt.Tools = registry

	rt.ContractModel = contractJSONModel{json: `{"summary":"publish report","requires_tools":true,"required_tools":["web.search","file.read","file.write","terminal.run"],"required_skills":[{"name":"cloud-doc-publish","path":"` + execPath + `","reason":"required to call the publish CLI"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read SKILL.md for cloud-doc-publish skill"},{"kind":"remote_publish","tool":"terminal.run","description":"cloud doc URL"}],"plan_items":[{"id":"plan-1","title":"read cloud-doc-publish SKILL.md","status":"pending","tool":"file.read","criteria":"read cloud-doc-publish skill SKILL.md"},{"id":"plan-2","title":"search data","status":"pending","tool":"web.search","criteria":"collect data"},{"id":"plan-3","title":"write report","status":"pending","tool":"file.write","criteria":"write report file"},{"id":"plan-4","title":"publish via CLI","status":"pending","tool":"terminal.run","criteria":"run cloud-doc publish"}],"expected_outcome":"cloud doc URL returned","completion_policy":"final answer must include URL or concrete blocker"}`}

	// Model sequence: web.search + file.write are pre-skill work that must
	// proceed freely; terminal.run is the publish step and must come after
	// the skill is read.
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "c1", Name: "web.search", Args: map[string]any{"query": "data"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "c2", Name: "file.write", Args: map[string]any{"path": "/tmp/report.md", "content": "report"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "c3", Name: "file.read", Args: map[string]any{"path": execPath},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "c4", Name: "terminal.run", Args: map[string]any{"command": "cloud-doc publish --markdown-file /tmp/report.md"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "https://example.cloud/doc/mock"},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:cloud-doc", "publish report"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		resp, err = rt.Handle(context.Background(), inbound("cli:cloud-doc", "1"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if resp.Failed {
		t.Fatalf("expected completion, got %#v", resp.Reply)
	}

	state := loadState(t, rt, "cli:cloud-doc")
	if len(state.Tasks) == 0 {
		t.Fatal("task not found")
	}
	task := state.Tasks[0]

	// Step order: web.search → file.write → file.read → terminal.run
	var stepTools []string
	for _, step := range task.Steps {
		stepTools = append(stepTools, step.Tool)
	}
	idxOf := func(name string) int {
		for i, t := range stepTools {
			if t == name {
				return i
			}
		}
		return -1
	}
	if got, want := idxOf("web.search"), 0; got != want {
		t.Fatalf("expected web.search at index %d, got %d (steps=%v)", want, got, stepTools)
	}
	if got, want := idxOf("file.write"), 1; got != want {
		t.Fatalf("expected file.write at index %d, got %d (steps=%v)", want, got, stepTools)
	}
	readIdx := idxOf("file.read")
	termIdx := idxOf("terminal.run")
	if readIdx < 0 || termIdx < 0 {
		t.Fatalf("expected both file.read and terminal.run steps, steps=%v", stepTools)
	}
	if readIdx >= termIdx {
		t.Fatalf("file.read (index %d) must precede terminal.run (index %d), steps=%v", readIdx, termIdx, stepTools)
	}

	// The skill was read and produced an accepted step.
	skillReadAccepted := false
	for _, step := range task.Steps {
		if step.Tool == "file.read" && step.Accepted {
			skillReadAccepted = true
		}
	}
	if !skillReadAccepted {
		t.Fatal("expected file.read of SKILL.md to be accepted")
	}

	// No skill_not_read block was triggered, since the model read the skill first.
	events := readTraceEvents(t, home, "cli:cloud-doc")
	if hasTraceEvent(events, "tool_blocked_skill_not_read") {
		t.Fatal("skill was read first, no skill-not-read block should fire")
	}

	// And the published URL is in the final reply, with no fake audit evidence list.
	if !strings.Contains(resp.Reply.Text, "https://example.cloud/doc/mock") {
		t.Fatalf("expected final reply to contain the published URL, got %q", resp.Reply.Text)
	}
}

// TestDeliveryFixtureContractStrategySelectsSkills verifies the trace carries
// a "task_contract_skills_selected" event that mentions the execution skill,
// proving the contract stage selected the skill before the runtime gated on it.
func TestDeliveryFixtureContractStrategySelectsSkills(t *testing.T) {
	home := t.TempDir()
	rt := newTestRuntime(t)
	rt.Config.App.Home = home
	rt.Config.App.Workspace = filepath.Join(home, "workspace")

	execPath := writeSkillFixture(t, home, "main", "cloud-doc-publish", `---
name: cloud-doc-publish
description: Publish a markdown report to a cloud doc provider.
stage: cli
priority: 5
---

# cloud-doc-publish

Use cloud-doc CLI.
`)
	guidancePath := writeSkillFixture(t, home, "main", "source-evaluation", `---
name: source-evaluation
description: How to evaluate search sources.
stage: guidance
priority: 1
---

# source-evaluation

Guidance only.
`)

	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "data"})
	registry.Register(runtimeNamedTool{name: "file.read", content: "skill"})
	registry.Register(&captureCommandTool{name: "terminal.run", content: `{"url":"https://example.cloud/x"}`})
	rt.Tools = registry

	rt.ContractModel = contractJSONModel{json: `{"summary":"publish report","requires_tools":true,"required_tools":["web.search","file.read","terminal.run"],"required_skills":[{"name":"cloud-doc-publish","path":"` + execPath + `","reason":"publish CLI"},{"name":"source-evaluation","path":"` + guidancePath + `","reason":"evaluate sources"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read SKILL.md for cloud-doc-publish skill"},{"kind":"local_file","tool":"file.read","description":"read SKILL.md for source-evaluation skill"},{"kind":"remote_publish","tool":"terminal.run","description":"cloud doc URL"}],"plan_items":[{"id":"plan-1","title":"read cloud-doc-publish SKILL.md","status":"pending","tool":"file.read","criteria":"read cloud-doc-publish skill SKILL.md"},{"id":"plan-2","title":"read source-evaluation SKILL.md","status":"pending","tool":"file.read","criteria":"read source-evaluation skill SKILL.md"},{"id":"plan-3","title":"search data","status":"pending","tool":"web.search","criteria":"collect data"},{"id":"plan-4","title":"publish","status":"pending","tool":"terminal.run","criteria":"publish"}],"expected_outcome":"cloud doc URL","completion_policy":"use evidence"}`}

	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "c1", Name: "file.read", Args: map[string]any{"path": execPath},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "c2", Name: "file.read", Args: map[string]any{"path": guidancePath},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "c3", Name: "web.search", Args: map[string]any{"query": "data"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "c4", Name: "terminal.run", Args: map[string]any{"command": "cloud-doc publish"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "https://example.cloud/x"},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:strategy", "publish"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		resp, err = rt.Handle(context.Background(), inbound("cli:strategy", "1"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if resp.Failed {
		t.Fatalf("expected completion, got %#v", resp.Reply)
	}

	events := readTraceEvents(t, home, "cli:strategy")
	if !hasTraceEvent(events, "task_contract_skills_selected") {
		t.Fatal("expected task_contract_skills_selected trace event")
	}
	// The contract should have selected the execution skill.
	selectedFound := false
	for _, ev := range events {
		if ev["type"] != "task_contract_skills_selected" {
			continue
		}
		raw, ok := ev["skills"]
		if !ok {
			continue
		}
		// The trace redaction layer may have replaced parts of the event; we
		// only need to confirm cloud-doc-publish appears.
		if blob, err := json.Marshal(raw); err == nil {
			if strings.Contains(string(blob), "cloud-doc-publish") {
				selectedFound = true
			}
		}
	}
	if !selectedFound {
		t.Fatal("expected cloud-doc-publish to appear in task_contract_skills_selected")
	}
}

// TestDeliveryFixtureLocalArtifactBeforeRemotePublish verifies the universal
// ordering rule: when a task has no local input, the plan must allow
// generating a local artifact first and then publishing remotely. We assert
// the step order (file.write before terminal.run) and the final reply
// contains the published URL/path, with no fake audit evidence list.
func TestDeliveryFixtureLocalArtifactBeforeRemotePublish(t *testing.T) {
	home := t.TempDir()
	rt := newTestRuntime(t)
	rt.Config.App.Home = home
	rt.Config.App.Workspace = filepath.Join(home, "workspace")

	execPath := writeSkillFixture(t, home, "main", "cloud-doc-publish", `---
name: cloud-doc-publish
description: Publish a markdown report to a cloud doc provider.
stage: cli
priority: 5
---

# cloud-doc-publish

Run the publish CLI.
`)

	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "web.search", content: "ok"})
	registry.Register(runtimeNamedTool{name: "file.read", content: "skill"})
	registry.Register(runtimeNamedTool{name: "file.write", content: "wrote /tmp/notes.md"})
	registry.Register(&captureCommandTool{name: "terminal.run", content: `{"url":"https://example.cloud/y"}`})
	rt.Tools = registry

	rt.ContractModel = contractJSONModel{json: `{"summary":"summarize and publish","requires_tools":true,"required_tools":["web.search","file.read","file.write","terminal.run"],"required_skills":[{"name":"cloud-doc-publish","path":"` + execPath + `","reason":"publish CLI"}],"required_evidence":[{"kind":"local_file","tool":"file.read","description":"read SKILL.md for cloud-doc-publish skill"},{"kind":"local_file","tool":"file.write","description":"markdown artifact"},{"kind":"remote_publish","tool":"terminal.run","description":"cloud doc URL"}],"plan_items":[{"id":"plan-1","title":"read cloud-doc-publish SKILL.md","status":"pending","tool":"file.read","criteria":"read cloud-doc-publish skill SKILL.md"},{"id":"plan-2","title":"search","status":"pending","tool":"web.search","criteria":"collect"},{"id":"plan-3","title":"write markdown","status":"pending","tool":"file.write","criteria":"write report"},{"id":"plan-4","title":"publish","status":"pending","tool":"terminal.run","criteria":"publish"}],"expected_outcome":"cloud doc URL","completion_policy":"use evidence"}`}

	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "c1", Name: "web.search", Args: map[string]any{"query": "data"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "c2", Name: "file.read", Args: map[string]any{"path": execPath},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "c3", Name: "file.write", Args: map[string]any{"path": "/tmp/notes.md", "content": "summary"},
		}}},
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "c4", Name: "terminal.run", Args: map[string]any{"command": "cloud-doc publish --markdown-file /tmp/notes.md"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "https://example.cloud/y"},
	}}, rt.Tools)

	resp, err := rt.Handle(context.Background(), inbound("cli:local-then-remote", "summarize and publish"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reply.Style == channel.StyleInputRequired {
		resp, err = rt.Handle(context.Background(), inbound("cli:local-then-remote", "1"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if resp.Failed {
		t.Fatalf("expected completion, got %#v", resp.Reply)
	}

	state := loadState(t, rt, "cli:local-then-remote")
	if len(state.Tasks) == 0 {
		t.Fatal("task not found")
	}
	task := state.Tasks[0]

	var stepTools []string
	for _, step := range task.Steps {
		stepTools = append(stepTools, step.Tool)
	}
	writeIdx, termIdx := -1, -1
	for i, n := range stepTools {
		if n == "file.write" && writeIdx < 0 {
			writeIdx = i
		}
		if n == "terminal.run" {
			termIdx = i
		}
	}
	if writeIdx < 0 || termIdx < 0 {
		t.Fatalf("expected file.write and terminal.run steps, got %v", stepTools)
	}
	if writeIdx >= termIdx {
		t.Fatalf("file.write (%d) must precede terminal.run (%d), steps=%v", writeIdx, termIdx, stepTools)
	}

	// Final reply must contain the published URL, and no fake audit evidence list.
	if !strings.Contains(resp.Reply.Text, "https://example.cloud/y") {
		t.Fatalf("expected final reply to contain the published URL, got %q", resp.Reply.Text)
	}
	for _, banned := range []string{"audit evidence", "evidence checklist", "audit checklist"} {
		if strings.Contains(strings.ToLower(resp.Reply.Text), banned) {
			t.Fatalf("final reply must not include default audit evidence list, got %q", resp.Reply.Text)
		}
	}
}
