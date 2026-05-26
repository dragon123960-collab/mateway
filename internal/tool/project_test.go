package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/memory"
)

func TestProjectIndexSummarizesDirectory(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "README.md"), "# Demo\nhello\n")
	mustWriteFile(t, filepath.Join(root, "cmd", "main.go"), "package main\n\nfunc main() {}\n")
	mustWriteFile(t, filepath.Join(root, "internal", "tool", "x.go"), "package tool\n")

	result := ProjectIndex().Run(context.Background(), Call{
		Args:    map[string]string{"path": root, "max_depth": "3", "max_files": "20"},
		Context: Context{ProjectRoot: root, Workspace: root},
	})
	if !result.OK {
		t.Fatalf("expected success, got %+v", result)
	}
	if !strings.Contains(result.Output, "Project index") || !strings.Contains(result.Output, "README.md") {
		t.Fatalf("unexpected output %q", result.Output)
	}
	if kind, _ := result.Evidence["kind"].(string); kind != "project_index" {
		t.Fatalf("unexpected evidence %#v", result.Evidence)
	}
}

func TestFileSummarySummarizesTextFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	mustWriteFile(t, path, "# Title\n\n## Usage\nhello\nworld\n")

	result := FileSummary().Run(context.Background(), Call{
		Args:    map[string]string{"path": path, "max_lines": "20"},
		Context: Context{ProjectRoot: root, Workspace: root},
	})
	if !result.OK {
		t.Fatalf("expected success, got %+v", result)
	}
	if !strings.Contains(result.Output, "File summary") || !strings.Contains(result.Output, "## Usage") {
		t.Fatalf("unexpected output %q", result.Output)
	}
	if kind, _ := result.Evidence["kind"].(string); kind != "file_summary" {
		t.Fatalf("unexpected evidence %#v", result.Evidence)
	}
}

func TestFileSummaryRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	result := FileSummary().Run(context.Background(), Call{
		Args:    map[string]string{"path": root},
		Context: Context{ProjectRoot: root, Workspace: root},
	})
	if result.OK || !strings.Contains(result.Error, "requires a file path") {
		t.Fatalf("expected directory error, got %+v", result)
	}
}

func TestSourceQualityHintForFreshSearch(t *testing.T) {
	hint := sourceQualityHint("2026 latest AI courses official")
	if !strings.Contains(hint, "official") || !strings.Contains(hint, "weak evidence") {
		t.Fatalf("expected fresh source quality hint, got %q", hint)
	}
}

func TestSoftwareSearchQueriesStayGeneric(t *testing.T) {
	got := softwareSearchQueries("飞书的cli")
	if len(got) != 1 || got[0] != "飞书的cli" {
		t.Fatalf("expected generic query without product-specific rewrite, got %#v", got)
	}
}

func TestSoftwareSearchQueriesIncludeCanonicalLarkCLIAlias(t *testing.T) {
	got := softwareSearchQueries("larkcli")
	found := false
	for _, item := range got {
		if item == "lark-cli" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected canonical lark-cli alias in queries, got %#v", got)
	}
	if len(got) == 0 || got[0] != "lark-cli" {
		t.Fatalf("expected canonical lark-cli alias to be prioritized first, got %#v", got)
	}
}

func TestCanonicalCommandNameNormalizesLarkCLIAliases(t *testing.T) {
	for _, input := range []string{"larkcli", "lark cli", "lark-cli", "@larksuite/cli"} {
		if got := canonicalCommandName(input); got != "lark-cli" {
			t.Fatalf("expected %q to normalize to lark-cli, got %q", input, got)
		}
	}
}

func TestSoftwareSearchQueriesGenerateGenericCLIVariants(t *testing.T) {
	got := softwareSearchQueries("ghcli")
	if !containsQuery(got, "gh cli") || !containsQuery(got, "gh-cli") {
		t.Fatalf("expected ghcli variants, got %#v", got)
	}

	got = softwareSearchQueries("aws cli")
	if !containsQuery(got, "aws-cli") {
		t.Fatalf("expected dashed cli variant, got %#v", got)
	}
}

func TestSoftwareSearchQueriesRecoverCanonicalCLIFromLongNaturalLanguageQuery(t *testing.T) {
	got := softwareSearchQueries("lark feishu CLI command line tool github")
	if !containsQuery(got, "lark-cli") {
		t.Fatalf("expected long natural-language query to recover lark-cli candidate, got %#v", got)
	}
	if !containsQuery(got, "lark cli") {
		t.Fatalf("expected long natural-language query to recover spaced lark cli candidate, got %#v", got)
	}
}

func containsQuery(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestSkillSearchNoResultSuggestsBroaderCapabilityPhrases(t *testing.T) {
	output := renderSkillSearchOutput("text humanizer rewriting assistant AI text", nil)
	if !strings.Contains(output, "Searched priority catalogs") || !strings.Contains(output, "broader capability phrases") {
		t.Fatalf("expected helpful no-result guidance, got %q", output)
	}
}

func TestSoftwareInstallPreviewAndResult(t *testing.T) {
	preview := SoftwareInstall().Run(context.Background(), Call{
		Args: map[string]string{"name": "ripgrep"},
	})
	if preview.OK || !strings.Contains(preview.Error, "install command is required") {
		t.Fatalf("expected missing command error, got %#v", preview)
	}

	preview = SoftwareInstall().Run(context.Background(), Call{
		Args: map[string]string{
			"name":           "larksuite/cli",
			"method":         "npx",
			"command":        "true",
			"executable":     "lark-cli",
			"verify_command": "true",
			"source_url":     "https://github.com/larksuite/cli",
			"source_name":    "upstream",
		},
	})
	if !preview.OK || preview.RequiresConfirm {
		t.Fatalf("expected direct install execution without confirmation, got %#v", preview)
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "lark-cli")
	mustWriteFile(t, binPath, "#!/bin/sh\nprintf 'lark version test\\n'\n")
	if err := os.Chmod(binPath, 0o755); err != nil {
		t.Fatal(err)
	}
	result := SoftwareInstall().Run(context.Background(), Call{
		Args: map[string]string{
			"name":           "lark",
			"command":        "true",
			"executable":     "lark-cli",
			"verify_command": shellQuote(binPath) + " --version",
		},
		Confirmed: true,
	})
	if !result.OK {
		t.Fatalf("expected install success, got %#v", result)
	}
	if !strings.Contains(result.Output, "安装完成") || !strings.Contains(result.Output, "lark-cli --help") {
		t.Fatalf("expected completion and next commands, got %q", result.Output)
	}
}

func TestSoftwareInstallTreatsVerifiedInstallAsSuccess(t *testing.T) {
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "agent-browser")
	mustWriteFile(t, binPath, "#!/bin/sh\nprintf 'agent-browser 0.27.0\\n'\n")
	if err := os.Chmod(binPath, 0o755); err != nil {
		t.Fatal(err)
	}
	result := SoftwareInstall().Run(context.Background(), Call{
		Args: map[string]string{
			"name":           "agent-browser",
			"method":         "npm",
			"command":        "sh -lc 'echo installing >&2; exit 1'",
			"executable":     "agent-browser",
			"verify_command": shellQuote(binPath) + " --version",
		},
		Confirmed: true,
	})
	if !result.OK {
		t.Fatalf("expected verified install to count as success, got %#v", result)
	}
	if strings.TrimSpace(result.Error) != "" {
		t.Fatalf("expected empty error after verify success, got %q", result.Error)
	}
	if !strings.Contains(result.Output, "安装完成") || !strings.Contains(result.Output, "agent-browser --help") {
		t.Fatalf("expected verified install guidance, got %q", result.Output)
	}
}

func TestTerminalRunReturnsCommandEvidence(t *testing.T) {
	root := t.TempDir()
	result := TerminalRun().Run(context.Background(), Call{
		Args:    map[string]string{"command": "printf hello", "timeout": "5", "purpose": "diagnose local cli"},
		Context: Context{ProjectRoot: root, Workspace: root},
	})
	if !result.OK {
		t.Fatalf("expected terminal success, got %#v", result)
	}
	if !strings.Contains(result.Output, "hello") {
		t.Fatalf("expected stdout in output, got %q", result.Output)
	}
	if result.Evidence["kind"] != "terminal" || result.Evidence["command"] != "printf hello" || result.Evidence["exit_code"] != 0 {
		t.Fatalf("unexpected terminal evidence %#v", result.Evidence)
	}
	if result.Evidence["stdout"] != "hello" || result.Evidence["purpose"] != "diagnose local cli" {
		t.Fatalf("expected stdout and purpose evidence, got %#v", result.Evidence)
	}
}

func TestMemoryToolsSearchAndIndex(t *testing.T) {
	workspace := t.TempDir()
	store := memory.NewStore(workspace)
	proposal, err := store.Propose(memory.ProposalInput{
		AgentID: "main",
		Title:   "Tool Memory",
		Body:    "Runtime tools can search reviewed Markdown memory.",
		Sources: []string{"manual"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(memory.CommitInput{AgentID: "main", Proposal: proposal.ID}); err != nil {
		t.Fatal(err)
	}
	search := MemorySearch().Run(context.Background(), Call{
		Args:    map[string]string{"query": "reviewed Markdown memory"},
		Context: Context{Workspace: workspace},
	})
	if !search.OK {
		t.Fatalf("expected search success, got %#v", search)
	}
	if !strings.Contains(search.Output, "Tool Memory") || !strings.Contains(search.Output, "lines:") {
		t.Fatalf("unexpected search output %q", search.Output)
	}
	index := MemoryIndex().Run(context.Background(), Call{
		Args:    map[string]string{},
		Context: Context{Workspace: workspace},
	})
	if !index.OK {
		t.Fatalf("expected index success, got %#v", index)
	}
	if !strings.Contains(index.Output, "entries=") || !strings.Contains(index.Output, "parsed_sources=") || !strings.Contains(index.Output, "long:") {
		t.Fatalf("unexpected index output %q", index.Output)
	}
}

func TestMemoryListAndShowIncludeReviewSignals(t *testing.T) {
	workspace := t.TempDir()
	store := memory.NewStore(workspace)
	longDir := filepath.Join(workspace, "memory", "agents", "main", "long")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `---
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

Old decision note.
`
	fresh := `---
type: project
scope: agent
status: active
sources:
  - manual
confidence: low
created_at: 2026-05-20
updated_at: 2026-05-20
---

# Fresh Memory

Recent project note.
`
	if err := os.WriteFile(filepath.Join(longDir, "decision-stale-memory.md"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(longDir, "project-fresh-memory.md"), []byte(fresh), 0o644); err != nil {
		t.Fatal(err)
	}
	list := MemoryList().Run(context.Background(), Call{
		Args:    map[string]string{"area": "long", "status": "active", "review": "stale"},
		Context: Context{Workspace: workspace},
	})
	if !list.OK || !strings.Contains(list.Output, "Stale Memory") || strings.Contains(list.Output, "Fresh Memory") || !strings.Contains(list.Output, "review=stale") {
		t.Fatalf("expected stale review filter in memory list, got %#v", list)
	}
	grouped := MemoryList().Run(context.Background(), Call{
		Args:    map[string]string{"area": "long", "status": "active", "group_by": "review"},
		Context: Context{Workspace: workspace},
	})
	if !grouped.OK || !strings.Contains(grouped.Output, "[review=stale]") || !strings.Contains(grouped.Output, "[review=fresh]") {
		t.Fatalf("expected review grouping in memory list, got %#v", grouped)
	}
	targetGrouped := MemoryList().Run(context.Background(), Call{
		Args:    map[string]string{"area": "long", "status": "active", "group_by": "target"},
		Context: Context{Workspace: workspace},
	})
	if !targetGrouped.OK || !strings.Contains(targetGrouped.Output, "[target=decision-style long memory]") || !strings.Contains(targetGrouped.Output, "[target=project fact/note-style long memory]") {
		t.Fatalf("expected target grouping in memory list, got %#v", targetGrouped)
	}
	show := MemoryShow().Run(context.Background(), Call{
		Args:    map[string]string{"id": filepath.Join(longDir, "decision-stale-memory.md")},
		Context: Context{Workspace: workspace},
	})
	if !show.OK || !strings.Contains(show.Output, "Review: stale") || !strings.Contains(show.Output, "Review tip: this entry appears stale") {
		t.Fatalf("expected review label in memory show, got %#v", show)
	}
	review := MemoryReview().Run(context.Background(), Call{
		Args:    map[string]string{},
		Context: Context{Workspace: workspace},
	})
	if !review.OK || !strings.Contains(review.Output, "Long memory review queue") || !strings.Contains(review.Output, "Stale Memory") || strings.Contains(review.Output, "Fresh Memory") || !strings.Contains(review.Output, "suggestion: re-validate this decision-style long memory") {
		t.Fatalf("expected memory review queue to include stale/soon only, got %#v", review)
	}
	if strings.Index(review.Output, "Stale Memory") > strings.Index(review.Output, "Fresh Memory") && strings.Contains(review.Output, "Fresh Memory") {
		t.Fatalf("expected stale entries to be prioritized in review queue, got %#v", review)
	}
	targetReview := MemoryReview().Run(context.Background(), Call{
		Args:    map[string]string{"target": "decision-style long memory"},
		Context: Context{Workspace: workspace},
	})
	if !targetReview.OK || !strings.Contains(targetReview.Output, "Stale Memory") || strings.Contains(targetReview.Output, "Fresh Memory") {
		t.Fatalf("expected target-filtered review queue, got %#v", targetReview)
	}
	proposalReview := MemoryReview().Run(context.Background(), Call{
		Args:    map[string]string{"proposal": "true"},
		Context: Context{Workspace: workspace},
	})
	if !proposalReview.OK || !strings.Contains(proposalReview.Output, "Long memory review proposal written:") {
		t.Fatalf("expected review proposal write, got %#v", proposalReview)
	}
	_ = store
}

func TestMemoryToolsListShowRejectAndCommit(t *testing.T) {
	workspace := t.TempDir()
	store := memory.NewStore(workspace)
	proposal, err := store.Propose(memory.ProposalInput{
		AgentID:    "main",
		Type:       "decision",
		Title:      "Review Memory",
		Body:       "Review this proposal before promotion.",
		Sources:    []string{"manual"},
		Confidence: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	list := MemoryList().Run(context.Background(), Call{
		Args:    map[string]string{"area": "inbox", "status": "proposed"},
		Context: Context{Workspace: workspace},
	})
	if !list.OK || !strings.Contains(list.Output, proposal.ID) {
		t.Fatalf("expected list output to include proposal, got %#v", list)
	}
	if !strings.Contains(list.Output, "target=decision") {
		t.Fatalf("expected typed target hint in list output, got %#v", list)
	}
	if !strings.Contains(list.Output, "origin=manual") {
		t.Fatalf("expected origin hint in list output, got %#v", list)
	}
	if !strings.Contains(list.Output, "--tag distill-decision|distill-playbook|distill-preference|distill-project") {
		t.Fatalf("expected review tip for distillation tags, got %#v", list)
	}
	third, err := store.Propose(memory.ProposalInput{
		AgentID:    "main",
		Type:       "project",
		Title:      "Project Note",
		Body:       "Project summary candidate.",
		Sources:    []string{"manual"},
		Confidence: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = third
	fourth, err := store.Propose(memory.ProposalInput{
		AgentID:    "main",
		Type:       "playbook",
		Title:      "Workflow Note",
		Body:       "Workflow candidate.",
		Sources:    []string{"manual"},
		Confidence: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = fourth
	list = MemoryList().Run(context.Background(), Call{
		Args:    map[string]string{"area": "inbox", "status": "proposed"},
		Context: Context{Workspace: workspace},
	})
	lines := strings.Split(list.Output, "\n")
	if len(lines) < 3 || !strings.Contains(lines[1], "decision") || !strings.Contains(lines[2], "playbook") {
		t.Fatalf("expected high-priority memory kinds first in list output, got %#v", list.Output)
	}
	filtered := MemoryList().Run(context.Background(), Call{
		Args:    map[string]string{"area": "inbox", "status": "proposed", "kind": "playbook"},
		Context: Context{Workspace: workspace},
	})
	if !filtered.OK || !strings.Contains(filtered.Output, "Workflow Note") || strings.Contains(filtered.Output, "Review Memory") {
		t.Fatalf("expected kind-filtered list output, got %#v", filtered)
	}
	tagged, err := store.Propose(memory.ProposalInput{
		AgentID:    "main",
		Type:       "decision",
		Title:      "Distilled Memory",
		Body:       "Auto distilled memory.",
		Sources:    []string{"manual"},
		Tags:       []string{"daily-distillation", "auto-proposal"},
		Confidence: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = tagged
	filtered = MemoryList().Run(context.Background(), Call{
		Args:    map[string]string{"area": "inbox", "status": "proposed", "tag": "daily-distillation"},
		Context: Context{Workspace: workspace},
	})
	if !filtered.OK || !strings.Contains(filtered.Output, "Distilled Memory") || !strings.Contains(filtered.Output, "origin=daily_distillation") || strings.Contains(filtered.Output, "Workflow Note") {
		t.Fatalf("expected tag-filtered list output, got %#v", filtered)
	}
	grouped := MemoryList().Run(context.Background(), Call{
		Args:    map[string]string{"area": "inbox", "status": "proposed", "group_by": "origin"},
		Context: Context{Workspace: workspace},
	})
	if !grouped.OK || !strings.Contains(grouped.Output, "[origin=daily_distillation]") || !strings.Contains(grouped.Output, "[origin=manual]") {
		t.Fatalf("expected grouped list output, got %#v", grouped)
	}
	show := MemoryShow().Run(context.Background(), Call{
		Args:    map[string]string{"id": proposal.ID},
		Context: Context{Workspace: workspace},
	})
	if !show.OK || !strings.Contains(show.Output, "Review Memory") {
		t.Fatalf("expected show output to include proposal body, got %#v", show)
	}
	for _, want := range []string{"Type: decision", "Confidence: medium", "Recommended target: decision-style long memory"} {
		if !strings.Contains(show.Output, want) {
			t.Fatalf("expected show output to contain %q, got %#v", want, show)
		}
	}
	if !strings.Contains(show.Output, "Origin: manual") {
		t.Fatalf("expected show output to contain origin summary, got %#v", show)
	}
	reject := MemoryReject().Run(context.Background(), Call{
		Args:    map[string]string{"proposal": proposal.ID, "reason": "test reject"},
		Context: Context{Workspace: workspace},
	})
	if !reject.OK || !strings.Contains(reject.Output, "rejected") {
		t.Fatalf("expected reject success, got %#v", reject)
	}

	second, err := store.Propose(memory.ProposalInput{
		AgentID:    "main",
		Type:       "decision",
		Title:      "Commit Memory",
		Body:       "This proposal should become long memory.",
		Sources:    []string{"manual"},
		Confidence: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	commit := MemoryCommit().Run(context.Background(), Call{
		Args:    map[string]string{"proposal": second.ID},
		Context: Context{Workspace: workspace},
	})
	if commit.OK || !commit.RequiresConfirm || !strings.Contains(commit.ConfirmMessage, "类型：decision") {
		t.Fatalf("expected high-impact memory commit confirmation, got %#v", commit)
	}
	commit = MemoryCommit().Run(context.Background(), Call{
		Args:      map[string]string{"proposal": second.ID},
		Context:   Context{Workspace: workspace},
		Confirmed: true,
	})
	if !commit.OK || !strings.Contains(commit.Output, "Memory committed as") {
		t.Fatalf("expected commit success after confirmation, got %#v", commit)
	}
	if !strings.Contains(commit.Output, "Recommended target: decision-style long memory") {
		t.Fatalf("expected commit output to include recommended target, got %#v", commit)
	}
	if !RequireConfirmForTool("memory.commit", map[string]string{"type": "decision"}) {
		t.Fatalf("expected high-impact memory commit to require confirmation")
	}
	if RequireConfirmForTool("memory.commit", map[string]string{"type": "project"}) {
		t.Fatalf("expected project memory commit not to require confirmation by default")
	}
	thirdCommit, err := store.Propose(memory.ProposalInput{
		AgentID:    "main",
		Type:       "project",
		Title:      "Project Commit",
		Body:       "This proposal should become project memory.",
		Sources:    []string{"manual"},
		Confidence: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	projectCommit := MemoryCommit().Run(context.Background(), Call{
		Args:    map[string]string{"proposal": thirdCommit.ID},
		Context: Context{Workspace: workspace},
	})
	if !projectCommit.OK || projectCommit.RequiresConfirm {
		t.Fatalf("expected low-impact project memory commit without confirmation, got %#v", projectCommit)
	}
}

func TestScheduleToolsConfirmationBoundary(t *testing.T) {
	home := t.TempDir()
	ctx := Context{Home: home, ProjectRoot: home, Workspace: home}
	create := ScheduleCreate().Run(context.Background(), Call{
		Args: map[string]string{
			"id":       "ai-trends",
			"title":    "AI Trends",
			"prompt":   "Collect AI trends.",
			"daily_at": "09:00",
		},
		Context: ctx,
	})
	if !create.OK || create.RequiresConfirm {
		t.Fatalf("expected schedule create without confirmation, got %#v", create)
	}
	update := ScheduleUpdate().Run(context.Background(), Call{
		Args:    map[string]string{"id": "ai-trends", "daily_at": "10:00"},
		Context: ctx,
	})
	if !update.OK || update.RequiresConfirm {
		t.Fatalf("expected schedule update without confirmation, got %#v", update)
	}
	if !RequireConfirmForTool("schedule.delete", map[string]string{"id": "ai-trends"}) {
		t.Fatalf("expected schedule.delete to require confirmation")
	}
	if RequireConfirmForTool("schedule.update", map[string]string{"id": "ai-trends"}) {
		t.Fatalf("expected schedule.update not to require confirmation")
	}
}

func TestSkillPromoteRequiresConfirmationAndPromotes(t *testing.T) {
	workspace := t.TempDir()
	store := memory.NewStore(workspace)
	cfg := memory.LearningConfig{Enabled: true, SuccessThreshold: 1, RequireUserConfirm: true}
	skillCandidate, err := store.ProcessTask(memory.TaskOutcome{
		AgentID:     "main",
		TraceID:     "trace-1",
		TaskID:      "task-1",
		Intent:      "review latest release notes",
		PlanSummary: "review release notes",
		Tools:       []string{"web.search", "file.summary"},
		Success:     true,
		FinishedAt:  time.Now(),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	promote := SkillPromote().Run(context.Background(), Call{
		Args:    map[string]string{"proposal": skillCandidate.CandidatePath, "name": "release-review"},
		Context: Context{Workspace: workspace},
	})
	if promote.OK || !promote.RequiresConfirm {
		t.Fatalf("expected skill promote confirmation, got %#v", promote)
	}
	promote = SkillPromote().Run(context.Background(), Call{
		Args:      map[string]string{"proposal": skillCandidate.CandidatePath, "name": "release-review"},
		Context:   Context{Workspace: workspace},
		Confirmed: true,
	})
	if !promote.OK || !strings.Contains(promote.Output, "Skill promoted as:") || !strings.Contains(promote.Output, "next planning turn") {
		t.Fatalf("expected skill promote success, got %#v", promote)
	}
	if _, err := os.Stat(filepath.Join(workspace, "skills", "release-review", "SKILL.md")); err != nil {
		t.Fatalf("expected promoted skill file: %v", err)
	}
}

func TestScheduleCreateSupportsOneShotRunAt(t *testing.T) {
	home := t.TempDir()
	ctx := Context{Home: home, ProjectRoot: home, Workspace: home}
	create := ScheduleCreate().Run(context.Background(), Call{
		Args: map[string]string{
			"id":     "mail-once",
			"title":  "Mail Once",
			"prompt": "Remind me to check mail.",
			"run_at": "2026-05-25T10:30:00+08:00",
		},
		Context: ctx,
	})
	if !create.OK || create.RequiresConfirm {
		t.Fatalf("expected one-shot schedule create without confirmation, got %#v", create)
	}
	if got, _ := create.Evidence["schedule"].(string); got != "once@2026-05-25T10:30:00+08:00" {
		t.Fatalf("expected one-shot schedule evidence, got %#v", create.Evidence)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
