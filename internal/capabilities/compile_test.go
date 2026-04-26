package capabilities

import (
	"testing"

	"github.com/dongping/mateway/internal/agents"
	"github.com/dongping/mateway/internal/tools"
)

func TestCompileAndNarrow(t *testing.T) {
	specs := []tools.Spec{
		{Name: "read_file", Kind: tools.KindBuiltin},
		{Name: "spawn", Kind: tools.KindBuiltin},
		{Name: "skill_a", Kind: tools.KindSkill},
		{Name: "skill_b", Kind: tools.KindSkill},
	}
	parent := Compile("/tmp/ws", tools.Scope{AgentName: "coordinator"}, agents.Profile{
		Name:          "coordinator",
		BuiltinTools:  []string{"read_file", "spawn"},
		AllowedSkills: []string{"skill_a", "skill_b"},
		CanSpawn:      true,
		AsyncAllowed:  true,
	}, specs)
	child := Compile("/tmp/ws", tools.Scope{AgentName: "worker"}, agents.Profile{
		Name:          "worker",
		BuiltinTools:  []string{"read_file"},
		AllowedSkills: []string{"skill_a"},
		CanSpawn:      false,
	}, specs)
	narrowed := Narrow(parent, child)
	if Allows(narrowed, "spawn") {
		t.Fatal("spawn should be narrowed away")
	}
	if !Allows(narrowed, "read_file") || !Allows(narrowed, "skill_a") {
		t.Fatalf("unexpected narrowed capabilities: %#v", narrowed)
	}
}

func TestApplyScopePolicy(t *testing.T) {
	base := Effective{
		AgentName:     "default",
		VisibleTools:  []string{"exec", "read_file"},
		CallableTools: []string{"exec", "read_file"},
	}
	got := ApplyScopePolicy(base, tools.Scope{Channel: "feishu"})
	if Allows(got, "exec") {
		t.Fatalf("expected exec to be filtered for feishu scope: %#v", got)
	}
	if !Allows(got, "read_file") {
		t.Fatalf("expected read_file to remain: %#v", got)
	}
}
