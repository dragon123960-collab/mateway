package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Catalog struct {
	mu          sync.RWMutex
	roots       []string
	skills      map[string]Skill
	lastRefresh time.Time
	lastError   string
}

func NewCatalog(roots []string) *Catalog {
	cleaned := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root != "" {
			cleaned = append(cleaned, root)
		}
	}
	return &Catalog{
		roots:  cleaned,
		skills: make(map[string]Skill),
	}
}

func (c *Catalog) Refresh() error {
	skillsMap := make(map[string]Skill)
	var failures []string
	for _, root := range c.roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			failures = append(failures, fmt.Sprintf("%s: %v", root, err))
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillPath := filepath.Join(root, entry.Name(), "SKILL.md")
			if _, err := os.Stat(skillPath); errors.Is(err, os.ErrNotExist) {
				continue
			}
			skill, err := loadSkill(filepath.Dir(skillPath))
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", skillPath, err))
				continue
			}
			skillsMap[skill.Manifest.Name] = skill
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.skills = skillsMap
	c.lastRefresh = time.Now()
	if len(failures) > 0 {
		c.lastError = strings.Join(failures, "; ")
		return errors.New(c.lastError)
	}
	c.lastError = ""
	return nil
}

func loadSkill(dir string) (Skill, error) {
	skillPath := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return Skill{}, err
	}
	manifest, body, err := parseSkillMarkdown(data, filepath.Base(dir))
	if err != nil {
		return Skill{}, err
	}
	resources, err := loadSkillResources(dir, manifest)
	if err != nil {
		return Skill{}, err
	}
	metaPath := filepath.Join(dir, "_meta.json")
	executable := false
	if meta, ok, err := loadRuntimeMetadata(metaPath); err != nil {
		return Skill{}, err
	} else if ok {
		if err := validateRuntimeMetadata(meta); err != nil {
			return Skill{}, err
		}
		manifest.Type = meta.Type
		manifest.Entry = strings.TrimSpace(meta.Entry)
		manifest.Method = strings.TrimSpace(meta.Method)
		manifest.URL = strings.TrimSpace(meta.URL)
		manifest.Env = append([]string(nil), meta.Env...)
		manifest.ReadOnly = meta.ReadOnly
		manifest.RiskLevel = strings.TrimSpace(meta.RiskLevel)
		manifest.Tags = append([]string(nil), meta.Tags...)
		executable = manifest.Type != ""
	}
	return Skill{
		Manifest:   manifest,
		Directory:  dir,
		SkillPath:  skillPath,
		MetaPath:   metaPath,
		Body:       body,
		Resources:  resources,
		Executable: executable,
	}, nil
}

func loadSkillResources(dir string, manifest Manifest) (ResourceSet, error) {
	var out ResourceSet
	var err error
	if out.Scripts, err = collectSkillResources(dir, "scripts"); err != nil {
		return ResourceSet{}, err
	}
	if out.References, err = collectSkillResources(dir, "references"); err != nil {
		return ResourceSet{}, err
	}
	if out.Assets, err = collectSkillResources(dir, "assets"); err != nil {
		return ResourceSet{}, err
	}
	extraDirs := declaredResourceDirs(manifest)
	if len(extraDirs) > 0 {
		out.Extra = make(map[string][]string, len(extraDirs))
		for _, item := range extraDirs {
			if item == "scripts" || item == "references" || item == "assets" {
				continue
			}
			files, err := collectSkillResources(dir, item)
			if err != nil {
				return ResourceSet{}, err
			}
			if len(files) > 0 {
				out.Extra[item] = files
			}
		}
	}
	return out, nil
}

func declaredResourceDirs(manifest Manifest) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(manifest.ResourceDirs)+2)
	add := func(values []string) {
		for _, value := range values {
			name := normalizeResourceDir(value)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	add(manifest.ResourceDirs)
	for _, key := range []string{"resource_dirs", "resource-dirs", "resource_dirs_extra", "resource-dirs-extra"} {
		value, ok := manifest.Metadata[key]
		if !ok || value == nil {
			continue
		}
		switch v := value.(type) {
		case string:
			add(strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' || r == '|' || r == ' ' }))
		case []any:
			items := make([]string, 0, len(v))
			for _, item := range v {
				s, _ := item.(string)
				items = append(items, s)
			}
			add(items)
		}
	}
	sort.Strings(out)
	return out
}

func collectSkillResources(root, subdir string) ([]string, error) {
	base := filepath.Join(root, subdir)
	info, err := os.Stat(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat %s: %w", base, err)
	}
	if !info.IsDir() {
		return nil, nil
	}
	var out []string
	err = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := strings.TrimSpace(d.Name())
		if name == "" {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(name, ".") && path != base {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", base, err)
	}
	sort.Strings(out)
	return out, nil
}

func validateRuntimeMetadata(meta RuntimeMetadata) error {
	switch meta.Type {
	case "":
		return nil
	case TypeCLI:
		if strings.TrimSpace(meta.Entry) == "" {
			return fmt.Errorf("_meta.json mateway.type=cli requires entry")
		}
	case TypeAPI:
		if strings.TrimSpace(meta.URL) == "" {
			return fmt.Errorf("_meta.json mateway.type=api requires url")
		}
	case TypeWorkflow:
	default:
		return fmt.Errorf("unsupported mateway runtime type %q", meta.Type)
	}
	return nil
}

func parseSkillMarkdown(data []byte, fallbackName string) (Manifest, string, error) {
	text := strings.TrimSpace(string(data))
	manifest := Manifest{
		Name:     strings.TrimSpace(fallbackName),
		Metadata: map[string]any{},
	}
	if strings.HasPrefix(text, "---\n") {
		rest := strings.TrimPrefix(text, "---\n")
		parts := strings.SplitN(rest, "\n---\n", 2)
		if len(parts) == 2 {
			var fm struct {
				Name          string         `yaml:"name"`
				Version       string         `yaml:"version"`
				Description   string         `yaml:"description"`
				Homepage      string         `yaml:"homepage"`
				License       string         `yaml:"license"`
				Compatibility string         `yaml:"compatibility"`
				AllowedTools  []string       `yaml:"allowed-tools"`
				ResourceDirs  []string       `yaml:"resource_dirs"`
				ResourceDirs2 []string       `yaml:"resource-dirs"`
				Context       string         `yaml:"context"`
				Agent         string         `yaml:"agent"`
				Model         string         `yaml:"model"`
				Metadata      map[string]any `yaml:"metadata"`
			}
			if err := yaml.Unmarshal([]byte(parts[0]), &fm); err != nil {
				return Manifest{}, "", fmt.Errorf("decode skill frontmatter: %w", err)
			}
			if strings.TrimSpace(fm.Name) != "" {
				manifest.Name = strings.TrimSpace(fm.Name)
			}
			manifest.Version = strings.TrimSpace(fm.Version)
			manifest.Description = strings.TrimSpace(fm.Description)
			manifest.Homepage = strings.TrimSpace(fm.Homepage)
			manifest.License = strings.TrimSpace(fm.License)
			manifest.Compatibility = strings.TrimSpace(fm.Compatibility)
			manifest.AllowedTools = append([]string(nil), fm.AllowedTools...)
			manifest.ResourceDirs = append(append([]string(nil), fm.ResourceDirs...), fm.ResourceDirs2...)
			manifest.Context = strings.TrimSpace(fm.Context)
			manifest.Agent = strings.TrimSpace(fm.Agent)
			manifest.Model = strings.TrimSpace(fm.Model)
			if len(fm.Metadata) > 0 {
				manifest.Metadata = fm.Metadata
			}
			text = strings.TrimSpace(parts[1])
		}
	}
	if manifest.Name == "" {
		return Manifest{}, "", fmt.Errorf("skill missing name")
	}
	if manifest.Description == "" {
		manifest.Description = inferSkillDescription(text)
	}
	return manifest, text, nil
}

func loadRuntimeMetadata(path string) (RuntimeMetadata, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RuntimeMetadata{}, false, nil
		}
		return RuntimeMetadata{}, false, fmt.Errorf("read _meta.json: %w", err)
	}
	var meta MetaFile
	if err := json.Unmarshal(data, &meta); err != nil {
		return RuntimeMetadata{}, false, fmt.Errorf("decode _meta.json: %w", err)
	}
	return meta.Mateway, true, nil
}

func inferSkillDescription(markdown string) string {
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) > 180 {
			return line[:180]
		}
		return line
	}
	return "Workspace skill"
}

func (c *Catalog) Snapshot() []Skill {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Skill, 0, len(c.skills))
	for _, skill := range c.skills {
		out = append(out, skill)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Manifest.Name < out[j].Manifest.Name
	})
	return out
}

func (c *Catalog) LastStatus() (time.Time, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastRefresh, c.lastError
}
