package session

import (
	"fmt"
	"strings"
	"time"
)

type NodeVerificationResult struct {
	Status       string        // passed | failed | blocked | needs_input
	Reason       string        // human-readable explanation
	Missing      []string      // missing evidence or criteria
	EvidenceRefs []EvidenceRef // supporting evidence
	Confidence   string        // low | medium | high (from model verifier)
}

const (
	VerificationPassed     = "passed"
	VerificationFailed     = "failed"
	VerificationBlocked    = "blocked"
	VerificationNeedsInput = "needs_input"
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
	isTerminal := node.Status == NodeStatusCompleted || node.Status == NodeStatusFailed
	if !hasResult && !isTerminal {
		return NodeVerificationResult{Status: VerificationPending, Reason: "node has not been executed or produced no result"}
	}

	switch node.Type {
	case NodeTypeTool:
		return verifyToolNode(node)
	case NodeTypeModel:
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
				Status:  VerificationFailed,
				Reason:  node.FailureReason,
				Missing: []string{"tool did not complete within deadline"},
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

	return NodeVerificationResult{
		Status:       VerificationPassed,
		EvidenceRefs: node.EvidenceRefs,
	}
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
	case VerificationNeedsInput:
		if node.Status != NodeStatusAwaitingInput {
			node.Status = NodeStatusAwaitingInput
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
		case VerificationFailed:
			allPassed = false
			anyFailed = true
			result.MissingNodes = append(result.MissingNodes, n.ID)
		case VerificationBlocked:
			allPassed = false
			anyBlocked = true
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

	if contract != nil && result.Status == GraphStatusCompleted {
		missing := validateContractAgainstNodes(contract, g)
		if len(missing) > 0 {
			result.Status = GraphStatusFailed
			result.Reason = fmt.Sprintf("task contract unsatisfied: missing %s", strings.Join(missing, ", "))
			result.MissingNodes = missing
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

func validateContractAgainstNodes(contract *TaskContract, g *TaskGraph) []string {
	var missing []string
	completedTools := make(map[string]bool)

	for _, n := range g.Nodes {
		if n.Type == NodeTypeTool && n.Status == NodeStatusCompleted {
			completedTools[n.Executor] = true
		}
	}

	for _, tool := range contract.RequiredTools {
		if !completedTools[tool] {
			missing = append(missing, "tool:"+tool)
		}
	}

	for _, evidence := range contract.RequiredEvidence {
		if evidence.Tool != "" && !completedTools[evidence.Tool] {
			missing = append(missing, "evidence:"+evidence.Tool)
		}
	}

	return missing
}
