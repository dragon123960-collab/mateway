package cli

import (
	"strings"
	"testing"
)

func TestParseSlash(t *testing.T) {
	cmd, ok := ParseSlash("/resume --attach feishu:thread")
	if !ok {
		t.Fatal("expected slash command")
	}
	if cmd.Name != "resume" || len(cmd.Args) != 2 || cmd.Args[0] != "--attach" || cmd.Args[1] != "feishu:thread" {
		t.Fatalf("unexpected command: %#v", cmd)
	}
	if _, ok := ParseSlash("hello"); ok {
		t.Fatal("plain text should not parse as slash command")
	}
}

func TestChatHelpIncludesTools(t *testing.T) {
	var out strings.Builder
	printChatHelp(&out)
	if !strings.Contains(out.String(), "/tools [--agent <agent_id>] [--verbose]") {
		t.Fatalf("help missing tools:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "/model [--agent <agent_id>] [--verbose]") {
		t.Fatalf("help missing model:\n%s", out.String())
	}
}
