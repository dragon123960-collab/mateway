package harness

import "strings"

const (
	TaskTypeAnswer    = "answer"
	TaskTypeResearch  = "research"
	TaskTypeCodeRead  = "code_read"
	TaskTypeCodeWrite = "code_write"
	TaskTypeLocalCLI  = "local_cli"
	TaskTypeTool      = "tool_task"
	TaskTypeSchedule  = "schedule_task"
	TaskTypeMemory    = "memory_task"
	TaskTypeDelegate  = "delegate_task"
	TaskTypeDiagnose  = "diagnose_task"
)

func normalizeTaskType(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case TaskTypeAnswer, TaskTypeResearch, TaskTypeCodeRead, TaskTypeCodeWrite, TaskTypeLocalCLI, TaskTypeTool, TaskTypeSchedule, TaskTypeMemory, TaskTypeDelegate, TaskTypeDiagnose:
		return strings.TrimSpace(strings.ToLower(raw))
	default:
		return ""
	}
}

func resolveTaskType(req Request) string {
	if explicit := normalizeTaskType(firstNonEmpty(req.TaskType, argumentString(req.Arguments, "task_type"))); explicit != "" {
		return explicit
	}
	return classifyTaskType(req)
}

func resolveOriginKind(req Request) string {
	origin := strings.TrimSpace(strings.ToLower(argumentString(req.Arguments, "task_kind")))
	switch origin {
	case "new_task", "follow_up", "control", "approval_resume", "schedule", "manual":
		return origin
	}
	if strings.EqualFold(strings.TrimSpace(req.Channel), "schedule") {
		return "schedule"
	}
	return "manual"
}

func classifyTaskType(req Request) string {
	if strings.EqualFold(strings.TrimSpace(req.Mode), "tool") {
		switch toolName := strings.TrimSpace(req.ToolName); {
		case strings.HasPrefix(toolName, "schedule_"):
			return TaskTypeSchedule
		case toolName == "spawn" || toolName == "wait_agent":
			return TaskTypeDelegate
		case toolName == "read_memory" || toolName == "read_session_summary" || toolName == "recall_last_task" || toolName == "search_history" || toolName == "search_scoped_memory" || toolName == "write_memory_note" || toolName == "wiki_ingest" || toolName == "wiki_query" || toolName == "wiki_lint":
			return TaskTypeMemory
		case toolName == "read_file" || toolName == "list_files" || toolName == "search_text":
			return TaskTypeCodeRead
		case toolName == "write_file":
			return TaskTypeCodeWrite
		default:
			return TaskTypeTool
		}
	}
	return classifyTaskTypeFromGoal(req.UserText)
}

func classifyTaskTypeFromGoal(goal string) string {
	text := strings.ToLower(strings.TrimSpace(goal))
	if text == "" {
		return TaskTypeAnswer
	}
	switch {
	case containsAny(text, "记忆", "memory", "wiki", "历史", "summary", "learn", "复盘", "沉淀"):
		return TaskTypeMemory
	case containsAny(text, "报错", "错误", "失败", "异常", "定位", "排查", "诊断", "日志", "根因", "why failed", "debug", "diagnose", "traceback", "stack trace"):
		return TaskTypeDiagnose
	case goalSuggestsScheduleInspection(text) || containsAny(text, "定时", "提醒", "schedule", "cron", "自动执行", "自动运行"):
		return TaskTypeSchedule
	case containsAny(text, "lark-cli", "larkcli", "opencli", "gh", "zsh", "bash", "shell", "cli", "命令", "本机", "本地", "安装", "path", "终端"):
		return TaskTypeLocalCLI
	case containsAny(text, "调研", "研究", "分析", "对比", "报告", "搜集", "收集", "整理", "总结", "research", "analyze", "analysis", "compare", "report", "investigate"):
		return TaskTypeResearch
	case containsAny(text, "看看代码", "阅读代码", "解释代码", "代码怎么", "读一下", "read code", "explain code", "codebase", "repo", "仓库", "项目", "文件", "解释一下"):
		return TaskTypeCodeRead
	case containsAny(text, "修改代码", "改代码", "修复", "实现", "新增", "重构", "补测试", "写测试", "更新文件", "edit code", "fix", "implement", "refactor", "write test"):
		return TaskTypeCodeWrite
	case containsAny(text, "子 agent", "子agent", "spawn", "delegate", "并行", "协作 agent", "协作agent"):
		return TaskTypeDelegate
	default:
		return TaskTypeAnswer
	}
}
