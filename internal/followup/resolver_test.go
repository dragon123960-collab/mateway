package followup

import (
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/session"
)

func TestResolverDetectsContinue(t *testing.T) {
	r := Resolver{}
	decision := r.Resolve(Input{
		CurrentMessage: "继续",
		LastTask: &session.TaskState{
			UserText:      "搜集现在AI应用的最新趋势和走向",
			ResolvedQuery: "搜集现在AI应用的最新趋势和走向，优先最近 12 个月，输出中文总结",
			Topic:         "AI 应用趋势",
		},
	})
	if !decision.IsFollowup {
		t.Fatalf("expected followup, got %#v", decision)
	}
	if decision.ResolvedQuery != "搜集现在AI应用的最新趋势和走向，优先最近 12 个月，输出中文总结" {
		t.Fatalf("unexpected resolved query %q", decision.ResolvedQuery)
	}
}

func TestResolverDetectsReferenceEdit(t *testing.T) {
	r := Resolver{}
	decision := r.Resolve(Input{
		CurrentMessage: "按刚才那个文件改成更口语一点",
		LastTask: &session.TaskState{
			UserText: "帮我整理 README",
		},
	})
	if !decision.IsFollowup {
		t.Fatalf("expected followup, got %#v", decision)
	}
	if !strings.Contains(decision.ResolvedQuery, "Original task: 帮我整理 README") {
		t.Fatalf("unexpected resolved query %q", decision.ResolvedQuery)
	}
}

func TestResolverTreatsManualAuthDoneAsContinuation(t *testing.T) {
	r := Resolver{}
	decision := r.Resolve(Input{
		CurrentMessage: "已经授权了",
		LastTask: &session.TaskState{
			ResolvedQuery: "用本机的 lark-cli 给飞书发送一条消息",
			Topic:         "飞书消息发送",
		},
	})
	if !decision.IsFollowup {
		t.Fatalf("expected manual auth completion to continue previous task, got %#v", decision)
	}
	if decision.ResolvedQuery != "用本机的 lark-cli 给飞书发送一条消息" {
		t.Fatalf("unexpected resolved query %q", decision.ResolvedQuery)
	}
}

func TestResolverLeavesIndependentRequestAlone(t *testing.T) {
	r := Resolver{}
	decision := r.Resolve(Input{
		CurrentMessage: "现在几点",
		LastTask: &session.TaskState{
			UserText: "帮我整理 README",
		},
	})
	if decision.IsFollowup {
		t.Fatalf("expected independent request, got %#v", decision)
	}
	if decision.ResolvedQuery != "现在几点" {
		t.Fatalf("unexpected resolved query %q", decision.ResolvedQuery)
	}
}
