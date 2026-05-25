package eval

import (
	"context"
	"fmt"
	"strings"

	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/tool"
)

type RoutingCase struct {
	Name           string
	User           string
	ExpectTools    []string
	ExpectNeeds    []string
	ExpectSubtasks bool
	AllowPrefixes  []string
}

type RoutingResult struct {
	Name          string
	User          string
	Passed        bool
	Plan          model.Plan
	Tools         []string
	Errors        []string
	Warnings      []string
	ExpectedTools []string
}

type RoutingSummary struct {
	Total   int
	Passed  int
	Results []RoutingResult
}

func DefaultRoutingCases() []RoutingCase {
	return []RoutingCase{
		{Name: "schedule-create", User: "每天 9 点帮我收集 AI 趋势文章", ExpectTools: []string{"schedule.create"}, ExpectNeeds: []string{"schedule.create"}, ExpectSubtasks: true},
		{Name: "schedule-delete", User: "删除 ai-trends 定时任务", ExpectTools: []string{"schedule.delete"}, ExpectNeeds: []string{"schedule.delete"}},
		{Name: "skill-search-install", User: "搜索一个浏览器自动化 skill，然后直接安装最匹配的那个", ExpectTools: []string{"skill.search", "skill.install"}, ExpectNeeds: []string{"skill.search", "skill.install"}, ExpectSubtasks: true},
		{Name: "software-search", User: "帮我安装一个叫 example-cli 的 CLI", ExpectTools: []string{"software.search"}, ExpectNeeds: []string{"software.search", "software.install"}},
		{Name: "project-overview", User: "总结当前项目结构", ExpectTools: []string{"project.index"}, ExpectNeeds: []string{"project.index"}},
		{Name: "file-summary", User: "总结 README.md 这个文件", AllowPrefixes: []string{"file."}, ExpectNeeds: []string{"file.summary"}},
		{Name: "fresh-web", User: "最新 AI 应用趋势是什么", ExpectTools: []string{"web.search"}, ExpectNeeds: []string{"web.search"}},
		{Name: "memory-search", User: "从长期记忆里查一下我们的 schedule 设计原则", ExpectTools: []string{"memory.search"}, ExpectNeeds: []string{"memory.search"}},
		{Name: "terminal-diagnose-local-software", User: "帮我看看 openclaw 是不是卡住了", ExpectTools: []string{"terminal.run"}, ExpectNeeds: []string{"terminal.run"}},
		{Name: "complex-project-read-test", User: "先概览当前仓库结构，再重点总结 README.md 和 docs/开发TODO.md，最后如果有测试命令的话跑一下最小测试确认项目状态", ExpectTools: []string{"project.index", "terminal.run"}, ExpectNeeds: []string{"project.index", "file.summary", "terminal.run"}, ExpectSubtasks: true, AllowPrefixes: []string{"file."}},
		{Name: "complex-install-diagnose", User: "帮我安装一个叫 example-cli 的 CLI，如果安装方式不明确先查官方安装方法，装完后验证可执行文件是否能正常输出版本信息", ExpectTools: []string{"software.search", "software.install"}, ExpectNeeds: []string{"software.search", "software.install", "terminal.run"}, ExpectSubtasks: true},
		{Name: "complex-search-memory-schedule", User: "先从长期记忆里查一下我们对 schedule 的设计原则，再补充搜索一下最新的 agent scheduler 实践，如果信息足够就帮我设计一个每天 9 点执行的 AI 趋势收集任务", ExpectTools: []string{"memory.search", "web.search", "schedule.create"}, ExpectNeeds: []string{"memory.search", "web.search", "schedule.create"}, ExpectSubtasks: true},
	}
}

func FirstStageFocusCases() []RoutingCase {
	return []RoutingCase{
		{Name: "project-overview", User: "总结当前项目结构", ExpectTools: []string{"project.index"}, ExpectNeeds: []string{"project.index"}, ExpectSubtasks: true},
		{Name: "complex-project-read-test", User: "先概览当前仓库结构，再重点总结 README.md 和 docs/开发TODO.md，最后如果有测试命令的话跑一下最小测试确认项目状态", ExpectTools: []string{"project.index", "terminal.run"}, ExpectNeeds: []string{"project.index", "file.summary", "terminal.run"}, ExpectSubtasks: true, AllowPrefixes: []string{"file."}},
		{Name: "complex-install-diagnose", User: "帮我安装一个叫 example-cli 的 CLI，如果安装方式不明确先查官方安装方法，装完后验证可执行文件是否能正常输出版本信息", ExpectTools: []string{"software.search", "software.install"}, ExpectNeeds: []string{"software.search", "software.install", "terminal.run"}, ExpectSubtasks: true},
		{Name: "complex-search-memory-schedule", User: "先从长期记忆里查一下我们对 schedule 的设计原则，再补充搜索一下最新的 agent scheduler 实践，如果信息足够就帮我设计一个每天 9 点执行的 AI 趋势收集任务", ExpectTools: []string{"memory.search", "web.search", "schedule.create"}, ExpectNeeds: []string{"memory.search", "web.search", "schedule.create"}, ExpectSubtasks: true},
	}
}

func FirstStageUltraFocusCases() []RoutingCase {
	return []RoutingCase{
		{Name: "project-overview", User: "总结当前项目结构", ExpectTools: []string{"project.index"}, ExpectNeeds: []string{"project.index"}, ExpectSubtasks: true},
		{Name: "complex-install-diagnose", User: "帮我安装一个叫 example-cli 的 CLI，如果安装方式不明确先查官方安装方法，装完后验证可执行文件是否能正常输出版本信息", ExpectTools: []string{"software.search", "software.install"}, ExpectNeeds: []string{"software.search", "software.install", "terminal.run"}, ExpectSubtasks: true},
	}
}

func RunRouting(ctx context.Context, planner model.Planner, registry *tool.Registry, skillPrompt string, cases []RoutingCase) (RoutingSummary, error) {
	if planner == nil {
		return RoutingSummary{}, fmt.Errorf("planner is required")
	}
	if registry == nil {
		registry = tool.NewBuiltinRegistry()
	}
	if len(cases) == 0 {
		cases = DefaultRoutingCases()
	}
	summary := RoutingSummary{Total: len(cases)}
	for _, item := range cases {
		result := RoutingResult{Name: item.Name, User: item.User, ExpectedTools: item.ExpectTools}
		plan, err := planner.PlanJSON(ctx, item.User, registry.Definitions(), skillPrompt)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			summary.Results = append(summary.Results, result)
			continue
		}
		result.Plan = plan
		result.Tools = planTools(plan)
		verification := runtime.VerifyPlanContract(plan, registry, item.User)
		result.Warnings = append(result.Warnings, verification.Warnings...)
		result.Warnings = append(result.Warnings, downgradeExpectedRoutingErrors(item, verification.Errors)...)
		result.Errors = append(result.Errors, routingBlockingVerificationErrors(item, verification.Errors)...)
		result.Errors = append(result.Errors, routingExpectationErrors(item, result.Tools)...)
		result.Errors = append(result.Errors, understandingExpectationErrors(item, result.Plan.Understanding)...)
		result.Passed = len(result.Errors) == 0
		if result.Passed {
			summary.Passed++
		}
		summary.Results = append(summary.Results, result)
	}
	return summary, nil
}

func understandingExpectationErrors(item RoutingCase, understanding model.UnderstandingJSON) []string {
	var errors []string
	if strings.TrimSpace(understanding.Goal) == "" {
		errors = append(errors, "understanding.goal is empty")
	}
	for _, expected := range item.ExpectNeeds {
		if !containsExpectedNeed(understanding.ToolNeeds, expected) {
			errors = append(errors, "missing expected tool_need "+expected)
		}
	}
	if item.ExpectSubtasks && len(understanding.Subtasks) == 0 {
		errors = append(errors, "understanding.subtasks is empty")
	}
	return errors
}

func RenderRoutingMarkdown(summary RoutingSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Routing Eval\n\nPassed: %d/%d\n\n", summary.Passed, summary.Total)
	for _, result := range summary.Results {
		status := "PASS"
		if !result.Passed {
			status = "FAIL"
		}
		fmt.Fprintf(&b, "## %s - %s\n\n", status, result.Name)
		fmt.Fprintf(&b, "- User: %s\n", result.User)
		fmt.Fprintf(&b, "- Tools: %s\n", strings.Join(result.Tools, ", "))
		if strings.TrimSpace(result.Plan.Summary) != "" {
			fmt.Fprintf(&b, "- Summary: %s\n", result.Plan.Summary)
		}
		if strings.TrimSpace(result.Plan.Understanding.Goal) != "" {
			fmt.Fprintf(&b, "- Understanding goal: %s\n", result.Plan.Understanding.Goal)
		}
		if len(result.Plan.Understanding.Subtasks) > 0 {
			fmt.Fprintf(&b, "- Subtasks: %s\n", strings.Join(result.Plan.Understanding.Subtasks, " | "))
		}
		if len(result.Plan.Understanding.ToolNeeds) > 0 {
			fmt.Fprintf(&b, "- Tool needs: %s\n", strings.Join(result.Plan.Understanding.ToolNeeds, ", "))
		}
		if len(result.Plan.Steps) > 0 {
			fmt.Fprintf(&b, "\n### Steps\n\n")
			for _, step := range result.Plan.Steps {
				fmt.Fprintf(&b, "- `%s` tool=`%s` goal=%s\n", step.ID, step.Tool, step.Goal)
			}
		}
		if len(result.Errors) > 0 {
			fmt.Fprintf(&b, "\n### Errors\n\n")
			for _, item := range result.Errors {
				fmt.Fprintf(&b, "- %s\n", item)
			}
		}
		if len(result.Warnings) > 0 {
			fmt.Fprintf(&b, "\n### Warnings\n\n")
			for _, item := range result.Warnings {
				fmt.Fprintf(&b, "- %s\n", item)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func planTools(plan model.Plan) []string {
	out := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		if toolName := strings.TrimSpace(step.Tool); toolName != "" {
			out = append(out, toolName)
		}
	}
	return out
}

func routingExpectationErrors(item RoutingCase, tools []string) []string {
	var errors []string
	for _, expected := range item.ExpectTools {
		if !containsTool(tools, expected) {
			errors = append(errors, "missing expected tool "+expected)
		}
	}
	if len(item.AllowPrefixes) > 0 {
		matched := false
		for _, toolName := range tools {
			for _, prefix := range item.AllowPrefixes {
				if strings.HasPrefix(toolName, prefix) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			errors = append(errors, "missing tool with allowed prefix "+strings.Join(item.AllowPrefixes, ","))
		}
	}
	return errors
}

func routingBlockingVerificationErrors(item RoutingCase, verificationErrors []string) []string {
	var out []string
	for _, err := range verificationErrors {
		if isExpectedRoutingRepairBoundary(item, err) {
			continue
		}
		out = append(out, err)
	}
	return out
}

func downgradeExpectedRoutingErrors(item RoutingCase, verificationErrors []string) []string {
	var out []string
	for _, err := range verificationErrors {
		if isExpectedRoutingRepairBoundary(item, err) {
			out = append(out, "expected repair boundary: "+err)
		}
	}
	return out
}

func isExpectedRoutingRepairBoundary(item RoutingCase, verificationError string) bool {
	if item.Name != "complex-install-diagnose" {
		return false
	}
	return strings.Contains(verificationError, "missing required arg command")
}

func containsTool(tools []string, target string) bool {
	for _, toolName := range tools {
		if toolName == target {
			return true
		}
	}
	return false
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func containsExpectedNeed(items []string, target string) bool {
	for _, item := range items {
		if toolNeedMatches(item, target) {
			return true
		}
	}
	return false
}

func toolNeedMatches(actual, expected string) bool {
	actual = strings.TrimSpace(actual)
	expected = strings.TrimSpace(expected)
	if actual == expected {
		return true
	}
	if expected == "file.summary" {
		return actual == "file.read" || strings.HasPrefix(actual, "file.")
	}
	return false
}
