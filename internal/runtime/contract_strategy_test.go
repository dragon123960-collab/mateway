package runtime

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/session"
)

// --- Unit tests for classifyContractStrategy ---

func TestContractStrategyDirectSimpleQAndA(t *testing.T) {
	cases := []struct {
		goal string
		text string
	}{
		{"explain what Mateway is", "explain what Mateway is"},
		{"what is the meaning of life", "what is the meaning of life"},
		{"how does Go garbage collector work", "how does Go garbage collector work"},
	}
	for _, tc := range cases {
		t.Run(tc.goal, func(t *testing.T) {
			contract := fallbackTaskContract(tc.goal, tc.text)
			strategy := classifyContractStrategy(tc.goal, tc.text, contract)
			if strategy != contractStrategyDirect {
				t.Fatalf("expected direct, got %q for %q", strategy, tc.goal)
			}
		})
	}
}

func TestContractStrategyAutoContractForLowRiskSearch(t *testing.T) {
	cases := []string{
		"check weather today",
		"search latest Go release",
		"查一下今天天气",
		"search for news",
	}
	for _, text := range cases {
		t.Run(text, func(t *testing.T) {
			contract := fallbackTaskContract(text, text)
			strategy := classifyContractStrategy(text, text, contract)
			if strategy != contractStrategyAutoContract {
				t.Fatalf("expected auto_contract, got %q for %q", strategy, text)
			}
		})
	}
}

func TestContractStrategyAutoContractForReadFile(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.read"},
	}
	strategy := classifyContractStrategy("read config file", "read config file", contract)
	if strategy != contractStrategyAutoContract {
		t.Fatalf("expected auto_contract for file.read, got %q", strategy)
	}
}

func TestContractStrategyReviewRequiredForFileWrite(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.write"},
	}
	strategy := classifyContractStrategy("write report", "write report to file", contract)
	if strategy != contractStrategyReviewRequired {
		t.Fatalf("expected review_required for file.write, got %q", strategy)
	}
}

func TestContractStrategyReviewRequiredForTerminalRun(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"terminal.run"},
	}
	strategy := classifyContractStrategy("run service", "run systemctl status", contract)
	if strategy != contractStrategyReviewRequired {
		t.Fatalf("expected review_required for terminal.run, got %q", strategy)
	}
}

func TestContractStrategyReviewRequiredForExternalPublish(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"web.search", "file.write"},
	}
	strategy := classifyContractStrategy("feishu doc", "publish to feishu cloud doc", contract)
	if strategy != contractStrategyReviewRequired {
		t.Fatalf("expected review_required for external publish, got %q", strategy)
	}
}

func TestContractStrategyReviewRequiredForExplicitPlanRequest(t *testing.T) {
	cases := []string{
		"plan first then deploy",
		"先计划一下",
		"show me the plan",
		"plan first how to explain X",
		"show me the plan for X",
	}
	for _, text := range cases {
		contract := session.TaskContract{RequiresTools: false}
		strategy := classifyContractStrategy(text, text, contract)
		if strategy != contractStrategyReviewRequired {
			t.Fatalf("expected review_required for %q, got %q", text, strategy)
		}
	}
}

func TestContractStrategyReviewRequiredForMultiStepDelivery(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"web.search", "file.write", "terminal.run"},
	}
	strategy := classifyContractStrategy("deploy report", "publish report to server", contract)
	if strategy != contractStrategyReviewRequired {
		t.Fatalf("expected review_required for multi-step delivery, got %q", strategy)
	}
}

// --- Unit tests for shouldPauseForTaskPlan with strategy ---

func TestShouldPauseForTaskPlanDirectNeverPauses(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"web.search"},
	}
	if shouldPauseForTaskPlan(contract, contractStrategyDirect) {
		t.Fatal("direct strategy should never pause")
	}
}

func TestShouldPauseForTaskPlanAutoContractNeverPauses(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools:    true,
		RequiredTools:    []string{"web.search"},
		RequiredEvidence: []session.TaskEvidenceContract{{Tool: "web.search", Description: "test"}},
	}
	if shouldPauseForTaskPlan(contract, contractStrategyAutoContract) {
		t.Fatal("auto_contract strategy should never pause")
	}
}

func TestShouldPauseForTaskPlanReviewRequiredPauses(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.write"},
	}
	if !shouldPauseForTaskPlan(contract, contractStrategyReviewRequired) {
		t.Fatal("review_required with tools should pause")
	}
}

// --- Integration test: trace records contract strategy ---

func TestRuntimeTraceRecordsContractStrategy(t *testing.T) {
	t.Run("direct", func(t *testing.T) {
		rt := newTestRuntime(t)
		rt.Pool.agents["main"] = agentcore.NewAgent(staticTextModel{text: "Mateway is a local agent runtime."}, rt.Tools)
		rt.ContractModel = contractJSONModel{json: `{"summary":"explain Mateway","requires_tools":false,"expected_outcome":"answer"}`}

		resp, err := rt.Handle(context.Background(), inbound("cli:strategy", "explain what Mateway is"))
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(resp.TracePath)
		if err != nil {
			t.Fatal(err)
		}
		trace := string(data)
		if !strings.Contains(trace, `"strategy":"direct"`) {
			t.Fatalf("expected direct strategy in trace, got:\n%s", trace)
		}
	})

	t.Run("auto_contract", func(t *testing.T) {
		rt := newTestRuntime(t)
		registry := agentcore.NewToolRegistry()
		registry.Register(runtimeNamedTool{name: "web.search", content: "weather data"})
		rt.Tools = registry
		rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
			{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "web.search", Args: map[string]any{"query": "weather"}}}},
			{Role: agentcore.RoleAssistant, Content: "Weather is clear."},
		}}, rt.Tools)
		rt.ContractModel = contractJSONModel{json: `{"summary":"check weather","requires_tools":true,"required_tools":["web.search"],"expected_outcome":"weather info"}`}

		resp, err := rt.Handle(context.Background(), inbound("cli:strategy", "check weather today"))
		if err != nil {
			t.Fatal(err)
		}
		if resp.Reply.Style == channel.StyleInputRequired {
			t.Fatalf("auto_contract should not show plan review, got style=%q", resp.Reply.Style)
		}
		data, err := os.ReadFile(resp.TracePath)
		if err != nil {
			t.Fatal(err)
		}
		trace := string(data)
		if !strings.Contains(trace, `"strategy":"auto_contract"`) {
			t.Fatalf("expected auto_contract strategy in trace, got:\n%s", trace)
		}
		if strings.Contains(trace, "task_plan_review") {
			t.Fatalf("auto_contract should not have plan_review trace event:\n%s", trace)
		}
	})

	t.Run("review_required", func(t *testing.T) {
		rt := newTestRuntime(t)
		registry := agentcore.NewToolRegistry()
		registry.Register(runtimeNamedTool{name: "file.write", content: "wrote"})
		registry.Register(runtimeNamedTool{name: "terminal.run", content: "deployed"})
		rt.Tools = registry
		rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
			{Role: agentcore.RoleAssistant, Content: "done"},
		}}, rt.Tools)
		rt.ContractModel = contractJSONModel{json: `{"summary":"deploy report","requires_tools":true,"required_tools":["file.write","terminal.run"],"required_evidence":[{"kind":"local_file","tool":"file.write","description":"report"}],"plan_items":[{"id":"plan-1","title":"write report","status":"pending","tool":"file.write","criteria":"write file"},{"id":"plan-2","title":"deploy","status":"pending","tool":"terminal.run","criteria":"deploy"}],"expected_outcome":"deployed","completion_policy":"must deploy"}`}

		resp, err := rt.Handle(context.Background(), inbound("cli:strategy", "create report and deploy to server"))
		if err != nil {
			t.Fatal(err)
		}
		if resp.Reply.Style != channel.StyleInputRequired {
			t.Fatalf("review_required should show plan review, got style=%q", resp.Reply.Style)
		}
		data, err := os.ReadFile(resp.TracePath)
		if err != nil {
			t.Fatal(err)
		}
		trace := string(data)
		if !strings.Contains(trace, `"strategy":"review_required"`) {
			t.Fatalf("expected review_required strategy in trace, got:\n%s", trace)
		}
	})
}

// --- Integration test: existing contract validation/skill-aware replan still works ---

func TestContractStrategyDoesNotBreakExistingValidation(t *testing.T) {
	rt := newTestRuntime(t)
	registry := agentcore.NewToolRegistry()
	registry.Register(runtimeNamedTool{name: "file.read", content: "ok"})
	registry.Register(runtimeNamedTool{name: "terminal.run", content: "service is running"})
	rt.Tools = registry
	rt.Pool.agents["main"] = agentcore.NewAgent(&sequenceModel{messages: []agentcore.Message{
		{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
			ID: "call_1", Name: "terminal.run", Args: map[string]any{"command": "systemctl status singbox"},
		}}},
		{Role: agentcore.RoleAssistant, Content: "service is running."},
	}}, rt.Tools)
	rt.ContractModel = contractJSONModel{json: `{"summary":"check service status","requires_tools":true,"required_tools":["terminal.run"],"expected_outcome":"status"}`}

	planResp, err := rt.Handle(context.Background(), inbound("cli:valid", "check singbox service status"))
	if err != nil {
		t.Fatal(err)
	}
	if planResp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected plan review for review_required, got style=%q text=%q", planResp.Reply.Style, planResp.Reply.Text)
	}

	resp, err := rt.Handle(context.Background(), inbound("cli:valid", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("expected completion, got failed: %q", resp.Reply.Text)
	}
}
