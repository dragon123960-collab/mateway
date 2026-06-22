package session

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var httpURLPattern = regexp.MustCompile(`https?://[^\s<>"')\]]+`)

type NodeVerificationResult struct {
	Status                    string        // passed | retry | failed | blocked | needs_input | replan | pending
	Reason                    string        // human-readable explanation
	Missing                   []string      // missing evidence or criteria
	EvidenceRefs              []EvidenceRef // supporting evidence
	Confidence                string        // low | medium | high (from model verifier)
	Retryable                 bool          // whether the same node can be retried
	FeedbackForNextAttempt    string        // compact feedback injected into the next attempt
	RequiresHumanConfirmation bool          // needs explicit approval before retrying a guarded mutation
}

const (
	VerificationPassed     = "passed"
	VerificationRetry      = "retry"
	VerificationFailed     = "failed"
	VerificationBlocked    = "blocked"
	VerificationNeedsInput = "needs_input"
	VerificationReplan     = "replan"
	VerificationPending    = "pending"
)

func VerifyNode(node *TaskGraphNode) NodeVerificationResult {
	if node == nil {
		return NodeVerificationResult{Status: VerificationFailed, Reason: "node is nil"}
	}

	if node.Status == NodeStatusSkipped {
		return NodeVerificationResult{Status: VerificationPassed, Reason: "node was skipped"}
	}
	if node.Status == NodeStatusAwaitingInput {
		return NodeVerificationResult{Status: VerificationNeedsInput, Reason: "waiting for human input"}
	}
	if node.Status == NodeStatusNeedsReplan {
		reason := node.FailureReason
		if reason == "" {
			reason = "node needs local replan"
		}
		return NodeVerificationResult{Status: VerificationReplan, Reason: reason}
	}

	if node.Status == NodeStatusBlocked {
		reason := node.FailureReason
		if reason == "" {
			reason = "node is blocked"
		}
		return NodeVerificationResult{
			Status: VerificationBlocked,
			Reason: reason,
		}
	}

	if node.Type != "" && !IsValidNodeType(node.Type) {
		return NodeVerificationResult{Status: VerificationFailed, Reason: fmt.Sprintf("unknown node type %q", node.Type)}
	}

	hasResult := node.FailureReason != "" || node.ResultSummary != "" || len(node.EvidenceRefs) > 0
	isTerminal := node.Status == NodeStatusCompleted || node.Status == NodeStatusFailed || node.Status == NodeStatusNeedsReplan
	if !hasResult && !isTerminal {
		return NodeVerificationResult{Status: VerificationPending, Reason: "node has not been executed or produced no result"}
	}

	switch node.Type {
	case NodeTypeTool:
		return verifyToolNode(node)
	case NodeTypeModel, NodeTypeSubtask:
		return verifyModelNode(node)
	case NodeTypeSkill:
		return verifySkillNode(node)
	case NodeTypeHumanReview, NodeTypeHumanConfirm:
		return verifyHumanNode(node)
	default:
		return NodeVerificationResult{Status: VerificationFailed, Reason: fmt.Sprintf("unknown node type %q", node.Type)}
	}
}

func verifyToolNode(node *TaskGraphNode) NodeVerificationResult {
	if node.FailureReason != "" {
		if strings.Contains(strings.ToLower(node.FailureReason), "timed out") ||
			strings.Contains(strings.ToLower(node.FailureReason), "deadline") {
			return NodeVerificationResult{
				Status:                 VerificationRetry,
				Reason:                 node.FailureReason,
				Missing:                []string{"tool did not complete within deadline"},
				Retryable:              true,
				FeedbackForNextAttempt: node.FailureReason,
			}
		}
		return NodeVerificationResult{
			Status:  VerificationFailed,
			Reason:  node.FailureReason,
			Missing: []string{"tool execution failed"},
		}
	}

	if len(node.EvidenceRefs) == 0 {
		return NodeVerificationResult{
			Status:  VerificationFailed,
			Reason:  "no evidence recorded for tool execution",
			Missing: []string{"tool evidence"},
		}
	}

	return NodeVerificationResult{
		Status:       VerificationPassed,
		EvidenceRefs: node.EvidenceRefs,
	}
}

func verifyModelNode(node *TaskGraphNode) NodeVerificationResult {
	if node.FailureReason != "" {
		return NodeVerificationResult{
			Status:  VerificationFailed,
			Reason:  node.FailureReason,
			Missing: []string{"model output"},
		}
	}

	summary := strings.TrimSpace(node.ResultSummary)
	if summary == "" {
		return NodeVerificationResult{
			Status:  VerificationFailed,
			Reason:  "model produced no output",
			Missing: []string{"model result"},
		}
	}
	if looksLikeUnfinishedNodeOutput(summary) {
		return NodeVerificationResult{
			Status:                 VerificationFailed,
			Reason:                 "node output appears unfinished or requests a tool/input instead of satisfying the node goal",
			Missing:                []string{"completed node result"},
			Confidence:             "hard",
			Retryable:              true,
			FeedbackForNextAttempt: "Produce the completed result for this node. Do not output tool-call markup, future intentions, or requests for missing input unless the node is explicitly a human input node.",
		}
	}
	if blockedToolEvidence(node) {
		return NodeVerificationResult{
			Status:     VerificationBlocked,
			Reason:     "node contains blocked tool evidence",
			Missing:    []string{"allowed tool path or user confirmation/configuration change"},
			Confidence: "hard",
		}
	}
	if unresolvedFailedToolEvidence(node, summary) {
		return NodeVerificationResult{
			Status:                 VerificationFailed,
			Reason:                 "node has only failed tool evidence and did not produce an independent completed result",
			Missing:                []string{"successful tool evidence or completed node result"},
			Confidence:             "hard",
			Retryable:              true,
			FeedbackForNextAttempt: "Retry the node using an allowed safe approach, or report a concrete blocker instead of treating the failed tool result as completed work.",
		}
	}
	if requiresConcreteURL(node) && !hasConcreteURLArtifact(node) {
		if requestsUserConfirmationBeforeMutation(node) {
			return NodeVerificationResult{
				Status:                    VerificationNeedsInput,
				Reason:                    "node requests explicit user confirmation before performing the mutation",
				Missing:                   []string{"user confirmation"},
				Confidence:                "hard",
				RequiresHumanConfirmation: true,
			}
		}
		return NodeVerificationResult{
			Status:                 VerificationFailed,
			Reason:                 "node requires a concrete URL/link result but none was produced",
			Missing:                []string{"concrete http(s) URL"},
			Confidence:             "hard",
			Retryable:              true,
			FeedbackForNextAttempt: "Produce a real http(s) URL in the node result or report the concrete blocker. Do not use placeholder links such as feishu_doc_url.",
		}
	}

	return NodeVerificationResult{
		Status:       VerificationPassed,
		EvidenceRefs: node.EvidenceRefs,
	}
}

func blockedToolEvidence(node *TaskGraphNode) bool {
	if node == nil {
		return false
	}
	for _, ref := range node.EvidenceRefs {
		if ref.Kind == "tool" && ref.Blocked {
			return true
		}
	}
	return false
}

func unresolvedFailedToolEvidence(node *TaskGraphNode, summary string) bool {
	if node == nil || len(node.EvidenceRefs) == 0 {
		return false
	}
	failedTools := 0
	successTools := 0
	for _, ref := range node.EvidenceRefs {
		if ref.Kind != "tool" {
			continue
		}
		if ref.IsError {
			failedTools++
			continue
		}
		successTools++
	}
	if failedTools == 0 || successTools > 0 {
		return false
	}
	return true
}

func requiresConcreteURL(node *TaskGraphNode) bool {
	if node == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{
		node.Goal,
		node.Acceptance.Criteria,
		fmt.Sprint(node.Output["url"]),
		fmt.Sprint(node.Output["link"]),
	}, " "))
	for _, marker := range []string{"url", "link"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func requestsUserConfirmationBeforeMutation(node *TaskGraphNode) bool {
	if node == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{
		node.Goal,
		node.Acceptance.Criteria,
		node.ResultSummary,
		fmt.Sprint(node.Output["text"]),
	}, "\n"))
	if text == "" {
		return false
	}
	hasConfirm := strings.Contains(text, "confirm") ||
		strings.Contains(text, "approval") ||
		strings.Contains(text, "permission") ||
		strings.Contains(text, "human_confirm")
	hasMutation := strings.Contains(text, "create") ||
		strings.Contains(text, "send") ||
		strings.Contains(text, "publish") ||
		strings.Contains(text, "write") ||
		strings.Contains(text, "mutation")
	if !hasConfirm || !hasMutation {
		return false
	}
	if hasConcreteURLArtifact(node) {
		return false
	}
	return true
}

func hasConcreteURLArtifact(node *TaskGraphNode) bool {
	if node == nil {
		return false
	}
	values := []string{
		node.ResultSummary,
		fmt.Sprint(node.Output["url"]),
		fmt.Sprint(node.Output["link"]),
		fmt.Sprint(node.Output["text"]),
	}
	for _, ref := range node.EvidenceRefs {
		values = append(values, ref.Summary)
	}
	for _, value := range values {
		if hasConcreteURL(value) {
			return true
		}
	}
	return false
}

func hasConcreteURL(text string) bool {
	for _, match := range httpURLPattern.FindAllString(text, -1) {
		lower := strings.ToLower(strings.TrimSpace(match))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "example.") ||
			strings.Contains(lower, "placeholder") ||
			strings.Contains(lower, "feishu_doc_url") ||
			strings.Contains(lower, "lark_doc_url") {
			continue
		}
		return true
	}
	return false
}

func looksLikeUnfinishedNodeOutput(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"<tool_call",
		"</tool_call>",
		"\"tool\":",
		"\"tool_name\":",
		"\"function\":",
		"\"params\":",
		"\"arguments\":",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func verifySkillNode(node *TaskGraphNode) NodeVerificationResult {
	return verifyModelNode(node)
}

func verifyHumanNode(node *TaskGraphNode) NodeVerificationResult {
	if node.Status == NodeStatusCompleted {
		if node.Acceptance.Verified {
			return NodeVerificationResult{Status: VerificationPassed}
		}
		return NodeVerificationResult{
			Status: VerificationPassed,
			Reason: "human node marked completed",
		}
	}
	if node.FailureReason != "" {
		return NodeVerificationResult{
			Status: VerificationFailed,
			Reason: node.FailureReason,
		}
	}
	return NodeVerificationResult{
		Status: VerificationNeedsInput,
		Reason: "human node requires input",
	}
}

func ApplyNodeVerification(node *TaskGraphNode, result NodeVerificationResult) {
	if node == nil {
		return
	}
	now := time.Now()
	switch result.Status {
	case VerificationPassed:
		if node.Status != NodeStatusSkipped && node.Status != NodeStatusCompleted {
			node.Status = NodeStatusCompleted
		}
		node.Acceptance.Verified = true
		node.VerifiedAt = now
		if result.Reason != "" && node.Acceptance.Reason == "" {
			node.Acceptance.Reason = result.Reason
		}
	case VerificationRetry:
		node.Status = NodeStatusRetrying
		if result.Reason != "" {
			node.FailureReason = result.Reason
		}
		if result.FeedbackForNextAttempt != "" {
			if node.Input == nil {
				node.Input = map[string]any{}
			}
			node.Input["attempt_feedback"] = result.FeedbackForNextAttempt
		}
	case VerificationFailed:
		if node.Status != NodeStatusBlocked && node.Status != NodeStatusCompleted && node.Status != NodeStatusSkipped {
			node.Status = NodeStatusFailed
		}
		if result.Reason != "" && node.FailureReason == "" {
			node.FailureReason = result.Reason
		}
	case VerificationBlocked:
		node.Status = NodeStatusBlocked
		if result.Reason != "" {
			node.FailureReason = result.Reason
		}
	case VerificationReplan:
		node.Status = NodeStatusNeedsReplan
		if result.Reason != "" {
			node.FailureReason = result.Reason
		}
	case VerificationNeedsInput:
		if node.Status != NodeStatusAwaitingInput {
			node.Status = NodeStatusAwaitingInput
		}
		if result.RequiresHumanConfirmation {
			if node.Input == nil {
				node.Input = map[string]any{}
			}
			node.Input["requires_human_confirmation"] = true
		}
	}
	node.UpdatedAt = now
}

type GraphVerificationResult struct {
	Status       string // passed | failed | blocked | awaiting_input | running
	Reason       string
	NodeResults  map[string]NodeVerificationResult
	MissingNodes []string
}

func VerifyTaskGraph(g *TaskGraph) GraphVerificationResult {
	return VerifyTaskGraphWithContract(g, nil)
}

func VerifyTaskGraphWithContract(g *TaskGraph, contract *TaskContract) GraphVerificationResult {
	_ = contract // Compatibility parameter: graph-native verification is driven by node acceptance.

	result := GraphVerificationResult{
		Status:      GraphStatusCompleted,
		NodeResults: make(map[string]NodeVerificationResult, len(g.Nodes)),
	}

	allPassed := true
	anyFailed := false
	anyBlocked := false
	anyAwaiting := false
	anyPending := false

	for i := range g.Nodes {
		n := &g.Nodes[i]
		nr := VerifyNode(n)
		result.NodeResults[n.ID] = nr

		switch nr.Status {
		case VerificationPassed:
		case VerificationRetry:
			allPassed = false
			anyPending = true
		case VerificationFailed:
			allPassed = false
			anyFailed = true
			result.MissingNodes = append(result.MissingNodes, n.ID)
		case VerificationBlocked:
			allPassed = false
			anyBlocked = true
		case VerificationReplan:
			allPassed = false
			anyFailed = true
			result.MissingNodes = append(result.MissingNodes, n.ID)
		case VerificationNeedsInput:
			allPassed = false
			anyAwaiting = true
		case VerificationPending:
			allPassed = false
			anyPending = true
		}
	}

	switch {
	case anyAwaiting:
		result.Status = GraphStatusAwaitingInput
		result.Reason = "one or more nodes require human input"
	case anyFailed:
		result.Status = GraphStatusFailed
		result.Reason = fmt.Sprintf("verification failed for nodes: %s", strings.Join(result.MissingNodes, ", "))
	case anyBlocked:
		result.Status = GraphStatusBlocked
		result.Reason = "one or more nodes are blocked"
	case anyPending:
		result.Status = GraphStatusRunning
		result.Reason = "one or more nodes are still pending or running"
	case allPassed:
		result.Status = GraphStatusCompleted
	}

	if result.Status == GraphStatusCompleted {
		if unverified := findUnverifiedCriteriaNodes(g); len(unverified) > 0 {
			result.Status = GraphStatusBlocked
			result.Reason = fmt.Sprintf("nodes with unverified acceptance criteria: %s", strings.Join(unverified, ", "))
			result.MissingNodes = unverified
		}
	}

	return result
}

func findUnverifiedCriteriaNodes(g *TaskGraph) []string {
	var unverified []string
	for _, n := range g.Nodes {
		if n.Acceptance.Criteria != "" && !n.Acceptance.Verified {
			unverified = append(unverified, n.ID)
		}
	}
	return unverified
}
