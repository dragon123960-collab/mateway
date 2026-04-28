package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/dongping/mateway/internal/config"
	agentharness "github.com/dongping/mateway/internal/harness"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skills"
	"github.com/dongping/mateway/internal/textutil"
	"github.com/dongping/mateway/internal/tools"
)

var tuiInput io.Reader = os.Stdin

func runTUICommand(ctx context.Context, _ []string, stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	catalog, _, _, _, registry, runner := buildRuntime(cfg)
	sessionKey := "cli:local"
	printTUIWelcome(stdout, cfg)

	scanner := bufio.NewScanner(tuiInput)
	for {
		_, _ = fmt.Fprint(stdout, "\n› ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(stdout, "\n会话结束。")
			return nil
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "/") {
			handled, err := handleLocalSlashCommand(ctx, stdout, runner, registry, catalog, sessionKey, text)
			if err != nil {
				return err
			}
			if handled {
				if text == "/exit" || text == "/quit" {
					return nil
				}
				continue
			}
		}
		run, err := runner.Start(ctx, agentharness.Request{
			SessionKey: sessionKey,
			ThreadID:   sessionKey,
			Channel:    "cli",
			UserText:   text,
			Mode:       "chat",
		}, nil)
		if err != nil {
			_, _ = fmt.Fprintf(stdout, "运行失败: %v\n", err)
			continue
		}
		_, _ = fmt.Fprintln(stdout, "")
		_, _ = fmt.Fprintln(stdout, strings.TrimSpace(run.Result))
	}
}

func printTUIWelcome(stdout io.Writer, cfg config.Config) {
	_, _ = fmt.Fprintln(stdout, strings.TrimSpace(`
 __  __       _                           
|  \/  | __ _| |_ _____      ____ _ _   _ 
| |\/| |/ _`+"`"+` | __/ _ \ \ /\ / / _`+"`"+` | | | |
| |  | | (_| | ||  __/\ V  V / (_| | |_| |
|_|  |_|\__,_|\__\___| \_/\_/ \__,_|\__, |
                                    |___/
`))
	_, _ = fmt.Fprintln(stdout, "Local interactive session")
	_, _ = fmt.Fprintf(stdout, "workspace: %s\n", cfg.App.Workspace)
	_, _ = fmt.Fprintf(stdout, "model: %s\n", cfg.Models.Default)
	_, _ = fmt.Fprintln(stdout, "输入 /help 查看命令，输入 /exit 退出。")
}

func handleLocalSlashCommand(ctx context.Context, stdout io.Writer, runner *agentharness.Harness, registry *tools.Registry, catalog *skills.Catalog, sessionKey, text string) (bool, error) {
	switch {
	case text == "/help":
		_, _ = fmt.Fprintln(stdout, "可用命令:")
		printAnnotatedSlashCommands(stdout, []slashCommandHelp{
			{Command: "/help", Summary: "查看本地命令说明"},
			{Command: "/new", Summary: "重置当前本地 session 的历史与偏好"},
			{Command: "/skills", Summary: "列出 skills 目录里的技能清单"},
			{Command: "/tools", Summary: "列出当前 session 可见的工具与能力"},
			{Command: "/runs", Summary: "查看最近 run"},
			{Command: "/trace [run_id]", Summary: "查看执行轨迹"},
			{Command: "/learn [run_id]", Summary: "查看学习摘要"},
			{Command: "/learn_apply <run_id> [proposal_id]", Summary: "批准并落盘学习建议"},
			{Command: "/agent <name>", Summary: "切换当前 session 的 agent"},
			{Command: "/run <tool-or-skill> [json]", Summary: "执行可调用 tool 或 executable skill"},
			{Command: "/exit", Summary: "退出本地会话"},
		})
		return true, nil
	case text == "/exit" || text == "/quit":
		_, _ = fmt.Fprintln(stdout, "退出本地会话。")
		return true, nil
	case text == "/new":
		if runner.SessionBusy(sessionKey) {
			_, _ = fmt.Fprintln(stdout, "当前 session 正在处理中，请稍后再试 /new。")
			return true, nil
		}
		if err := runner.ResetSession(ctx, sessionKey); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintln(stdout, "已重置当前本地 session。")
		return true, nil
	case text == "/skills":
		return true, printLocalSkills(stdout, catalog)
	case text == "/tools":
		return true, printLocalVisibleTools(ctx, stdout, runner, sessionKey)
	case text == "/runs":
		return true, printLocalRuns(ctx, stdout, runner, sessionKey)
	case text == "/trace":
		_, _ = fmt.Fprintln(stdout, localTraceReply(ctx, runner, sessionKey, ""))
		return true, nil
	case strings.HasPrefix(text, "/trace "):
		_, _ = fmt.Fprintln(stdout, localTraceReply(ctx, runner, sessionKey, strings.TrimSpace(strings.TrimPrefix(text, "/trace "))))
		return true, nil
	case text == "/learn":
		_, _ = fmt.Fprintln(stdout, localLearnReply(ctx, runner, sessionKey, ""))
		return true, nil
	case strings.HasPrefix(text, "/learn "):
		_, _ = fmt.Fprintln(stdout, localLearnReply(ctx, runner, sessionKey, strings.TrimSpace(strings.TrimPrefix(text, "/learn "))))
		return true, nil
	case strings.HasPrefix(text, "/learn_apply"):
		parts := strings.Fields(text)
		if len(parts) < 2 {
			_, _ = fmt.Fprintln(stdout, "用法: /learn_apply <run-id> [proposal-id]")
			return true, nil
		}
		proposalID := ""
		if len(parts) > 2 {
			proposalID = parts[2]
		}
		applied, err := runner.ApplyLearningProposal(ctx, parts[1], proposalID, sessionKey)
		if err != nil {
			return true, err
		}
		for _, proposal := range applied {
			_, _ = fmt.Fprintf(stdout, "已应用 %s [%s] -> %s\n", proposal.ID, proposal.Kind, appFirstNonEmpty(proposal.TargetPath, "-"))
		}
		return true, nil
	case strings.HasPrefix(text, "/agent "):
		name := strings.TrimSpace(strings.TrimPrefix(text, "/agent "))
		if name == "" {
			_, _ = fmt.Fprintln(stdout, "用法: /agent <name>")
			return true, nil
		}
		if err := runner.Sessions.SavePreferences(sessionKey, session.Preferences{AgentName: name}); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintf(stdout, "当前本地会话 agent 已切换为 `%s`\n", name)
		return true, nil
	case strings.HasPrefix(text, "/run "):
		return true, runLocalToolCommand(ctx, stdout, runner, registry, sessionKey, strings.TrimSpace(strings.TrimPrefix(text, "/run ")))
	}
	return false, nil
}

type slashCommandHelp struct {
	Command string
	Summary string
}

func printAnnotatedSlashCommands(stdout io.Writer, entries []slashCommandHelp) {
	width := 0
	for _, entry := range entries {
		if len(entry.Command) > width {
			width = len(entry.Command)
		}
	}
	for _, entry := range entries {
		_, _ = fmt.Fprintf(stdout, "%-*s  %s\n", width, entry.Command, entry.Summary)
	}
}

func printLocalSkills(stdout io.Writer, catalog *skills.Catalog) error {
	if catalog == nil {
		_, _ = fmt.Fprintln(stdout, "当前 skill catalog 未加载。")
		return nil
	}
	snapshot := catalog.Snapshot()
	if len(snapshot) == 0 {
		_, _ = fmt.Fprintln(stdout, "当前 skills 目录还没有技能。")
		return nil
	}
	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].Manifest.Name < snapshot[j].Manifest.Name })
	for _, skill := range snapshot {
		_, _ = fmt.Fprintf(stdout, "- %s\n", formatLocalSkillLine(skill))
	}
	return nil
}

func printLocalVisibleTools(ctx context.Context, stdout io.Writer, runner *agentharness.Harness, sessionKey string) error {
	prefs, _ := runner.Sessions.LoadPreferences(sessionKey)
	specs, err := runner.ListVisibleTools(ctx, tools.Scope{
		Channel:   "cli",
		ThreadID:  sessionKey,
		AgentName: appFirstNonEmpty(strings.TrimSpace(prefs.AgentName), "default"),
	})
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		_, _ = fmt.Fprintln(stdout, "当前没有可见工具。")
		return nil
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	for _, spec := range specs {
		_, _ = fmt.Fprintf(stdout, "- %s\n", formatLocalToolLine(spec))
	}
	return nil
}

func printLocalRuns(ctx context.Context, stdout io.Writer, runner *agentharness.Harness, sessionKey string) error {
	runs, err := runner.ListTaskRuns(ctx, sessionKey, 10)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		_, _ = fmt.Fprintln(stdout, "当前本地会话还没有 run。")
		return nil
	}
	for _, run := range runs {
		_, _ = fmt.Fprintf(stdout, "- %s  task=%s  %s\n", run.ID, appFirstNonEmpty(strings.TrimSpace(run.TaskID), "-"), agentharness.FormatTaskDigest(run))
	}
	return nil
}

func runLocalToolCommand(ctx context.Context, stdout io.Writer, runner *agentharness.Harness, registry *tools.Registry, sessionKey, raw string) error {
	name, payload := splitToolCommand(raw)
	if strings.TrimSpace(name) == "" {
		_, _ = fmt.Fprintln(stdout, "用法: /run <tool-or-skill> [json]")
		return nil
	}
	_ = registry
	args := map[string]any{}
	if strings.TrimSpace(payload) != "" {
		if err := json.Unmarshal([]byte(payload), &args); err != nil {
			return fmt.Errorf("解析工具参数失败: %w", err)
		}
	}
	run, err := runner.Start(ctx, agentharness.Request{
		SessionKey: sessionKey,
		ThreadID:   sessionKey,
		Channel:    "cli",
		Mode:       "tool",
		ToolName:   name,
		Arguments:  args,
	}, nil)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, strings.TrimSpace(run.Result))
	return nil
}

func splitToolCommand(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	parts := strings.SplitN(raw, " ", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func formatLocalSkillLine(skill skills.Skill) string {
	kind := "doc"
	if skill.Executable && strings.TrimSpace(string(skill.Manifest.Type)) != "" {
		kind = string(skill.Manifest.Type)
	}
	description := appFirstNonEmpty(strings.TrimSpace(skill.Manifest.Description), "No description.")
	return fmt.Sprintf("%s [%s] %s", skill.Manifest.Name, kind, description)
}

func formatLocalToolLine(spec tools.Spec) string {
	description := appFirstNonEmpty(strings.TrimSpace(spec.Description), "No description.")
	return fmt.Sprintf("%s [%s] %s", spec.Name, spec.Kind, description)
}

func localTraceReply(ctx context.Context, runner *agentharness.Harness, sessionKey, runID string) string {
	run, ok, errMsg := loadLocalTraceRun(ctx, runner, sessionKey, runID)
	if !ok {
		return errMsg
	}
	lines := []string{
		fmt.Sprintf("run `%s`", run.ID),
		fmt.Sprintf("status: %s", appFirstNonEmpty(run.Status, "-")),
		fmt.Sprintf("agent: %s", appFirstNonEmpty(run.AgentName, "-")),
		fmt.Sprintf("mode: %s", appFirstNonEmpty(run.Mode, "-")),
	}
	if run.Route != "" {
		lines = append(lines, fmt.Sprintf("route: %s", run.Route))
	}
	if run.ModelName != "" {
		lines = append(lines, fmt.Sprintf("model: %s", run.ModelName))
	}
	if run.Goal != "" {
		lines = append(lines, "goal: "+localTrimBlock(run.Goal))
	}
	if len(run.VisibleTools) > 0 {
		lines = append(lines, "visible_tools: "+strings.Join(run.VisibleTools, ", "))
	}
	if run.Error != "" {
		lines = append(lines, "error: "+localTrimBlock(run.Error))
	}
	if len(run.Steps) > 0 {
		lines = append(lines, "steps:")
		for _, step := range run.Steps {
			head := fmt.Sprintf("%d. %s %s", step.Index, step.Kind, step.Status)
			if step.ToolName != "" {
				head += " " + step.ToolName
			}
			lines = append(lines, head)
			if step.Input != "" {
				lines = append(lines, "in: "+localTrimBlock(step.Input))
			}
			if step.Output != "" {
				lines = append(lines, "out: "+localTrimBlock(step.Output))
			}
		}
	}
	if run.Result != "" {
		lines = append(lines, "result: "+localTrimBlock(run.Result))
	}
	return strings.Join(lines, "\n")
}

func localLearnReply(ctx context.Context, runner *agentharness.Harness, sessionKey, runID string) string {
	run, ok, errMsg := loadLocalTraceRun(ctx, runner, sessionKey, runID)
	if !ok {
		return errMsg
	}
	return agentharness.FormatLearnReport(run)
}

func loadLocalTraceRun(ctx context.Context, runner *agentharness.Harness, sessionKey, runID string) (agentharness.Run, bool, string) {
	if strings.TrimSpace(runID) != "" {
		run, ok := runner.GetRun(ctx, runID)
		if !ok {
			return agentharness.Run{}, false, fmt.Sprintf("没有找到 run `%s`。", runID)
		}
		return run, true, ""
	}
	runs, err := runner.ListTaskRuns(ctx, sessionKey, 1)
	if err != nil {
		return agentharness.Run{}, false, fmt.Sprintf("读取 trace 失败：%v", err)
	}
	if len(runs) == 0 {
		return agentharness.Run{}, false, "当前本地会话还没有可查看的 run。"
	}
	return runs[0], true, ""
}

func localFirstStepByKinds(steps []agentharness.RunStep, kinds ...string) *agentharness.RunStep {
	for _, kind := range kinds {
		for i := range steps {
			if strings.EqualFold(steps[i].Kind, kind) {
				return &steps[i]
			}
		}
	}
	return nil
}

func localCollectExecutionNarrative(steps []agentharness.RunStep) []string {
	lines := make([]string, 0, len(steps))
	for _, step := range steps {
		switch step.Kind {
		case "dev_plan":
			continue
		case "plan", "replan":
			lines = append(lines, fmt.Sprintf("- %s 产出计划：%s", appFirstNonEmpty(step.AgentName, step.Kind), localTrimBlock(step.Output)))
		case "tool_choice":
			lines = append(lines, fmt.Sprintf("- 选择工具 `%s`：%s", appFirstNonEmpty(step.ToolName, "-"), localTrimBlock(step.Output)))
		case "route_fallback":
			lines = append(lines, fmt.Sprintf("- 运行时降级：%s", localTrimBlock(step.Output)))
		case "tool":
			lines = append(lines, fmt.Sprintf("- 调用工具 `%s`，状态 `%s`。", appFirstNonEmpty(step.ToolName, "-"), step.Status))
		case "callback_model_start":
			lines = append(lines, fmt.Sprintf("- 模型节点开始：%s", localTrimBlock(appFirstNonEmpty(step.Output, step.Input))))
		case "callback_model_end":
			lines = append(lines, fmt.Sprintf("- 模型节点完成：%s", localTrimBlock(step.Output)))
		case "callback_tool_start":
			lines = append(lines, fmt.Sprintf("- 工具节点开始 `%s`：%s", appFirstNonEmpty(step.ToolName, "-"), localTrimBlock(step.Input)))
		case "callback_tool_end":
			lines = append(lines, fmt.Sprintf("- 工具节点完成 `%s`：%s", appFirstNonEmpty(step.ToolName, "-"), localTrimBlock(step.Output)))
		case "tool_result":
			lines = append(lines, fmt.Sprintf("- 工具 `%s` 返回：%s", appFirstNonEmpty(step.ToolName, "-"), localTrimBlock(step.Output)))
		case "learn_proposal":
			lines = append(lines, fmt.Sprintf("- 已生成学习草稿：%s", localTrimBlock(step.Output)))
		case "respond", "llm", "agent_message":
			lines = append(lines, fmt.Sprintf("- `%s` 输出：%s", appFirstNonEmpty(step.Kind, "respond"), localTrimBlock(step.Output)))
		}
	}
	return lines
}

func localCollectFallbackNarrative(steps []agentharness.RunStep, runError string) []string {
	lines := make([]string, 0)
	for _, step := range steps {
		if step.Kind == "route_fallback" {
			lines = append(lines, "- 路由已自动降级："+localTrimBlock(step.Output))
			continue
		}
		if step.Status == "failed" {
			lines = append(lines, fmt.Sprintf("- `%s` 失败：%s", appFirstNonEmpty(step.ToolName, step.Kind), localTrimBlock(step.Output)))
		}
	}
	if strings.TrimSpace(runError) != "" {
		lines = append(lines, "- 最终错误："+localTrimBlock(runError))
	}
	return lines
}

func localTrimBlock(value string) string {
	return textutil.CleanBlock(textutil.HumanizeRunError(value), 1000)
}
