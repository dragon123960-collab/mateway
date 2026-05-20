package model

import (
	"strings"
	"testing"
)

func TestParsePlanRepairsUnescapedChineseQuotes(t *testing.T) {
	raw := `{
  "summary": "读取测试文档、添加一行、运行pwd并写入报告",
  "steps": [
    {
      "id": "step-1",
      "goal": "读取测试文档内容，定位"本轮清单"位置",
      "tool": "file.read",
      "args": {
        "path": "/Users/dongping/project/mateway/docs/测试文档.md"
      },
      "risk": "safe_read",
      "requires_confirm": false,
      "expected_evidence": ["文档内容，包含"本轮清单"章节"]
    }
  ]
}`
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected one step, got %#v", plan.Steps)
	}
	if !strings.Contains(plan.Steps[0].Goal, `"本轮清单"`) {
		t.Fatalf("expected repaired inner quotes, got %q", plan.Steps[0].Goal)
	}
	if !strings.Contains(plan.Steps[0].ExpectedEvidence[0], `"本轮清单"`) {
		t.Fatalf("expected repaired evidence quotes, got %#v", plan.Steps[0].ExpectedEvidence)
	}
}

func TestParsePlanRepairsMissingStepObjectClosures(t *testing.T) {
	raw := `{"summary":"搜索2025-2026年AI趋势，聚焦独立开发者可切入的方向","steps":[{"id":"step-1","goal":"搜索2025-2026年AI技术趋势最新动态","tool":"web.search","args":{"max_results":10,"query":"AI trends 2025 2026 latest developments"},{"id":"step-2","goal":"搜索独立开发者做AI应用的方向和机会","tool":"web.search","args":{"max_results":10,"query":"indie developer AI app opportunities 2025"},{"id":"step-3","goal":"搜索AI应用市场现状和变现模式","tool":"web.search","args":{"max_results":8,"query":"AI application market trends monetization 2025"},{"id":"step-4","goal":"搜索中国AI应用趋势","tool":"web.search","args":{"max_results":8,"query":"2025中国AI应用趋势 独立开发者机会"}]}`
	result, err := CheckAndRepairPlanJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	plan := result.Plan
	if len(plan.Steps) != 4 {
		t.Fatalf("expected four repaired steps, got %#v", plan.Steps)
	}
	if plan.Steps[0].Tool != "web.search" || plan.Steps[3].Args["query"] != "2025中国AI应用趋势 独立开发者机会" {
		t.Fatalf("unexpected repaired plan %#v", plan)
	}
	if !result.Fixed || !containsString(result.Warnings, "repaired_missing_step_object_closures") {
		t.Fatalf("expected repair warning, got fixed=%t warnings=%v", result.Fixed, result.Warnings)
	}
}

func TestPlanCheckerRejectsEmptySteps(t *testing.T) {
	_, err := CheckAndRepairPlanJSON(`{"summary":"empty","steps":[]}`)
	if err == nil || !strings.Contains(err.Error(), "steps must not be empty") {
		t.Fatalf("expected empty steps schema error, got %v", err)
	}
}

func TestPlanCheckerRejectsMissingTool(t *testing.T) {
	_, err := CheckAndRepairPlanJSON(`{"summary":"bad","steps":[{"id":"s1","args":{}}]}`)
	if err == nil || !strings.Contains(err.Error(), "tool is required") {
		t.Fatalf("expected missing tool schema error, got %v", err)
	}
}

func TestPlanCheckerNormalizesMissingIDAndArgs(t *testing.T) {
	result, err := CheckAndRepairPlanJSON(`{"summary":"","steps":[{"tool":"time.now"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Plan.Summary != "execute user task" || result.Plan.Steps[0].ID != "step-1" || result.Plan.Steps[0].Args == nil {
		t.Fatalf("expected normalized plan, got %#v", result.Plan)
	}
	if !result.Fixed {
		t.Fatalf("expected normalization to mark result fixed")
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
