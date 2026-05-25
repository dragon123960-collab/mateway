package eval

import (
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/model"
)

func TestRenderRoutingMarkdownIncludesUnderstandingAndSteps(t *testing.T) {
	text := RenderRoutingMarkdown(RoutingSummary{
		Total:  1,
		Passed: 1,
		Results: []RoutingResult{{
			Name:   "install-cli",
			User:   "安装 lark cli",
			Passed: true,
			Tools:  []string{"software.search", "software.install"},
			Plan: model.Plan{
				Summary: "install cli",
				Understanding: model.UnderstandingJSON{
					Goal:      "install lark cli",
					Subtasks:  []string{"find install method", "run install"},
					ToolNeeds: []string{"software.search", "software.install"},
				},
				Steps: []model.PlanStep{
					{ID: "s1", Tool: "software.search", Goal: "find install method"},
				},
			},
		}},
	})
	for _, want := range []string{
		"# Routing Eval",
		"Understanding goal: install lark cli",
		"Subtasks: find install method | run install",
		"Tool needs: software.search, software.install",
		"`s1` tool=`software.search` goal=find install method",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected markdown to contain %q, got %q", want, text)
		}
	}
}

func TestUnderstandingExpectationErrorsChecksToolNeedsAndSubtasks(t *testing.T) {
	errs := understandingExpectationErrors(RoutingCase{
		ExpectNeeds:    []string{"software.search", "software.install"},
		ExpectSubtasks: true,
	}, model.UnderstandingJSON{
		Goal:      "install lark cli",
		ToolNeeds: []string{"software.search"},
	})
	if len(errs) != 2 {
		t.Fatalf("expected 2 understanding errors, got %#v", errs)
	}
}

func TestUnderstandingExpectationAllowsFileReadForFileSummaryNeed(t *testing.T) {
	errs := understandingExpectationErrors(RoutingCase{
		ExpectNeeds: []string{"file.summary"},
	}, model.UnderstandingJSON{
		Goal:      "summarize local files",
		ToolNeeds: []string{"file.read"},
	})
	if len(errs) != 0 {
		t.Fatalf("expected file.read to satisfy file.summary need, got %#v", errs)
	}
}

func TestRoutingEvalDowngradesExpectedSoftwareInstallRepairBoundary(t *testing.T) {
	errs := routingBlockingVerificationErrors(RoutingCase{Name: "complex-install-diagnose"}, []string{"step-2: missing required arg command"})
	if len(errs) != 0 {
		t.Fatalf("expected missing install command to be downgraded for routing eval, got %#v", errs)
	}
	warnings := downgradeExpectedRoutingErrors(RoutingCase{Name: "complex-install-diagnose"}, []string{"step-2: missing required arg command"})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "expected repair boundary") {
		t.Fatalf("expected repair boundary warning, got %#v", warnings)
	}
}
