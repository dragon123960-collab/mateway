package capabilities

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/dongping/mateway/internal/agents"
	"github.com/dongping/mateway/internal/tools"
)

type Effective struct {
	AgentName           string   `json:"agent_name"`
	VisibleTools        []string `json:"visible_tools,omitempty"`
	CallableTools       []string `json:"callable_tools,omitempty"`
	VisibleSkills       []string `json:"visible_skills,omitempty"`
	AllowedMCPProviders []string `json:"allowed_mcp_providers,omitempty"`
	ReadablePaths       []string `json:"readable_paths,omitempty"`
	WritablePaths       []string `json:"writable_paths,omitempty"`
	CanSpawn            bool     `json:"can_spawn"`
	AsyncAllowed        bool     `json:"async_allowed"`
	PolicySource        []string `json:"policy_source,omitempty"`
}

func Compile(workspace string, scope tools.Scope, profile agents.Profile, specs []tools.Spec) Effective {
	effective := Effective{
		AgentName:           firstNonEmpty(profile.Name, scope.AgentName, "default"),
		VisibleTools:        make([]string, 0, len(specs)),
		CallableTools:       make([]string, 0, len(specs)),
		VisibleSkills:       make([]string, 0),
		AllowedMCPProviders: append([]string(nil), profile.MCPTags...),
		ReadablePaths:       []string{workspace},
		WritablePaths:       []string{workspace},
		CanSpawn:            profile.CanSpawn,
		AsyncAllowed:        profile.AsyncAllowed,
		PolicySource:        []string{"agent"},
	}

	allowedBuiltins := toSet(profile.BuiltinTools)
	allowedSkills := toSet(profile.AllowedSkills)
	agentRestrictedBuiltins := len(allowedBuiltins) > 0
	agentRestrictedSkills := len(allowedSkills) > 0

	for _, spec := range specs {
		switch spec.Kind {
		case tools.KindBuiltin:
			if agentRestrictedBuiltins && !allowedBuiltins[spec.Name] {
				continue
			}
			if spec.Name == "spawn" && !profile.CanSpawn {
				continue
			}
			if spec.Name == "wait_agent" && !profile.CanSpawn {
				continue
			}
			effective.VisibleTools = append(effective.VisibleTools, spec.Name)
			effective.CallableTools = append(effective.CallableTools, spec.Name)
		case tools.KindSkill:
			if agentRestrictedSkills && !allowedSkills[spec.Name] {
				continue
			}
			effective.VisibleTools = append(effective.VisibleTools, spec.Name)
			effective.CallableTools = append(effective.CallableTools, spec.Name)
			effective.VisibleSkills = append(effective.VisibleSkills, spec.Name)
		case tools.KindMCP:
			if len(profile.MCPTags) == 0 || hasTag(spec.Tags, profile.MCPTags) {
				effective.VisibleTools = append(effective.VisibleTools, spec.Name)
				effective.CallableTools = append(effective.CallableTools, spec.Name)
			}
		default:
			effective.VisibleTools = append(effective.VisibleTools, spec.Name)
			effective.CallableTools = append(effective.CallableTools, spec.Name)
		}
	}

	sort.Strings(effective.VisibleTools)
	sort.Strings(effective.CallableTools)
	sort.Strings(effective.VisibleSkills)
	return effective
}

func ApplyScopePolicy(base Effective, scope tools.Scope) Effective {
	out := base
	if strings.EqualFold(strings.TrimSpace(scope.Channel), "feishu") {
		// Keep exec hidden from default Feishu sessions until explicit approval/session policy exists.
		out.VisibleTools = removeValue(out.VisibleTools, "exec")
		out.CallableTools = removeValue(out.CallableTools, "exec")
		out.PolicySource = append(out.PolicySource, "channel:feishu")
	}
	if strings.TrimSpace(scope.Visibility) != "" {
		out.PolicySource = append(out.PolicySource, "session:"+strings.TrimSpace(scope.Visibility))
	}
	return out
}

func Narrow(parent, child Effective) Effective {
	out := child
	out.VisibleTools = intersect(parent.VisibleTools, child.VisibleTools)
	out.CallableTools = intersect(parent.CallableTools, child.CallableTools)
	out.VisibleSkills = intersect(parent.VisibleSkills, child.VisibleSkills)
	out.AllowedMCPProviders = intersect(parent.AllowedMCPProviders, child.AllowedMCPProviders)
	out.ReadablePaths = intersectPaths(parent.ReadablePaths, child.ReadablePaths)
	out.WritablePaths = intersectPaths(parent.WritablePaths, child.WritablePaths)
	out.CanSpawn = parent.CanSpawn && child.CanSpawn
	out.AsyncAllowed = parent.AsyncAllowed && child.AsyncAllowed
	return out
}

func Allows(e Effective, toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	for _, item := range e.CallableTools {
		if item == toolName {
			return true
		}
	}
	return false
}

func toSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = true
		}
	}
	return out
}

func hasTag(tags, wanted []string) bool {
	for _, tag := range tags {
		for _, candidate := range wanted {
			if strings.TrimSpace(tag) != "" && tag == candidate {
				return true
			}
		}
	}
	return false
}

func intersect(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	set := make(map[string]bool, len(a))
	for _, item := range a {
		set[item] = true
	}
	out := make([]string, 0, len(b))
	for _, item := range b {
		if set[item] {
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return dedupe(out)
}

func intersectPaths(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	out := make([]string, 0, len(b))
	for _, pathA := range a {
		for _, pathB := range b {
			pathA = filepath.Clean(pathA)
			pathB = filepath.Clean(pathB)
			switch {
			case strings.HasPrefix(pathA, pathB):
				out = append(out, pathA)
			case strings.HasPrefix(pathB, pathA):
				out = append(out, pathB)
			}
		}
	}
	sort.Strings(out)
	return dedupe(out)
}

func dedupe(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := values[:0]
	var last string
	for i, item := range values {
		if i == 0 || item != last {
			out = append(out, item)
			last = item
		}
	}
	return out
}

func removeValue(values []string, target string) []string {
	out := values[:0]
	for _, value := range values {
		if value != target {
			out = append(out, value)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
