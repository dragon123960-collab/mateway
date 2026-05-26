package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/heartbeat"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/observer"
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
)

type directMatewayCommand struct {
	Raw  string
	Args []string
}

func (l *AgentLoop) resolveMatewayDirectCommand() *Response {
	cmd, ok := l.detectDirectMatewayCommand()
	if !ok {
		return nil
	}
	if directMatewayCommandNeedsApproval(cmd) {
		return l.pendingMatewayDirectCommand(cmd)
	}
	text, title, handled := l.executeDirectMatewayCommand(cmd)
	if !handled {
		return nil
	}
	reply := l.runtime.sanitizeReply(channel.OutboundMessage{
		Channel:  l.state.message.Channel,
		ThreadID: l.state.message.ThreadID,
		Text:     directCommandReplyText(cmd, title, text),
		Style:    "reply",
		Title:    title,
	})
	resp := Response{Reply: reply, TraceID: l.state.traceID}
	l.runtime.Logger.Event("runtime.reply", map[string]any{
		"trace_id":     l.state.traceID,
		"failed":       false,
		"reply_chars":  len(reply.Text),
		"result_count": 0,
		"direct":       "mateway_command",
		"command":      cmd.Raw,
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Reply(l.state.traceID, reply.Text, false)
	}
	l.saveConversationOnly(resp)
	return &resp
}

func (l *AgentLoop) detectDirectMatewayCommand() (directMatewayCommand, bool) {
	if cmd, ok := parseDirectMatewayCommand(l.state.message.Text); ok {
		return cmd, true
	}
	return resolveReferencedMatewayCommand(l.state.message.Text, l.state.session)
}

func parseDirectMatewayCommand(text string) (directMatewayCommand, bool) {
	raw := strings.TrimSpace(text)
	raw = strings.Trim(raw, "`")
	if !strings.HasPrefix(raw, "mateway ") {
		return directMatewayCommand{}, false
	}
	fields := strings.Fields(raw)
	if len(fields) < 2 || fields[0] != "mateway" {
		return directMatewayCommand{}, false
	}
	return directMatewayCommand{Raw: raw, Args: fields[1:]}, true
}

func (l *AgentLoop) executeDirectMatewayCommand(cmd directMatewayCommand) (string, string, bool) {
	switch cmd.Args[0] {
	case "init", "doctor", "ask", "test", "eval", "feishu":
		return directTopLevelCLICommandNote(cmd), "Mateway command note", true
	case "memory":
		return l.executeDirectMemoryCommand(cmd)
	case "gateway":
		return l.executeDirectGatewayCommand(cmd)
	case "schedule":
		return l.executeDirectScheduleCommand(cmd)
	case "heartbeat":
		return l.executeDirectHeartbeatCommand(cmd)
	case "skill":
		return l.executeDirectSkillCommand(cmd)
	case "trace":
		return l.executeDirectTraceCommand(cmd)
	default:
		return fmt.Sprintf("unknown mateway command %q\n\n%s", cmd.Args[0], directMatewayHelpText()), "Mateway command error", true
	}
}

func directTopLevelCLICommandNote(cmd directMatewayCommand) string {
	switch cmd.Args[0] {
	case "ask":
		return "runtime 对话里不会嵌套执行 `" + cmd.Raw + "`。\n\n如果你想让当前 agent 处理一个任务，直接发送任务内容即可；如果你想测试 CLI 入口，请在本机终端运行：`" + cmd.Raw + "`"
	case "init":
		return "runtime 对话里不会执行 `" + cmd.Raw + "`。\n\n`mateway init` 会初始化本机 `~/.mateway` 配置、workspace、memory 和默认 skills。请在本机终端运行它。"
	case "doctor":
		return "runtime 对话里不会执行 `" + cmd.Raw + "`。\n\n`mateway doctor` 用于检查本机配置和工具状态。请在本机终端运行它。"
	case "test":
		return "runtime 对话里不会执行 `" + cmd.Raw + "`。\n\n`mateway test` 是本机 CLI 测试入口。请在本机终端运行它。"
	case "eval":
		return "runtime 对话里不会执行 `" + cmd.Raw + "`。\n\n`mateway eval` 是本机评测入口。请在本机终端运行它。"
	case "feishu":
		return "runtime 对话里不会执行 `" + cmd.Raw + "`。\n\n`mateway feishu` 只是兼容 shortcut；请在本机终端使用 `mateway gateway serve`。"
	default:
		return "runtime 对话里不会执行 `" + cmd.Raw + "`。请在本机终端运行。"
	}
}

func (l *AgentLoop) executeDirectGatewayCommand(cmd directMatewayCommand) (string, string, bool) {
	if len(cmd.Args) < 2 {
		return "用法：`mateway gateway <serve|start|restart|stop|status>`\n\n这类命令属于本机进程或 OS service 管理，请在本机终端执行。", "Mateway gateway help", true
	}
	switch cmd.Args[1] {
	case "serve", "start", "restart", "stop", "status":
		return "runtime 对话里暂不执行 `" + cmd.Raw + "`。\n\n`mateway gateway serve` 只是在前台运行 gateway，并不会安装开机自启动。`mateway gateway start/restart/stop/status` 只控制已经注册好的 OS service；如果需要登录或重启后自动启动，请先在系统里配置 LaunchAgent 或 systemd user unit。\n\n请在本机终端运行：`" + cmd.Raw + "`", "Mateway command note", true
	default:
		return fmt.Sprintf("unknown gateway command %q\n\n用法：`mateway gateway <serve|start|restart|stop|status>`", cmd.Args[1]), "Mateway gateway error", true
	}
}

func (l *AgentLoop) executeDirectScheduleCommand(cmd directMatewayCommand) (string, string, bool) {
	if len(cmd.Args) < 2 {
		return directScheduleHelpText(), "Mateway schedule help", true
	}
	store := schedule.NewStore(l.runtime.ToolCtx.Home)
	switch cmd.Args[1] {
	case "list":
		tasks, err := store.List()
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway schedule error", true
		}
		if len(tasks) == 0 {
			return "No schedule tasks found.", "Mateway schedule list", true
		}
		lines := make([]string, 0, len(tasks))
		for _, task := range tasks {
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s\t%s", task.ID, task.Status, task.AgentID, schedule.Summary(task.Schedule), task.Title))
		}
		return strings.Join(lines, "\n"), "Mateway schedule list", true
	case "proposals":
		status, err := parseDirectScheduleProposalStatusArgs(cmd.Args[2:])
		if err != nil {
			return err.Error(), "Mateway schedule error", true
		}
		items, err := store.ListProposals(status)
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway schedule error", true
		}
		if len(items) == 0 {
			return "No schedule proposals found.", "Mateway schedule proposals", true
		}
		lines := make([]string, 0, len(items))
		for _, item := range items {
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s", item.ID, item.Status, item.Schedule, item.Title))
		}
		return strings.Join(lines, "\n"), "Mateway schedule proposals", true
	case "show":
		id, err := parseDirectSingleIDArg(cmd.Args[2:], "usage: mateway schedule show <id>")
		if err != nil {
			return err.Error(), "Mateway schedule error", true
		}
		task, path, err := store.Show(id)
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway schedule error", true
		}
		if strings.TrimSpace(task.ID) == "" {
			return "执行失败：`invalid schedule task`", "Mateway schedule error", true
		}
		data, err := readFileForDirectCommand(path)
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway schedule error", true
		}
		return strings.TrimRight(data, "\n"), "Mateway schedule show", true
	case "due":
		tasks, err := store.Due(time.Now())
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway schedule error", true
		}
		if len(tasks) == 0 {
			return "No schedule tasks are due.", "Mateway schedule due", true
		}
		lines := make([]string, 0, len(tasks))
		for _, task := range tasks {
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s", task.ID, task.AgentID, schedule.Summary(task.Schedule), task.Title))
		}
		return strings.Join(lines, "\n"), "Mateway schedule due", true
	default:
		return fmt.Sprintf("unknown schedule command %q\n\n%s", cmd.Args[1], directScheduleHelpText()), "Mateway schedule error", true
	}
}

func (l *AgentLoop) executeDirectHeartbeatCommand(cmd directMatewayCommand) (string, string, bool) {
	if len(cmd.Args) < 2 {
		return "用法：`mateway heartbeat status`", "Mateway heartbeat help", true
	}
	if len(cmd.Args) != 2 || cmd.Args[1] != "status" {
		return fmt.Sprintf("unknown heartbeat command %q\n\n用法：`mateway heartbeat status`", strings.Join(cmd.Args[1:], " ")), "Mateway heartbeat error", true
	}
	runner := heartbeat.NewRunner(l.runtime.Config)
	state, path, err := runner.Status()
	if err != nil {
		return "执行失败：`" + err.Error() + "`", "Mateway heartbeat error", true
	}
	lines := []string{"Heartbeat state: " + path}
	if len(state.Jobs) == 0 {
		lines = append(lines, "No heartbeat jobs have run yet.")
		return strings.Join(lines, "\n"), "Mateway heartbeat status", true
	}
	for _, job := range state.Jobs {
		lines = append(lines, fmt.Sprintf("- agent=%s job=%s status=%s last_run_at=%s summary=%s", job.AgentID, job.Job, job.Status, job.LastRunAt.Format(time.RFC3339), job.Summary))
		if strings.TrimSpace(job.LastError) != "" {
			lines = append(lines, "  error="+job.LastError)
		}
	}
	return strings.Join(lines, "\n"), "Mateway heartbeat status", true
}

func (l *AgentLoop) executeDirectSkillCommand(cmd directMatewayCommand) (string, string, bool) {
	if len(cmd.Args) < 2 {
		return directSkillHelpText(), "Mateway skill help", true
	}
	switch cmd.Args[1] {
	case "list":
		defs, err := skill.ListInstalled(l.runtime.ToolCtx.Workspace)
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway skill error", true
		}
		if len(defs) == 0 {
			return "No installed skills found.", "Mateway skill list", true
		}
		lines := make([]string, 0, len(defs))
		for _, def := range defs {
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s", def.Name, def.Stage, def.Dir))
		}
		return strings.Join(lines, "\n"), "Mateway skill list", true
	case "search":
		if len(cmd.Args) < 3 {
			return "usage: mateway skill search <query>", "Mateway skill error", true
		}
		query := strings.Join(cmd.Args[2:], " ")
		items, err := skill.SearchCatalog(context.Background(), l.runtime.ToolCtx.Workspace, query, skill.CatalogSearchOptions{Limit: 8})
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway skill error", true
		}
		if len(items) == 0 {
			return fmt.Sprintf("No matching skills found for: %s", query), "Mateway skill search", true
		}
		lines := make([]string, 0, len(items))
		for _, item := range items {
			status := "not-installed"
			if item.Installed {
				status = "installed"
			}
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s", item.Name, item.Source, status, item.URL))
		}
		return strings.Join(lines, "\n"), "Mateway skill search", true
	case "promote":
		opts, err := parseDirectSkillPromoteArgs(cmd.Args[2:])
		if err != nil {
			return err.Error(), "Mateway skill error", true
		}
		store := l.runtime.Memory
		if strings.TrimSpace(store.Root) == "" {
			store = memory.NewStore(l.runtime.ToolCtx.Workspace)
		}
		result, err := store.PromoteSkillCandidate(memory.SkillPromotionInput{
			AgentID:   opts.AgentID,
			Proposal:  opts.Proposal,
			SkillName: opts.SkillName,
		})
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway skill error", true
		}
		return fmt.Sprintf("Skill promoted: %s\nThis skill will be reloadable from workspace skills on the next planning turn.", result.TargetPath), "Mateway skill promote", true
	default:
		return fmt.Sprintf("unknown skill command %q\n\n%s", cmd.Args[1], directSkillHelpText()), "Mateway skill error", true
	}
}

func (l *AgentLoop) executeDirectTraceCommand(cmd directMatewayCommand) (string, string, bool) {
	if len(cmd.Args) < 3 || cmd.Args[1] != "show" {
		return "用法：`mateway trace show <trace_id> [--raw]`", "Mateway trace help", true
	}
	opts, traceID, err := parseDirectTraceShowArgs(cmd.Args[2:])
	if err != nil {
		return err.Error(), "Mateway trace error", true
	}
	traceDir := filepath.Join(config.DefaultHome(), "trace")
	var out bytes.Buffer
	err = observer.ShowTrace(traceDir, traceID, opts, &out)
	if err != nil {
		return "执行失败：`" + err.Error() + "`", "Mateway trace error", true
	}
	return strings.TrimRight(out.String(), "\n"), "Mateway trace show", true
}

func (l *AgentLoop) executeDirectMemoryCommand(cmd directMatewayCommand) (string, string, bool) {
	if len(cmd.Args) < 2 {
		return directMemoryHelpText(), "Mateway memory help", true
	}
	store := l.runtime.Memory
	if strings.TrimSpace(store.Root) == "" {
		store = memory.NewStore(l.runtime.ToolCtx.Workspace)
	}
	switch cmd.Args[1] {
	case "lint":
		report, err := memory.Lint(filepath.Join(l.runtime.ToolCtx.Workspace, "memory"))
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway memory error", true
		}
		lines := []string{fmt.Sprintf("Memory lint checked %s", report.Root)}
		if len(report.Issues) == 0 {
			lines = append(lines, "No issues found.")
		} else {
			for _, issue := range report.Issues {
				lines = append(lines, fmt.Sprintf("- [%s] %s: %s", issue.Code, issue.Path, issue.Message))
			}
		}
		return strings.Join(lines, "\n"), "Mateway memory lint", true
	case "index":
		result, err := store.RebuildIndex(time.Now())
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway memory error", true
		}
		return fmt.Sprintf("Memory index written: %s\nentries=%d issues=%d", result.Path, len(result.Index.Entries), result.Index.IssueCount), "Mateway memory index", true
	case "list":
		opts, err := parseDirectMemoryListArgs(cmd.Args[2:])
		if err != nil {
			return err.Error(), "Mateway memory error", true
		}
		items, err := store.List(memory.ListOptions{AgentID: opts.AgentID, Status: opts.Status, Area: opts.Area})
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway memory error", true
		}
		if len(items) == 0 {
			return "No memory items found.", "Mateway memory list", true
		}
		return directRenderMemoryListOutput(opts, items), "Mateway memory list", true
	case "show":
		opts, err := parseDirectMemoryShowArgs(cmd.Args[2:])
		if err != nil {
			return err.Error(), "Mateway memory error", true
		}
		result, err := store.Show(opts.AgentID, opts.ID)
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway memory error", true
		}
		return directRenderMemoryShowOutput(result), "Mateway memory show", true
	case "review":
		opts, err := parseDirectMemoryReviewArgs(cmd.Args[2:])
		if err != nil {
			return err.Error(), "Mateway memory error", true
		}
		items, err := store.List(memory.ListOptions{AgentID: opts.AgentID, Area: "long", Status: "active"})
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway memory error", true
		}
		items = directFilterMemoryItemsForReviewQueue(opts.Review, items, time.Now())
		items = directFilterMemoryItemsByKind(opts.Kind, items)
		items = directFilterMemoryItemsByTarget(opts.Target, items)
		if opts.Proposal {
			input, ok := memory.BuildLongMemoryReviewProposal(memory.ReviewProposalOptions{
				AgentID: opts.AgentID,
				Review:  opts.Review,
				Kind:    opts.Kind,
				Target:  opts.Target,
				Items:   items,
				At:      time.Now(),
			})
			if !ok {
				return "No long memory items currently need review, so no review proposal was written.", "Mateway memory review", true
			}
			result, err := store.Propose(input)
			if err != nil {
				return "执行失败：`" + err.Error() + "`", "Mateway memory error", true
			}
			return fmt.Sprintf("Long memory review proposal written: %s", result.Path), "Mateway memory review", true
		}
		return directRenderMemoryReviewOutput(opts.AgentID, opts.Review, opts.Target, items, time.Now()), "Mateway memory review", true
	case "commit":
		opts, err := parseDirectMemoryCommitArgs(cmd.Args[2:])
		if err != nil {
			return err.Error(), "Mateway memory error", true
		}
		result, err := store.Commit(memory.CommitInput{
			AgentID:  opts.AgentID,
			Proposal: opts.Proposal,
			Title:    opts.Title,
		})
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway memory error", true
		}
		return fmt.Sprintf("Memory committed: %s", result.TargetPath), "Mateway memory commit", true
	case "reject":
		opts, err := parseDirectMemoryRejectArgs(cmd.Args[2:])
		if err != nil {
			return err.Error(), "Mateway memory error", true
		}
		result, err := store.Reject(memory.RejectInput{
			AgentID:  opts.AgentID,
			Proposal: opts.Proposal,
			Reason:   opts.Reason,
		})
		if err != nil {
			return "执行失败：`" + err.Error() + "`", "Mateway memory error", true
		}
		return fmt.Sprintf("Memory proposal rejected: %s", result.Path), "Mateway memory reject", true
	case "propose":
		return "这条 `mateway memory propose` 会创建新的 memory proposal，当前直达执行仍保持保守，暂未开放。", "Mateway memory guarded command", true
	default:
		return fmt.Sprintf("unknown memory command %q\n\n%s", cmd.Args[1], directMemoryHelpText()), "Mateway memory error", true
	}
}

type directMemoryListArgs struct {
	AgentID string
	Area    string
	Status  string
}

type directMemoryShowArgs struct {
	AgentID string
	ID      string
}

type directMemoryCommitArgs struct {
	AgentID  string
	Proposal string
	Title    string
}

type directMemoryRejectArgs struct {
	AgentID  string
	Proposal string
	Reason   string
}

type directMemoryReviewArgs struct {
	AgentID  string
	Review   string
	Kind     string
	Target   string
	Proposal bool
}

type directSkillPromoteArgs struct {
	AgentID   string
	Proposal  string
	SkillName string
}

func parseDirectMemoryListArgs(args []string) (directMemoryListArgs, error) {
	opts := directMemoryListArgs{AgentID: "main", Area: "inbox"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AgentID, i = value, next
		case "--area":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Area, i = value, next
		case "--status":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Status, i = value, next
		default:
			return opts, fmt.Errorf("unknown memory list option %q", args[i])
		}
	}
	return opts, nil
}

func parseDirectMemoryShowArgs(args []string) (directMemoryShowArgs, error) {
	opts := directMemoryShowArgs{AgentID: "main"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AgentID, i = value, next
		case "--id":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.ID, i = value, next
		default:
			if strings.TrimSpace(opts.ID) == "" && !strings.HasPrefix(args[i], "-") {
				opts.ID = args[i]
				continue
			}
			return opts, fmt.Errorf("unknown memory show option %q", args[i])
		}
	}
	if strings.TrimSpace(opts.ID) == "" {
		return opts, fmt.Errorf("usage: mateway memory show <id-or-path>")
	}
	return opts, nil
}

func parseDirectMemoryReviewArgs(args []string) (directMemoryReviewArgs, error) {
	opts := directMemoryReviewArgs{AgentID: "main"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AgentID, i = value, next
		case "--review":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Review, i = value, next
		case "--kind":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Kind, i = value, next
		case "--target":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Target, i = value, next
		case "--proposal":
			opts.Proposal = true
		default:
			return opts, fmt.Errorf("unknown memory review option %q", args[i])
		}
	}
	return opts, nil
}

func directFilterMemoryItemsForReviewQueue(review string, items []memory.MemoryItem, now time.Time) []memory.MemoryItem {
	switch strings.TrimSpace(review) {
	case "all":
		return items
	case "stale", "soon", "fresh":
		var out []memory.MemoryItem
		for _, item := range items {
			if directMemoryReviewLabel(item.Updated, now) == strings.TrimSpace(review) {
				out = append(out, item)
			}
		}
		return out
	default:
		var out []memory.MemoryItem
		for _, item := range items {
			label := directMemoryReviewLabel(item.Updated, now)
			if label == "stale" || label == "soon" {
				out = append(out, item)
			}
		}
		return out
	}
}

func directRenderMemoryReviewOutput(agentID, review, target string, items []memory.MemoryItem, now time.Time) string {
	if len(items) == 0 {
		return fmt.Sprintf("No long memory items currently need review for agent=%s review=%s target=%s.", firstNonEmpty(agentID, "main"), firstNonEmpty(review, "soon_or_stale"), firstNonEmpty(target, "any"))
	}
	items = directSortMemoryItemsForReviewQueue(items, now)
	lines := []string{fmt.Sprintf("Long memory review queue for agent=%s review=%s target=%s:", firstNonEmpty(agentID, "main"), firstNonEmpty(review, "soon_or_stale"), firstNonEmpty(target, "any"))}
	for _, item := range items {
		label := directMemoryReviewLabel(item.Updated, now)
		lines = append(lines, fmt.Sprintf("- %s\t%s\t%s\t%s\t%s", item.ID, item.Kind, item.Updated, label, item.Title))
		if suggestion := directMemoryReviewSuggestion(label, item.Kind); suggestion != "" {
			lines = append(lines, "  - suggestion: "+suggestion)
		}
	}
	return strings.Join(lines, "\n")
}

func directRenderMemoryListOutput(opts directMemoryListArgs, items []memory.MemoryItem) string {
	lines := []string{fmt.Sprintf("Memory items for agent=%s area=%s status=%s:", firstNonEmpty(opts.AgentID, "main"), firstNonEmpty(opts.Area, "inbox"), firstNonEmpty(opts.Status, "any"))}
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("- %s\t%s\t%s\t%s\t%s", item.ID, item.Status, item.Kind, firstNonEmpty(item.Updated, "unknown"), item.Title))
		if strings.EqualFold(firstNonEmpty(opts.Area, "inbox"), "inbox") && strings.EqualFold(item.Status, "proposed") {
			lines = append(lines, "  - inspect: `mateway memory show "+item.ID+"`")
			lines = append(lines, directSuggestedCommandsForInboxProposal(item.ID, item.Kind)...)
			continue
		}
		lines = append(lines, "  - inspect: `mateway memory show "+item.ID+"`")
	}
	return strings.Join(lines, "\n")
}

func directRenderMemoryShowOutput(result memory.ShowResult) string {
	text := strings.TrimRight(result.Text, "\n")
	status := directFrontmatterValue(text, "status")
	kind := directFrontmatterValue(text, "type")
	title := directMarkdownTitle(text)
	lines := []string{
		"Memory item: " + firstNonEmpty(result.ID, filepath.Base(result.Path)),
		"Path: " + result.Path,
	}
	if strings.TrimSpace(status) != "" {
		lines = append(lines, "Status: "+status)
	}
	if strings.TrimSpace(kind) != "" {
		lines = append(lines, "Type: "+kind)
	}
	if strings.TrimSpace(title) != "" {
		lines = append(lines, "Title: "+title)
	}
	lines = append(lines, "", text)
	if strings.EqualFold(status, "proposed") {
		lines = append(lines, "", "Suggested commands:")
		lines = append(lines, directSuggestedCommandsForInboxProposal(result.ID, kind)...)
	} else {
		lines = append(lines, "", "Suggested commands:")
		lines = append(lines, "- `mateway memory list --area long --status active`")
		lines = append(lines, "- `mateway memory review --review stale`")
	}
	return strings.Join(lines, "\n")
}

func directSuggestedCommandsForInboxProposal(id, kind string) []string {
	switch strings.TrimSpace(kind) {
	case "skill_candidate":
		return []string{
			"  - promote: `mateway skill promote --proposal " + id + " --name <skill-name>`",
			"  - reject: `mateway memory reject --proposal " + id + " --reason <reason>`",
		}
	case "skill_improvement":
		return []string{
			"  - review manually: `mateway memory show " + id + "`",
			"  - reject: `mateway memory reject --proposal " + id + " --reason <reason>`",
		}
	default:
		return []string{
			"  - approve: `mateway memory commit --proposal " + id + "`",
			"  - reject: `mateway memory reject --proposal " + id + " --reason <reason>`",
		}
	}
}

func directMarkdownTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		}
	}
	return ""
}

func directMemoryReviewLabel(updated string, now time.Time) string {
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

func directFilterMemoryItemsByKind(kind string, items []memory.MemoryItem) []memory.MemoryItem {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return items
	}
	var out []memory.MemoryItem
	for _, item := range items {
		if strings.EqualFold(item.Kind, kind) {
			out = append(out, item)
		}
	}
	return out
}

func directFilterMemoryItemsByTarget(target string, items []memory.MemoryItem) []memory.MemoryItem {
	target = strings.TrimSpace(target)
	if target == "" {
		return items
	}
	var out []memory.MemoryItem
	for _, item := range items {
		if strings.EqualFold(directPromotionTargetHint(item.Kind), target) {
			out = append(out, item)
		}
	}
	return out
}

func directSortMemoryItemsForReviewQueue(items []memory.MemoryItem, now time.Time) []memory.MemoryItem {
	items = append([]memory.MemoryItem(nil), items...)
	sort.SliceStable(items, func(i, j int) bool {
		li := directMemoryReviewPriority(directMemoryReviewLabel(items[i].Updated, now))
		lj := directMemoryReviewPriority(directMemoryReviewLabel(items[j].Updated, now))
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

func directMemoryReviewPriority(label string) int {
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

func directMemoryReviewSuggestion(review, kind string) string {
	target := directPromotionTargetHint(kind)
	switch strings.TrimSpace(review) {
	case "stale":
		return "re-validate this " + firstNonEmpty(target, kind, "memory") + " before relying on it in new tasks"
	case "soon":
		return "schedule a quick review if this " + firstNonEmpty(target, kind, "memory") + " still affects active work"
	default:
		return ""
	}
}

func directPromotionTargetHint(kind string) string {
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

func directOptionValue(args []string, i int) (string, int, error) {
	if i+1 >= len(args) {
		return "", i, fmt.Errorf("%s requires a value", args[i])
	}
	return args[i+1], i + 1, nil
}

func directFrontmatterValue(text, key string) string {
	prefix := strings.ToLower(strings.TrimSpace(key)) + ":"
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.EqualFold(trimmed, "---") {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(trimmed[len(prefix):])
		}
	}
	return ""
}

func parseDirectSingleIDArg(args []string, usage string) (string, error) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return "", fmt.Errorf("%s", usage)
	}
	return strings.TrimSpace(args[0]), nil
}

func parseDirectScheduleProposalStatusArgs(args []string) (string, error) {
	status := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--status":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return "", err
			}
			status, i = value, next
		default:
			return "", fmt.Errorf("unknown schedule proposals option %q", args[i])
		}
	}
	return status, nil
}

func parseDirectTraceShowArgs(args []string) (observer.TraceShowOptions, string, error) {
	var opts observer.TraceShowOptions
	var traceID string
	for _, arg := range args {
		switch arg {
		case "--raw":
			opts.Raw = true
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, "", fmt.Errorf("unknown trace show option %q", arg)
			}
			if traceID != "" {
				return opts, "", fmt.Errorf("usage: mateway trace show <trace_id>")
			}
			traceID = arg
		}
	}
	if strings.TrimSpace(traceID) == "" {
		return opts, "", fmt.Errorf("usage: mateway trace show <trace_id>")
	}
	return opts, traceID, nil
}

func readFileForDirectCommand(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

const directMatewayApprovalPrefix = "mateway_direct:"

func directMatewayCommandNeedsApproval(cmd directMatewayCommand) bool {
	if len(cmd.Args) < 2 {
		return false
	}
	switch cmd.Args[0] {
	case "memory":
		return cmd.Args[1] == "commit" || cmd.Args[1] == "reject"
	default:
		return false
	}
}

func isDirectMatewayApprovalAction(action string) bool {
	return strings.HasPrefix(strings.TrimSpace(action), directMatewayApprovalPrefix)
}

func directMatewayApprovalCommand(action string) (directMatewayCommand, bool) {
	raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(action), directMatewayApprovalPrefix))
	if raw == "" {
		return directMatewayCommand{}, false
	}
	return parseDirectMatewayCommand(raw)
}

func (l *AgentLoop) pendingMatewayDirectCommand(cmd directMatewayCommand) *Response {
	task := session.TaskState{
		ID:            l.state.traceID,
		TraceID:       l.state.traceID,
		Status:        session.TaskAwaitConfirm,
		UserText:      l.state.message.Text,
		ResolvedQuery: l.state.message.Text,
		StartedAt:     l.state.startedAt,
		UpdatedAt:     l.state.startedAt,
		PendingApproval: &session.PendingApproval{
			ApprovalType:    "boolean_confirm",
			RequestedAction: directMatewayApprovalPrefix + cmd.Raw,
		},
	}
	prompt := pendingMatewayDirectCommandPrompt(cmd)
	task.PendingApproval.Prompt = prompt
	l.state.currentTask = &task
	reply := l.runtime.sanitizeReply(channel.OutboundMessage{
		Channel:  l.state.message.Channel,
		ThreadID: l.state.message.ThreadID,
		Text:     prompt,
		Style:    "approval_pending",
		Title:    "Mateway pending confirmation",
	})
	resp := Response{Reply: reply, TraceID: l.state.traceID, AwaitConfirm: true}
	l.runtime.Logger.Event("runtime.reply", map[string]any{
		"trace_id":     l.state.traceID,
		"failed":       false,
		"reply_chars":  len(reply.Text),
		"result_count": 0,
		"direct":       "mateway_command_pending",
		"command":      cmd.Raw,
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Control(l.state.traceID, "await_confirm", reply.Style)
	}
	l.saveSession(resp)
	return &resp
}

func pendingMatewayDirectCommandPrompt(cmd directMatewayCommand) string {
	return "这条命令会修改 memory 状态，执行前需要你确认。\n\n命令：`" + cmd.Raw + "`\n\n回复“确认”继续执行，或回复“取消”放弃。"
}

func (l *AgentLoop) resolveApprovedMatewayDirectCommand() *Response {
	if l.state.binding.Kind != bindingApprovalReply || !l.state.binding.ApprovalGranted {
		return nil
	}
	action := strings.TrimSpace(l.state.binding.ApprovalAction)
	if !isDirectMatewayApprovalAction(action) {
		return nil
	}
	cmd, ok := directMatewayApprovalCommand(action)
	if !ok {
		return nil
	}
	if l.state.currentTask != nil {
		l.state.currentTask.PendingApproval = nil
	}
	text, title, handled := l.executeDirectMatewayCommand(cmd)
	if !handled {
		return nil
	}
	reply := l.runtime.sanitizeReply(channel.OutboundMessage{
		Channel:  l.state.message.Channel,
		ThreadID: l.state.message.ThreadID,
		Text:     directCommandReplyText(cmd, title, text),
		Style:    "reply",
		Title:    title,
	})
	resp := Response{Reply: reply, TraceID: l.state.traceID}
	l.runtime.Logger.Event("runtime.reply", map[string]any{
		"trace_id":     l.state.traceID,
		"failed":       false,
		"reply_chars":  len(reply.Text),
		"result_count": 0,
		"direct":       "mateway_command_approved",
		"command":      cmd.Raw,
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Reply(l.state.traceID, reply.Text, false)
	}
	l.saveSession(resp)
	return &resp
}

func (l *AgentLoop) resolveApprovedMatewayDirectCommandFromBinding(decision taskBindingDecision) *Response {
	if decision.Kind != bindingApprovalReply || !decision.ApprovalGranted {
		return nil
	}
	action := strings.TrimSpace(decision.ApprovalAction)
	if !isDirectMatewayApprovalAction(action) {
		return nil
	}
	cmd, ok := directMatewayApprovalCommand(action)
	if !ok {
		return nil
	}
	if task, exists := l.state.session.Tasks[decision.TargetTaskID]; exists {
		task.TraceID = l.state.traceID
		task.UserText = l.state.message.Text
		task.ResolvedQuery = firstNonEmpty(decision.ResolvedQuery, l.state.message.Text)
		task.Status = session.TaskOpen
		task.PendingApproval = nil
		task.PendingQuestions = nil
		task.UpdatedAt = l.state.startedAt
		l.state.currentTask = &task
	}
	text, title, handled := l.executeDirectMatewayCommand(cmd)
	if !handled {
		return nil
	}
	reply := l.runtime.sanitizeReply(channel.OutboundMessage{
		Channel:  l.state.message.Channel,
		ThreadID: l.state.message.ThreadID,
		Text:     directCommandReplyText(cmd, title, text),
		Style:    "reply",
		Title:    title,
	})
	resp := Response{Reply: reply, TraceID: l.state.traceID}
	l.runtime.Logger.Event("runtime.reply", map[string]any{
		"trace_id":     l.state.traceID,
		"failed":       false,
		"reply_chars":  len(reply.Text),
		"result_count": 0,
		"direct":       "mateway_command_approved",
		"command":      cmd.Raw,
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Reply(l.state.traceID, reply.Text, false)
	}
	l.saveSession(resp)
	return &resp
}

func directCommandReplyText(cmd directMatewayCommand, title, text string) string {
	body := strings.TrimSpace(text)
	header := directCommandReplyHeader(cmd, title, body)
	next := strings.TrimSpace(directCommandNextStepText(cmd))
	if directCommandTitleKind(title) == "error" || directCommandBodyLooksLikeFailure(body) {
		next = ""
	}
	if body == "" && next == "" {
		return header
	}
	var sections []string
	sections = append(sections, header)
	if body != "" {
		sections = append(sections, body)
	}
	if next != "" {
		sections = append(sections, "下一步："+next)
	}
	return strings.Join(sections, "\n\n")
}

func directCommandReplyHeader(cmd directMatewayCommand, title, body string) string {
	raw := strings.TrimSpace(cmd.Raw)
	switch {
	case directCommandTitleKind(title) == "error" || directCommandBodyLooksLikeFailure(body):
		return "命令未完成：`" + raw + "`"
	case directCommandTitleKind(title) == "help":
		return "命令帮助：`" + raw + "`"
	case directCommandTitleKind(title) == "note":
		return "命令说明：`" + raw + "`"
	default:
		return "已执行命令：`" + raw + "`"
	}
}

func directCommandTitleKind(title string) string {
	lower := strings.ToLower(strings.TrimSpace(title))
	switch {
	case strings.Contains(lower, " error"):
		return "error"
	case strings.Contains(lower, " help"):
		return "help"
	case strings.Contains(lower, " note"):
		return "note"
	default:
		return ""
	}
}

func directCommandBodyLooksLikeFailure(body string) bool {
	trimmed := strings.TrimSpace(body)
	return strings.HasPrefix(trimmed, "执行失败：") ||
		strings.HasPrefix(strings.ToLower(trimmed), "usage:") ||
		strings.HasPrefix(strings.ToLower(trimmed), "unknown ")
}

func directMemoryHelpText() string {
	lines := []string{
		"第一批一等 memory 直达命令：",
		"- `mateway memory list [--area inbox|long] [--status <status>] [--agent <agent>]`",
		"- `mateway memory show <id-or-path>`",
		"- `mateway memory review [--review stale|soon|all] [--kind <kind>] [--target <target>] [--proposal]`",
		"- `mateway memory commit --proposal <proposal-id-or-path> [--title <title>]`",
		"- `mateway memory reject --proposal <proposal-id-or-path> [--reason <reason>]`",
		"",
		"说明：",
		"- `list`、`show`、`review`、`lint`、`index` 可直接执行。",
		"- `commit`、`reject` 会修改 memory 状态，执行前会要求确认。",
		"- `propose` 当前仍保持保守，暂不开放直达执行。",
	}
	return strings.Join(lines, "\n")
}

func directSkillHelpText() string {
	lines := []string{
		"可用的 skill 直达命令：",
		"- `mateway skill list`",
		"- `mateway skill search <query>`",
		"- `mateway skill promote --proposal <proposal-id-or-path> [--name <skill-name>]`",
		"",
		"说明：",
		"- `promote` 用于把已 review 的 skill candidate 提升到 `~/.mateway/workspace/skills`。",
		"- promote 成功后，下一次 planning turn 会自动从 workspace skills 重新加载。",
	}
	return strings.Join(lines, "\n")
}

func directMatewayHelpText() string {
	lines := []string{
		"可识别的 mateway 顶层命令：",
		"- `mateway init`",
		"- `mateway doctor`",
		"- `mateway ask <message>`",
		"- `mateway gateway <serve|start|restart|stop|status>`",
		"- `mateway memory <lint|index|list|show|review|propose|commit|reject>`",
		"- `mateway skill <list|search|promote>`",
		"- `mateway schedule <list|proposals|show|due>`",
		"- `mateway heartbeat status`",
		"- `mateway trace show <trace_id> [--raw]`",
		"- `mateway test ...` / `mateway eval ...` / `mateway feishu`",
		"",
		"说明：对话 runtime 只会直达执行安全的查看、review、promote 类命令；本机初始化、服务管理、测试和评测类命令会提示到终端执行。",
	}
	return strings.Join(lines, "\n")
}

func directScheduleHelpText() string {
	return "用法：`mateway schedule <list|proposals|show|due>`\n\n说明：runtime 对话里只开放只读 schedule 直达命令；create/update/delete 等变更操作请走 CLI 或正常 runtime planning + confirmation。"
}

func directCommandNextStepText(cmd directMatewayCommand) string {
	if len(cmd.Args) < 2 {
		return ""
	}
	switch cmd.Args[0] {
	case "memory":
		switch cmd.Args[1] {
		case "list":
			return "可继续用 `mateway memory show <id>` 查看详情；普通 memory proposal 可执行 `memory commit/reject`，skill candidate 请走 `mateway skill promote --proposal <id> --name <skill-name>`。"
		case "show":
			return "如果这条内容来自 inbox proposal，请按上面的 Suggested commands 继续审核。"
		case "review":
			if directCommandHasFlag(cmd.Args[2:], "--proposal") {
				return "可用 `mateway memory list --area inbox --status proposed` 查看新写入的 review proposal。"
			}
			return "如果要把当前 review queue 写入 inbox，可追加 `--proposal`；也可以先用 `mateway memory show <id>` 查看具体条目。"
		case "commit":
			return "可用 `mateway memory list --area long --status active` 查看已进入 long memory 的条目。"
		case "reject":
			return "可用 `mateway memory list --area inbox --status proposed` 继续处理其他待审核 proposal。"
		case "lint":
			return "如果需要刷新索引，可继续执行 `mateway memory index`。"
		case "index":
			return "可继续用 `mateway memory list` 或 `mateway memory review` 检查当前 memory 内容。"
		}
	case "skill":
		switch cmd.Args[1] {
		case "promote":
			return "可用 `mateway skill list` 确认技能已进入 workspace；下一次 planning turn 会自动重载。"
		case "list":
			return "如需查找更多候选技能，可继续执行 `mateway skill search <query>`。"
		case "search":
			return "找到合适技能后，可继续执行 `mateway skill install <ref>` 或 review 后再走 promote 流程。"
		}
	}
	return ""
}

func directCommandHasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if strings.TrimSpace(arg) == flag {
			return true
		}
	}
	return false
}

func parseDirectMemoryCommitArgs(args []string) (directMemoryCommitArgs, error) {
	opts := directMemoryCommitArgs{AgentID: "main"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AgentID, i = value, next
		case "--proposal":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Proposal, i = value, next
		case "--title":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Title, i = value, next
		default:
			if strings.TrimSpace(opts.Proposal) == "" && !strings.HasPrefix(args[i], "-") {
				opts.Proposal = args[i]
				continue
			}
			return opts, fmt.Errorf("unknown memory commit option %q", args[i])
		}
	}
	if strings.TrimSpace(opts.Proposal) == "" {
		return opts, fmt.Errorf("usage: mateway memory commit --proposal <proposal-id-or-path>")
	}
	return opts, nil
}

func parseDirectMemoryRejectArgs(args []string) (directMemoryRejectArgs, error) {
	opts := directMemoryRejectArgs{AgentID: "main"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AgentID, i = value, next
		case "--proposal":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Proposal, i = value, next
		case "--reason":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Reason, i = value, next
		default:
			if strings.TrimSpace(opts.Proposal) == "" && !strings.HasPrefix(args[i], "-") {
				opts.Proposal = args[i]
				continue
			}
			return opts, fmt.Errorf("unknown memory reject option %q", args[i])
		}
	}
	if strings.TrimSpace(opts.Proposal) == "" {
		return opts, fmt.Errorf("usage: mateway memory reject --proposal <proposal-id-or-path>")
	}
	return opts, nil
}

func parseDirectSkillPromoteArgs(args []string) (directSkillPromoteArgs, error) {
	opts := directSkillPromoteArgs{AgentID: "main"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.AgentID, i = value, next
		case "--proposal":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.Proposal, i = value, next
		case "--name":
			value, next, err := directOptionValue(args, i)
			if err != nil {
				return opts, err
			}
			opts.SkillName, i = value, next
		default:
			if strings.TrimSpace(opts.Proposal) == "" && !strings.HasPrefix(args[i], "-") {
				opts.Proposal = args[i]
				continue
			}
			return opts, fmt.Errorf("unknown skill promote option %q", args[i])
		}
	}
	if strings.TrimSpace(opts.Proposal) == "" {
		return opts, fmt.Errorf("usage: mateway skill promote --proposal <proposal-id-or-path> [--name <skill-name>]")
	}
	return opts, nil
}

var referencedCommandPattern = regexp.MustCompile("`(mateway [^`]+)`")

func resolveReferencedMatewayCommand(text string, st session.State) (directMatewayCommand, bool) {
	if !looksLikeReferencedCommandIntent(text) {
		return directMatewayCommand{}, false
	}
	for i := len(st.RecentTurns) - 1; i >= 0; i-- {
		turn := st.RecentTurns[i]
		if strings.TrimSpace(turn.Role) != "assistant" {
			continue
		}
		matches := referencedCommandPattern.FindAllStringSubmatch(turn.Text, -1)
		for j := len(matches) - 1; j >= 0; j-- {
			if len(matches[j]) < 2 {
				continue
			}
			if cmd, ok := parseDirectMatewayCommand(matches[j][1]); ok {
				return cmd, true
			}
		}
	}
	return directMatewayCommand{}, false
}

func looksLikeReferencedCommandIntent(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return false
	}
	triggers := []string{
		"执行这个命令", "执行上一条命令", "运行这个命令", "运行上一条命令", "就执行这个", "就运行这个",
		"按你刚才那个命令", "用你上条命令", "run that command", "run the previous command",
		"execute that command", "execute the previous command", "use the command you suggested",
	}
	for _, trigger := range triggers {
		if strings.Contains(normalized, strings.ToLower(trigger)) {
			return true
		}
	}
	return false
}
