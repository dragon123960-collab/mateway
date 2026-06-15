package runtime

import (
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
)

func TestWebFetchClassifiesRateLimit(t *testing.T) {
	result := agentcore.ToolResult{
		ToolCallID: "call_1",
		Content:    "Edge: Too Many Requests",
		IsError:    true,
	}
	info := ClassifyToolFailure("web.fetch", result)
	if info.Category != FailureRetryable || !strings.Contains(info.Reason, "rate") {
		t.Fatalf("expected rate_limit, got category=%s reason=%q", info.Category, info.Reason)
	}
}

func TestWebFetchClassifiesBotProtection(t *testing.T) {
	cases := []string{
		"Cloudflare challenge page",
		"Please enable cookies and JavaScript",
		"Please enable JS to continue",
		"Disable any ad blocker",
		"CAPTCHA verification required",
	}
	for _, content := range cases {
		result := agentcore.ToolResult{
			ToolCallID: "call_1",
			Content:    content,
			IsError:    true,
		}
		info := ClassifyToolFailure("web.fetch", result)
		if info.Category != FailureBlocked || !strings.Contains(info.Reason, "bot") {
			t.Fatalf("case=%q: expected bot_protection, got category=%s reason=%q", content, info.Category, info.Reason)
		}
	}
}

func TestWebFetchClassifiesTimeout(t *testing.T) {
	cases := []string{
		"context deadline exceeded (Client.Timeout exceeded while awaiting headers)",
		"i/o timeout",
		"request timed out",
	}
	for _, content := range cases {
		result := agentcore.ToolResult{
			ToolCallID: "call_1",
			Content:    content,
			IsError:    true,
		}
		info := ClassifyToolFailure("web.fetch", result)
		if info.Category != FailureRetryable || !strings.Contains(info.Reason, "time") {
			t.Fatalf("case=%q: expected timeout, got category=%s reason=%q", content, info.Category, info.Reason)
		}
	}
}

func TestWebFetchClassifiesHttpError(t *testing.T) {
	result := agentcore.ToolResult{
		ToolCallID: "call_1",
		Content:    "HTTP status 403 Forbidden",
		IsError:    true,
	}
	info := ClassifyToolFailure("web.fetch", result)
	if info.Category != FailureFallback || !strings.Contains(info.Reason, "HTTP") {
		t.Fatalf("expected HTTP error, got category=%s reason=%q", info.Category, info.Reason)
	}
}

func TestWebFetchClassifiesNetworkError(t *testing.T) {
	cases := []string{
		"connection refused",
		"connection reset by peer",
		"no such host",
		"DNS error",
	}
	for _, content := range cases {
		result := agentcore.ToolResult{
			ToolCallID: "call_1",
			Content:    content,
			IsError:    true,
		}
		info := ClassifyToolFailure("web.fetch", result)
		if info.Category != FailureRetryable || !strings.Contains(info.Reason, "network") {
			t.Fatalf("case=%q: expected network error, got category=%s reason=%q", content, info.Category, info.Reason)
		}
	}
}

func TestClassifyTurnFailuresGroupsByTool(t *testing.T) {
	results := []agentcore.ToolResult{
		{ToolCallID: "call_1", Content: "Too Many Requests", IsError: true},
		{ToolCallID: "call_2", Content: "old_string not found in file", IsError: true},
	}
	calls := []agentcore.ToolCall{
		{ID: "call_1", Name: "web.fetch"},
		{ID: "call_2", Name: "file.edit"},
	}
	infos := classifyTurnFailures(results, calls)
	if len(infos) != 2 {
		t.Fatalf("expected 2 classified failures, got %d: %v", len(infos), infos)
	}
	if fi, ok := infos["web.fetch"]; !ok || fi.Category != FailureRetryable {
		t.Fatalf("expected web.fetch as retryable, got %v", infos)
	}
	if fi, ok := infos["file.edit"]; !ok || fi.Category != FailureRetryable {
		t.Fatalf("expected file.edit as retryable, got %v", infos)
	}
}

func TestFetchFailureBudgetExhaustsOnSameURL(t *testing.T) {
	budget := NewFetchFailureBudget(2, 3)
	budget.Record(FetchFailureEntry{
		URL: "https://example.com/page1", Domain: "example.com", Kind: FetchFailureTimeout,
	})
	budget.Record(FetchFailureEntry{
		URL: "https://example.com/page1", Domain: "example.com", Kind: FetchFailureTimeout,
	})
	if !budget.IsExhausted("https://example.com/page1") {
		t.Fatal("expected budget exhausted for same URL after 2 failures")
	}
}

func TestFetchFailureBudgetExhaustsOnSameDomain(t *testing.T) {
	budget := NewFetchFailureBudget(3, 2)
	budget.Record(FetchFailureEntry{
		URL: "https://example.com/a", Domain: "example.com", Kind: FetchFailureNetwork,
	})
	budget.Record(FetchFailureEntry{
		URL: "https://example.com/b", Domain: "example.com", Kind: FetchFailureNetwork,
	})
	if !budget.IsExhausted("https://example.com/c") {
		t.Fatal("expected budget exhausted for same domain after 2 failures")
	}
}

func TestFetchFailureBudgetNotExhaustedOnDifferentDomain(t *testing.T) {
	budget := NewFetchFailureBudget(2, 3)
	budget.Record(FetchFailureEntry{
		URL: "https://example.com/a", Domain: "example.com", Kind: FetchFailureTimeout,
	})
	budget.Record(FetchFailureEntry{
		URL: "https://example.com/a", Domain: "example.com", Kind: FetchFailureTimeout,
	})
	if budget.IsExhausted("https://other.com/page") {
		t.Fatal("expected budget NOT exhausted for different domain")
	}
}

func TestClassifyFetchFailureTimeout(t *testing.T) {
	result := agentcore.ToolResult{
		ToolCallID: "call_1", Content: "i/o timeout", IsError: true,
	}
	kind, reason := ClassifyFetchFailure(result)
	if kind != FetchFailureTimeout {
		t.Fatalf("expected timeout, got kind=%s reason=%q", kind, reason)
	}
}

func TestClassifyFetchFailureNetwork(t *testing.T) {
	result := agentcore.ToolResult{
		ToolCallID: "call_1", Content: "connection refused", IsError: true,
	}
	kind, _ := ClassifyFetchFailure(result)
	if kind != FetchFailureNetwork {
		t.Fatalf("expected network, got kind=%s", kind)
	}
}

func TestClassifyFetchFailureRateLimit(t *testing.T) {
	result := agentcore.ToolResult{
		ToolCallID: "call_1", Content: "too many requests", IsError: true,
	}
	kind, _ := ClassifyFetchFailure(result)
	if kind != FetchFailureRateLimit {
		t.Fatalf("expected rate_limit, got kind=%s", kind)
	}
}

func TestClassifyFetchFailureBotProtection(t *testing.T) {
	result := agentcore.ToolResult{
		ToolCallID: "call_1", Content: "Cloudflare challenge", IsError: true,
	}
	kind, _ := ClassifyFetchFailure(result)
	if kind != FetchFailureBlockedOrBot {
		t.Fatalf("expected blocked_or_bot_protection, got kind=%s", kind)
	}
}

func TestClassifyFetchFailureBotProtectionEvidence(t *testing.T) {
	result := agentcore.ToolResult{
		ToolCallID: "call_1", Content: "blocked", IsError: true,
		Evidence: map[string]any{"failure_kind": "bot_protection"},
	}
	kind, _ := ClassifyFetchFailure(result)
	if kind != FetchFailureBlockedOrBot {
		t.Fatalf("expected blocked_or_bot_protection from evidence, got kind=%s", kind)
	}
}

func TestClassifyFetchFailurePolicyDenied(t *testing.T) {
	result := agentcore.ToolResult{
		ToolCallID: "call_1", Content: "SSRF blocked private address", IsError: true,
	}
	kind, _ := ClassifyFetchFailure(result)
	if kind != FetchFailurePolicyDenied {
		t.Fatalf("expected policy_denied, got kind=%s", kind)
	}
}

func TestFetchFailureBudgetDefaultValues(t *testing.T) {
	budget := NewFetchFailureBudget(0, 0)
	if budget.maxPerURL != 2 {
		t.Fatalf("expected default maxPerURL=2, got %d", budget.maxPerURL)
	}
	if budget.maxPerDomain != 3 {
		t.Fatalf("expected default maxPerDomain=3, got %d", budget.maxPerDomain)
	}
}

func TestFetchBudgetForStateRebuildsFromExecutionEvents(t *testing.T) {
	state := session.State{}
	task := state.StartTask("fetch page")
	state.AddExecutionEvent(task.ID, session.ExecutionEvent{
		Type:   "fetch_failure",
		Status: "failed",
		Tool:   "call_1",
		Evidence: map[string]any{
			"url":          "https://example.com/page",
			"domain":       "example.com",
			"failure_kind": string(FetchFailureTimeout),
		},
	})
	state.AddExecutionEvent(task.ID, session.ExecutionEvent{
		Type:   "fetch_failure",
		Status: "failed",
		Tool:   "call_2",
		Evidence: map[string]any{
			"url":          "https://example.com/page",
			"domain":       "example.com",
			"failure_kind": string(FetchFailureTimeout),
		},
	})
	budget := fetchBudgetForState(&state, task.ID)
	if !budget.IsExhausted("https://example.com/page") {
		t.Fatal("expected rebuilt fetch budget to be exhausted for same URL")
	}
}

func TestFetchBudgetForStateSingleFailureNotExhausted(t *testing.T) {
	state := session.State{}
	task := state.StartTask("fetch page")
	state.AddExecutionEvent(task.ID, session.ExecutionEvent{
		Type:   "fetch_failure",
		Status: "failed",
		Tool:   "call_1",
		Evidence: map[string]any{
			"url":          "https://example.com/page",
			"domain":       "example.com",
			"failure_kind": string(FetchFailureTimeout),
		},
	})
	budget := fetchBudgetForState(&state, task.ID)
	if budget.IsExhausted("https://example.com/page") {
		t.Fatal("single fetch failure must not exhaust default URL budget")
	}
}

func TestSearchEvidenceSatisfiesFetchContract(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"web.fetch"},
		RequiredEvidence: []session.TaskEvidenceContract{
			{Kind: "current_external_fact", Tool: "web.fetch", Description: "example product pricing"},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "p1", Title: "fetch pricing page", Status: "blocked", Tool: "web.fetch", Criteria: "example product pricing"},
			{ID: "p2", Title: "search pricing", Status: "completed", Tool: "web.search", Criteria: "example product pricing"},
		},
	}
	validation := validateTaskContract(contract, session.TaskNode{
		Steps: []session.TaskStep{
			{
				Tool:     "web.search",
				Status:   "accepted",
				Accepted: true,
				Summary:  "Search results include example product pricing from current sources.",
			},
		},
	})
	if !validation.Satisfied {
		t.Fatalf("expected matching web.search evidence to satisfy blocked web.fetch contract, missing=%v", validation.Missing)
	}
}

func TestUnrelatedSearchEvidenceDoesNotSatisfyFetchContract(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"web.fetch"},
		RequiredEvidence: []session.TaskEvidenceContract{
			{Kind: "current_external_fact", Tool: "web.fetch", Description: "example product pricing"},
		},
		PlanItems: []session.TaskPlanItem{
			{ID: "p1", Title: "fetch pricing page", Status: "blocked", Tool: "web.fetch", Criteria: "example product pricing"},
			{ID: "p2", Title: "search weather", Status: "completed", Tool: "web.search", Criteria: "weather"},
		},
	}
	validation := validateTaskContract(contract, session.TaskNode{
		Steps: []session.TaskStep{
			{
				Tool:     "web.search",
				Status:   "accepted",
				Accepted: true,
				Summary:  "Search results include tomorrow weather forecast.",
			},
		},
	})
	if validation.Satisfied {
		t.Fatal("unrelated web.search evidence must not satisfy blocked web.fetch contract")
	}
	if !strings.Contains(strings.Join(validation.Missing, ";"), "example product pricing") {
		t.Fatalf("expected missing fetch evidence to remain visible, got %v", validation.Missing)
	}
}

func TestUnrelatedSearchEvidenceDoesNotSatisfyFetchToolOnlyContract(t *testing.T) {
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"web.fetch"},
		PlanItems: []session.TaskPlanItem{
			{ID: "p1", Title: "fetch pricing page", Status: "blocked", Tool: "web.fetch", Criteria: "example product pricing"},
			{ID: "p2", Title: "search weather", Status: "completed", Tool: "web.search", Criteria: "weather"},
		},
	}
	validation := validateTaskContract(contract, session.TaskNode{
		Steps: []session.TaskStep{
			{
				Tool:     "web.search",
				Status:   "accepted",
				Accepted: true,
				Summary:  "Search results include tomorrow weather forecast.",
			},
		},
	})
	if validation.Satisfied {
		t.Fatal("unrelated web.search evidence must not satisfy blocked web.fetch required_tool")
	}
	if !strings.Contains(strings.Join(validation.Missing, ";"), "tool:web.fetch") {
		t.Fatalf("expected missing web.fetch tool to remain visible, got %v", validation.Missing)
	}
}

func TestAllFetchPlanItemsBlocked(t *testing.T) {
	contract := session.TaskContract{
		PlanItems: []session.TaskPlanItem{
			{ID: "p1", Title: "fetch page", Status: "blocked", Tool: "web.fetch"},
			{ID: "p2", Title: "fetch other", Status: "blocked", Tool: "web.fetch"},
		},
	}
	if !allFetchPlanItemsBlocked(contract) {
		t.Fatal("expected all fetch plan items to be blocked")
	}
	contract.PlanItems[1].Status = "pending"
	if allFetchPlanItemsBlocked(contract) {
		t.Fatal("expected NOT all blocked when one is pending")
	}
}

func TestAllFetchPlanItemsBlockedNoFetchItems(t *testing.T) {
	contract := session.TaskContract{
		PlanItems: []session.TaskPlanItem{
			{ID: "p1", Title: "search", Status: "completed", Tool: "web.search"},
		},
	}
	if allFetchPlanItemsBlocked(contract) {
		t.Fatal("expected false when no fetch plan items exist")
	}
}
