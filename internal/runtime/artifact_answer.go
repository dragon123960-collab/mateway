package runtime

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/textmatch"
)

const artifactAnswerThreshold = 7

type artifactMatch struct {
	Task     session.TaskState
	Artifact session.Artifact
	Score    int
}

func (l *AgentLoop) resolveArtifactDirectAnswer() *Response {
	query := strings.TrimSpace(l.state.message.Text)
	if !isArtifactLookupIntent(query) {
		return nil
	}
	matches := findArtifactMatches(query, l.state.session, 3)
	if len(matches) == 0 || matches[0].Score < artifactAnswerThreshold {
		l.runtime.Logger.Event("runtime.artifact_lookup", map[string]any{
			"trace_id":    l.state.traceID,
			"session_key": l.state.message.SessionKey,
			"matched":     false,
		})
		return nil
	}
	text := artifactDirectAnswerText(matches)
	reply := l.runtime.sanitizeReply(channel.OutboundMessage{
		Channel:  l.state.message.Channel,
		ThreadID: l.state.message.ThreadID,
		Text:     text,
		Style:    "reply",
		Title:    "Mateway found a historical artifact",
	})
	resp := Response{Reply: reply, TraceID: l.state.traceID}
	l.runtime.Logger.Event("runtime.artifact_lookup", map[string]any{
		"trace_id":       l.state.traceID,
		"session_key":    l.state.message.SessionKey,
		"matched":        true,
		"match_count":    len(matches),
		"top_kind":       matches[0].Artifact.Kind,
		"top_path":       matches[0].Artifact.Path,
		"top_source_url": matches[0].Artifact.SourceURL,
		"top_task_id":    matches[0].Task.ID,
		"top_score":      matches[0].Score,
	})
	l.runtime.Logger.Event("runtime.reply", map[string]any{
		"trace_id":     l.state.traceID,
		"failed":       false,
		"reply_chars":  len(reply.Text),
		"result_count": 0,
		"direct":       "artifact",
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Reply(l.state.traceID, reply.Text, false)
	}
	l.saveConversationOnly(resp)
	return &resp
}

func isArtifactLookupIntent(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return false
	}
	hasArtifactWord := containsAny(normalized,
		append([]string{"artifact", "url"}, textmatch.Terms("artifact_words")...)...,
	)
	hasHistoricalReference := textmatch.ContainsGroup(normalized, "artifact_historical")
	hasLocationRequest := textmatch.ContainsGroup(normalized, "artifact_location")
	hasDeliveryRequest := textmatch.ContainsGroup(normalized, "artifact_delivery")
	if hasArtifactWord && (hasHistoricalReference || hasLocationRequest || hasDeliveryRequest && containsAny(normalized, append([]string{"url"}, textmatch.Terms("artifact_link_words")...)...)) {
		return true
	}
	return textmatch.ContainsGroup(normalized, "artifact_exact_lookup")
}

func findArtifactMatches(query string, st session.State, limit int) []artifactMatch {
	var matches []artifactMatch
	for _, task := range tasksNewestFirst(st) {
		for _, artifact := range task.Artifacts {
			score := scoreArtifactMatch(query, task, artifact)
			if score <= 0 {
				continue
			}
			matches = append(matches, artifactMatch{Task: task, Artifact: artifact, Score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Task.UpdatedAt.After(matches[j].Task.UpdatedAt)
	})
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func scoreArtifactMatch(query string, task session.TaskState, artifact session.Artifact) int {
	normalized := strings.ToLower(strings.TrimSpace(query))
	if normalized == "" {
		return 0
	}
	if isGenericRecentArtifactLookup(normalized, artifact) {
		score := 8
		score += artifactRecencyScore(normalized, task.UpdatedAt)
		if !artifactHasAddress(artifact) {
			score -= 3
		}
		return score
	}
	score := 0
	if strings.TrimSpace(artifact.Path) != "" && asksForFileArtifact(normalized) {
		score += 4
	}
	if strings.TrimSpace(artifact.SourceURL) != "" && asksForLinkArtifact(normalized) {
		score += 4
	}
	if asksForDocArtifact(normalized) && artifactLooksDocument(artifact) {
		score += 3
	}
	if textmatch.ContainsGroup(normalized, "artifact_location") {
		score += 2
	}
	if textmatch.ContainsGroup(normalized, "artifact_historical") {
		score += 2
	}
	if textmatch.ContainsGroup(normalized, "artifact_delivery") {
		score += 1
	}
	score += artifactKeywordScore(normalized, task, artifact)
	score += artifactRecencyScore(normalized, task.UpdatedAt)
	if score > 0 && !artifactHasAddress(artifact) {
		score -= 3
	}
	return score
}

func isGenericRecentArtifactLookup(query string, artifact session.Artifact) bool {
	if !textmatch.ContainsGroup(query, "artifact_historical") {
		return false
	}
	if !(textmatch.ContainsGroup(query, "artifact_location") || textmatch.ContainsGroup(query, "artifact_delivery")) {
		return false
	}
	if asksForLinkArtifact(query) {
		return strings.TrimSpace(artifact.SourceURL) != ""
	}
	if asksForFileArtifact(query) || asksForDocArtifact(query) {
		return strings.TrimSpace(artifact.Path) != "" || artifactLooksDocument(artifact)
	}
	return artifactHasAddress(artifact)
}

func artifactKeywordScore(query string, task session.TaskState, artifact session.Artifact) int {
	tokens := artifactQueryTokens(query)
	if len(tokens) == 0 {
		return 0
	}
	haystacks := []string{
		task.Topic,
		task.UserText,
		task.ResolvedQuery,
		task.PlanSummary,
		task.ReplyPreview,
		artifact.Kind,
		artifact.Path,
		filepath.Base(artifact.Path),
		artifact.Label,
		artifact.SourceURL,
		filepathBase(artifact.SourceURL),
		artifact.Summary,
	}
	score := 0
	for _, token := range tokens {
		for _, hay := range haystacks {
			if strings.Contains(strings.ToLower(hay), token) {
				score += 2
				break
			}
		}
	}
	return score
}

func artifactQueryTokens(query string) []string {
	stop := textmatch.StopSet("artifact_stop")
	stop["url"] = struct{}{}
	seen := map[string]struct{}{}
	var out []string
	for _, token := range tokenizeForMatch(query) {
		token = strings.ToLower(strings.TrimSpace(token))
		if _, ok := stop[token]; ok {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	markers := []string{"ai", "docker", "nginx", "mateway", "minimax"}
	markers = append(markers, textmatch.Terms("artifact_markers")...)
	for _, marker := range markers {
		if strings.Contains(strings.ToLower(query), strings.ToLower(marker)) {
			if _, ok := seen[marker]; !ok {
				seen[marker] = struct{}{}
				out = append(out, strings.ToLower(marker))
			}
		}
	}
	return out
}

func artifactRecencyScore(query string, updatedAt time.Time) int {
	if updatedAt.IsZero() {
		return 0
	}
	now := time.Now()
	if textmatch.ContainsGroup(query, "artifact_yesterday") && sameDate(updatedAt, now.AddDate(0, 0, -1)) {
		return 4
	}
	if textmatch.ContainsGroup(query, "artifact_recent") && now.Sub(updatedAt) <= 2*time.Hour {
		return 3
	}
	if textmatch.ContainsGroup(query, "artifact_previous") && now.Sub(updatedAt) <= 72*time.Hour {
		return 2
	}
	return 0
}

func artifactDirectAnswerText(matches []artifactMatch) string {
	if len(matches) == 0 {
		return ""
	}
	if len(matches) == 1 {
		return "我找到了最可能的历史产物：\n" + formatArtifactMatch(matches[0])
	}
	var b strings.Builder
	b.WriteString("我找到了这些可能相关的历史产物：")
	for i, match := range matches {
		fmt.Fprintf(&b, "\n%d. %s", i+1, formatArtifactMatch(match))
	}
	return b.String()
}

func formatArtifactMatch(match artifactMatch) string {
	artifact := match.Artifact
	target := firstNonEmpty(artifact.Path, artifact.SourceURL, artifact.Label)
	label := firstNonEmpty(artifact.Label, artifact.Kind, "artifact")
	summary := firstNonEmpty(artifact.Summary, match.Task.Topic, match.Task.PlanSummary)
	parts := []string{fmt.Sprintf("%s: %s", label, target)}
	if summary != "" && summary != target {
		parts = append(parts, "Hint: "+shortenReply(summary, 120))
	}
	if match.Task.Topic != "" {
		parts = append(parts, "From task: "+match.Task.Topic)
	}
	return strings.Join(parts, "\n   ")
}

func tasksNewestFirst(st session.State) []session.TaskState {
	tasks := make([]session.TaskState, 0, len(st.Tasks))
	for _, task := range st.Tasks {
		tasks = append(tasks, task)
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
	})
	return tasks
}

func asksForFileArtifact(text string) bool {
	return textmatch.ContainsGroup(text, "artifact_doc_words")
}

func asksForDocArtifact(text string) bool {
	return containsAny(text, append([]string{"doc", "pdf", "md"}, textmatch.Terms("artifact_document_words")...)...)
}

func asksForLinkArtifact(text string) bool {
	return containsAny(text, append([]string{"url"}, textmatch.Terms("artifact_link_words")...)...)
}

func artifactLooksDocument(artifact session.Artifact) bool {
	text := strings.ToLower(strings.Join([]string{artifact.Kind, artifact.Path, artifact.Label, artifact.Summary}, " "))
	if containsAny(text, append([]string{"document", "doc"}, textmatch.Terms("artifact_document_kind")...)...) {
		return true
	}
	ext := strings.ToLower(filepath.Ext(artifact.Path))
	switch ext {
	case ".md", ".txt", ".doc", ".docx", ".pdf", ".html", ".htm":
		return true
	default:
		return false
	}
}

func artifactHasAddress(artifact session.Artifact) bool {
	return strings.TrimSpace(artifact.Path) != "" || strings.TrimSpace(artifact.SourceURL) != ""
}

func sameDate(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func containsAny(text string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(text, strings.ToLower(part)) {
			return true
		}
	}
	return false
}
