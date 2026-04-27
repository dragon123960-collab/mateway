package harness

import (
	"context"
	"testing"

	"github.com/dongping/mateway/internal/tools"
)

type namedTool struct {
	spec tools.Spec
}

func (t namedTool) Spec() tools.Spec { return t.spec }
func (t namedTool) Invoke(context.Context, tools.Call) (tools.Result, error) {
	return tools.Result{}, nil
}

func TestProgressiveToolDisclosureResearchGoal(t *testing.T) {
	list := []tools.Tool{
		namedTool{spec: tools.Spec{Name: "web_search", Description: "Search the web", Kind: tools.KindBuiltin, Tags: []string{"web", "search"}}},
		namedTool{spec: tools.Spec{Name: "browser_fetch", Description: "Read a page", Kind: tools.KindBuiltin, Tags: []string{"browser", "web"}}},
		namedTool{spec: tools.Spec{Name: "read_file", Description: "Read files", Kind: tools.KindBuiltin, Tags: []string{"filesystem"}}},
		namedTool{spec: tools.Spec{Name: "sandbox_exec", Description: "Run commands", Kind: tools.KindBuiltin, Tags: []string{"exec", "sandbox"}}},
		namedTool{spec: tools.Spec{Name: "read_memory", Description: "Read memory", Kind: tools.KindBuiltin, Tags: []string{"memory"}}},
		namedTool{spec: tools.Spec{Name: "search_history", Description: "Search history", Kind: tools.KindBuiltin, Tags: []string{"history"}}},
		namedTool{spec: tools.Spec{Name: "search_scoped_memory", Description: "Scoped memory", Kind: tools.KindBuiltin, Tags: []string{"memory", "search"}}},
		namedTool{spec: tools.Spec{Name: "wiki_query", Description: "Knowledge lookup", Kind: tools.KindBuiltin, Tags: []string{"wiki", "search"}}},
		namedTool{spec: tools.Spec{Name: "opencli_run", Description: "External cli web google reddit", Kind: tools.KindCLI, Tags: []string{"cli", "opencli", "google", "reddit"}}},
	}
	allowed := progressiveToolDisclosure("调研基金趋势并总结", list)
	if !allowed["web_search"] || !allowed["browser_fetch"] {
		t.Fatalf("expected research tools to be allowed: %#v", allowed)
	}
	if !allowed["wiki_query"] || !allowed["search_scoped_memory"] {
		t.Fatalf("expected memory tools to remain allowed: %#v", allowed)
	}
}

func TestProgressiveToolDisclosureExecutionGoal(t *testing.T) {
	list := []tools.Tool{
		namedTool{spec: tools.Spec{Name: "web_search", Description: "Search the web", Kind: tools.KindBuiltin, Tags: []string{"web", "search"}}},
		namedTool{spec: tools.Spec{Name: "exec", Description: "Run a command inside the workspace.", Kind: tools.KindBuiltin, Tags: []string{"exec"}}},
		namedTool{spec: tools.Spec{Name: "sandbox_exec", Description: "Run commands in a sandbox", Kind: tools.KindBuiltin, Tags: []string{"exec", "sandbox", "testing"}}},
		namedTool{spec: tools.Spec{Name: "read_memory", Description: "Read memory", Kind: tools.KindBuiltin, Tags: []string{"memory"}}},
		namedTool{spec: tools.Spec{Name: "search_history", Description: "Search history", Kind: tools.KindBuiltin, Tags: []string{"history"}}},
		namedTool{spec: tools.Spec{Name: "search_scoped_memory", Description: "Scoped memory", Kind: tools.KindBuiltin, Tags: []string{"memory", "search"}}},
		namedTool{spec: tools.Spec{Name: "wiki_query", Description: "Knowledge lookup", Kind: tools.KindBuiltin, Tags: []string{"wiki", "search"}}},
		namedTool{spec: tools.Spec{Name: "opencli_run", Description: "External cli shell run", Kind: tools.KindCLI, Tags: []string{"cli", "opencli", "run"}}},
		namedTool{spec: tools.Spec{Name: "build_skill", Description: "Skill to validate builds", Kind: tools.KindSkill, Tags: []string{"skill", "build", "test"}}},
		namedTool{spec: tools.Spec{Name: "read_file", Description: "Read files", Kind: tools.KindBuiltin, Tags: []string{"filesystem"}}},
	}
	allowed := progressiveToolDisclosure("执行测试命令并验证结果", list)
	if !allowed["sandbox_exec"] {
		t.Fatalf("expected sandbox_exec to be selected: %#v", allowed)
	}
	if !allowed["opencli_run"] && !allowed["build_skill"] {
		t.Fatalf("expected a cli or skill tool to be selected for execution goal: %#v", allowed)
	}
}

func TestScoreToolForGoalPrefersExecForEnvironmentBoundCLI(t *testing.T) {
	goal := "在本地 zsh 里执行 opencli zhihu hot，复用浏览器 cookie 和 daemon"
	execScore := scoreToolForGoal(goal, tools.Spec{
		Name:        "exec",
		Description: "Run a command inside the workspace.",
		Kind:        tools.KindBuiltin,
		Tags:        []string{"exec"},
	})
	sandboxScore := scoreToolForGoal(goal, tools.Spec{
		Name:        "sandbox_exec",
		Description: "Run commands in a sandbox",
		Kind:        tools.KindBuiltin,
		Tags:        []string{"exec", "sandbox", "testing"},
	})
	if execScore <= sandboxScore {
		t.Fatalf("expected exec to outrank sandbox_exec for environment-bound cli goals: exec=%d sandbox=%d", execScore, sandboxScore)
	}
}

func TestScoreToolForGoalPrefersSandboxExecForGenericExecution(t *testing.T) {
	goal := "执行测试命令并验证结果"
	execScore := scoreToolForGoal(goal, tools.Spec{
		Name:        "exec",
		Description: "Run a command inside the workspace.",
		Kind:        tools.KindBuiltin,
		Tags:        []string{"exec"},
	})
	sandboxScore := scoreToolForGoal(goal, tools.Spec{
		Name:        "sandbox_exec",
		Description: "Run commands in a sandbox",
		Kind:        tools.KindBuiltin,
		Tags:        []string{"exec", "sandbox", "testing"},
	})
	if sandboxScore <= execScore {
		t.Fatalf("expected sandbox_exec to outrank exec for generic execution goals: exec=%d sandbox=%d", execScore, sandboxScore)
	}
}

func TestProgressiveToolDisclosureScheduleGoal(t *testing.T) {
	list := []tools.Tool{
		namedTool{spec: tools.Spec{Name: "read_memory", Description: "Read memory", Kind: tools.KindBuiltin, Tags: []string{"memory"}}},
		namedTool{spec: tools.Spec{Name: "search_history", Description: "Search history", Kind: tools.KindBuiltin, Tags: []string{"history"}}},
		namedTool{spec: tools.Spec{Name: "search_scoped_memory", Description: "Scoped memory", Kind: tools.KindBuiltin, Tags: []string{"memory", "search"}}},
		namedTool{spec: tools.Spec{Name: "wiki_query", Description: "Knowledge lookup", Kind: tools.KindBuiltin, Tags: []string{"wiki", "search"}}},
		namedTool{spec: tools.Spec{Name: "schedule_create", Description: "Create a recurring schedule job with interval or cron semantics.", Kind: tools.KindBuiltin, Tags: []string{"schedule", "cron", "automation"}}},
		namedTool{spec: tools.Spec{Name: "schedule_list", Description: "List recurring schedule jobs.", Kind: tools.KindBuiltin, Tags: []string{"schedule", "cron", "automation"}}},
		namedTool{spec: tools.Spec{Name: "schedule_get", Description: "Read a schedule job.", Kind: tools.KindBuiltin, Tags: []string{"schedule", "cron", "automation"}}},
		namedTool{spec: tools.Spec{Name: "web_search", Description: "Search the web", Kind: tools.KindBuiltin, Tags: []string{"web", "search"}}},
		namedTool{spec: tools.Spec{Name: "read_file", Description: "Read files", Kind: tools.KindBuiltin, Tags: []string{"filesystem"}}},
	}
	allowed := progressiveToolDisclosure("请每天早上3点定时执行课程搜集任务并提醒我", list)
	if !allowed["schedule_create"] {
		t.Fatalf("expected schedule_create to be selected: %#v", allowed)
	}
	if !allowed["schedule_list"] && !allowed["schedule_get"] {
		t.Fatalf("expected schedule inspection tool to remain visible: %#v", allowed)
	}
}
