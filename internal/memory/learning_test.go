package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessTaskGeneratesSkillCandidateAtThreshold(t *testing.T) {
	store := NewStore(t.TempDir())
	cfg := LearningConfig{Enabled: true, SuccessThreshold: 3, RequireUserConfirm: true}
	outcome := TaskOutcome{
		AgentID:     "main",
		TraceID:     "trace-1",
		TaskID:      "task-1",
		Intent:      "review latest release notes",
		PlanSummary: "review release notes",
		Tools:       []string{"web.search", "file.summary"},
		Success:     true,
		FinishedAt:  time.Now(),
	}

	for i := 0; i < 2; i++ {
		result, err := store.ProcessTask(outcome, cfg)
		if err != nil {
			t.Fatalf("process task: %v", err)
		}
		if result.CandidateGenerated {
			t.Fatalf("candidate generated too early at iteration %d", i+1)
		}
	}
	result, err := store.ProcessTask(outcome, cfg)
	if err != nil {
		t.Fatalf("process task: %v", err)
	}
	if !result.CandidateGenerated {
		t.Fatal("expected candidate at threshold")
	}
	if _, err := os.Stat(result.CandidatePath); err != nil {
		t.Fatalf("expected candidate file: %v", err)
	}
}

func TestWriteSkillCandidateDedupesNearDuplicateCandidates(t *testing.T) {
	store := NewStore(t.TempDir())
	root := store.agentLearningRoot("main")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := LearningConfig{Enabled: true, SuccessThreshold: 1, RequireUserConfirm: true}
	first, err := store.writeSkillCandidate("main", PatternRecord{
		TaskID:      "task-1",
		TraceID:     "trace-1",
		PlanSummary: "review release notes",
		ReplyPreview:"done",
	}, Counter{
		PatternKey:   "pattern-a",
		IntentFamily: "review-latest-release-notes",
		Tools:        []string{"file.summary", "web.search"},
		SuccessCount: 3,
		LastTaskID:   "task-1",
		LastTraceID:  "trace-1",
	}, cfg)
	if err != nil {
		t.Fatalf("first candidate: %v", err)
	}
	second, err := store.writeSkillCandidate("main", PatternRecord{
		TaskID:      "task-2",
		TraceID:     "trace-2",
		PlanSummary: "review release-notes",
		ReplyPreview:"done again",
	}, Counter{
		PatternKey:   "pattern-b",
		IntentFamily: "review-latest-release-notes",
		Tools:        []string{"file.summary", "web.search"},
		SuccessCount: 4,
		LastTaskID:   "task-2",
		LastTraceID:  "trace-2",
	}, cfg)
	if err != nil {
		t.Fatalf("second candidate: %v", err)
	}
	if first != second {
		t.Fatalf("expected near-duplicate skill candidates to reuse existing path, first=%q second=%q", first, second)
	}
	matches, err := filepath.Glob(filepath.Join(store.Root, "agents", "main", "inbox", "skill-candidate-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one deduped skill candidate, got %v", matches)
	}
	data, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"success_count: 4",
		"- Last task: task-2",
		"- Last trace: trace-2",
		"- Last plan summary: review release-notes",
		"- Last reply preview: done again",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected upgraded skill candidate to contain %q, got:\n%s", want, text)
		}
	}
}

func TestProcessTaskSkipsTestLikeOutcomes(t *testing.T) {
	store := NewStore(t.TempDir())
	cfg := LearningConfig{Enabled: true, SuccessThreshold: 1, RequireUserConfirm: true}
	outcome := TaskOutcome{
		AgentID:     "main",
		SessionKey:  "test:memory-learning",
		TraceID:     "cli-test-learning",
		TaskID:      "task-1",
		Intent:      "review latest release notes",
		PlanSummary: "review release notes",
		Tools:       []string{"web.search", "file.summary"},
		Success:     true,
		FinishedAt:  time.Now(),
	}

	result, err := store.ProcessTask(outcome, cfg)
	if err != nil {
		t.Fatalf("process task: %v", err)
	}
	if result.CandidateGenerated || result.CandidatePath != "" || result.PatternKey != "" {
		t.Fatalf("expected test-like outcome to be skipped, got %#v", result)
	}
	matches, err := filepath.Glob(filepath.Join(store.Root, "agents", "main", "inbox", "skill-candidate-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no skill candidate for test-like outcome, got %v", matches)
	}
}

func TestLintReportsBrokenLinksAndMissingFrontmatter(t *testing.T) {
	root := t.TempDir()
	longDir := filepath.Join(root, "agents", "main", "long")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(longDir, "project.md")
	if err := os.WriteFile(path, []byte("# Project\n\nSee [[missing-page]].\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Lint(root)
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	codes := map[string]bool{}
	for _, issue := range report.Issues {
		codes[issue.Code] = true
	}
	if !codes["missing_frontmatter"] {
		t.Fatalf("expected missing_frontmatter, got %#v", report.Issues)
	}
	if !codes["broken_wikilink"] {
		t.Fatalf("expected broken_wikilink, got %#v", report.Issues)
	}
}

func TestProposeAndCommitMemory(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	proposal, err := store.Propose(ProposalInput{
		AgentID:    "main",
		Scope:      "agent",
		Type:       "decision",
		Title:      "Memory Direction",
		Body:       "Mateway uses Markdown as the source of truth for memory.",
		Sources:    []string{"docs/记忆系统设计.md"},
		Tags:       []string{"mateway", "memory"},
		Confidence: "high",
		CreatedAt:  now,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	data, err := os.ReadFile(proposal.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "status: proposed") || !strings.Contains(text, "sources:") {
		t.Fatalf("unexpected proposal text:\n%s", text)
	}

	committed, err := store.Commit(CommitInput{AgentID: "main", Proposal: proposal.ID, At: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if committed.Type != "decision" {
		t.Fatalf("expected committed type decision, got %#v", committed)
	}
	if !strings.Contains(filepath.Base(committed.TargetPath), "decision-memory-direction") {
		t.Fatalf("expected typed long memory filename, got %q", committed.TargetPath)
	}
	longData, err := os.ReadFile(committed.TargetPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(longData), "status: active") {
		t.Fatalf("expected active long memory, got:\n%s", string(longData))
	}
	indexData, err := os.ReadFile(filepath.Join(store.Root, "agents", "main", "index.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(indexData), "Memory Direction") {
		t.Fatalf("expected index update, got:\n%s", string(indexData))
	}
	logData, err := os.ReadFile(filepath.Join(store.Root, "log.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "propose memory") || !strings.Contains(string(logData), "commit memory") {
		t.Fatalf("expected log entries, got:\n%s", string(logData))
	}
}

func TestListShowAndRejectMemoryProposal(t *testing.T) {
	store := NewStore(t.TempDir())
	proposal, err := store.Propose(ProposalInput{
		AgentID: "main",
		Title:   "Project Fact",
		Body:    "Mateway keeps memory as Markdown files.",
		Sources: []string{"manual"},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	items, err := store.List(ListOptions{AgentID: "main", Area: "inbox", Status: "proposed"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 || items[0].ID != proposal.ID || items[0].Status != "proposed" {
		t.Fatalf("unexpected list items: %#v", items)
	}
	shown, err := store.Show("main", proposal.ID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(shown.Text, "Project Fact") {
		t.Fatalf("expected proposal text, got:\n%s", shown.Text)
	}
	rejected, err := store.Reject(RejectInput{AgentID: "main", Proposal: proposal.ID, Reason: "Not stable enough."})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	data, err := os.ReadFile(rejected.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "status: rejected") || !strings.Contains(string(data), "Not stable enough.") {
		t.Fatalf("expected rejected proposal, got:\n%s", string(data))
	}
	items, err = store.List(ListOptions{AgentID: "main", Area: "inbox", Status: "proposed"})
	if err != nil {
		t.Fatalf("list after reject: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no proposed items after reject, got %#v", items)
	}
}

func TestSearchLongReturnsRelevantActiveMemory(t *testing.T) {
	store := NewStore(t.TempDir())
	proposal, err := store.Propose(ProposalInput{
		AgentID: "main",
		Title:   "Mateway Memory Direction",
		Body:    "Mateway stores long memory as Markdown and keeps proposals in an inbox before commit.",
		Sources: []string{"manual"},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := store.Commit(CommitInput{AgentID: "main", Proposal: proposal.ID}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	results, err := store.SearchLong(SearchOptions{AgentID: "main", Query: "How does Mateway store memory?", Limit: 3})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %#v", results)
	}
	if !strings.Contains(results[0].Snippet, "Markdown") {
		t.Fatalf("expected memory snippet, got %#v", results[0])
	}
	if results[0].Type != "note" {
		t.Fatalf("expected default committed proposal type note, got %#v", results[0])
	}
	if results[0].StartLine <= 0 || results[0].EndLine < results[0].StartLine {
		t.Fatalf("expected snippet line evidence, got %#v", results[0])
	}
}

func TestSearchLongUsesIndexCandidatesWhenAvailable(t *testing.T) {
	store := NewStore(t.TempDir())
	keep, err := store.Propose(ProposalInput{
		AgentID: "main",
		Title:   "Keep Memory",
		Body:    "Needle knowledge should be found from the indexed candidate.",
		Sources: []string{"manual"},
	})
	if err != nil {
		t.Fatal(err)
	}
	skip, err := store.Propose(ProposalInput{
		AgentID: "main",
		Title:   "Skip Memory",
		Body:    "Needle knowledge is present here but this entry will be removed from the index.",
		Sources: []string{"manual"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(CommitInput{AgentID: "main", Proposal: keep.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(CommitInput{AgentID: "main", Proposal: skip.ID}); err != nil {
		t.Fatal(err)
	}
	result, err := store.RebuildIndex(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var index Index
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	var entries []IndexEntry
	for _, entry := range index.Entries {
		if entry.Area == "long" && strings.Contains(entry.Title, "Keep Memory") {
			entries = append(entries, entry)
		}
	}
	index.Entries = entries
	data, err = json.MarshalIndent(index, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.Path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	results, err := store.SearchLong(SearchOptions{AgentID: "main", Query: "Needle knowledge", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !strings.Contains(results[0].Title, "Keep Memory") {
		t.Fatalf("expected search to use index candidates, got %#v", results)
	}
}

func TestWriteSkillImprovementProposal(t *testing.T) {
	store := NewStore(t.TempDir())
	path, err := store.WriteSkillImprovementProposal(SkillImprovementInput{
		AgentID:             "main",
		SkillName:           "doc-review",
		ImprovementType:     "weak_verification",
		Reason:              "The current flow misses a quick verification step after reading the file.",
		ProposedChange:      "Add a final check that confirms headings and preview lines are present before summarizing.",
		RepairReason:        "preview evidence was incomplete",
		PreviousPlanSummary: "read doc and summarize",
		RepairedPlanSummary: "read doc, verify evidence, then summarize",
		TaskID:              "task-1",
		TraceID:             "trace-1",
		Sources:             []string{"task:task-1", "trace:trace-1"},
	})
	if err != nil {
		t.Fatalf("write improvement proposal: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"type: skill_improvement",
		"improvement_type: weak_verification",
		"# Proposed Skill Improvement: doc-review",
		"The current flow misses a quick verification step",
		"Add a final check that confirms headings",
		"- weak_verification",
		"Repair reason: preview evidence was incomplete",
		"Previous plan summary: read doc and summarize",
		"Repaired plan summary: read doc, verify evidence, then summarize",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected proposal to contain %q, got:\n%s", want, text)
		}
	}
}

func TestWriteSkillImprovementProposalDedupesSameRepairContext(t *testing.T) {
	store := NewStore(t.TempDir())
	input := SkillImprovementInput{
		AgentID:             "main",
		SkillName:           "doc-review",
		ImprovementType:     "weak_verification",
		Reason:              "The current flow misses a quick verification step after reading the file.",
		ProposedChange:      "Add a final check that confirms headings and preview lines are present before summarizing.",
		RepairReason:        "preview evidence was incomplete",
		PreviousPlanSummary: "read doc and summarize",
		RepairedPlanSummary: "read doc, verify evidence, then summarize",
		TaskID:              "task-1",
		TraceID:             "trace-1",
		Sources:             []string{"task:task-1", "trace:trace-1"},
	}
	first, err := store.WriteSkillImprovementProposal(input)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	second, err := store.WriteSkillImprovementProposal(input)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if first != second {
		t.Fatalf("expected duplicate skill improvement proposal to reuse existing path, first=%q second=%q", first, second)
	}
	matches, err := filepath.Glob(filepath.Join(store.Root, "agents", "main", "inbox", "skill-improvement-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one deduped skill improvement proposal, got %v", matches)
	}
}

func TestWriteSkillImprovementProposalDedupesNearDuplicateRepairContext(t *testing.T) {
	store := NewStore(t.TempDir())
	first, err := store.WriteSkillImprovementProposal(SkillImprovementInput{
		AgentID:             "main",
		SkillName:           "doc-review",
		ImprovementType:     "weak_verification",
		Reason:              "The current flow misses a quick verification step after reading the file.",
		ProposedChange:      "Add a final check that confirms headings and preview lines are present before summarizing.",
		RepairReason:        "preview evidence was incomplete",
		PreviousPlanSummary: "read doc and summarize",
		RepairedPlanSummary: "read doc, verify evidence, then summarize",
		TaskID:              "task-1",
		TraceID:             "trace-1",
		Sources:             []string{"task:task-1", "trace:trace-1"},
	})
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	second, err := store.WriteSkillImprovementProposal(SkillImprovementInput{
		AgentID:             "main",
		SkillName:           "doc-review",
		ImprovementType:     "weak_verification",
		Reason:              "The current flow still misses a verification step.",
		ProposedChange:      "Add a final check for headings and preview lines.",
		RepairReason:        "preview evidence incomplete",
		PreviousPlanSummary: "read doc, summarize",
		RepairedPlanSummary: "read doc verify evidence then summarize",
		TaskID:              "task-2",
		TraceID:             "trace-2",
		Sources:             []string{"task:task-2", "trace:trace-2"},
	})
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if first != second {
		t.Fatalf("expected near-duplicate skill improvement proposal to reuse existing path, first=%q second=%q", first, second)
	}
	matches, err := filepath.Glob(filepath.Join(store.Root, "agents", "main", "inbox", "skill-improvement-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one merged skill improvement proposal, got %v", matches)
	}
}

func TestPromoteSkillCandidate(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(workspace)
	cfg := LearningConfig{Enabled: true, SuccessThreshold: 1, RequireUserConfirm: true}
	path, err := store.writeSkillCandidate("main", PatternRecord{
		TaskID:       "task-1",
		TraceID:      "trace-1",
		PlanSummary:  "review release notes",
		ReplyPreview: "done",
	}, Counter{
		PatternKey:   "pattern-a",
		IntentFamily: "review-latest-release-notes",
		Tools:        []string{"file.summary", "web.search"},
		SuccessCount: 3,
		LastTaskID:   "task-1",
		LastTraceID:  "trace-1",
	}, cfg)
	if err != nil {
		t.Fatalf("write skill candidate: %v", err)
	}
	result, err := store.PromoteSkillCandidate(SkillPromotionInput{
		AgentID:   "main",
		Proposal:  path,
		SkillName: "release-review",
		At:        time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("promote skill candidate: %v", err)
	}
	if !strings.HasSuffix(result.TargetPath, filepath.Join("skills", "release-review", "SKILL.md")) {
		t.Fatalf("unexpected promoted skill path %q", result.TargetPath)
	}
	data, err := os.ReadFile(result.TargetPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"name: release-review",
		"stage: planning",
		"# release-review",
		"## Workflow",
		"## Source Candidate Notes",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected promoted skill file to contain %q, got:\n%s", want, text)
		}
	}
	if strings.Contains(text, "# Proposed Skill:") {
		t.Fatalf("expected promoted skill file to stop using proposal heading, got:\n%s", text)
	}
	if !strings.Contains(text, "Review the source tasks") {
		t.Fatalf("expected promoted skill file to contain skill heading, got:\n%s", string(data))
	}
	sourceData, err := os.ReadFile(result.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceText := string(sourceData)
	if !strings.Contains(sourceText, "status: committed") || !strings.Contains(sourceText, "Promoted to: [[skills/release-review/SKILL]]") {
		t.Fatalf("expected source proposal updated as committed, got:\n%s", sourceText)
	}
}

func TestLintReportsWeakEvidenceForSpecificClaims(t *testing.T) {
	root := t.TempDir()
	longDir := filepath.Join(root, "agents", "main", "long")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(longDir, "metrics.md")
	text := `---
type: note
status: active
sources:
  - manual
---

# Metrics

Conversion improved by 42 percent.
`
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Lint(root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, issue := range report.Issues {
		if issue.Code == "weak_evidence" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected weak_evidence issue, got %#v", report.Issues)
	}
}

func TestLintAcceptsLineEvidenceForSpecificClaims(t *testing.T) {
	root := t.TempDir()
	longDir := filepath.Join(root, "agents", "main", "long")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(longDir, "metrics.md")
	text := `---
type: note
scope: agent
status: active
sources:
  - file:docs/source.md:12
confidence: medium
---

# Metrics

Conversion improved by 42 percent.
`
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Lint(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range report.Issues {
		if issue.Code == "weak_evidence" {
			t.Fatalf("did not expect weak_evidence, got %#v", report.Issues)
		}
	}
}

func TestLintReportsStaleLongMemory(t *testing.T) {
	root := t.TempDir()
	longDir := filepath.Join(root, "agents", "main", "long")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	text := `---
type: decision
scope: agent
status: active
sources:
  - manual
confidence: medium
created_at: 2026-03-01
updated_at: 2026-03-01
---

# Stale Memory

This long memory should be reviewed again.
`
	if err := os.WriteFile(filepath.Join(longDir, "stale.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Lint(root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, issue := range report.Issues {
		if issue.Code == "stale_long_memory" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected stale_long_memory issue, got %#v", report.Issues)
	}
}

func TestLintValidatesFrontmatterSchema(t *testing.T) {
	root := t.TempDir()
	longDir := filepath.Join(root, "agents", "main", "long")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(longDir, "bad-schema.md")
	text := `---
type: note
scope: team
status: active
sources:
  - file:docs/source.md:12
confidence: sure
---

# Bad Schema

This memory has invalid schema fields.
`
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Lint(root)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, issue := range report.Issues {
		codes[issue.Code] = true
	}
	if !codes["invalid_scope"] || !codes["invalid_confidence"] {
		t.Fatalf("expected schema issues, got %#v", report.Issues)
	}
}

func TestRebuildIndexWritesJSONFromMarkdown(t *testing.T) {
	store := NewStore(t.TempDir())
	proposal, err := store.Propose(ProposalInput{
		AgentID:    "main",
		Title:      "Indexed Memory",
		Body:       "Mateway memory index can be rebuilt from Markdown.",
		Sources:    []string{"file:docs/source.md:12"},
		Tags:       []string{"memory", "index"},
		Confidence: "high",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := store.Commit(CommitInput{AgentID: "main", Proposal: proposal.ID}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	result, err := store.RebuildIndex(time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("rebuild index: %v", err)
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Fatalf("expected index file: %v", err)
	}
	if len(result.Index.Entries) != 2 {
		t.Fatalf("expected proposal and long memory entries, got %#v", result.Index.Entries)
	}
	foundLong := false
	for _, entry := range result.Index.Entries {
		if entry.Area == "long" && entry.Status == "active" && entry.Title == "Memory: Indexed Memory" {
			foundLong = true
			if len(entry.Tags) == 0 || entry.Tags[0] != "memory" {
				t.Fatalf("expected tags in index entry, got %#v", entry)
			}
		}
	}
	if !foundLong {
		t.Fatalf("expected active long memory entry, got %#v", result.Index.Entries)
	}
	foundParsedSource := false
	for _, entry := range result.Index.Entries {
		for _, source := range entry.ParsedSources {
			if source.Kind == "file" && source.Target == "docs/source.md" && source.Line == 12 {
				foundParsedSource = true
			}
		}
	}
	if !foundParsedSource {
		t.Fatalf("expected parsed file source, got %#v", result.Index.Entries)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"entries"`) || !strings.Contains(string(data), `"Indexed Memory"`) {
		t.Fatalf("unexpected index json:\n%s", string(data))
	}
}

func TestParseSources(t *testing.T) {
	cases := []struct {
		raw     string
		kind    string
		target  string
		line    int
		hasLine bool
	}{
		{"file:docs/source.md:12", "file", "docs/source.md", 12, true},
		{"docs/source.md:12-14", "file", "docs/source.md", 12, true},
		{"https://example.com/a", "url", "https://example.com/a", 0, false},
		{"trace:abc", "trace", "abc", 0, false},
		{"task:def", "task", "def", 0, false},
		{"manual", "manual", "", 0, false},
	}
	for _, tc := range cases {
		got := ParseSource(tc.raw)
		if got.Kind != tc.kind || got.Target != tc.target || got.Line != tc.line || got.HasLines != tc.hasLine {
			t.Fatalf("ParseSource(%q) = %#v", tc.raw, got)
		}
	}
}

func TestMemoryWritesRebuildIndexBestEffort(t *testing.T) {
	store := NewStore(t.TempDir())
	proposal, err := store.Propose(ProposalInput{
		AgentID: "main",
		Title:   "Auto Indexed",
		Body:    "Memory writes should refresh the rebuildable index.",
		Sources: []string{"manual"},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	indexPath := filepath.Join(store.Root, "index.json")
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("expected index after propose: %v", err)
	}
	if _, err := store.Commit(CommitInput{AgentID: "main", Proposal: proposal.ID}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"status": "active"`) || !strings.Contains(string(data), `"area": "long"`) {
		t.Fatalf("expected committed long memory in index:\n%s", string(data))
	}
	rejectable, err := store.Propose(ProposalInput{
		AgentID: "main",
		Title:   "Reject Indexed",
		Body:    "Rejected proposals should also refresh the index.",
		Sources: []string{"manual"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reject(RejectInput{AgentID: "main", Proposal: rejectable.ID}); err != nil {
		t.Fatalf("reject: %v", err)
	}
	data, err = os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"status": "rejected"`) {
		t.Fatalf("expected rejected proposal in index:\n%s", string(data))
	}
}

func TestProposeRejectsSecretLikeContent(t *testing.T) {
	store := NewStore(t.TempDir())
	_, err := store.Propose(ProposalInput{
		Title: "Bad Memory",
		Body:  "api_key=secret-value should not be stored",
	})
	if err == nil {
		t.Fatal("expected secret-like proposal to be rejected")
	}
}
