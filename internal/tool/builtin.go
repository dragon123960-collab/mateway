package tool

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/skill"
	"github.com/dongping/mateway/internal/textmatch"
)

func RegisterBuiltins(r *Registry) {
	r.Register(TimeNow())
	r.Register(ConfigSummary())
	r.Register(MemoryList())
	r.Register(MemoryShow())
	r.Register(MemorySearch())
	r.Register(MemoryIndex())
	r.Register(MemoryReview())
	r.Register(MemoryCommit())
	r.Register(MemoryReject())
	r.Register(SkillSearch())
	r.Register(SkillInstall())
	r.Register(SkillPromote())
	r.Register(SoftwareSearch())
	r.Register(SoftwareInstall())
	r.Register(ScheduleCreate())
	r.Register(ScheduleList())
	r.Register(ScheduleShow())
	r.Register(SchedulePause())
	r.Register(ScheduleResume())
	r.Register(ScheduleUpdate())
	r.Register(ScheduleDelete())
	r.Register(WebSearch())
	r.Register(WebFetch())
	r.Register(FileRead())
	r.Register(ProjectIndex())
	r.Register(FileSummary())
	r.Register(FileWrite())
	r.Register(FilePatch())
	r.Register(TerminalRun())
	r.Register(ShellRun())
	r.Register(UserAsk())
}

func SkillSearch() Definition {
	return Definition{
		Name:        "skill.search",
		Description: "Search installable agent skills from configured remote catalogs, then report local Mateway workspace install state. This tool never installs; when the user asks to install, plan skill.install after search.",
		Metadata: Metadata{
			Purpose:            "search installable agent skills",
			WhenToUse:          []string{"find agent skill", "look up installable skill by capability"},
			WhenNotToUse:       []string{"installing directly without search"},
			RequiredArgs:       []string{"query"},
			OutputContract:     []string{"query", "result count"},
			AcceptanceSpecRef:  "skill.search/default",
			AcceptanceMode:     AcceptanceCodeLLM,
			SoftFailureSignals: []string{"no matching skills found"},
			ParallelMode:       ParallelReadOnlyOK,
			ResourceScope:      "skill:query",
			ReusePolicy:        ReuseNever,
		},
		Risk: RiskSafeRead,
		ArgsSchema: map[string]string{
			"query": "skill search query",
			"limit": "optional result limit",
		},
		Run: func(ctx context.Context, call Call) Result {
			query := strings.TrimSpace(firstNonEmpty(call.Args["query"], call.Args["q"], call.Args["name"]))
			if query == "" {
				return ErrorResult("query is required")
			}
			limit := parsePositiveArg(call.Args["limit"], 6)
			items, err := skill.SearchCatalog(ctx, call.Context.Workspace, query, skill.CatalogSearchOptions{Limit: limit})
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{
				OK:       true,
				Output:   renderSkillSearchOutput(query, items),
				Evidence: skillSearchEvidence(query, items),
			}
		},
	}
}

func SkillInstall() Definition {
	return Definition{
		Name:        "skill.install",
		Description: "Install one agent skill into the Mateway workspace skills directory. The name argument can be an exact skill name, URL, or capability query; the installer resolves the best catalog match.",
		Metadata: Metadata{
			Purpose:            "install an agent skill into the workspace",
			WhenToUse:          []string{"install selected skill", "materialize skill into workspace"},
			WhenNotToUse:       []string{"searching only"},
			RequiredArgs:       []string{"name"},
			OutputContract:     []string{"skill name", "target path", "install source"},
			AcceptanceSpecRef:  "skill.install/default",
			AcceptanceMode:     AcceptanceCodeLLM,
			SoftFailureSignals: []string{"skill name or url is required"},
			ParallelMode:       ParallelForbid,
			ResourceScope:      "skill:install",
			ReusePolicy:        ReuseNever,
		},
		Risk: RiskGuardedMutation,
		ArgsSchema: map[string]string{
			"name": "skill name or URL",
			"url":  "optional direct skill URL",
		},
		Run: func(ctx context.Context, call Call) Result {
			ref := strings.TrimSpace(firstNonEmpty(call.Args["url"], call.Args["name"], call.Args["query"]))
			if ref == "" {
				return ErrorResult("skill name or URL is required")
			}
			result, err := skill.InstallCatalogSkill(ctx, call.Context.Workspace, ref, skill.CatalogSearchOptions{Limit: 1})
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{
				OK:       true,
				Output:   renderSkillInstallOutput(result),
				Evidence: map[string]any{"kind": "skill_install", "name": result.Item.Name, "source": result.Item.Source, "url": result.Item.URL, "install_url": result.Item.InstallURL, "target_path": result.TargetPath, "already_installed": result.AlreadyDone},
			}
		},
	}
}

func SkillPromote() Definition {
	return Definition{
		Name:        "skill.promote",
		Description: "Promote one proposed skill candidate from memory inbox into workspace/skills/<name>/SKILL.md.",
		Metadata: Metadata{
			Purpose:            "promote a reviewed skill candidate into the workspace skills directory",
			WhenToUse:          []string{"approve a generated skill candidate", "materialize reviewed skill candidate into workspace skills"},
			WhenNotToUse:       []string{"searching only", "editing an existing skill in place"},
			RequiredArgs:       []string{"proposal"},
			OutputContract:     []string{"source path", "target path", "skill name"},
			AcceptanceMode:     AcceptanceCodeOnly,
			SoftFailureSignals: []string{"skill candidate path is required", "must have frontmatter with type: skill_candidate and status: proposed"},
			ParallelMode:       ParallelForbid,
			ResourceScope:      "skill:promote",
			ReusePolicy:        ReuseNever,
		},
		Risk: RiskGuardedMutation,
		ArgsSchema: map[string]string{
			"agent":    "optional agent id, defaults to main",
			"proposal": "skill candidate id or path",
			"name":     "optional promoted skill directory name",
		},
		Run: func(ctx context.Context, call Call) Result {
			store, err := memoryStoreFromToolContext(call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			proposal := strings.TrimSpace(firstNonEmpty(call.Args["proposal"], call.Args["id"], call.Args["path"]))
			if proposal == "" {
				return ErrorResult("proposal is required")
			}
			if !call.Confirmed {
				return ConfirmResult(
					fmt.Sprintf("这个操作会把 skill candidate 提升为 workspace skill。\n\nProposal：%s\n目标目录：workspace/skills\n\n回复“确认”继续执行，或回复“取消”放弃。", proposal),
					map[string]any{"kind": "skill_promote_confirm", "proposal": proposal},
				)
			}
			result, err := store.PromoteSkillCandidate(memory.SkillPromotionInput{
				AgentID:   firstNonEmpty(call.Args["agent"], "main"),
				Proposal:  proposal,
				SkillName: strings.TrimSpace(call.Args["name"]),
				At:        time.Now(),
			})
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{
				OK:     true,
				Output: fmt.Sprintf("Skill promoted as: %s\nSource proposal: %s\nThe runtime reloads workspace skills on the next planning turn, so this skill can be selected in subsequent tasks.", result.TargetPath, result.SourcePath),
				Evidence: map[string]any{
					"kind":        "skill_promote",
					"source_path": result.SourcePath,
					"target_path": result.TargetPath,
					"skill_name":  result.SkillName,
				},
			}
		},
	}
}

func SoftwareSearch() Definition {
	return Definition{
		Name:        "software.search",
		Description: "Search public software, CLI tools, repositories, and installation clues with GitHub-first fallback behavior.",
		Metadata: Metadata{
			Purpose:            "discover public software sources and explicit installation guidance",
			WhenToUse:          []string{"find CLI tools or GitHub repositories", "find official install docs or upstream install commands", "check whether public software exists or can likely be used"},
			WhenNotToUse:       []string{"local project inspection", "reading a known URL in full", "guessing or executing install commands"},
			RequiredArgs:       []string{"query"},
			OutputContract:     []string{"query", "provider", "result count", "repository or install clue summary"},
			AcceptanceSpecRef:  "software.search/default",
			AcceptanceMode:     AcceptanceCodeLLM,
			SoftFailureSignals: []string{"no software results found"},
			ParallelMode:       ParallelReadOnlyOK,
			ResourceScope:      "software:query",
			ReusePolicy:        ReuseNever,
		},
		Risk: RiskSafeRead,
		ArgsSchema: map[string]string{
			"query": "software or CLI query",
			"limit": "optional result limit",
		},
		Run: func(ctx context.Context, call Call) Result {
			query := strings.TrimSpace(firstNonEmpty(call.Args["query"], call.Args["q"], call.Args["name"]))
			if query == "" {
				return ErrorResult("query is required")
			}
			limit := parsePositiveArg(call.Args["limit"], 5)
			results, err := githubSoftwareSearch(ctx, query, limit)
			if err != nil {
				return ErrorResult(err.Error())
			}
			if len(results) == 0 {
				return Result{OK: true, Output: "No software results found for: " + query, Evidence: map[string]any{"kind": "software_search", "provider": "github", "query": query, "result_count": 0}}
			}
			return Result{OK: true, Output: renderSoftwareSearchOutput(query, results), Evidence: softwareSearchEvidence(query, results)}
		},
	}
}

func SoftwareInstall() Definition {
	return Definition{
		Name:        "software.install",
		Description: "Install CLI software with an explicit install command, then verify the installed executable and return next commands.",
		Metadata: Metadata{
			Purpose:            "install CLI software and verify the result",
			WhenToUse:          []string{"install a CLI from explicit upstream command", "verify installed executable after install evidence is known"},
			WhenNotToUse:       []string{"guessing install commands", "installing before upstream docs or user-provided command is explicit", "using search output as a substitute for verification"},
			RequiredArgs:       []string{"command"},
			OutputContract:     []string{"install command", "verify command", "verified status", "install and verify output summary"},
			AcceptanceSpecRef:  "software.install/default",
			AcceptanceMode:     AcceptanceCodeLLM,
			SoftFailureSignals: []string{"install command is required", "not found", "permission denied", "timed out"},
			ParallelMode:       ParallelForbid,
			ResourceScope:      "software:install",
			ReusePolicy:        ReuseNever,
		},
		Risk: RiskGuardedMutation,
		ArgsSchema: map[string]string{
			"name":           "software name",
			"method":         "install method from upstream docs, for example npm, npx, brew, go, pip, cargo, or binary",
			"command":        "required install command copied from upstream docs or selected by the user",
			"verify_command": "optional verification command; defaults to command -v <executable> && <executable> --version",
			"executable":     "optional installed executable name",
			"source_url":     "optional upstream documentation URL",
		},
		Run: func(ctx context.Context, call Call) Result {
			name := strings.TrimSpace(firstNonEmpty(call.Args["name"], call.Args["package"], call.Args["tool"], call.Args["executable"]))
			method := strings.ToLower(strings.TrimSpace(call.Args["method"]))
			installCommand := strings.TrimSpace(call.Args["command"])
			executable := strings.TrimSpace(firstNonEmpty(call.Args["executable"], executableFromInstallCommand(installCommand), name))
			verifyCommand := strings.TrimSpace(call.Args["verify_command"])
			if verifyCommand == "" {
				verifyCommand = defaultVerifyCommand(executable)
			}
			if installCommand == "" {
				return ErrorResult("install command is required; run software.search or read the upstream install docs before using software.install")
			}
			installOK, exitCode, output := runCommandWithTimeout(ctx, installCommand, call.Context.ProjectRoot, 12*time.Second)
			verifyOK, verifyExit, verifyOutput := runCommandWithTimeout(ctx, verifyCommand, call.Context.ProjectRoot, 12*time.Second)
			effectiveOK := installOK && verifyOK
			if verifyOK {
				effectiveOK = true
			}
			result := renderSoftwareInstallResult(name, executable, installCommand, effectiveOK, exitCode, output, verifyCommand, verifyOK, verifyExit, verifyOutput)
			return Result{
				OK:     effectiveOK,
				Output: result,
				Error:  softwareInstallError(effectiveOK, verifyOK, output, verifyOutput),
				Evidence: map[string]any{
					"kind":              "software_install",
					"name":              name,
					"method":            method,
					"command":           installCommand,
					"exit_code":         exitCode,
					"verify_command":    verifyCommand,
					"verify_exit_code":  verifyExit,
					"verified":          verifyOK,
					"installed_command": executable,
					"source_url":        call.Args["source_url"],
				},
			}
		},
	}
}

func ScheduleCreate() Definition {
	return Definition{
		Name:        "schedule.create",
		Description: "Create a user scheduled task YAML under the Mateway home directory after all required fields are known.",
		Metadata: Metadata{
			Purpose:            "create a user scheduled task",
			WhenToUse:          []string{"create recurring task", "set daily or weekly schedule"},
			RequiredArgs:       []string{"title", "prompt"},
			OutputContract:     []string{"task id", "status", "schedule", "path"},
			AcceptanceSpecRef:  "schedule.create/default",
			AcceptanceMode:     AcceptanceCodeOnly,
			SoftFailureSignals: []string{"missing_schedule_fields"},
			ParallelMode:       ParallelForbid,
			ResourceScope:      "schedule:task",
			ReusePolicy:        ReuseNever,
		},
		Risk: RiskGuardedMutation,
		ArgsSchema: map[string]string{
			"id":          "optional schedule id",
			"title":       "schedule title",
			"prompt":      "task prompt to execute when the schedule fires",
			"run_at":      "RFC3339 timestamp for one-shot schedules",
			"daily_at":    "HH:MM for daily schedule",
			"weekly_at":   "HH:MM for weekly schedule",
			"weekday":     "weekday for weekly schedule",
			"monthly_at":  "HH:MM for monthly schedule",
			"monthly_day": "day of month",
			"interval":    "duration such as 2h",
		},
		Run: func(ctx context.Context, call Call) Result {
			input := scheduleInputFromArgs(call)
			check := schedule.CheckDraft(input)
			if !check.Ready {
				return Result{OK: false, Output: check.ClarifyMessage, Error: "missing_schedule_fields", Evidence: map[string]any{"kind": "schedule_missing_fields", "missing_fields": check.MissingFields}}
			}
			task, path, err := schedule.NewStore(call.Context.Home).Create(input)
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{OK: true, Output: renderScheduleTaskOutput("created", task, path), Evidence: scheduleTaskEvidence("schedule_create", task, path)}
		},
	}
}

func ScheduleList() Definition {
	return Definition{
		Name:        "schedule.list",
		Description: "List user scheduled tasks from the Mateway home directory.",
		Metadata: Metadata{
			Purpose:           "list schedule tasks",
			WhenToUse:         []string{"inspect existing schedules"},
			OutputContract:    []string{"task count"},
			AcceptanceSpecRef: "schedule.list/default",
			AcceptanceMode:    AcceptanceCodeOnly,
			ParallelMode:      ParallelReadOnlyOK,
			ResourceScope:     "schedule:list",
			ReusePolicy:       ReuseNever,
		},
		Risk:       RiskSafeRead,
		ArgsSchema: map[string]string{},
		Run: func(ctx context.Context, call Call) Result {
			tasks, err := schedule.NewStore(call.Context.Home).List()
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{OK: true, Output: renderScheduleList(tasks), Evidence: map[string]any{"kind": "schedule_list", "task_count": len(tasks)}}
		},
	}
}

func ScheduleShow() Definition {
	return Definition{
		Name:        "schedule.show",
		Description: "Show one user scheduled task.",
		Metadata: Metadata{
			Purpose:           "show one schedule task",
			WhenToUse:         []string{"inspect one schedule by id"},
			RequiredArgs:      []string{"id"},
			OutputContract:    []string{"task id", "status", "path"},
			AcceptanceSpecRef: "schedule.show/default",
			AcceptanceMode:    AcceptanceCodeOnly,
			ParallelMode:      ParallelReadOnlyOK,
			ResourceScope:     "schedule:task",
			ReusePolicy:       ReuseNever,
		},
		Risk:       RiskSafeRead,
		ArgsSchema: map[string]string{"id": "schedule id"},
		Run: func(ctx context.Context, call Call) Result {
			id := strings.TrimSpace(call.Args["id"])
			if id == "" {
				return ErrorResult("id is required")
			}
			task, path, err := schedule.NewStore(call.Context.Home).Show(id)
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{OK: true, Output: renderScheduleTaskOutput("found", task, path), Evidence: scheduleTaskEvidence("schedule_show", task, path)}
		},
	}
}

func SchedulePause() Definition {
	return scheduleStatusTool("schedule.pause", "Pause one user scheduled task.", schedule.StatusPaused, "paused")
}

func ScheduleResume() Definition {
	return scheduleStatusTool("schedule.resume", "Resume one paused user scheduled task.", schedule.StatusActive, "resumed")
}

func scheduleStatusTool(name, description, status, verb string) Definition {
	return Definition{
		Name:        name,
		Description: description,
		Metadata: Metadata{
			Purpose:           "change schedule task status",
			WhenToUse:         []string{"pause schedule", "resume schedule"},
			RequiredArgs:      []string{"id"},
			OutputContract:    []string{"task id", "status", "path"},
			AcceptanceSpecRef: name + "/default",
			AcceptanceMode:    AcceptanceCodeOnly,
			ParallelMode:      ParallelForbid,
			ResourceScope:     "schedule:task",
			ReusePolicy:       ReuseNever,
		},
		Risk:       RiskGuardedMutation,
		ArgsSchema: map[string]string{"id": "schedule id"},
		Run: func(ctx context.Context, call Call) Result {
			id := strings.TrimSpace(call.Args["id"])
			if id == "" {
				return ErrorResult("id is required")
			}
			task, path, err := schedule.NewStore(call.Context.Home).SetStatus(id, status)
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{OK: true, Output: renderScheduleTaskOutput(verb, task, path), Evidence: scheduleTaskEvidence(name, task, path)}
		},
	}
}

func ScheduleUpdate() Definition {
	return Definition{
		Name:        "schedule.update",
		Description: "Update user scheduled task fields. Provide only fields that should change.",
		Metadata: Metadata{
			Purpose:           "update a schedule task",
			WhenToUse:         []string{"change schedule fields", "change prompt or title"},
			RequiredArgs:      []string{"id"},
			OutputContract:    []string{"task id", "status", "schedule", "path"},
			AcceptanceSpecRef: "schedule.update/default",
			AcceptanceMode:    AcceptanceCodeOnly,
			ParallelMode:      ParallelForbid,
			ResourceScope:     "schedule:task",
			ReusePolicy:       ReuseNever,
		},
		Risk: RiskGuardedMutation,
		ArgsSchema: map[string]string{
			"id":          "schedule id",
			"title":       "optional new title",
			"prompt":      "optional new prompt",
			"run_at":      "optional RFC3339 timestamp for one-shot schedules",
			"daily_at":    "optional HH:MM for daily schedule",
			"weekly_at":   "optional HH:MM for weekly schedule",
			"weekday":     "optional weekday for weekly schedule",
			"monthly_at":  "optional HH:MM for monthly schedule",
			"monthly_day": "optional day of month",
			"interval":    "optional duration such as 2h",
		},
		Run: func(ctx context.Context, call Call) Result {
			id := strings.TrimSpace(call.Args["id"])
			if id == "" {
				return ErrorResult("id is required")
			}
			input := scheduleInputFromArgs(call)
			var spec *schedule.ScheduleSpec
			if hasScheduleSpecArgs(call.Args) {
				tmp := schedule.ApplyDraftFields(schedule.CreateInput{Title: "tmp", Prompt: "tmp", DailyAt: call.Args["daily_at"]}, map[string]string{})
				tmp.DailyAt = input.DailyAt
				tmp.WeeklyAt = input.WeeklyAt
				tmp.Weekday = input.Weekday
				tmp.Weekdays = input.Weekdays
				tmp.MonthlyAt = input.MonthlyAt
				tmp.MonthlyDay = input.MonthlyDay
				tmp.Interval = input.Interval
				built, err := buildScheduleSpecForTool(tmp)
				if err != nil {
					return ErrorResult(err.Error())
				}
				spec = &built
			}
			task, path, err := schedule.NewStore(call.Context.Home).Update(id, schedule.UpdateInput{
				Title:    call.Args["title"],
				Prompt:   call.Args["prompt"],
				AgentID:  call.Args["agent_id"],
				Schedule: spec,
			})
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{OK: true, Output: renderScheduleTaskOutput("updated", task, path), Evidence: scheduleTaskEvidence("schedule_update", task, path)}
		},
	}
}

func ScheduleDelete() Definition {
	return Definition{
		Name:        "schedule.delete",
		Description: "Delete a user scheduled task. This destructive action requires runtime confirmation.",
		Metadata: Metadata{
			Purpose:           "delete a schedule task",
			WhenToUse:         []string{"remove recurring schedule"},
			RequiredArgs:      []string{"id"},
			OutputContract:    []string{"task id", "path"},
			AcceptanceSpecRef: "schedule.delete/default",
			AcceptanceMode:    AcceptanceCodeOnly,
			ParallelMode:      ParallelForbid,
			ResourceScope:     "schedule:task",
			ReusePolicy:       ReuseNever,
		},
		Risk:       RiskGuardedMutation,
		ArgsSchema: map[string]string{"id": "schedule id"},
		Run: func(ctx context.Context, call Call) Result {
			id := strings.TrimSpace(call.Args["id"])
			if id == "" {
				return ErrorResult("id is required")
			}
			path, err := schedule.NewStore(call.Context.Home).Delete(id)
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{OK: true, Output: "Deleted schedule task " + id + "\nPath: " + path, Evidence: map[string]any{"kind": "schedule_delete", "task_id": id, "path": path}}
		},
	}
}

func scheduleInputFromArgs(call Call) schedule.CreateInput {
	return schedule.CreateInput{
		ID:           call.Args["id"],
		Title:        call.Args["title"],
		Prompt:       call.Args["prompt"],
		AgentID:      firstNonEmpty(call.Args["agent_id"], "main"),
		RunAt:        call.Args["run_at"],
		DailyAt:      call.Args["daily_at"],
		WeeklyAt:     call.Args["weekly_at"],
		Weekday:      call.Args["weekday"],
		Weekdays:     splitCommaArg(call.Args["weekdays"]),
		MonthlyAt:    call.Args["monthly_at"],
		MonthlyDay:   parsePositiveArg(call.Args["monthly_day"], 0),
		Interval:     call.Args["interval"],
		Channel:      firstNonEmpty(call.Args["channel"], "cli"),
		ThreadID:     call.Args["thread_id"],
		UserID:       call.Args["user_id"],
		DeliveryMode: firstNonEmpty(call.Args["delivery_mode"], "artifact"),
		DeliveryPath: call.Args["delivery_path"],
	}
}

func hasScheduleSpecArgs(args map[string]string) bool {
	for _, key := range []string{"run_at", "daily_at", "weekly_at", "weekday", "weekdays", "monthly_at", "monthly_day", "interval"} {
		if strings.TrimSpace(args[key]) != "" {
			return true
		}
	}
	return false
}

func splitCommaArg(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	var out []string
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func buildScheduleSpecForTool(input schedule.CreateInput) (schedule.ScheduleSpec, error) {
	switch {
	case strings.TrimSpace(input.RunAt) != "":
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(input.RunAt)); err != nil {
			return schedule.ScheduleSpec{}, fmt.Errorf("schedule.run_at must be RFC3339")
		}
		return schedule.ScheduleSpec{Kind: "once", RunAt: strings.TrimSpace(input.RunAt)}, nil
	case strings.TrimSpace(input.Interval) != "":
		if _, err := time.ParseDuration(strings.TrimSpace(input.Interval)); err != nil {
			return schedule.ScheduleSpec{}, fmt.Errorf("schedule.interval must be a duration")
		}
		return schedule.ScheduleSpec{Kind: "interval", Interval: strings.TrimSpace(input.Interval)}, nil
	case strings.TrimSpace(input.MonthlyAt) != "" || input.MonthlyDay > 0:
		if input.MonthlyDay < 1 || input.MonthlyDay > 31 {
			return schedule.ScheduleSpec{}, fmt.Errorf("schedule.monthly_day must be between 1 and 31")
		}
		return schedule.ScheduleSpec{Kind: "monthly", MonthlyAt: firstNonEmpty(input.MonthlyAt, input.DailyAt, "09:00"), MonthlyDay: input.MonthlyDay}, nil
	case strings.TrimSpace(input.WeeklyAt) != "" || strings.TrimSpace(input.Weekday) != "" || len(input.Weekdays) > 0:
		weekdays := input.Weekdays
		if len(weekdays) == 0 && strings.TrimSpace(input.Weekday) != "" {
			weekdays = []string{strings.TrimSpace(input.Weekday)}
		}
		return schedule.ScheduleSpec{Kind: "weekly", WeeklyAt: firstNonEmpty(input.WeeklyAt, input.DailyAt, "09:00"), Weekday: input.Weekday, Weekdays: weekdays}, nil
	default:
		return schedule.ScheduleSpec{Kind: "daily", DailyAt: firstNonEmpty(input.DailyAt, "09:00")}, nil
	}
}

func renderScheduleTaskOutput(verb string, task schedule.Task, path string) string {
	return strings.Join([]string{
		"Schedule task " + verb + ": " + task.Title,
		"ID: " + task.ID,
		"Status: " + task.Status,
		"Schedule: " + schedule.Summary(task.Schedule),
		"Prompt: " + task.Prompt,
		"Path: " + path,
	}, "\n")
}

func renderScheduleList(tasks []schedule.Task) string {
	if len(tasks) == 0 {
		return "No schedule tasks found."
	}
	lines := []string{"Schedule tasks:"}
	for _, task := range tasks {
		lines = append(lines, fmt.Sprintf("- %s [%s] %s: %s", task.ID, task.Status, schedule.Summary(task.Schedule), task.Title))
	}
	return strings.Join(lines, "\n")
}

func scheduleTaskEvidence(kind string, task schedule.Task, path string) map[string]any {
	return map[string]any{
		"kind":     kind,
		"task_id":  task.ID,
		"title":    task.Title,
		"status":   task.Status,
		"schedule": schedule.Summary(task.Schedule),
		"path":     path,
	}
}

func TimeNow() Definition {
	return Definition{
		Name:        "time.now",
		Description: "Return current local time, date, and timezone.",
		Metadata: Metadata{
			Purpose:        "get current date and time",
			WhenToUse:      []string{"current time", "today", "timezone-sensitive task"},
			OutputContract: []string{"timezone", "unix timestamp"},
			AcceptanceMode: AcceptanceCodeOnly,
			ParallelMode:   ParallelReadOnlyOK,
			ResourceScope:  "system:time",
			ReusePolicy:    ReuseNever,
		},
		Risk:       RiskSafeRead,
		ArgsSchema: map[string]string{"timezone": "optional IANA timezone, defaults to local"},
		Run: func(ctx context.Context, call Call) Result {
			loc := time.Local
			if tz := strings.TrimSpace(call.Args["timezone"]); tz != "" {
				if loaded, err := time.LoadLocation(tz); err == nil {
					loc = loaded
				}
			}
			now := time.Now().In(loc)
			return Result{OK: true, Output: now.Format(time.RFC3339), Evidence: map[string]any{
				"kind":     "time",
				"timezone": loc.String(),
				"unix":     now.Unix(),
			}}
		},
	}
}

func ConfigSummary() Definition {
	return Definition{
		Name:        "config.summary",
		Description: "Return safe summary of loaded Mateway configuration without secrets.",
		Metadata: Metadata{
			Purpose:        "inspect runtime configuration safely",
			WhenToUse:      []string{"configuration summary", "debug runtime config"},
			OutputContract: []string{"config summary text"},
			AcceptanceMode: AcceptanceCodeOnly,
			ParallelMode:   ParallelReadOnlyOK,
			ResourceScope:  "config:summary",
			ReusePolicy:    ReuseNever,
		},
		Risk:       RiskSafeRead,
		ArgsSchema: map[string]string{},
		Run: func(ctx context.Context, call Call) Result {
			return Result{OK: true, Output: call.Context.ConfigSummary, Evidence: map[string]any{"kind": "config_summary"}}
		},
	}
}

func MemorySearch() Definition {
	return Definition{
		Name:        "memory.search",
		Description: "Search reviewed long memory and return snippets with path and line evidence.",
		Metadata: Metadata{
			Purpose:            "search reviewed long memory",
			WhenToUse:          []string{"user preference lookup", "stable memory lookup"},
			WhenNotToUse:       []string{"raw session history"},
			RequiredArgs:       []string{"query"},
			OutputContract:     []string{"path", "line range", "result count"},
			AcceptanceSpecRef:  "memory.search/default",
			AcceptanceMode:     AcceptanceCodeOnly,
			SoftFailureSignals: []string{"no matching long memory found"},
			ParallelMode:       ParallelReadOnlyOK,
			ResourceScope:      "memory:query",
			ReusePolicy:        ReuseNever,
		},
		Risk: RiskSafeRead,
		ArgsSchema: map[string]string{
			"query":   "search query",
			"agent":   "optional agent id, defaults to main",
			"limit":   "optional result limit",
			"rebuild": "optional true to rebuild index before searching",
		},
		Run: func(ctx context.Context, call Call) Result {
			query := strings.TrimSpace(firstNonEmpty(call.Args["query"], call.Args["q"]))
			if query == "" {
				return ErrorResult("query is required")
			}
			store, err := memoryStoreFromToolContext(call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			if parseBoolArg(call.Args["rebuild"]) {
				if _, err := store.RebuildIndex(time.Now()); err != nil {
					return ErrorResult(err.Error())
				}
			}
			limit := parsePositiveArg(call.Args["limit"], 4)
			results, err := store.SearchLong(memory.SearchOptions{
				AgentID:      firstNonEmpty(call.Args["agent"], "main"),
				Query:        query,
				Limit:        limit,
				SnippetLimit: 600,
			})
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{
				OK:       true,
				Output:   renderMemorySearchOutput(query, results),
				Evidence: memorySearchEvidence(query, results),
			}
		},
	}
}

func MemoryList() Definition {
	return Definition{
		Name:        "memory.list",
		Description: "List inbox or long memory items by area and optional status/kind.",
		Metadata: Metadata{
			Purpose:            "inspect inbox or long memory items",
			WhenToUse:          []string{"review inbox proposals", "check pending memory items", "inspect long memory entries"},
			WhenNotToUse:       []string{"semantic long-memory lookup"},
			OutputContract:     []string{"item id", "status", "type", "updated date"},
			AcceptanceMode:     AcceptanceCodeOnly,
			SoftFailureSignals: []string{"no memory items found"},
			ParallelMode:       ParallelReadOnlyOK,
			ResourceScope:      "memory:list",
			ReusePolicy:        ReuseNever,
		},
		Risk: RiskSafeRead,
		ArgsSchema: map[string]string{
			"agent":    "optional agent id, defaults to main",
			"area":     "optional memory area: inbox or long",
			"status":   "optional filter such as proposed, active, rejected, or committed",
			"kind":     "optional type filter such as decision, playbook, preference, project, source, or skill_candidate",
			"tag":      "optional tag filter such as daily-distillation, auto-proposal, skill-candidate, or skill-improvement",
			"group_by": "optional grouping key: kind, origin, review, or target",
			"review":   "optional review filter for long memory recency: stale, soon, or fresh",
		},
		Run: func(ctx context.Context, call Call) Result {
			store, err := memoryStoreFromToolContext(call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			agentID := firstNonEmpty(call.Args["agent"], "main")
			area := firstNonEmpty(call.Args["area"], "inbox")
			status := strings.TrimSpace(call.Args["status"])
			kind := strings.TrimSpace(call.Args["kind"])
			tag := strings.TrimSpace(call.Args["tag"])
			groupBy := strings.TrimSpace(call.Args["group_by"])
			review := strings.TrimSpace(call.Args["review"])
			items, err := store.List(memory.ListOptions{AgentID: agentID, Area: area, Status: status, Kind: kind, Tag: tag})
			if err != nil {
				return ErrorResult(err.Error())
			}
			items = filterMemoryItemsByReviewStatus(firstNonEmpty(area, "inbox"), review, items, time.Now())
			return Result{
				OK:       true,
				Output:   renderMemoryListOutput(agentID, area, status, groupBy, review, items),
				Evidence: memoryListEvidence(agentID, area, status, items),
			}
		},
	}
}

func MemoryShow() Definition {
	return Definition{
		Name:        "memory.show",
		Description: "Show one memory item by proposal id or path.",
		Metadata: Metadata{
			Purpose:        "inspect one memory item in full",
			WhenToUse:      []string{"review one proposal before commit or reject"},
			OutputContract: []string{"item id", "path", "full text"},
			AcceptanceMode: AcceptanceCodeOnly,
			ParallelMode:   ParallelReadOnlyOK,
			ResourceScope:  "memory:item",
			ReusePolicy:    ReuseNever,
		},
		Risk: RiskSafeRead,
		ArgsSchema: map[string]string{
			"agent": "optional agent id, defaults to main",
			"id":    "proposal id or path",
			"path":  "proposal path; alias of id",
		},
		Run: func(ctx context.Context, call Call) Result {
			store, err := memoryStoreFromToolContext(call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			ref := strings.TrimSpace(firstNonEmpty(call.Args["id"], call.Args["path"], call.Args["proposal"]))
			if ref == "" {
				return ErrorResult("memory id or path is required")
			}
			result, err := store.Show(firstNonEmpty(call.Args["agent"], "main"), ref)
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{
				OK:       true,
				Output:   renderMemoryShowOutput(result),
				Evidence: map[string]any{"kind": "memory_show", "id": result.ID, "path": result.Path},
			}
		},
	}
}

func MemoryReview() Definition {
	return Definition{
		Name:        "memory.review",
		Description: "Show a review-focused long-memory checklist, prioritizing soon/stale entries.",
		Metadata: Metadata{
			Purpose:            "review long memory entries that may need re-validation",
			WhenToUse:          []string{"review stale long memory", "inspect long memory that may need refresh"},
			WhenNotToUse:       []string{"full-text memory inspection", "memory commit or reject"},
			OutputContract:     []string{"item id", "review status", "type", "updated date"},
			AcceptanceMode:     AcceptanceCodeOnly,
			SoftFailureSignals: []string{"no long memory items currently need review"},
			ParallelMode:       ParallelReadOnlyOK,
			ResourceScope:      "memory:review",
			ReusePolicy:        ReuseNever,
		},
		Risk: RiskSafeRead,
		ArgsSchema: map[string]string{
			"agent":    "optional agent id, defaults to main",
			"review":   "optional review filter: stale, soon, or all; default includes soon and stale",
			"kind":     "optional type filter such as decision, playbook, preference, or project",
			"target":   "optional recommended target filter",
			"proposal": "optional true to write the current review queue into inbox as a review proposal",
		},
		Run: func(ctx context.Context, call Call) Result {
			store, err := memoryStoreFromToolContext(call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			agentID := firstNonEmpty(call.Args["agent"], "main")
			items, err := store.List(memory.ListOptions{AgentID: agentID, Area: "long", Status: "active", Kind: strings.TrimSpace(call.Args["kind"])})
			if err != nil {
				return ErrorResult(err.Error())
			}
			review := strings.TrimSpace(call.Args["review"])
			target := strings.TrimSpace(call.Args["target"])
			items = filterMemoryItemsForReviewQueue(review, items, time.Now())
			items = filterMemoryItemsByTarget(target, items)
			if parseBoolArg(call.Args["proposal"]) {
				input, ok := memory.BuildLongMemoryReviewProposal(memory.ReviewProposalOptions{
					AgentID: agentID,
					Review:  review,
					Kind:    strings.TrimSpace(call.Args["kind"]),
					Target:  target,
					Items:   items,
					At:      time.Now(),
				})
				if !ok {
					return Result{OK: true, Output: "No long memory items currently need review, so no review proposal was written."}
				}
				result, err := store.Propose(input)
				if err != nil {
					return ErrorResult(err.Error())
				}
				return Result{
					OK:       true,
					Output:   fmt.Sprintf("Long memory review proposal written: %s", result.Path),
					Evidence: map[string]any{"kind": "memory_review_proposal", "path": result.Path, "item_count": len(items)},
				}
			}
			return Result{
				OK:       true,
				Output:   renderMemoryReviewOutput(agentID, review, target, items, time.Now()),
				Evidence: memoryListEvidence(agentID, "long", "active", items),
			}
		},
	}
}

func memorySearchEvidence(query string, results []memory.SearchResult) map[string]any {
	evidence := map[string]any{
		"kind":         "memory_search",
		"query":        query,
		"result_count": len(results),
	}
	if len(results) > 0 {
		evidence["path"] = results[0].Path
		evidence["start_line"] = results[0].StartLine
		evidence["end_line"] = results[0].EndLine
	}
	return evidence
}

func MemoryCommit() Definition {
	return Definition{
		Name:        "memory.commit",
		Description: "Promote one inbox memory proposal into long memory.",
		Metadata: Metadata{
			Purpose:            "commit a reviewed memory proposal into long memory",
			WhenToUse:          []string{"approve a stable memory proposal", "promote reviewed memory from inbox"},
			WhenNotToUse:       []string{"skill candidate promotion", "bulk delete via shell"},
			RequiredArgs:       []string{"proposal"},
			OutputContract:     []string{"source path", "target path"},
			AcceptanceMode:     AcceptanceCodeOnly,
			SoftFailureSignals: []string{"memory proposal path is required", "must have frontmatter with status: proposed"},
			ParallelMode:       ParallelForbid,
			ResourceScope:      "memory:mutation",
			ReusePolicy:        ReuseNever,
		},
		Risk: RiskGuardedMutation,
		ArgsSchema: map[string]string{
			"agent":    "optional agent id, defaults to main",
			"proposal": "proposal id or path",
			"title":    "optional title override for committed memory",
		},
		Run: func(ctx context.Context, call Call) Result {
			store, err := memoryStoreFromToolContext(call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			proposal := strings.TrimSpace(firstNonEmpty(call.Args["proposal"], call.Args["id"], call.Args["path"]))
			if proposal == "" {
				return ErrorResult("proposal is required")
			}
			shown, err := store.Show(firstNonEmpty(call.Args["agent"], "main"), proposal)
			if err != nil {
				return ErrorResult(err.Error())
			}
			parsed, err := memory.ParseMarkdownForTools(shown.Text)
			if err != nil {
				return ErrorResult(err.Error())
			}
			kind := strings.TrimSpace(parsed.Frontmatter.Type)
			target := promotionTargetHint(kind)
			if RequireConfirmForTool("memory.commit", map[string]string{"type": kind}) && !call.Confirmed {
				return ConfirmResult(memoryCommitConfirmPrompt(proposal, kind, target), map[string]any{
					"kind":        "memory_commit_confirm",
					"proposal":    proposal,
					"type":        kind,
					"target_hint": target,
				})
			}
			result, err := store.Commit(memory.CommitInput{
				AgentID:  firstNonEmpty(call.Args["agent"], "main"),
				Proposal: proposal,
				Title:    strings.TrimSpace(call.Args["title"]),
				At:       time.Now(),
			})
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{
				OK:       true,
				Output:   renderMemoryCommitOutput(result),
				Evidence: map[string]any{"kind": "memory_commit", "source_path": result.SourcePath, "target_path": result.TargetPath, "type": result.Type},
			}
		},
	}
}

func MemoryReject() Definition {
	return Definition{
		Name:        "memory.reject",
		Description: "Reject one inbox memory proposal without deleting the file.",
		Metadata: Metadata{
			Purpose:            "reject an inbox memory proposal safely",
			WhenToUse:          []string{"clear a bad proposal from inbox review", "mark unstable memory as rejected"},
			WhenNotToUse:       []string{"removing files with rm", "promoting approved memory"},
			RequiredArgs:       []string{"proposal"},
			OutputContract:     []string{"proposal path", "updated status"},
			AcceptanceMode:     AcceptanceCodeOnly,
			SoftFailureSignals: []string{"proposal is required", "only proposed memory items can be rejected"},
			ParallelMode:       ParallelForbid,
			ResourceScope:      "memory:mutation",
			ReusePolicy:        ReuseNever,
		},
		Risk: RiskGuardedMutation,
		ArgsSchema: map[string]string{
			"agent":    "optional agent id, defaults to main",
			"proposal": "proposal id or path",
			"reason":   "optional rejection reason",
		},
		Run: func(ctx context.Context, call Call) Result {
			store, err := memoryStoreFromToolContext(call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			proposal := strings.TrimSpace(firstNonEmpty(call.Args["proposal"], call.Args["id"], call.Args["path"]))
			if proposal == "" {
				return ErrorResult("proposal is required")
			}
			result, err := store.Reject(memory.RejectInput{
				AgentID:  firstNonEmpty(call.Args["agent"], "main"),
				Proposal: proposal,
				Reason:   strings.TrimSpace(call.Args["reason"]),
				At:       time.Now(),
			})
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{
				OK:       true,
				Output:   renderMemoryRejectOutput(result),
				Evidence: map[string]any{"kind": "memory_reject", "path": result.Path},
			}
		},
	}
}

func MemoryIndex() Definition {
	return Definition{
		Name:        "memory.index",
		Description: "Return a concise summary of the rebuildable memory index.",
		Metadata: Metadata{
			Purpose:           "inspect or rebuild memory index summary",
			WhenToUse:         []string{"memory index status", "memory index rebuild"},
			OutputContract:    []string{"index path", "entry count"},
			AcceptanceSpecRef: "memory.index/default",
			AcceptanceMode:    AcceptanceCodeOnly,
			ParallelMode:      ParallelReadOnlyOK,
			ResourceScope:     "memory:index",
			ReusePolicy:       ReuseNever,
		},
		Risk: RiskSafeRead,
		ArgsSchema: map[string]string{
			"rebuild": "optional true to rebuild index before reading",
		},
		Run: func(ctx context.Context, call Call) Result {
			store, err := memoryStoreFromToolContext(call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			var result memory.RebuildIndexResult
			if parseBoolArg(call.Args["rebuild"]) {
				result, err = store.RebuildIndex(time.Now())
				if err != nil {
					return ErrorResult(err.Error())
				}
			} else {
				result, err = store.ReadIndex()
				if err != nil {
					return ErrorResult(err.Error())
				}
			}
			return Result{
				OK:     true,
				Output: renderMemoryIndexOutput(result),
				Evidence: map[string]any{
					"kind":        "memory_index",
					"path":        result.Path,
					"entry_count": len(result.Index.Entries),
					"issue_count": result.Index.IssueCount,
				},
			}
		},
	}
}

func renderMemoryListOutput(agentID, area, status, groupBy, review string, items []memory.MemoryItem) string {
	if len(items) == 0 {
		return fmt.Sprintf("No memory items found for area=%s status=%s review=%s agent=%s.", firstNonEmpty(area, "inbox"), firstNonEmpty(status, "any"), firstNonEmpty(review, "any"), firstNonEmpty(agentID, "main"))
	}
	items = sortMemoryItemsForReview(items)
	lines := []string{fmt.Sprintf("Memory items for area=%s status=%s review=%s agent=%s:", firstNonEmpty(area, "inbox"), firstNonEmpty(status, "any"), firstNonEmpty(review, "any"), firstNonEmpty(agentID, "main"))}
	if groupBy == "kind" || groupBy == "origin" || groupBy == "review" || groupBy == "target" {
		current := ""
		for _, item := range items {
			group := memoryGroupValue(item, firstNonEmpty(area, "inbox"), groupBy, time.Now())
			if group != current {
				current = group
				lines = append(lines, fmt.Sprintf("[%s=%s]", groupBy, firstNonEmpty(current, "other")))
			}
			lines = append(lines, fmt.Sprintf("- %s\t%s\t%s\t%s\t%s%s%s%s", item.ID, item.Status, item.Kind, item.Updated, item.Title, memoryKindHint(item.Kind), memoryOriginHint(item), memoryReviewHint(firstNonEmpty(area, "inbox"), item, time.Now())))
		}
		return strings.Join(lines, "\n")
	}
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("- %s\t%s\t%s\t%s\t%s%s%s%s", item.ID, item.Status, item.Kind, item.Updated, item.Title, memoryKindHint(item.Kind), memoryOriginHint(item), memoryReviewHint(firstNonEmpty(area, "inbox"), item, time.Now())))
	}
	lines = append(lines, "")
	lines = append(lines, "Review tips:")
	lines = append(lines, "- `--tag daily-distillation` shows all daily distillation proposals")
	lines = append(lines, "- `--tag distill-decision|distill-playbook|distill-preference|distill-project` narrows distillation proposals by target type")
	if firstNonEmpty(area, "inbox") == "long" {
		lines = append(lines, "- `--review stale|soon|fresh` filters long memory by review age")
		lines = append(lines, "- `--group_by target` shows which long-memory targets are aging")
	}
	return strings.Join(lines, "\n")
}

func memoryGroupValue(item memory.MemoryItem, area, groupBy string, now time.Time) string {
	switch strings.TrimSpace(groupBy) {
	case "kind":
		return strings.TrimSpace(item.Kind)
	case "origin":
		return memoryOriginFromFrontmatter(item.Tags, item.Sources, item.Kind)
	case "review":
		if strings.TrimSpace(area) != "long" {
			return ""
		}
		return memoryReviewLabel(item.Updated, now)
	case "target":
		return promotionTargetHint(item.Kind)
	default:
		return ""
	}
}

func memoryListEvidence(agentID, area, status string, items []memory.MemoryItem) map[string]any {
	evidence := map[string]any{
		"kind":       "memory_list",
		"agent":      firstNonEmpty(agentID, "main"),
		"area":       firstNonEmpty(area, "inbox"),
		"status":     status,
		"item_count": len(items),
	}
	if len(items) > 0 {
		evidence["first_id"] = items[0].ID
		evidence["first_path"] = items[0].Path
	}
	return evidence
}

func renderMemoryShowOutput(result memory.ShowResult) string {
	summary := renderMemoryShowSummary(result.Text)
	if summary != "" {
		return fmt.Sprintf("Memory item: %s\nPath: %s\n%s\n\n%s", result.ID, result.Path, summary, result.Text)
	}
	return fmt.Sprintf("Memory item: %s\nPath: %s\n\n%s", result.ID, result.Path, result.Text)
}

func renderMemoryReviewOutput(agentID, review, target string, items []memory.MemoryItem, now time.Time) string {
	if len(items) == 0 {
		return fmt.Sprintf("No long memory items currently need review for agent=%s review=%s target=%s.", firstNonEmpty(agentID, "main"), firstNonEmpty(review, "soon_or_stale"), firstNonEmpty(target, "any"))
	}
	items = sortMemoryItemsForReviewQueue(items, now)
	lines := []string{fmt.Sprintf("Long memory review queue for agent=%s review=%s target=%s:", firstNonEmpty(agentID, "main"), firstNonEmpty(review, "soon_or_stale"), firstNonEmpty(target, "any"))}
	for _, item := range items {
		label := memoryReviewLabel(item.Updated, now)
		lines = append(lines, fmt.Sprintf("- %s\t%s\t%s\t%s\t%s%s", item.ID, item.Kind, item.Updated, label, item.Title, memoryKindHint(item.Kind)))
		if suggestion := memoryReviewSuggestion(label, item.Kind); suggestion != "" {
			lines = append(lines, "  - suggestion: "+suggestion)
		}
	}
	lines = append(lines, "")
	lines = append(lines, "Review tips:")
	lines = append(lines, "- Start with `review=stale` items before relying on them as default long memory")
	lines = append(lines, "- Use `mateway memory show <id-or-path>` to inspect one item in full")
	return strings.Join(lines, "\n")
}

func renderMemoryCommitOutput(result memory.CommitResult) string {
	if strings.TrimSpace(result.Type) != "" {
		target := promotionTargetHint(result.Type)
		if target != "" {
			return fmt.Sprintf("Memory committed as %s.\nRecommended target: %s\nSource: %s\nTarget: %s", result.Type, target, result.SourcePath, result.TargetPath)
		}
		return fmt.Sprintf("Memory committed as %s.\nSource: %s\nTarget: %s", result.Type, result.SourcePath, result.TargetPath)
	}
	return fmt.Sprintf("Memory committed.\nSource: %s\nTarget: %s", result.SourcePath, result.TargetPath)
}

func renderMemoryRejectOutput(result memory.RejectResult) string {
	return fmt.Sprintf("Memory proposal rejected: %s", result.Path)
}

func memoryCommitConfirmPrompt(proposal, kind, target string) string {
	text := "这条长期记忆提交可能会影响后续默认记忆注入，执行前需要你确认。\n\nProposal：" + proposal
	if strings.TrimSpace(kind) != "" {
		text += "\n类型：" + kind
	}
	if strings.TrimSpace(target) != "" {
		text += "\n推荐落点：" + target
	}
	return text + "\n\n回复“确认”继续执行，或回复“取消”放弃。"
}

func memoryKindHint(kind string) string {
	switch strings.TrimSpace(kind) {
	case "decision", "playbook", "preference", "project":
		return fmt.Sprintf("\ttarget=%s", kind)
	default:
		return ""
	}
}

func renderMemoryShowSummary(text string) string {
	parsed, err := memoryParseMarkdownSafe(text)
	if err != nil {
		return ""
	}
	parts := []string{}
	if kind := strings.TrimSpace(parsed.Frontmatter.Type); kind != "" {
		parts = append(parts, "Type: "+kind)
	}
	if confidence := strings.TrimSpace(parsed.Frontmatter.Confidence); confidence != "" {
		parts = append(parts, "Confidence: "+confidence)
	}
	if target := promotionTargetHint(strings.TrimSpace(parsed.Frontmatter.Type)); target != "" {
		parts = append(parts, "Recommended target: "+target)
	}
	if origin := memoryOriginFromFrontmatter(parsed.Frontmatter.Tags, parsed.Frontmatter.Sources, parsed.Frontmatter.Type); origin != "" {
		parts = append(parts, "Origin: "+origin)
	}
	if review := memoryReviewLabel(parsed.Frontmatter.UpdatedAt, time.Now()); review != "" {
		parts = append(parts, "Review: "+review)
		if tip := memoryReviewTip(review); tip != "" {
			parts = append(parts, "Review tip: "+tip)
		}
	}
	return strings.Join(parts, "\n")
}

func memoryOriginHint(item memory.MemoryItem) string {
	if origin := memoryOriginFromFrontmatter(item.Tags, item.Sources, item.Kind); origin != "" {
		return fmt.Sprintf("\torigin=%s", origin)
	}
	return ""
}

func memoryReviewHint(area string, item memory.MemoryItem, now time.Time) string {
	if strings.TrimSpace(area) != "long" {
		return ""
	}
	if review := memoryReviewLabel(item.Updated, now); review != "" {
		return fmt.Sprintf("\treview=%s", review)
	}
	return ""
}

func filterMemoryItemsByReviewStatus(area, review string, items []memory.MemoryItem, now time.Time) []memory.MemoryItem {
	if strings.TrimSpace(area) != "long" || strings.TrimSpace(review) == "" {
		return items
	}
	var out []memory.MemoryItem
	for _, item := range items {
		if strings.EqualFold(memoryReviewLabel(item.Updated, now), review) {
			out = append(out, item)
		}
	}
	return out
}

func filterMemoryItemsForReviewQueue(review string, items []memory.MemoryItem, now time.Time) []memory.MemoryItem {
	switch strings.TrimSpace(review) {
	case "all":
		return items
	case "stale", "soon", "fresh":
		return filterMemoryItemsByReviewStatus("long", review, items, now)
	default:
		var out []memory.MemoryItem
		for _, item := range items {
			label := memoryReviewLabel(item.Updated, now)
			if label == "stale" || label == "soon" {
				out = append(out, item)
			}
		}
		return out
	}
}

func filterMemoryItemsByTarget(target string, items []memory.MemoryItem) []memory.MemoryItem {
	target = strings.TrimSpace(target)
	if target == "" {
		return items
	}
	var out []memory.MemoryItem
	for _, item := range items {
		if strings.EqualFold(promotionTargetHint(item.Kind), target) {
			out = append(out, item)
		}
	}
	return out
}

func sortMemoryItemsForReviewQueue(items []memory.MemoryItem, now time.Time) []memory.MemoryItem {
	items = append([]memory.MemoryItem(nil), items...)
	sort.SliceStable(items, func(i, j int) bool {
		li := memoryReviewPriority(memoryReviewLabel(items[i].Updated, now))
		lj := memoryReviewPriority(memoryReviewLabel(items[j].Updated, now))
		if li != lj {
			return li > lj
		}
		if items[i].Updated != items[j].Updated {
			return items[i].Updated < items[j].Updated
		}
		return items[i].Title < items[j].Title
	})
	return items
}

func memoryReviewPriority(label string) int {
	switch strings.TrimSpace(label) {
	case "stale":
		return 3
	case "soon":
		return 2
	case "fresh":
		return 1
	default:
		return 0
	}
}

func memoryReviewSuggestion(review, kind string) string {
	target := promotionTargetHint(kind)
	switch strings.TrimSpace(review) {
	case "stale":
		return "re-validate this " + firstNonEmpty(target, kind, "memory") + " before relying on it in new tasks"
	case "soon":
		return "schedule a quick review if this " + firstNonEmpty(target, kind, "memory") + " still affects active work"
	default:
		return ""
	}
}

func memoryReviewLabel(updated string, now time.Time) string {
	updated = strings.TrimSpace(updated)
	if updated == "" {
		return ""
	}
	day, err := time.Parse("2006-01-02", updated)
	if err != nil {
		return ""
	}
	ageDays := int(now.Sub(day).Hours() / 24)
	switch {
	case ageDays >= 30:
		return "stale"
	case ageDays >= 14:
		return "soon"
	default:
		return "fresh"
	}
}

func memoryReviewTip(review string) string {
	switch strings.TrimSpace(review) {
	case "stale":
		return "this entry appears stale and should be re-validated before relying on it as default long memory"
	case "soon":
		return "this entry is aging and should be reviewed soon if it still affects current decisions"
	case "fresh":
		return "this entry looks recent enough for normal review cadence"
	default:
		return ""
	}
}

func memoryOriginFromFrontmatter(tags, sources []string, kind string) string {
	switch strings.TrimSpace(kind) {
	case "skill_candidate":
		return "skill_candidate"
	case "skill_improvement":
		return "skill_improvement"
	}
	for _, tag := range tags {
		switch strings.TrimSpace(tag) {
		case "daily-distillation":
			return "daily_distillation"
		case "auto-proposal":
			return "task_auto_proposal"
		}
	}
	for _, source := range sources {
		if strings.EqualFold(strings.TrimSpace(source), "manual") {
			return "manual"
		}
	}
	return ""
}

func promotionTargetHint(kind string) string {
	switch strings.TrimSpace(kind) {
	case "decision":
		return "decision-style long memory"
	case "playbook":
		return "workflow/playbook-style long memory"
	case "preference":
		return "preference-style long memory"
	case "project":
		return "project fact/note-style long memory"
	default:
		return ""
	}
}

func sortMemoryItemsForReview(items []memory.MemoryItem) []memory.MemoryItem {
	out := append([]memory.MemoryItem(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		pi := memoryKindPriority(out[i].Kind)
		pj := memoryKindPriority(out[j].Kind)
		if pi != pj {
			return pi < pj
		}
		if out[i].Updated != out[j].Updated {
			return out[i].Updated > out[j].Updated
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func memoryKindPriority(kind string) int {
	switch strings.TrimSpace(kind) {
	case "decision", "playbook":
		return 0
	case "preference", "project":
		return 1
	default:
		return 2
	}
}

func memoryParseMarkdownSafe(text string) (memory.ParsedMarkdown, error) {
	return memory.ParseMarkdownForTools(text)
}

func WebSearch() Definition {
	return Definition{
		Name:        "web.search",
		Description: "Search the web with local cache, provider order, and budget-aware Tavily fallback.",
		Metadata: Metadata{
			Purpose:            "discover public web information and relevant source URLs",
			WhenToUse:          []string{"web lookup", "latest information", "find public documentation or source URLs"},
			WhenNotToUse:       []string{"local file search", "project inspection", "reading a known URL", "software-specific source discovery that fits software.search better"},
			RequiredArgs:       []string{"query"},
			OutputContract:     []string{"query", "provider", "result count", "result URL summary"},
			AcceptanceSpecRef:  "web.search/default",
			AcceptanceMode:     AcceptanceCodeLLM,
			SoftFailureSignals: []string{"no results"},
			ParallelMode:       ParallelReadOnlyOK,
			ResourceScope:      "web:query",
			ReusePolicy:        ReuseNever,
		},
		Risk: RiskSafeRead,
		ArgsSchema: map[string]string{
			"query":       "search query",
			"max_results": "optional max results",
			"freshness":   "optional: fresh/current to prefer fresh provider results over cache",
			"provider":    "optional provider override: cache, duckduckgo, tavily",
		},
		Run: func(ctx context.Context, call Call) Result {
			query := strings.TrimSpace(firstNonEmpty(call.Args["query"], call.Args["q"]))
			if query == "" {
				return ErrorResult("query is required")
			}
			return runWebSearch(ctx, call.Context.Search, query, call.Args)
		},
	}
}

func WebFetch() Definition {
	return Definition{
		Name:        "web.fetch",
		Description: "Fetch one known URL and return title, text preview, URL, status, and source evidence. Use this when a URL is already known instead of searching again.",
		Metadata: Metadata{
			Purpose:            "read one known web page or document source",
			WhenToUse:          []string{"known URL", "read linked page", "verify source page or upstream README"},
			WhenNotToUse:       []string{"discovering unknown sources", "broad multi-source latest-info lookup"},
			RequiredArgs:       []string{"url"},
			OutputContract:     []string{"url", "status", "title", "text preview", "source page evidence"},
			AcceptanceSpecRef:  "web.fetch/default",
			AcceptanceMode:     AcceptanceCodeOnly,
			SoftFailureSignals: []string{"unsupported url scheme", "status="},
			ParallelMode:       ParallelReadOnlyOK,
			ResourceScope:      "web:url",
			ReusePolicy:        ReuseNever,
		},
		Risk: RiskSafeRead,
		ArgsSchema: map[string]string{
			"url":     "URL to fetch",
			"timeout": "optional timeout seconds, max 30",
		},
		Run: func(ctx context.Context, call Call) Result {
			rawURL := strings.TrimSpace(call.Args["url"])
			if rawURL == "" {
				return ErrorResult("url is required")
			}
			timeout := parsePositiveArg(call.Args["timeout"], 8)
			if timeout > 30 {
				timeout = 30
			}
			return webFetchURL(ctx, rawURL, time.Duration(timeout)*time.Second)
		},
	}
}

func FileRead() Definition {
	return Definition{
		Name:        "file.read",
		Description: "Read a text file under the project root or Mateway workspace.",
		Metadata: Metadata{
			Purpose:           "read one text file exactly",
			WhenToUse:         []string{"need exact file contents", "need full file text", "need precise lines or bytes"},
			WhenNotToUse:      []string{"directory overview", "repository map", "quick one-file summary", "writing files"},
			RequiredArgs:      []string{"path"},
			OutputContract:    []string{"file path", "line range", "bytes"},
			AcceptanceSpecRef: "file.read/default",
			AcceptanceMode:    AcceptanceCodeOnly,
			ParallelMode:      ParallelReadOnlyOK,
			ResourceScope:     "filesystem:path",
			ReusePolicy:       ReuseNever,
		},
		Risk:       RiskSafeRead,
		ArgsSchema: map[string]string{"path": "file path"},
		Run: func(ctx context.Context, call Call) Result {
			path, err := ResolveAllowedPath(call.Args["path"], call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return ErrorResult(err.Error())
			}
			return Result{OK: true, Output: Truncate(string(data), DefaultOutputLimit), Evidence: map[string]any{
				"kind":       "file_read",
				"path":       path,
				"bytes":      len(data),
				"start_line": 1,
				"end_line":   countTextLines(string(data)),
			}}
		},
	}
}

func FileWrite() Definition {
	return Definition{
		Name:        "file.write",
		Description: "Write a text file under allowed roots.",
		Metadata: Metadata{
			Purpose:           "write text content to file",
			WhenToUse:         []string{"create file", "replace file content"},
			WhenNotToUse:      []string{"small in-place edit"},
			RequiredArgs:      []string{"path", "content"},
			OutputContract:    []string{"file path", "bytes written"},
			AcceptanceSpecRef: "file.write/default",
			AcceptanceMode:    AcceptanceCodeOnly,
			ParallelMode:      ParallelForbid,
			ResourceScope:     "filesystem:path",
			ReusePolicy:       ReuseNever,
		},
		Risk:       RiskGuardedMutation,
		ArgsSchema: map[string]string{"path": "file path", "content": "new file content"},
		Run: func(ctx context.Context, call Call) Result {
			path, err := ResolveAllowedPath(call.Args["path"], call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			content := call.Args["content"]
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return ErrorResult(err.Error())
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return ErrorResult(err.Error())
			}
			return Result{OK: true, Output: "wrote " + path, Evidence: map[string]any{"kind": "file_write", "path": path, "bytes": len(content)}}
		},
	}
}

func FilePatch() Definition {
	return Definition{
		Name:        "file.patch",
		Description: "Patch a text file by replacing old text with new text, or appending content.",
		Metadata: Metadata{
			Purpose:            "apply targeted patch to one text file",
			WhenToUse:          []string{"in-place edit", "append content", "single replacement"},
			WhenNotToUse:       []string{"create brand-new file tree"},
			RequiredArgs:       []string{"path"},
			OutputContract:     []string{"file path", "diff summary"},
			AcceptanceSpecRef:  "file.patch/default",
			AcceptanceMode:     AcceptanceCodeLLM,
			SoftFailureSignals: []string{"old text not found", "old text is not unique"},
			ParallelMode:       ParallelForbid,
			ResourceScope:      "filesystem:path",
			ReusePolicy:        ReuseNever,
		},
		Risk: RiskGuardedMutation,
		ArgsSchema: map[string]string{
			"path":      "file path",
			"old":       "old text to replace",
			"new":       "new text",
			"append":    "content to append when old is empty",
			"confirmed": "true to apply",
		},
		Run: func(ctx context.Context, call Call) Result {
			path, err := ResolveAllowedPath(call.Args["path"], call.Context)
			if err != nil {
				return ErrorResult(err.Error())
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return ErrorResult(err.Error())
			}
			oldText := string(data)
			old := call.Args["old"]
			newText := oldText
			if old != "" {
				count := strings.Count(oldText, old)
				if count == 0 {
					return ErrorResult("old text not found")
				}
				if count > 1 {
					return ErrorResult("old text is not unique")
				}
				newText = strings.Replace(oldText, old, call.Args["new"], 1)
			} else if appendText := call.Args["append"]; appendText != "" {
				newText = oldText
				if !strings.HasSuffix(newText, "\n") {
					newText += "\n"
				}
				newText += appendText
				if !strings.HasSuffix(newText, "\n") {
					newText += "\n"
				}
			} else {
				return ErrorResult("old or append is required")
			}
			diff := simpleDiff(oldText, newText)
			if err := os.WriteFile(path, []byte(newText), 0o644); err != nil {
				return ErrorResult(err.Error())
			}
			return Result{OK: true, Output: "patched " + path + "\n\n" + Truncate(diff, 4000), Evidence: map[string]any{"kind": "file_patch", "path": path}}
		},
	}
}

func ShellRun() Definition {
	return Definition{
		Name:        "shell.run",
		Description: "Deprecated compatibility alias for terminal.run. Do not plan new work with this tool.",
		Metadata: Metadata{
			ReusePolicy: ReuseNever,
		},
		Risk:   RiskDangerous,
		Hidden: true,
		ArgsSchema: map[string]string{
			"command":   "shell command",
			"workdir":   "optional working directory",
			"confirmed": "true to run dangerous command",
		},
		Run: func(ctx context.Context, call Call) Result {
			command := strings.TrimSpace(call.Args["command"])
			if command == "" {
				return ErrorResult("command is required")
			}
			if IsDangerousCommand(command) && !call.Confirmed {
				return ConfirmResult("shell.run blocked pending confirmation for dangerous command:\n\n"+command, map[string]any{"kind": "command_confirm", "command": command})
			}
			run, err := executeLocalCommand(ctx, command, call.Context, call.Args["workdir"], 60*time.Second)
			if err != nil {
				return ErrorResult(err.Error())
			}
			return run.toResult("shell")
		},
	}
}

func TerminalRun() Definition {
	return Definition{
		Name:        "terminal.run",
		Description: "Run a local terminal command for safe diagnostics, CLI status checks, logs, tests, builds, and small scripts. Prefer this for checking local software such as gateway status: first verify the CLI exists with command -v, then run the read-only status command. Dangerous commands require confirmation.",
		Metadata: Metadata{
			Purpose:            "run local terminal command",
			WhenToUse:          []string{"diagnostics", "status checks", "tests", "builds", "small scripts", "read-only CLI verification"},
			WhenNotToUse:       []string{"file editing when dedicated tools exist", "repository overview better handled by project.index", "one-file reading better handled by file.read or file.summary"},
			RequiredArgs:       []string{"command"},
			OutputContract:     []string{"exit code", "stdout", "stderr", "timed_out"},
			AcceptanceSpecRef:  "terminal.run/diagnostic",
			AcceptanceMode:     AcceptanceCodeLLM,
			SoftFailureSignals: []string{"not found", "data not found", "no results", "permission denied", "unauthorized", "timed out"},
			ParallelMode:       ParallelReadOnlyOK,
			ResourceScope:      "terminal:command",
			ReusePolicy:        ReuseNever,
			RecoverHints:       []string{"retry with narrower command", "ask user when destructive command is needed"},
		},
		Risk: RiskDangerous,
		ArgsSchema: map[string]string{
			"command": "command to run",
			"workdir": "optional working directory within allowed roots",
			"timeout": "optional timeout seconds, max 300",
			"purpose": "short diagnostic purpose",
		},
		Run: func(ctx context.Context, call Call) Result {
			command := strings.TrimSpace(call.Args["command"])
			if command == "" {
				return ErrorResult("command is required")
			}
			if IsDangerousCommand(command) && !call.Confirmed {
				return ConfirmResult("terminal.run blocked pending confirmation for dangerous command:\n\n"+command, map[string]any{"kind": "command_confirm", "command": command})
			}
			run, err := executeLocalCommand(ctx, command, call.Context, call.Args["workdir"], parseTerminalTimeout(call.Args["timeout"], 60*time.Second))
			if err != nil {
				return ErrorResult(err.Error())
			}
			result := run.toResult("terminal")
			if purpose := strings.TrimSpace(call.Args["purpose"]); purpose != "" {
				result.Evidence["purpose"] = purpose
			}
			return result
		},
	}
}

func UserAsk() Definition {
	return Definition{
		Name:        "user.ask",
		Description: "Ask user for missing information.",
		Metadata: Metadata{
			ReusePolicy: ReuseNever,
		},
		Risk:       RiskSafeRead,
		ArgsSchema: map[string]string{"question": "question for user"},
		Run: func(ctx context.Context, call Call) Result {
			q := strings.TrimSpace(call.Args["question"])
			if q == "" {
				q = "I need more information to continue."
			}
			return Result{OK: false, Output: q, RequiresConfirm: true, ConfirmMessage: q, Evidence: map[string]any{"kind": "user_input_required"}}
		},
	}
}

func renderSkillSearchOutput(query string, items []skill.CatalogItem) string {
	if len(items) == 0 {
		return "No matching skills found for: " + query + "\nSearched priority catalogs: skills.sh, skillhub.cn, clawhub.ai, then fallback web search.\nTry broader capability phrases, for example: rewriting, tone, writing assistant, copy editing, humanize text."
	}
	lines := []string{"Skill search results for: " + query}
	for i, item := range items {
		status := "not installed"
		if item.Installed {
			status = "installed at " + item.InstallPath
		}
		desc := strings.TrimSpace(item.Description)
		if desc != "" {
			desc = "\n   " + Truncate(desc, 260)
		}
		lines = append(lines, fmt.Sprintf("%d. %s [%s]\n   source: %s\n   url: %s%s", i+1, item.Name, status, item.Source, item.URL, desc))
	}
	return strings.Join(lines, "\n\n")
}

func skillSearchEvidence(query string, items []skill.CatalogItem) map[string]any {
	evidence := map[string]any{"kind": "skill_search", "query": query, "result_count": len(items)}
	if len(items) > 0 {
		evidence["name"] = items[0].Name
		evidence["source"] = items[0].Source
		evidence["url"] = items[0].URL
		evidence["installed"] = items[0].Installed
	}
	return evidence
}

func renderSkillInstallPreview(ref, workspace string, items []skill.CatalogItem) string {
	if len(items) == 0 {
		return fmt.Sprintf("skill.install requires confirmation before writing files.\n\nRequested skill: %s\nTarget workspace: %s\n\nI will search the priority skill catalogs and install the best matching SKILL.md into workspace/skills after confirmation.", ref, workspace)
	}
	item := items[0]
	targetName := strings.ToLower(strings.TrimSpace(item.Name))
	if targetName == "" {
		targetName = ref
	}
	return fmt.Sprintf("skill.install requires confirmation before writing files.\n\nSkill: %s\nSource: %s\nURL: %s\nTarget workspace: %s\n\nConfirm to install this skill into workspace/skills.", item.Name, item.Source, item.URL, workspace)
}

func renderSkillInstallOutput(result skill.InstallResult) string {
	if result.AlreadyDone {
		return fmt.Sprintf("Skill already installed: %s\nPath: %s\n\nYou can now ask Mateway to use this skill on matching tasks.", result.Item.Name, result.TargetPath)
	}
	return fmt.Sprintf("Skill installed: %s\nSource: %s\nPath: %s\n\nYou can now test it with a matching request. For example, ask me to open a website, inspect a page, click a button, or take a screenshot.", result.Item.Name, result.Item.Source, result.TargetPath)
}

type softwareResult struct {
	Name        string
	FullName    string
	URL         string
	Description string
	Language    string
	Stars       int
	Forks       int
	UpdatedAt   string
	PushedAt    string
	License     string
	OwnerType   string
}

func githubSoftwareSearch(ctx context.Context, query string, limit int) ([]softwareResult, error) {
	if limit <= 0 {
		limit = 5
	}
	searches := softwareSearchQueries(query)
	seen := map[string]bool{}
	var out []softwareResult
	client := &http.Client{Timeout: 10 * time.Second}
	for _, q := range searches {
		u := "https://api.github.com/search/repositories?" + url.Values{
			"q":        {q},
			"per_page": {fmt.Sprint(limit)},
			"sort":     {"stars"},
			"order":    {"desc"},
		}.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("accept", "application/vnd.github+json")
		req.Header.Set("user-agent", "mateway-software-search/1.0")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			continue
		}
		var parsed struct {
			Items []struct {
				Name        string `json:"name"`
				FullName    string `json:"full_name"`
				HTMLURL     string `json:"html_url"`
				Description string `json:"description"`
				Language    string `json:"language"`
				Stars       int    `json:"stargazers_count"`
				Forks       int    `json:"forks_count"`
				UpdatedAt   string `json:"updated_at"`
				PushedAt    string `json:"pushed_at"`
				Owner       struct {
					Type string `json:"type"`
				} `json:"owner"`
				License *struct {
					SPDXID string `json:"spdx_id"`
					Name   string `json:"name"`
				} `json:"license"`
			} `json:"items"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			continue
		}
		for _, item := range parsed.Items {
			if seen[item.FullName] {
				continue
			}
			seen[item.FullName] = true
			license := ""
			if item.License != nil {
				license = firstNonEmpty(item.License.SPDXID, item.License.Name)
			}
			out = append(out, softwareResult{
				Name:        item.Name,
				FullName:    item.FullName,
				URL:         item.HTMLURL,
				Description: item.Description,
				Language:    item.Language,
				Stars:       item.Stars,
				Forks:       item.Forks,
				UpdatedAt:   item.UpdatedAt,
				PushedAt:    item.PushedAt,
				License:     license,
				OwnerType:   item.Owner.Type,
			})
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func softwareSearchQueries(query string) []string {
	trimmed := strings.TrimSpace(query)
	queries := []string{}
	if alias := normalizedSoftwareAlias(trimmed); alias != "" {
		queries = append(queries, alias)
		if !strings.EqualFold(alias, trimmed) {
			queries = append(queries, trimmed)
		}
	} else {
		queries = append(queries, trimmed)
	}
	queries = append(queries, softwareNameVariants(trimmed)...)
	queries = append(queries, softwareQueryCandidates(trimmed)...)
	lower := strings.ToLower(trimmed)
	if !strings.Contains(lower, "cli") {
		queries = append(queries, trimmed+" cli")
	}
	return uniqueNonEmptyStrings(queries)
}

func renderSoftwareSearchOutput(query string, results []softwareResult) string {
	lines := []string{"Software search results for: " + query, "Source quality hint: prefer official organization repositories, recent activity, clear license, and installation docs."}
	for i, item := range results {
		desc := strings.TrimSpace(item.Description)
		if desc != "" {
			desc = "\n   " + Truncate(desc, 320)
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n   url: %s\n   language: %s stars: %d forks: %d license: %s updated: %s pushed: %s owner: %s%s",
			i+1,
			firstNonEmpty(item.FullName, item.Name),
			item.URL,
			firstNonEmpty(item.Language, "unknown"),
			item.Stars,
			item.Forks,
			firstNonEmpty(item.License, "unknown"),
			item.UpdatedAt,
			item.PushedAt,
			firstNonEmpty(item.OwnerType, "unknown"),
			desc,
		))
	}
	return Truncate(strings.Join(lines, "\n\n"), DefaultOutputLimit)
}

func softwareSearchEvidence(query string, results []softwareResult) map[string]any {
	evidence := map[string]any{"kind": "software_search", "provider": "github", "query": query, "result_count": len(results)}
	if len(results) > 0 {
		evidence["name"] = results[0].FullName
		evidence["url"] = results[0].URL
		evidence["stars"] = results[0].Stars
		evidence["updated_at"] = results[0].UpdatedAt
	}
	return evidence
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func defaultVerifyCommand(executable string) string {
	key := strings.TrimSpace(executable)
	if key == "" {
		key = "software"
	}
	return "command -v " + shellQuote(key) + " && " + shellQuote(key) + " --version"
}

func renderSoftwareInstallPreview(name, method, installCommand, verifyCommand, sourceURL string) string {
	lines := []string{
		"software.install requires confirmation before installing.",
		"",
		"Software: " + firstNonEmpty(name, "software"),
	}
	if strings.TrimSpace(method) != "" {
		lines = append(lines, "Method: "+method)
	}
	if strings.TrimSpace(sourceURL) != "" {
		lines = append(lines, "Source: "+sourceURL)
	}
	lines = append(lines,
		"Install command: `"+installCommand+"`",
		"Verify command: `"+verifyCommand+"`",
		"",
		"Confirm once to install and verify.",
	)
	return strings.Join(lines, "\n")
}

func renderSoftwareInstallResult(name, executable, installCommand string, ok bool, exitCode int, output string, verifyCommand string, verifyOK bool, verifyExit int, verifyOutput string) string {
	commandName := firstNonEmpty(executable, canonicalCommandName(name))
	if ok && verifyOK {
		return fmt.Sprintf("安装完成：%s\n\n执行的安装命令：`%s`\n验证命令：`%s`\n验证结果：%s\n\n现在可以试试：\n- `%s --version`\n- `%s --help`", displaySoftwareName(name), installCommand, verifyCommand, oneLine(verifyOutput), commandName, commandName)
	}
	lines := []string{fmt.Sprintf("安装未完成：%s", displaySoftwareName(name))}
	lines = append(lines, fmt.Sprintf("安装命令：`%s` exit=%d\n%s", installCommand, exitCode, strings.TrimSpace(output)))
	lines = append(lines, fmt.Sprintf("验证命令：`%s` exit=%d\n%s", verifyCommand, verifyExit, strings.TrimSpace(verifyOutput)))
	if strings.Contains(strings.ToLower(output+verifyOutput), "device not configured") || strings.Contains(strings.ToLower(output+verifyOutput), "authentication") {
		lines = append(lines, "看起来是 Git/GitHub 认证问题。可以先检查本机的 `gh auth status`、`git config --global --get credential.helper`，或按 GitHub 提示完成浏览器/令牌认证。")
	}
	return strings.Join(lines, "\n\n")
}

func softwareInstallError(installOK, verifyOK bool, output, verifyOutput string) string {
	if installOK && verifyOK {
		return ""
	}
	return strings.TrimSpace(firstNonEmpty(output, verifyOutput, "software install failed"))
}

func displaySoftwareName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "software"
	}
	return name
}

func canonicalCommandName(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	key = strings.ReplaceAll(key, "_", "-")
	key = strings.Join(strings.Fields(key), " ")
	if strings.Contains(key, " cli") {
		key = strings.ReplaceAll(key, " cli", "-cli")
	}
	return key
}

func executableFromInstallCommand(command string) string {
	return ""
}

func normalizedSoftwareAlias(value string) string {
	key := strings.ToLower(strings.TrimSpace(value))
	key = strings.ReplaceAll(key, "_", "-")
	key = strings.Join(strings.Fields(key), " ")
	if key == "" {
		return ""
	}
	if strings.Contains(key, " cli") {
		return strings.ReplaceAll(key, " cli", "-cli")
	}
	return ""
}

func softwareNameVariants(value string) []string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return nil
	}
	lower := strings.ToLower(raw)
	if !looksLikeCompactCLIName(lower) {
		return nil
	}
	normalizedSpaces := strings.Join(strings.Fields(lower), " ")
	normalizedDashes := strings.ReplaceAll(normalizedSpaces, " ", "-")
	var out []string
	if normalizedSpaces != lower {
		out = append(out, normalizedSpaces)
	}
	if normalizedDashes != lower && normalizedDashes != normalizedSpaces {
		out = append(out, normalizedDashes)
	}
	if strings.HasSuffix(normalizedSpaces, "cli") {
		base := strings.TrimSpace(strings.TrimSuffix(normalizedSpaces, "cli"))
		if base != "" {
			out = append(out, base+" cli")
			out = append(out, strings.ReplaceAll(base, " ", "-")+"-cli")
		}
	}
	if strings.Contains(normalizedSpaces, "-cli") {
		out = append(out, strings.ReplaceAll(normalizedSpaces, "-cli", " cli"))
	}
	if strings.Contains(normalizedSpaces, " cli") {
		out = append(out, strings.ReplaceAll(normalizedSpaces, " cli", "-cli"))
	}
	return uniqueNonEmptyStrings(out)
}

func softwareQueryCandidates(query string) []string {
	tokens := softwareQueryTokens(query)
	if len(tokens) == 0 {
		return nil
	}
	var out []string
	for i, token := range tokens {
		alias := normalizedSoftwareAlias(token)
		if alias != "" {
			out = append(out, alias)
		}
		if strings.EqualFold(token, "cli") {
			if i > 0 {
				out = append(out, tokens[i-1]+" cli")
				out = append(out, tokens[i-1]+"-cli")
			}
			if i > 1 {
				out = append(out, tokens[i-2]+" cli")
				out = append(out, tokens[i-2]+"-cli")
				out = append(out, tokens[i-2]+" "+tokens[i-1]+" cli")
			}
		}
	}
	if len(tokens) >= 2 {
		for i := 0; i < len(tokens)-1; i++ {
			pair := tokens[i] + " " + tokens[i+1]
			if looksLikeCompactCLIName(pair) {
				out = append(out, softwareNameVariants(pair)...)
			}
		}
	}
	return uniqueNonEmptyStrings(out)
}

func softwareQueryTokens(query string) []string {
	lower := strings.ToLower(strings.TrimSpace(query))
	if lower == "" {
		return nil
	}
	replacer := strings.NewReplacer(
		"(", " ", ")", " ", "[", " ", "]", " ", "{", " ", "}", " ",
		",", " ", ".", " ", ":", " ", ";", " ", "，", " ", "。", " ", "：", " ",
		"！", " ", "？", " ", "\n", " ", "\t", " ",
	)
	parts := strings.Fields(replacer.Replace(lower))
	stop := map[string]struct{}{
		"tool": {}, "tools": {}, "github": {}, "install": {}, "installer": {}, "installation": {},
		"command": {}, "commands": {}, "line": {}, "line-tool": {}, "download": {}, "latest": {},
		"official": {}, "package": {}, "binary": {}, "commandline": {}, "command-line": {},
		"怎么样": {}, "看看": {}, "测试": {}, "一下": {}, "这个": {}, "详情": {}, "查看": {},
	}
	var out []string
	for _, part := range parts {
		if _, drop := stop[part]; drop {
			continue
		}
		out = append(out, part)
	}
	return out
}

func looksLikeCompactCLIName(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || len(value) > 32 {
		return false
	}
	hasASCII := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			hasASCII = true
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == ' ' || r == '.' || r == '/':
		default:
			return false
		}
	}
	return hasASCII && strings.Contains(value, "cli")
}

func runCommandWithTimeout(ctx context.Context, command, workdir string, timeout time.Duration) (bool, int, string) {
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "sh", "-lc", command)
	if strings.TrimSpace(workdir) != "" {
		cmd.Dir = workdir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	output := strings.TrimSpace(stdout.String())
	errText := strings.TrimSpace(stderr.String())
	if errText != "" {
		output = strings.TrimSpace(output + "\n" + errText)
	}
	if output == "" {
		output = fmt.Sprintf("command exited with code %d", exitCode)
	}
	return err == nil, exitCode, output
}

type localCommandRun struct {
	Command  string
	Workdir  string
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
	TimedOut bool
}

func executeLocalCommand(ctx context.Context, command string, toolCtx Context, rawWorkdir string, timeout time.Duration) (localCommandRun, error) {
	workdir := preferredCommandWorkdir(toolCtx)
	if raw := strings.TrimSpace(rawWorkdir); raw != "" {
		resolved, err := ResolveAllowedPath(raw, toolCtx)
		if err != nil {
			return localCommandRun{}, err
		}
		workdir = resolved
	}
	if strings.TrimSpace(workdir) == "" {
		workdir = "."
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "sh", "-lc", command)
	cmd.Dir = workdir
	cmd.Env = mergedCommandEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	return localCommandRun{
		Command:  command,
		Workdir:  workdir,
		Stdout:   strings.TrimSpace(stdout.String()),
		Stderr:   strings.TrimSpace(stderr.String()),
		ExitCode: exitCode,
		Err:      err,
		TimedOut: runCtx.Err() == context.DeadlineExceeded,
	}, nil
}

func preferredCommandWorkdir(toolCtx Context) string {
	for _, candidate := range []string{toolCtx.ProjectRoot, toolCtx.Workspace, toolCtx.Home} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "."
}

var mergedCommandEnv = func() []string {
	env := os.Environ()
	current := os.Getenv("PATH")
	additions := []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/homebrew/sbin", "/usr/local/sbin"}
	parts := []string{}
	seen := map[string]bool{}
	for _, item := range append(additions, strings.Split(current, ":")...) {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		parts = append(parts, item)
	}
	pathValue := "PATH=" + strings.Join(parts, ":")
	filtered := make([]string, 0, len(env)+1)
	for _, item := range env {
		if strings.HasPrefix(item, "PATH=") {
			continue
		}
		filtered = append(filtered, item)
	}
	filtered = append(filtered, pathValue)
	return filtered
}

func (r localCommandRun) toResult(kind string) Result {
	combined := strings.TrimSpace(r.Stdout)
	if r.Stderr != "" {
		combined = strings.TrimSpace(combined + "\n\nstderr:\n" + r.Stderr)
	}
	if combined == "" {
		combined = fmt.Sprintf("command exited with code %d", r.ExitCode)
	}
	if r.TimedOut {
		combined = strings.TrimSpace(combined + "\n\ncommand timed out")
	}
	evidence := map[string]any{
		"kind":      kind,
		"command":   r.Command,
		"workdir":   r.Workdir,
		"exit_code": r.ExitCode,
		"stdout":    Truncate(r.Stdout, 20000),
		"stderr":    Truncate(r.Stderr, 20000),
		"timed_out": r.TimedOut,
	}
	return Result{OK: r.Err == nil, Output: Truncate(combined, DefaultOutputLimit), Error: errorString(r.Err), Evidence: evidence}
}

func parseTerminalTimeout(raw string, fallback time.Duration) time.Duration {
	value := parsePositiveArg(raw, int(fallback/time.Second))
	if value <= 0 {
		value = int(fallback / time.Second)
	}
	if value > 300 {
		value = 300
	}
	return time.Duration(value) * time.Second
}

func shellQuote(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func tavilySearch(ctx context.Context, cfg SearchConfig, query string) Result {
	maxResults := cfg.TavilyMaxResults
	if maxResults <= 0 {
		maxResults = 5
	}
	baseURL := strings.TrimRight(firstNonEmpty(cfg.TavilyBaseURL, "https://api.tavily.com"), "/")
	body := map[string]any{
		"query":          query,
		"max_results":    maxResults,
		"search_depth":   firstNonEmpty(cfg.TavilySearchDepth, "advanced"),
		"topic":          firstNonEmpty(cfg.TavilyTopic, "general"),
		"include_answer": false,
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/search", bytes.NewReader(payload))
	if err != nil {
		return ErrorResult(err.Error())
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+cfg.TavilyAPIKey)
	client := &http.Client{Timeout: searchTimeout(cfg.TavilyTimeoutSeconds, 8)}
	resp, err := client.Do(req)
	if err != nil {
		return ErrorResult(err.Error())
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrorResult(fmt.Sprintf("tavily search failed status=%d body=%s", resp.StatusCode, Truncate(string(data), 1000)))
	}
	var parsed struct {
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ErrorResult(err.Error())
	}
	lines := []string{"Search results for: " + query, sourceQualityHint(query)}
	for i, item := range parsed.Results {
		lines = append(lines, fmt.Sprintf("%d. %s\n%s\n%s", i+1, item.Title, item.URL, item.Content))
	}
	return Result{OK: true, Output: Truncate(strings.Join(lines, "\n\n"), DefaultOutputLimit), Evidence: map[string]any{"kind": "web_search", "provider": "tavily", "query": query, "result_count": len(parsed.Results)}}
}

func runWebSearch(ctx context.Context, cfg SearchConfig, query string, args map[string]string) Result {
	order := providerOrderForSearch(cfg, args)
	var errors []string
	for _, provider := range order {
		switch provider {
		case "cache":
			if cache, ok := readWebSearchCache(cfg, query, searchFreshness(args)); ok {
				return cache
			}
		case "duckduckgo":
			if !cfg.DuckDuckGoEnabled {
				errors = append(errors, "duckduckgo disabled")
				continue
			}
			result := duckDuckGoSearch(ctx, cfg, query)
			if result.OK {
				if resultCount, ok := searchResultCount(result.Evidence); ok && resultCount == 0 {
					errors = append(errors, "duckduckgo: no results")
					continue
				}
				writeWebSearchCache(cfg, query, result)
				return result
			}
			errors = append(errors, "duckduckgo: "+firstNonEmpty(result.Error, result.Output))
		case "tavily":
			if !cfg.TavilyEnabled || strings.TrimSpace(cfg.TavilyAPIKey) == "" {
				errors = append(errors, "tavily disabled")
				continue
			}
			if !tavilyBudgetAvailable(cfg) {
				errors = append(errors, "tavily budget exhausted")
				continue
			}
			result := tavilySearch(ctx, cfg, query)
			if result.OK {
				if resultCount, ok := searchResultCount(result.Evidence); ok && resultCount == 0 {
					errors = append(errors, "tavily: no results")
					continue
				}
				recordTavilyUsage(cfg)
				writeWebSearchCache(cfg, query, result)
				return result
			}
			errors = append(errors, "tavily: "+firstNonEmpty(result.Error, result.Output))
		}
	}
	if len(errors) == 0 {
		return ErrorResult("web.search has no enabled provider")
	}
	return ErrorResult("web.search failed: " + strings.Join(errors, "; "))
}

func providerOrderForSearch(cfg SearchConfig, args map[string]string) []string {
	override := strings.ToLower(strings.TrimSpace(args["provider"]))
	if override != "" {
		return dedupeProviderOrder([]string{override})
	}
	order := append([]string(nil), cfg.ProviderOrder...)
	if len(order) == 0 {
		defaultTool := strings.TrimSpace(strings.ToLower(cfg.DefaultTool))
		switch defaultTool {
		case "tavily":
			order = []string{"cache", "tavily", "duckduckgo"}
		case "duckduckgo":
			order = []string{"cache", "duckduckgo", "tavily"}
		default:
			order = []string{"cache", "duckduckgo", "tavily"}
		}
	}
	if searchFreshness(args) {
		var fresh []string
		for _, item := range order {
			if strings.TrimSpace(strings.ToLower(item)) != "cache" {
				fresh = append(fresh, strings.TrimSpace(strings.ToLower(item)))
			}
		}
		fresh = append(fresh, "cache")
		return dedupeProviderOrder(fresh)
	}
	for i := range order {
		order[i] = strings.TrimSpace(strings.ToLower(order[i]))
	}
	return dedupeProviderOrder(order)
}

func searchResultCount(evidence map[string]any) (int, bool) {
	if evidence == nil {
		return 0, false
	}
	switch value := evidence["result_count"].(type) {
	case int:
		return value, true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

func appendMissingFallbackProviders(primary, configured []string) []string {
	order := append([]string(nil), primary...)
	if len(configured) == 0 {
		configured = []string{"cache", "duckduckgo", "tavily"}
	}
	for _, provider := range configured {
		order = append(order, provider)
	}
	return dedupeProviderOrder(order)
}

func dedupeProviderOrder(order []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, item := range order {
		item = strings.TrimSpace(strings.ToLower(item))
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func searchFreshness(args map[string]string) bool {
	text := strings.ToLower(strings.TrimSpace(firstNonEmpty(args["freshness"], args["fresh"])))
	return text == "fresh" || text == "current" || text == "latest" || text == "true" || text == "1"
}

func duckDuckGoSearch(ctx context.Context, cfg SearchConfig, query string) Result {
	maxResults := cfg.DuckDuckGoMaxResults
	if maxResults <= 0 {
		maxResults = 5
	}
	u := "https://api.duckduckgo.com/?" + url.Values{
		"q":             {query},
		"format":        {"json"},
		"no_html":       {"1"},
		"skip_disambig": {"1"},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ErrorResult(err.Error())
	}
	client := &http.Client{Timeout: searchTimeout(cfg.DuckDuckGoTimeoutSeconds, 4)}
	resp, err := client.Do(req)
	if err != nil {
		fallbackCtx, cancel := context.WithTimeout(context.Background(), searchTimeout(cfg.DuckDuckGoTimeoutSeconds, 4))
		defer cancel()
		if fallback := duckDuckGoHTMLSearch(fallbackCtx, query, maxResults); fallback.OK {
			return fallback
		}
		if github := githubSoftwareSearchFallback(fallbackCtx, query, maxResults); github.OK {
			return github
		}
		return ErrorResult(err.Error())
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	var parsed struct {
		AbstractText  string `json:"AbstractText"`
		AbstractURL   string `json:"AbstractURL"`
		RelatedTopics []struct {
			Text     string `json:"Text"`
			FirstURL string `json:"FirstURL"`
		} `json:"RelatedTopics"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		if fallback := duckDuckGoHTMLSearch(ctx, query, maxResults); fallback.OK {
			return fallback
		}
		return ErrorResult(err.Error())
	}
	lines := []string{"Search results for: " + query, sourceQualityHint(query)}
	if parsed.AbstractText != "" {
		lines = append(lines, parsed.AbstractText+"\n"+parsed.AbstractURL)
	}
	for _, item := range parsed.RelatedTopics {
		if len(lines) > maxResults {
			break
		}
		if item.Text != "" {
			lines = append(lines, item.Text+"\n"+item.FirstURL)
		}
	}
	return Result{OK: true, Output: Truncate(strings.Join(lines, "\n\n"), DefaultOutputLimit), Evidence: map[string]any{"kind": "web_search", "provider": "duckduckgo", "query": query, "result_count": len(lines) - 2}}
}

func duckDuckGoHTMLSearch(ctx context.Context, query string, maxResults int) Result {
	u := "https://duckduckgo.com/html/?" + url.Values{"q": {query}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ErrorResult(err.Error())
	}
	req.Header.Set("user-agent", "mateway-web-search/1.0")
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ErrorResult(err.Error())
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	links := extractDDGHTMLResults(string(data), maxResults)
	if len(links) == 0 {
		return ErrorResult("duckduckgo html returned no results")
	}
	lines := []string{"Search results for: " + query, sourceQualityHint(query)}
	for i, item := range links {
		lines = append(lines, fmt.Sprintf("%d. %s\n%s", i+1, item.Title, item.URL))
	}
	return Result{OK: true, Output: Truncate(strings.Join(lines, "\n\n"), DefaultOutputLimit), Evidence: map[string]any{"kind": "web_search", "provider": "duckduckgo_html", "query": query, "result_count": len(links)}}
}

type webSearchCacheEntry struct {
	Query     string         `json:"query"`
	Output    string         `json:"output"`
	Evidence  map[string]any `json:"evidence"`
	FetchedAt time.Time      `json:"fetched_at"`
}

func readWebSearchCache(cfg SearchConfig, query string, fresh bool) (Result, bool) {
	if !cfg.CacheEnabled || strings.TrimSpace(cfg.CacheDir) == "" {
		return Result{}, false
	}
	path := webSearchCachePath(cfg, query)
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, false
	}
	var entry webSearchCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Result{}, false
	}
	ttl := cfg.CacheTTLHours
	if fresh {
		ttl = cfg.FreshCacheTTLHours
	}
	if ttl <= 0 {
		ttl = 168
	}
	if time.Since(entry.FetchedAt) > time.Duration(ttl)*time.Hour {
		return Result{}, false
	}
	evidence := copyEvidence(entry.Evidence)
	if evidence == nil {
		evidence = map[string]any{}
	}
	evidence["cache_hit"] = true
	evidence["cache_path"] = path
	evidence["provider"] = firstNonEmpty(fmt.Sprint(evidence["provider"]), "cache")
	return Result{OK: true, Output: entry.Output, Evidence: evidence}, true
}

func writeWebSearchCache(cfg SearchConfig, query string, result Result) {
	if !cfg.CacheEnabled || strings.TrimSpace(cfg.CacheDir) == "" || !result.OK {
		return
	}
	entry := webSearchCacheEntry{
		Query:     query,
		Output:    result.Output,
		Evidence:  result.Evidence,
		FetchedAt: time.Now(),
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return
	}
	path := webSearchCachePath(cfg, query)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

func webSearchCachePath(cfg SearchConfig, query string) string {
	return filepath.Join(cfg.CacheDir, "search", hashString(strings.ToLower(strings.TrimSpace(query)))+".json")
}

func hashString(value string) string {
	sum := sha1.Sum([]byte(value))
	return fmt.Sprintf("%x", sum)
}

func copyEvidence(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func searchTimeout(seconds int, fallback int) time.Duration {
	if seconds <= 0 {
		seconds = fallback
	}
	if seconds > 60 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

func tavilyBudgetAvailable(cfg SearchConfig) bool {
	if cfg.TavilyDailyBudget <= 0 && cfg.TavilyMonthlyBudget <= 0 {
		return true
	}
	usage := readProviderUsage(cfg, "tavily")
	now := time.Now()
	if cfg.TavilyDailyBudget > 0 && usage.Day == now.Format("2006-01-02") && usage.DayCount >= cfg.TavilyDailyBudget {
		return false
	}
	if cfg.TavilyMonthlyBudget > 0 && usage.Month == now.Format("2006-01") && usage.MonthCount >= cfg.TavilyMonthlyBudget {
		return false
	}
	return true
}

func recordTavilyUsage(cfg SearchConfig) {
	if strings.TrimSpace(cfg.CacheDir) == "" {
		return
	}
	usage := readProviderUsage(cfg, "tavily")
	now := time.Now()
	day := now.Format("2006-01-02")
	month := now.Format("2006-01")
	if usage.Day != day {
		usage.Day = day
		usage.DayCount = 0
	}
	if usage.Month != month {
		usage.Month = month
		usage.MonthCount = 0
	}
	usage.DayCount++
	usage.MonthCount++
	writeProviderUsage(cfg, "tavily", usage)
}

type providerUsage struct {
	Day        string `json:"day"`
	DayCount   int    `json:"day_count"`
	Month      string `json:"month"`
	MonthCount int    `json:"month_count"`
}

func readProviderUsage(cfg SearchConfig, provider string) providerUsage {
	var usage providerUsage
	data, err := os.ReadFile(providerUsagePath(cfg, provider))
	if err != nil {
		return usage
	}
	_ = json.Unmarshal(data, &usage)
	return usage
}

func writeProviderUsage(cfg SearchConfig, provider string, usage providerUsage) {
	path := providerUsagePath(cfg, provider)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(usage, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

func providerUsagePath(cfg SearchConfig, provider string) string {
	return filepath.Join(cfg.CacheDir, "usage", provider+".json")
}

func webFetchURL(ctx context.Context, rawURL string, timeout time.Duration) Result {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ErrorResult(err.Error())
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ErrorResult("unsupported url scheme: " + parsed.Scheme)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ErrorResult(err.Error())
	}
	req.Header.Set("user-agent", "mateway-web-fetch/1.0")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return ErrorResult(err.Error())
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrorResult(fmt.Sprintf("web.fetch status=%d url=%s", resp.StatusCode, rawURL))
	}
	raw := string(data)
	title := htmlTitle(raw)
	text := htmlToText(raw)
	output := strings.TrimSpace(strings.Join([]string{
		"Fetched URL: " + rawURL,
		"Title: " + title,
		"Preview:\n" + Truncate(text, 3000),
	}, "\n\n"))
	return Result{OK: true, Output: Truncate(output, DefaultOutputLimit), Evidence: map[string]any{"kind": "web_fetch", "url": rawURL, "status": resp.StatusCode, "title": title, "bytes": len(data)}}
}

func htmlTitle(raw string) string {
	match := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindStringSubmatch(raw)
	if len(match) < 2 {
		return ""
	}
	return stripHTML(match[1])
}

func htmlToText(raw string) string {
	raw = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`).ReplaceAllString(raw, " ")
	raw = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`).ReplaceAllString(raw, " ")
	return stripHTML(raw)
}

type simpleSearchResult struct {
	Title string
	URL   string
}

var ddgResultPattern = regexp.MustCompile(`(?is)<a[^>]+class=["'][^"']*result__a[^"']*["'][^>]+href=["']([^"']+)["'][^>]*>(.*?)</a>`)

func extractDDGHTMLResults(raw string, maxResults int) []simpleSearchResult {
	if maxResults <= 0 {
		maxResults = 5
	}
	var out []simpleSearchResult
	for _, match := range ddgResultPattern.FindAllStringSubmatch(raw, -1) {
		if len(match) < 3 {
			continue
		}
		title := stripHTML(match[2])
		link := decodeDDGResultURL(match[1])
		if title == "" || link == "" {
			continue
		}
		out = append(out, simpleSearchResult{Title: title, URL: link})
		if len(out) >= maxResults {
			break
		}
	}
	return out
}

func decodeDDGResultURL(raw string) string {
	raw = html.UnescapeString(raw)
	u, err := url.Parse(raw)
	if err == nil {
		if target := u.Query().Get("uddg"); target != "" {
			if decoded, err := url.QueryUnescape(target); err == nil {
				return decoded
			}
			return target
		}
	}
	return raw
}

func stripHTML(raw string) string {
	text := regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(raw, " ")
	text = html.UnescapeString(text)
	return strings.Join(strings.Fields(text), " ")
}

func githubSoftwareSearchFallback(ctx context.Context, query string, maxResults int) Result {
	results, err := githubSoftwareSearch(ctx, query, maxResults)
	if err != nil || len(results) == 0 {
		return ErrorResult("github software fallback returned no results")
	}
	return Result{OK: true, Output: renderSoftwareSearchOutput(query, results), Evidence: softwareSearchEvidence(query, results)}
}

func sourceQualityHint(query string) string {
	if !looksTimeSensitiveSearch(query) {
		return "Source quality hint: classify sources as official/primary, authoritative media, secondary roundup, or unclear-date before using them."
	}
	return strings.Join([]string{
		"Source quality hint for fresh/current query:",
		"- Prefer official docs/blogs, official GitHub, release notes, changelogs, academic or standards sources.",
		"- Treat secondary roundups, SEO listicles, reposts, and unclear-date pages as weak evidence.",
		"- Do not present a claim as latest/current unless the source date or release context supports it.",
	}, "\n")
}

func looksTimeSensitiveSearch(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	cues := []string{
		"latest", "current", "recent", "official", "release", "changelog", "2026", "trend", "trends", "course", "courses",
	}
	cues = append(cues, textmatch.Terms("fresh_search_cues")...)
	for _, cue := range cues {
		if strings.Contains(q, cue) {
			return true
		}
	}
	return false
}

func memoryStoreFromToolContext(ctx Context) (memory.Store, error) {
	workspace := strings.TrimSpace(ctx.Workspace)
	if workspace == "" {
		return memory.Store{}, fmt.Errorf("workspace is required")
	}
	return memory.NewStore(workspace), nil
}

func renderMemorySearchOutput(query string, results []memory.SearchResult) string {
	lines := []string{"Memory search results for: " + query}
	if len(results) == 0 {
		lines = append(lines, "No matching long memory found.")
		return strings.Join(lines, "\n")
	}
	for i, result := range results {
		lines = append(lines, fmt.Sprintf("%d. %s\npath: %s\nlines: %d-%d\nscore: %d\n%s", i+1, firstNonEmpty(result.Title, result.ID), result.Path, result.StartLine, result.EndLine, result.Score, result.Snippet))
	}
	return Truncate(strings.Join(lines, "\n\n"), DefaultOutputLimit)
}

func renderMemoryIndexOutput(result memory.RebuildIndexResult) string {
	counts := map[string]int{}
	sourceCount := 0
	for _, entry := range result.Index.Entries {
		counts[firstNonEmpty(entry.Area, "unknown")]++
		sourceCount += len(entry.ParsedSources)
	}
	areas := sortedIntKeys(counts)
	lines := []string{
		"Memory index: " + result.Path,
		fmt.Sprintf("entries=%d issues=%d parsed_sources=%d built_at=%s", len(result.Index.Entries), result.Index.IssueCount, sourceCount, result.Index.BuiltAt.Format(time.RFC3339)),
	}
	for _, area := range areas {
		lines = append(lines, fmt.Sprintf("- %s: %d", area, counts[area]))
	}
	return strings.Join(lines, "\n")
}

func countTextLines(text string) int {
	if text == "" {
		return 0
	}
	return len(strings.Split(text, "\n"))
}

func parseBoolArg(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func parsePositiveArg(value string, fallback int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	n := 0
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return fallback
		}
		n = n*10 + int(ch-'0')
	}
	if n <= 0 {
		return fallback
	}
	return n
}

func simpleDiff(oldText, newText string) string {
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")
	return fmt.Sprintf("--- before\n+++ after\n-old lines: %d\n+new lines: %d\n\n--- before preview\n%s\n\n+++ after preview\n%s", len(oldLines), len(newLines), Truncate(oldText, 1800), Truncate(newText, 1800))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedIntKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
