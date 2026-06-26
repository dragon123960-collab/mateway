package session

import (
	"fmt"
	"os"
	"path/filepath"
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
	if missing := missingConcretePathOutputs(node); len(missing) > 0 {
		return NodeVerificationResult{
			Status:                 VerificationFailed,
			Reason:                 "node declares local path output without a concrete existing file path",
			Missing:                missing,
			Confidence:             "hard",
			Retryable:              true,
			FeedbackForNextAttempt: "Write the artifact to disk with file.write or file.edit and return the exact absolute path. Do not use boolean placeholders for *_path outputs.",
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

func missingConcretePathOutputs(node *TaskGraphNode) []string {
	if node == nil || len(node.Output) == 0 {
		return nil
	}
	var missing []string
	for key, value := range node.Output {
		key = strings.TrimSpace(key)
		if !strings.HasSuffix(key, "_path") {
			continue
		}
		if concreteArtifactPathSatisfiesOutput(key, node.Output) {
			continue
		}
		paths := outputStringSlice(value)
		if len(paths) == 0 {
			missing = append(missing, key)
			continue
		}
		found := false
		for _, path := range paths {
			path = strings.TrimSpace(path)
			if path == "" || !filepath.IsAbs(path) {
				continue
			}
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, key)
		}
	}
	return missing
}

func concreteArtifactPathSatisfiesOutput(key string, output map[string]any) bool {
	if output == nil || !strings.HasSuffix(key, "_path") {
		return false
	}
	stem := strings.TrimSuffix(strings.TrimSpace(key), "_path")
	for _, path := range outputStringSlice(output["artifact_paths"]) {
		path = strings.TrimSpace(path)
		if path == "" || !filepath.IsAbs(path) || !artifactPathMatchesFinalOutput(stem, path) {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
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
	// Deterministic layer (always runs, free). The model layer is orchestrated
	// by the runtime (config.Runtime.TaskVerifier) so this function stays pure
	// and side-effect free.
	_ = contract

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
			return result
		}
		// Required final-output keys declared by the contract must be present in
		// some verified node's Output. A missing key is a salvageable synthesis
		// gap (needs_repair), not a hard blocker — the runtime may append a
		// repair/synthesis node to close it.
		if missing := missingFinalOutputs(g, contract); len(missing) > 0 {
			result.Status = GraphStatusNeedsRepair
			result.Reason = fmt.Sprintf("missing final outputs: %s", strings.Join(missing, ", "))
			result.MissingNodes = missing
		}
	}

	return result
}

// missingFinalOutputs returns the contract.FinalOutput keys that are not
// present in any verified node's Output map. Returns nil when contract is nil
// or declares no final outputs.
func missingFinalOutputs(g *TaskGraph, contract *TaskContract) []string {
	if contract == nil || len(contract.FinalOutput) == 0 {
		return nil
	}
	available := make(map[string]bool, len(contract.FinalOutput))
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Status != NodeStatusCompleted && n.Status != NodeStatusSkipped {
			continue
		}
		if n.Acceptance.Criteria != "" && !n.Acceptance.Verified {
			continue
		}
		for key := range n.Output {
			available[key] = true
		}
		for _, key := range contract.FinalOutput {
			key = strings.TrimSpace(key)
			if key == "" || available[key] {
				continue
			}
			if finalOutputSatisfiedByArtifactPath(key, n.Output) {
				available[key] = true
			}
		}
	}
	var missing []string
	for _, key := range contract.FinalOutput {
		key = strings.TrimSpace(key)
		if key == "" || available[key] {
			continue
		}
		missing = append(missing, key)
	}
	return missing
}

func finalOutputSatisfiedByArtifactPath(key string, output map[string]any) bool {
	if output == nil || !strings.HasSuffix(key, "_path") {
		return false
	}
	stem := strings.TrimSuffix(strings.TrimSpace(key), "_path")
	if stem == "" {
		return false
	}
	for _, path := range outputStringSlice(output["artifact_paths"]) {
		if artifactPathMatchesFinalOutput(stem, path) {
			return true
		}
	}
	return false
}

func artifactPathMatchesFinalOutput(stem, path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	stem = strings.ToLower(strings.TrimSpace(stem))
	if path == "" || stem == "" {
		return false
	}
	compactStem := strings.ReplaceAll(stem, "_", "-")
	if strings.Contains(path, compactStem) {
		return true
	}
	parts := strings.Split(compactStem, "-")
	matched := 0
	for _, part := range parts {
		if part == "" {
			continue
		}
		if strings.Contains(path, part) {
			matched++
		}
	}
	return matched > 0 && matched == len(nonEmptyStrings(parts))
}

func outputStringSlice(value any) []string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{strings.TrimSpace(v)}
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}

func nonEmptyStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
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
