package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Profile struct {
	Name              string   `yaml:"name"`
	Description       string   `yaml:"description"`
	Inherits          string   `yaml:"inherits"`
	BuiltinTools      []string `yaml:"builtin_tools"`
	AllowedSkills     []string `yaml:"allowed_skills"`
	MCPTags           []string `yaml:"mcp_tags"`
	CanSpawn          bool     `yaml:"can_spawn"`
	AsyncAllowed      bool     `yaml:"async_allowed"`
	MemoryPolicy      string   `yaml:"memory_policy"`
	PathPolicy        string   `yaml:"path_policy"`
	ChannelVisibility string   `yaml:"channel_visibility"`
	CollaborationMode string   `yaml:"collaboration_mode"`
	Prompt            string   `yaml:"-"`
	Path              string   `yaml:"-"`
}

func Load(path string) (Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, err
	}
	parts := strings.SplitN(string(data), "---", 3)
	profile := Profile{
		Name: filepath.Base(strings.TrimSuffix(path, filepath.Ext(path))),
		Path: path,
	}
	switch {
	case len(parts) >= 3 && strings.TrimSpace(parts[0]) == "":
		if err := yaml.Unmarshal([]byte(parts[1]), &profile); err != nil {
			return Profile{}, fmt.Errorf("decode agent frontmatter: %w", err)
		}
		profile.Prompt = strings.TrimSpace(parts[2])
	default:
		profile.Prompt = strings.TrimSpace(string(data))
	}
	if strings.TrimSpace(profile.Name) == "" {
		return Profile{}, fmt.Errorf("agent profile missing name")
	}
	return profile, nil
}

func List(dir string) ([]Profile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Profile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if ext := strings.ToLower(filepath.Ext(entry.Name())); ext != ".md" && ext != ".markdown" {
			continue
		}
		profile, err := Load(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, profile)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func Resolve(dir, name string) (Profile, error) {
	profiles, err := List(dir)
	if err != nil {
		return Profile{}, err
	}
	byName := make(map[string]Profile, len(profiles))
	for _, profile := range profiles {
		byName[profile.Name] = profile
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "default"
	}
	seen := map[string]bool{}
	return resolveProfile(byName, name, seen)
}

func resolveProfile(byName map[string]Profile, name string, seen map[string]bool) (Profile, error) {
	profile, ok := byName[name]
	if !ok {
		return Profile{}, fmt.Errorf("agent profile %q not found", name)
	}
	if seen[name] {
		return Profile{}, fmt.Errorf("agent profile inheritance cycle at %q", name)
	}
	if strings.TrimSpace(profile.Inherits) == "" {
		return profile, nil
	}
	seen[name] = true
	parent, err := resolveProfile(byName, strings.TrimSpace(profile.Inherits), seen)
	delete(seen, name)
	if err != nil {
		return Profile{}, err
	}
	return mergeProfiles(parent, profile), nil
}

func mergeProfiles(parent, child Profile) Profile {
	merged := parent
	merged.Name = firstNonEmpty(child.Name, parent.Name)
	merged.Description = firstNonEmpty(child.Description, parent.Description)
	merged.Inherits = child.Inherits
	merged.BuiltinTools = mergeStringLists(parent.BuiltinTools, child.BuiltinTools)
	merged.AllowedSkills = mergeStringLists(parent.AllowedSkills, child.AllowedSkills)
	merged.MCPTags = mergeStringLists(parent.MCPTags, child.MCPTags)
	merged.CanSpawn = parent.CanSpawn || child.CanSpawn
	merged.AsyncAllowed = parent.AsyncAllowed || child.AsyncAllowed
	merged.MemoryPolicy = firstNonEmpty(child.MemoryPolicy, parent.MemoryPolicy)
	merged.PathPolicy = firstNonEmpty(child.PathPolicy, parent.PathPolicy)
	merged.ChannelVisibility = firstNonEmpty(child.ChannelVisibility, parent.ChannelVisibility)
	merged.CollaborationMode = firstNonEmpty(child.CollaborationMode, parent.CollaborationMode)
	merged.Prompt = strings.TrimSpace(strings.TrimSpace(parent.Prompt) + "\n\n" + strings.TrimSpace(child.Prompt))
	merged.Path = child.Path
	return merged
}

func mergeStringLists(parent, child []string) []string {
	if len(parent) == 0 && len(child) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(parent)+len(child))
	for _, item := range append(append([]string(nil), parent...), child...) {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
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
