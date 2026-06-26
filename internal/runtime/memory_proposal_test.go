package runtime

import (
	"path/filepath"
	"testing"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/session"
)

func TestHandleGraphHumanPendingRequiresNumericReply(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("confirm write")
	task.Graph = newTestGraph(session.TaskGraphNode{
		ID:     "confirm",
		Type:   session.NodeTypeHumanConfirm,
		Goal:   "confirm write",
		Status: session.NodeStatusAwaitingInput,
	})
	state.Pending = &session.PendingAction{
		Kind:    session.PendingKindHumanConfirm,
		TaskID:  task.ID,
		GraphID: task.Graph.ID,
		NodeID:  "confirm",
	}

	resp, handled, err := rt.handleGraphHumanPending(&state, channel.InboundMessage{Channel: "cli", ThreadID: "test", Text: "yes"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected invalid non-numeric response to be handled")
	}
	if resp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected input_required reply, got %q", resp.Reply.Style)
	}
	if state.Pending == nil {
		t.Fatal("pending should remain after invalid reply")
	}
	if task.Graph.NodeByID("confirm").Status != session.NodeStatusAwaitingInput {
		t.Fatalf("node should remain awaiting input, got %q", task.Graph.NodeByID("confirm").Status)
	}
}

func TestHandleGraphHumanConfirmRejectsAdditionalContext(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("confirm publish")
	task.Graph = newTestGraph(session.TaskGraphNode{
		ID:     "confirm",
		Type:   session.NodeTypeHumanConfirm,
		Goal:   "confirm publish",
		Status: session.NodeStatusAwaitingInput,
	})
	state.Pending = &session.PendingAction{
		Kind:     session.PendingKindHumanConfirm,
		TaskID:   task.ID,
		GraphID:  task.Graph.ID,
		NodeID:   "confirm",
		Question: "Confirm publish details.",
	}

	resp, handled, err := rt.handleGraphHumanPending(&state, channel.InboundMessage{Channel: "cli", ThreadID: "test", Text: "title: weather, content: previous result"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected non-numeric confirmation reply to be handled")
	}
	if resp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected input_required reply, got %q", resp.Reply.Style)
	}
	if state.Pending == nil {
		t.Fatal("pending should remain")
	}
	node := task.Graph.NodeByID("confirm")
	if len(node.Input) != 0 {
		t.Fatalf("human_confirm should not absorb additional context, got %#v", node.Input)
	}
	if node.Status != session.NodeStatusAwaitingInput {
		t.Fatalf("node should remain awaiting input, got %q", node.Status)
	}
}

func TestHandleGraphHumanConfirmRevisionFeedbackResumesGraph(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("confirm artifacts")
	task.Graph = newTestGraph(session.TaskGraphNode{
		ID:            "metadata",
		Type:          session.NodeTypeHumanConfirm,
		Goal:          "confirm generated artifacts",
		Status:        session.NodeStatusAwaitingInput,
		ResultSummary: "Reply 1 to confirm.",
	})
	state.Pending = &session.PendingAction{
		Kind:     session.PendingKindHumanConfirm,
		TaskID:   task.ID,
		GraphID:  task.Graph.ID,
		NodeID:   "metadata",
		Question: "Reply 1 to confirm.",
	}

	_, handled, err := rt.handleGraphHumanPending(&state, channel.InboundMessage{
		Channel:  "cli",
		ThreadID: "test",
		Text:     "不要确认完成，deck-horizontal.html 不存在，请修复。",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("revision feedback should fall through to graph execution")
	}
	if state.Pending != nil {
		t.Fatal("pending should clear after revision feedback")
	}
	node := task.Graph.NodeByID("metadata")
	if node.Status != session.NodeStatusPending {
		t.Fatalf("expected node reset to pending, got %q", node.Status)
	}
	if node.Input["human_feedback"] == "" || node.Input["attempt_feedback"] == "" {
		t.Fatalf("expected human feedback recorded, got %#v", node.Input)
	}
}

func TestParseNumericHumanPendingActionAllowsDecoratedSingleDigit(t *testing.T) {
	cases := map[string]string{
		"1":         "confirm",
		"确认1":       "confirm",
		"confirm 1": "confirm",
		"2":         "cancel",
		"取消2":       "cancel",
	}
	for input, want := range cases {
		got, ok := parseNumericHumanPendingAction(input)
		if !ok || got != want {
			t.Fatalf("parseNumericHumanPendingAction(%q) = %q,%v want %q,true", input, got, ok, want)
		}
	}
	for _, input := range []string{"确认", "确认12", "1 or 2"} {
		if got, ok := parseNumericHumanPendingAction(input); ok {
			t.Fatalf("parseNumericHumanPendingAction(%q) = %q,true want false", input, got)
		}
	}
}

func TestHandleGraphHumanReviewAcceptsDirectInformation(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("collect missing info")
	task.Graph = newTestGraph(session.TaskGraphNode{
		ID:     "review",
		Type:   session.NodeTypeHumanReview,
		Goal:   "provide target folder and title",
		Status: session.NodeStatusAwaitingInput,
	})
	state.Pending = &session.PendingAction{
		Kind:    session.PendingKindHumanReview,
		TaskID:  task.ID,
		GraphID: task.Graph.ID,
		NodeID:  "review",
	}

	_, handled, err := rt.handleGraphHumanPending(&state, channel.InboundMessage{Channel: "cli", ThreadID: "test", Text: "personal space, title: AI report"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("valid human review response should fall through to graph execution")
	}
	if state.Pending != nil {
		t.Fatal("pending should clear after direct information")
	}
	node := task.Graph.NodeByID("review")
	if node.Status != session.NodeStatusCompleted || !node.Acceptance.Verified {
		t.Fatalf("expected review node completed and verified, got status=%q verified=%v", node.Status, node.Acceptance.Verified)
	}
	if node.ResultSummary != "personal space, title: AI report" {
		t.Fatalf("expected direct information to be recorded, got %q", node.ResultSummary)
	}
	if node.Output["text"] != "personal space, title: AI report" {
		t.Fatalf("expected direct information output, got %#v", node.Output)
	}
}

func TestHandleGraphHumanReviewWorkflowLaneContinuesToFinalize(t *testing.T) {
	rt := newTestRuntime(t)
	workspace := t.TempDir()
	skill := discoveredSkill{
		Name:        "creator-ppt-production-studio",
		Path:        filepath.Join(workspace, "skills", "creator-ppt-production-studio", "SKILL.md"),
		Granularity: "workflow",
		Outputs:     []string{"xiaohongshu_script_path", "deck_horizontal_path"},
		AllowedTools: []string{
			"file.read",
			"file.write",
			"file.edit",
		},
		HumanGates: []string{"review generated scripts and slide outline before deck generation"},
	}
	g, _ := buildWorkflowLaneGraph("task-workflow", "使用 creator-ppt-production-studio 生成小红书口播稿和 HTML PPT", workspace, skill)
	for i := range g.Nodes {
		switch g.Nodes[i].ID {
		case "load-workflow-skill", "draft-workflow-artifacts":
			g.Nodes[i].Status = session.NodeStatusCompleted
			g.Nodes[i].Acceptance.Verified = true
		case "review-workflow-gate":
			g.Nodes[i].Status = session.NodeStatusAwaitingInput
		}
	}
	state := session.State{Key: "cli:test"}
	task := state.StartTask("workflow")
	task.Graph = &g
	state.ActiveTask = task.ID
	state.Pending = &session.PendingAction{
		Kind:     session.PendingKindHumanReview,
		TaskID:   task.ID,
		GraphID:  g.ID,
		NodeID:   "review-workflow-gate",
		Question: "请审核口播稿并选择风格",
	}

	_, handled, err := rt.handleGraphHumanPending(&state, channel.InboundMessage{
		Channel:  "cli",
		ThreadID: "test",
		Text:     "口播稿不用修改，选择 H02 风格继续。",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("workflow human review should fall through to graph execution")
	}
	if state.Pending != nil {
		t.Fatal("pending should clear")
	}
	review := task.Graph.NodeByID("review-workflow-gate")
	if review.Status != session.NodeStatusCompleted || !review.Acceptance.Verified {
		t.Fatalf("expected review completed, got status=%q verified=%v", review.Status, review.Acceptance.Verified)
	}
	if review.Output["text"] != "口播稿不用修改，选择 H02 风格继续。" {
		t.Fatalf("expected user review text output, got %#v", review.Output)
	}
	ready := session.ReadyNodes(task.Graph, 10)
	if len(ready) != 1 || ready[0] != "finalize-workflow-artifacts" {
		t.Fatalf("expected finalize node ready, got %v", ready)
	}
}

func TestHandleGraphHumanPendingNumericConfirmCompletesNode(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("confirm write")
	task.Graph = newTestGraph(session.TaskGraphNode{
		ID:     "confirm",
		Type:   session.NodeTypeHumanConfirm,
		Goal:   "confirm write",
		Status: session.NodeStatusAwaitingInput,
	})
	state.Pending = &session.PendingAction{
		Kind:    session.PendingKindHumanConfirm,
		TaskID:  task.ID,
		GraphID: task.Graph.ID,
		NodeID:  "confirm",
	}

	_, handled, err := rt.handleGraphHumanPending(&state, channel.InboundMessage{Channel: "cli", ThreadID: "test", Text: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("valid human pending response should fall through to graph execution")
	}
	if state.Pending != nil {
		t.Fatal("pending should clear after confirmation")
	}
	node := task.Graph.NodeByID("confirm")
	if node.Status != session.NodeStatusCompleted || !node.Acceptance.Verified {
		t.Fatalf("expected confirmed node completed and verified, got status=%q verified=%v", node.Status, node.Acceptance.Verified)
	}
}

func TestHandleGraphHumanPendingNumericConfirmRequeuesMutationNode(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("publish document")
	task.Graph = newTestGraph(session.TaskGraphNode{
		ID:            "publish",
		Type:          session.NodeTypeSkill,
		Mode:          session.NodeModeSkill,
		Goal:          "create document and return URL",
		Status:        session.NodeStatusAwaitingInput,
		ResultSummary: "Dry-run passed. Need human_confirm approval before create mutation.",
		Output:        map[string]any{"url": true},
		Acceptance: session.Acceptance{
			Reason: "node requests explicit user confirmation before performing the mutation",
		},
	})
	state.Pending = &session.PendingAction{
		Kind:    session.PendingKindHumanConfirm,
		TaskID:  task.ID,
		GraphID: task.Graph.ID,
		NodeID:  "publish",
	}

	_, handled, err := rt.handleGraphHumanPending(&state, channel.InboundMessage{Channel: "cli", ThreadID: "test", Text: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("valid human pending response should fall through to graph execution")
	}
	if state.Pending != nil {
		t.Fatal("pending should clear after confirmation")
	}
	node := task.Graph.NodeByID("publish")
	if node.Status != session.NodeStatusPending {
		t.Fatalf("expected mutation node requeued, got %q", node.Status)
	}
	if node.Input["user_confirmed"] != true {
		t.Fatalf("expected user_confirmed input, got %#v", node.Input)
	}
}
