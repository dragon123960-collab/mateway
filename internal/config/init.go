package config

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	initassets "github.com/dongping/mateway/assets"
	"github.com/dongping/mateway/internal/agenttemplate"
	"gopkg.in/yaml.v3"
)

type templateFile struct {
	RelPath string
	Content string
}

func EnsureDefaultConfigFiles(home string) error {
	return EnsureDefaultConfigFilesWithAssets(home, "")
}

func EnsureDefaultConfigFilesWithAssets(home, assetsDir string) error {
	loader := NewLoader(home)
	source, err := resolveInitAssetSource(assetsDir)
	if err != nil {
		return err
	}
	assetFiles, err := source.files()
	if err != nil {
		return err
	}
	for _, file := range assetFiles {
		path := filepath.Join(loader.Home, file.RelPath)
		if err := writeFileIfMissing(path, file.Content); err != nil {
			return err
		}
	}
	if err := ensureDefaultSkillMetadata(loader.Home); err != nil {
		return err
	}
	mainAgentFiles := agenttemplate.CoreFiles(agenttemplate.Profile{ID: "main", Name: "Main Assistant"})
	files := []templateFile{
		{RelPath: filepath.Join("workspace", "agents", "main", "agent.md"), Content: mainAgentFiles["agent.md"]},
		{RelPath: filepath.Join("workspace", "agents", "main", "soul.md"), Content: mainAgentFiles["soul.md"]},
		{RelPath: filepath.Join("workspace", "agents", "main", "user.md"), Content: mainAgentFiles["user.md"]},
		{RelPath: filepath.Join("workspace", "agents", "main", "tools.md"), Content: mainAgentFiles["tools.md"]},
		{RelPath: filepath.Join("workspace", "agents", "main", "memory.md"), Content: mainAgentFiles["memory.md"]},
		{RelPath: filepath.Join("workspace", "agents", "main", "skills", "README.md"), Content: agentSkillsReadme()},
	}
	for _, file := range files {
		path := filepath.Join(loader.Home, file.RelPath)
		if err := writeFileIfMissing(path, file.Content); err != nil {
			return err
		}
	}
	configDefaults, err := source.read(filepath.Join("config", "config.yaml"))
	if err != nil {
		return err
	}
	feishuDefaults, err := source.read(filepath.Join("config", "channels", "feishu.yaml"))
	if err != nil {
		return err
	}
	weixinDefaults, err := source.read(filepath.Join("config", "channels", "weixin.yaml"))
	if err != nil {
		return err
	}
	if err := mergeDefaultYAMLFile(filepath.Join(loader.ConfigDir(), "config.yaml"), configDefaults); err != nil {
		return err
	}
	if err := mergeDefaultYAMLFile(filepath.Join(loader.ConfigDir(), "channels", "feishu.yaml"), feishuDefaults); err != nil {
		return err
	}
	if err := mergeDefaultYAMLFile(filepath.Join(loader.ConfigDir(), "channels", "weixin.yaml"), weixinDefaults); err != nil {
		return err
	}
	return nil
}

func ensureDefaultSkillMetadata(home string) error {
	skillsRoot := filepath.Join(home, "workspace", "skills")
	entries, err := os.ReadDir(skillsRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		skillDir := filepath.Join(skillsRoot, entry.Name())
		if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
			continue
		}
		metadataPath := filepath.Join(skillDir, ".mateway", "metadata.yaml")
		if _, err := os.Stat(metadataPath); err == nil {
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(metadataPath), 0o755); err != nil {
			return err
		}
		content := defaultSkillMetadataYAML(entry.Name())
		if err := os.WriteFile(metadataPath, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func defaultSkillMetadataYAML(name string) string {
	graphType := "prompt"
	allowedTools := ""
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "fresh-search":
		graphType = "react"
		allowedTools = `  allowed_tools:
    - web.search
    - web.fetch
`
	}
	return fmt.Sprintf(`adapter_version: "2"
source: "builtin"
installed_at: "2026-06-17T00:00:00Z"
tool_runtime: "mateway"
graph:
  mode: "adapted"
  type: %q
  stage: "execution"
  granularity: "subtask"
%s`, graphType, allowedTools)
}

type initAssetSource struct {
	Dir      string
	FS       fs.FS
	Embedded bool
}

func resolveInitAssetSource(override string) (initAssetSource, error) {
	var tried []string
	for _, candidate := range initAssetCandidates(override) {
		if candidate == "" {
			continue
		}
		clean, err := filepath.Abs(filepath.Clean(candidate))
		if err != nil {
			tried = append(tried, candidate)
			continue
		}
		if validInitAssetDir(clean) {
			return initAssetSource{Dir: clean}, nil
		}
		tried = append(tried, clean)
	}
	if strings.TrimSpace(override) == "" && strings.TrimSpace(os.Getenv("MATEWAY_ASSETS_DIR")) == "" {
		if source, err := embeddedInitAssetSource(); err == nil {
			return source, nil
		}
	}
	return initAssetSource{}, fmt.Errorf("mateway init assets not found; set --assets-dir or MATEWAY_ASSETS_DIR, or run from a release archive containing assets/init (tried: %s)", strings.Join(tried, ", "))
}

func initAssetCandidates(override string) []string {
	var out []string
	if strings.TrimSpace(override) != "" {
		out = append(out, override)
		return out
	}
	if env := strings.TrimSpace(os.Getenv("MATEWAY_ASSETS_DIR")); env != "" {
		out = append(out, env)
	}
	if exe, err := os.Executable(); err == nil {
		out = append(out, filepath.Join(filepath.Dir(exe), "assets", "init"))
	}
	if cwd, err := os.Getwd(); err == nil {
		out = append(out, ancestorAssetCandidates(cwd)...)
	}
	return out
}

func ancestorAssetCandidates(start string) []string {
	var out []string
	dir, err := filepath.Abs(filepath.Clean(start))
	if err != nil {
		return []string{filepath.Join(start, "assets", "init")}
	}
	for {
		out = append(out, filepath.Join(dir, "assets", "init"))
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return out
}

func validInitAssetDir(dir string) bool {
	for _, rel := range []string{
		filepath.Join("config", "config.yaml"),
		filepath.Join("config", "channels", "feishu.yaml"),
		filepath.Join("workspace", "skills", "software-install", "SKILL.md"),
		filepath.Join("workspace", "memory", "README.md"),
	} {
		info, err := os.Stat(filepath.Join(dir, rel))
		if err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

func embeddedInitAssetSource() (initAssetSource, error) {
	sub, err := fs.Sub(initassets.InitFS, "init")
	if err != nil {
		return initAssetSource{}, err
	}
	source := initAssetSource{FS: sub, Embedded: true}
	if !validInitAssetFS(source.FS) {
		return initAssetSource{}, fmt.Errorf("embedded init assets are incomplete")
	}
	return source, nil
}

func validInitAssetFS(source fs.FS) bool {
	for _, rel := range []string{
		filepath.ToSlash(filepath.Join("config", "config.yaml")),
		filepath.ToSlash(filepath.Join("config", "channels", "feishu.yaml")),
		filepath.ToSlash(filepath.Join("workspace", "skills", "software-install", "SKILL.md")),
		filepath.ToSlash(filepath.Join("workspace", "memory", "README.md")),
	} {
		info, err := fs.Stat(source, rel)
		if err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

func (s initAssetSource) files() ([]templateFile, error) {
	if s.FS != nil {
		return s.fsFiles()
	}
	var out []templateFile
	for _, rootName := range []string{"config", "workspace"} {
		root := filepath.Join(s.Dir, rootName)
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(s.Dir, path)
			if err != nil {
				return err
			}
			out = append(out, templateFile{RelPath: rel, Content: string(data)})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s initAssetSource) read(rel string) ([]byte, error) {
	if s.FS != nil {
		data, err := fs.ReadFile(s.FS, filepath.ToSlash(rel))
		if err != nil {
			return nil, fmt.Errorf("read init asset %s: %w", rel, err)
		}
		return data, nil
	}
	path := filepath.Join(s.Dir, rel)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read init asset %s: %w", rel, err)
	}
	return data, nil
}

func (s initAssetSource) fsFiles() ([]templateFile, error) {
	var out []templateFile
	for _, rootName := range []string{"config", "workspace"} {
		if err := fs.WalkDir(s.FS, rootName, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := fs.ReadFile(s.FS, path)
			if err != nil {
				return err
			}
			out = append(out, templateFile{RelPath: filepath.FromSlash(path), Content: string(data)})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func agentSkillsReadme() string {
	return `# main agent skills

Put agent-specific skills here as:

` + "```text" + `
skills/<skill-name>/SKILL.md
` + "```" + `

Shared skills can live under ` + "`workspace/skills`" + `. Agent-specific skills win when names collide.

Mateway discovers skills from both locations:

1. ` + "`workspace/agents/main/skills/<skill-name>/SKILL.md`" + ` for agent-specific overrides.
2. ` + "`workspace/skills/<skill-name>/SKILL.md`" + ` for shared installed skills.

You do not need to copy or symlink a shared skill into this directory unless you want an agent-specific override.
`
}

func mergeDefaultYAMLFile(path string, defaults []byte) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var currentNode yaml.Node
	if err := yaml.Unmarshal(existing, &currentNode); err != nil {
		return err
	}
	var defaultNode yaml.Node
	if err := yaml.Unmarshal(defaults, &defaultNode); err != nil {
		return err
	}
	if mergeYAMLMapping(documentMapping(&currentNode), documentMapping(&defaultNode)) {
		out, err := yaml.Marshal(&currentNode)
		if err != nil {
			return err
		}
		return os.WriteFile(path, out, 0o644)
	}
	return nil
}

func documentMapping(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0]
	}
	if node.Kind == yaml.MappingNode {
		return node
	}
	return nil
}

func mergeYAMLMapping(current, defaults *yaml.Node) bool {
	if current == nil || defaults == nil || current.Kind != yaml.MappingNode || defaults.Kind != yaml.MappingNode {
		return false
	}
	changed := false
	for i := 0; i+1 < len(defaults.Content); i += 2 {
		key := defaults.Content[i]
		value := defaults.Content[i+1]
		existing := mappingValue(current, key.Value)
		if existing == nil {
			current.Content = append(current.Content, cloneYAMLNode(key), cloneYAMLNode(value))
			changed = true
			continue
		}
		if existing.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode {
			if mergeYAMLMapping(existing, value) {
				changed = true
			}
		}
	}
	return changed
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func cloneYAMLNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	clone := *node
	clone.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		clone.Content[i] = cloneYAMLNode(child)
	}
	return &clone
}

func writeFileIfMissing(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir for %s: %w", path, err)
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
