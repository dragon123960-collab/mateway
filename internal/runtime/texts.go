package runtime

import (
	"strings"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
)

func runtimeText(cfg *config.Root, msg channel.InboundMessage, key string, values map[string]string) string {
	text := runtimeTexts[key]
	if text == "" {
		text = key
	}
	for key, value := range values {
		text = strings.ReplaceAll(text, "{"+key+"}", value)
	}
	return text
}

func textValues(pairs ...string) map[string]string {
	out := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[pairs[i]] = pairs[i+1]
	}
	return out
}

func runtimeCueList(cfg *config.Root, key string) []string {
	return runtimeCues[key]
}

var runtimeTexts = map[string]string{
	"runtime.context_budget_exceeded":   "The session context is too large to continue safely. Start a new task with /new or narrow the request.",
	"runtime.activity_timeout":          "The task stopped because no progress was observed before the inactivity timeout.",
	"runtime.partial.empty":             "I stopped before producing a complete answer.",
	"runtime.partial.prefix":            "Partial result: {text}",
	"runtime.session_reset.done":        "Started a new session.",
	"runtime.session_reset.archived":    "Old session archived: {archive_path}",
	"runtime.invalid_tool_call":         "The model produced an invalid tool call, so I stopped safely.",
	"runtime.empty_reply":               "I do not have a substantive reply yet.",
	"runtime.error.timeout":             "The model request timed out. You can ask me to continue.",
	"runtime.error.missing_api_key":     "The configured model API key is missing.",
	"runtime.error.all_models_failed":   "All configured models failed for this request.",
	"runtime.error.generic":             "The runtime hit an error and stopped safely. You can ask me to continue or send /new to start over.",
	"runtime.heuristic.echo":            "Received: {text}",
	"memory.proposal_review.header":     "Memory proposal: ",
	"memory.proposal_review.type":       "\nType: ",
	"memory.proposal_review.confidence": "\nConfidence: ",
	"memory.proposal_review.summary":    "\nSummary: ",
	"memory.proposal_review.sources":    "\nSources: ",
	"memory.proposal_review.show":       "\n\nReview: mateway memory proposal show {proposal_id}",
	"memory.proposal_review.commit":     "\nSave: reply 1 or run mateway memory proposal commit {proposal_id}",
	"memory.proposal_review.reject":     "\nIgnore: reply 2 or run mateway memory proposal reject {proposal_id}",
	"memory.proposal_review.reply":      "\n\nReply 1 to save, or 2 to ignore.",
	"memory.commit.error":               "Failed to save memory: {error}",
	"memory.commit.done":                "Saved memory: {target}",
	"memory.reject.error":               "Failed to ignore memory proposal: {error}",
	"memory.reject.done":                "Ignored this memory proposal.",
}

var runtimeCues = map[string][]string{
	"router.partial.already_marked": {"partial"},
	"router.input_request.contains": {"?", "which", "what", "where", "when", "who", "how", "please provide", "need", "missing"},
	"router.input_request.question": {"which", "what", "where", "when", "who", "how"},
	"memory.safe_read.markers":      {"memory", "remember", "preference", "project", "readme", "tool"},
}
