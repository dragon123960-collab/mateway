package memory

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type ReviewProposalOptions struct {
	AgentID string
	Review  string
	Kind    string
	Target  string
	Items   []MemoryItem
	At      time.Time
}

func BuildLongMemoryReviewProposal(opts ReviewProposalOptions) (ProposalInput, bool) {
	if len(opts.Items) == 0 {
		return ProposalInput{}, false
	}
	now := opts.At
	if now.IsZero() {
		now = time.Now()
	}
	agentID := firstNonEmptyMemory(opts.AgentID, "main")
	title := "Long memory review " + now.Format("2006-01-02")
	if strings.TrimSpace(opts.Review) == "" {
		opts.Review = "soon_or_stale"
	}
	body := renderLongMemoryReviewProposalBody(opts, now)
	return ProposalInput{
		AgentID:    agentID,
		Scope:      "agent",
		Type:       "project",
		Title:      title,
		Body:       body,
		Sources:    reviewProposalSources(opts.Items),
		Tags:       []string{"memory-review", "stale-review", "auto-proposal"},
		Confidence: "low",
		CreatedAt:  now,
	}, true
}

func renderLongMemoryReviewProposalBody(opts ReviewProposalOptions, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This proposal summarizes long memory entries that should be reviewed as of %s. Review the listed items before continuing to rely on them as default long memory.\n\n", now.Format("2006-01-02"))
	fmt.Fprintln(&b, "## Review Scope")
	fmt.Fprintf(&b, "- Review filter: %s\n", firstNonEmptyMemory(opts.Review, "soon_or_stale"))
	if strings.TrimSpace(opts.Kind) != "" {
		fmt.Fprintf(&b, "- Kind filter: %s\n", strings.TrimSpace(opts.Kind))
	}
	if strings.TrimSpace(opts.Target) != "" {
		fmt.Fprintf(&b, "- Target filter: %s\n", strings.TrimSpace(opts.Target))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Items Requiring Review")
	for _, item := range opts.Items {
		fmt.Fprintf(&b, "- [%s] %s (%s, updated=%s)\n", reviewLabelForItem(item), item.Title, item.Kind, firstNonEmptyMemory(item.Updated, "unknown"))
		fmt.Fprintf(&b, "  - path: %s\n", item.Path)
		if suggestion := reviewSuggestionForItem(item); suggestion != "" {
			fmt.Fprintf(&b, "  - suggestion: %s\n", suggestion)
		}
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Review Notes")
	fmt.Fprintln(&b, "- Re-validate stale entries before relying on them in new tasks.")
	fmt.Fprintln(&b, "- If an entry is still valid, refresh its evidence or update timestamp through the normal review flow.")
	fmt.Fprintln(&b, "- If an entry is obsolete, reject or replace it through a reviewed memory workflow instead of silently deleting it.")
	return strings.TrimSpace(b.String())
}

func reviewProposalSources(items []MemoryItem) []string {
	var out []string
	for _, item := range items {
		if strings.TrimSpace(item.Path) == "" {
			continue
		}
		out = append(out, "file:"+item.Path)
		if len(out) >= 12 {
			break
		}
	}
	if len(out) == 0 {
		return []string{"manual"}
	}
	return out
}

func reviewLabelForItem(item MemoryItem) string {
	updated := strings.TrimSpace(item.Updated)
	if updated == "" {
		return "unknown"
	}
	day, err := time.Parse("2006-01-02", updated)
	if err != nil {
		return "unknown"
	}
	ageDays := int(time.Now().Sub(day).Hours() / 24)
	switch {
	case ageDays >= 30:
		return "stale"
	case ageDays >= 14:
		return "soon"
	default:
		return "fresh"
	}
}

func reviewSuggestionForItem(item MemoryItem) string {
	switch strings.TrimSpace(reviewLabelForItem(item)) {
	case "stale":
		return "re-validate this entry before relying on it as default long memory"
	case "soon":
		return "review this entry soon if it still affects active work"
	default:
		return ""
	}
}

func reviewTargetForKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case "decision":
		return "decision-style long memory"
	case "playbook":
		return "workflow/playbook-style long memory"
	case "preference":
		return "preference-style long memory"
	case "project":
		return "project fact/note-style long memory"
	default:
		return ""
	}
}

func filterReviewProposalItemsByTarget(target string, items []MemoryItem) []MemoryItem {
	target = strings.TrimSpace(target)
	if target == "" {
		return items
	}
	var out []MemoryItem
	for _, item := range items {
		if strings.EqualFold(reviewTargetForKind(item.Kind), target) {
			out = append(out, item)
		}
	}
	return out
}

func normalizeReviewProposalItemPath(item MemoryItem) string {
	return filepath.ToSlash(item.Path)
}
