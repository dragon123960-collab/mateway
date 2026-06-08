package skill

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/secret"
	"gopkg.in/yaml.v3"
)

type Skill struct {
	Name        string
	Description string
	Stage       string
	Priority    string
	Path        string
	Scope       string
}

type SearchResult struct {
	Catalog    string
	URL        string
	InstallURL string
	Enabled    bool
	TrustLevel string
	Adapter    string
	CanInstall bool
}

type CatalogReport struct {
	Name       string
	Enabled    bool
	TrustLevel string
	SearchURL  string
	InstallURL string
	Adapter    string
	CanInstall bool
}

type InstallInput struct {
	Workspace string
	Source    string
	Name      string
	Force     bool
}

type InstallResult struct {
	Name         string
	Path         string
	MetadataPath string
}

type Metadata struct {
	AdapterVersion string    `yaml:"adapter_version"`
	Source         string    `yaml:"source"`
	InstalledAt    time.Time `yaml:"installed_at"`
	ToolRuntime    string    `yaml:"tool_runtime"`
	Notes          []string  `yaml:"notes,omitempty"`
}

func List(workspace string) ([]Skill, error) {
	workspace = cleanWorkspace(workspace)
	var out []Skill
	for _, root := range []struct {
		Path  string
		Scope string
	}{
		{Path: filepath.Join(workspace, "agents", "main", "skills"), Scope: "agent"},
		{Path: filepath.Join(workspace, "skills"), Scope: "shared"},
	} {
		skills, err := listRoot(root.Path, root.Scope)
		if err != nil {
			return nil, err
		}
		out = append(out, skills...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func SearchCatalogs(cfg *config.Root, query string) []SearchResult {
	query = strings.TrimSpace(query)
	if cfg == nil {
		cfg = &config.Root{}
		cfg.Skills.Catalogs = defaultCatalogs()
	}
	cfg.NormalizeForUse()
	var out []SearchResult
	for _, catalog := range cfg.Skills.Catalogs {
		searchURL := strings.TrimSpace(catalog.SearchURL)
		if searchURL == "" {
			searchURL = strings.TrimRight(catalog.BaseURL, "/") + "/search?q={query}"
		}
		searchURL = strings.ReplaceAll(searchURL, "{query}", url.QueryEscape(query))
		out = append(out, SearchResult{
			Catalog:    catalog.Name,
			URL:        searchURL,
			InstallURL: strings.TrimSpace(catalog.InstallURL),
			Enabled:    catalog.Enabled,
			TrustLevel: catalog.TrustLevel,
			Adapter:    adapterForCatalog(catalog),
			CanInstall: strings.TrimSpace(catalog.InstallURL) != "",
		})
	}
	return out
}

func CatalogReports(cfg *config.Root) []CatalogReport {
	if cfg == nil {
		cfg = &config.Root{}
		cfg.Skills.Catalogs = defaultCatalogs()
	}
	cfg.NormalizeForUse()
	var out []CatalogReport
	for _, catalog := range cfg.Skills.Catalogs {
		out = append(out, CatalogReport{
			Name:       catalog.Name,
			Enabled:    catalog.Enabled,
			TrustLevel: catalog.TrustLevel,
			SearchURL:  catalog.SearchURL,
			InstallURL: catalog.InstallURL,
			Adapter:    adapterForCatalog(catalog),
			CanInstall: strings.TrimSpace(catalog.InstallURL) != "",
		})
	}
	return out
}

func Install(input InstallInput) (InstallResult, error) {
	workspace := cleanWorkspace(input.Workspace)
	source := strings.TrimSpace(input.Source)
	if source == "" {
		return InstallResult{}, fmt.Errorf("skill source is required")
	}
	data, sourceName, err := readSource(source)
	if err != nil {
		return InstallResult{}, err
	}
	header := ParseHeader(string(data))
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = header.Name
	}
	if name == "" {
		name = sourceName
	}
	name = sanitizeName(name)
	if name == "" {
		return InstallResult{}, fmt.Errorf("skill name is required")
	}
	if err := secret.RejectIfSecretLike(string(data), "SKILL.md"); err != nil {
		return InstallResult{}, err
	}
	target := filepath.Join(workspace, "skills", name, "SKILL.md")
	if _, err := os.Stat(target); err == nil && !input.Force {
		return InstallResult{}, fmt.Errorf("skill %q already exists; use --force to overwrite", name)
	} else if err != nil && !os.IsNotExist(err) {
		return InstallResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return InstallResult{}, err
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return InstallResult{}, err
	}
	metadataPath, err := writeMetadata(filepath.Dir(target), Metadata{
		AdapterVersion: "1",
		Source:         source,
		InstalledAt:    time.Now().UTC(),
		ToolRuntime:    "mateway",
		Notes: []string{
			"Original SKILL.md is preserved. Mateway-specific adaptation lives in this metadata directory.",
			"Use terminal.run for command execution; use file.read/write/delete for local files; use secret.set and terminal.run.env_secrets for credentials.",
		},
	})
	if err != nil {
		return InstallResult{}, err
	}
	return InstallResult{Name: name, Path: target, MetadataPath: metadataPath}, nil
}

func ReadMetadata(skillDir string) (Metadata, bool, error) {
	path := filepath.Join(skillDir, ".mateway", "metadata.yaml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Metadata{}, false, nil
	}
	if err != nil {
		return Metadata{}, false, err
	}
	var metadata Metadata
	if err := yaml.Unmarshal(data, &metadata); err != nil {
		return Metadata{}, false, err
	}
	return metadata, true, nil
}

func writeMetadata(skillDir string, metadata Metadata) (string, error) {
	dir := filepath.Join(skillDir, ".mateway")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "metadata.yaml")
	data, err := yaml.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o644)
}

func ParseHeader(text string) Skill {
	var skill Skill
	lines := strings.Split(text, "\n")
	inFrontMatter := len(lines) > 0 && strings.TrimSpace(lines[0]) == "---"
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if i == 0 && trimmed == "---" {
			continue
		}
		if inFrontMatter && trimmed == "---" {
			break
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			if !inFrontMatter && strings.HasPrefix(trimmed, "# ") && skill.Name == "" {
				skill.Name = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			}
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			skill.Name = value
		case "description":
			skill.Description = value
		case "stage":
			skill.Stage = value
		case "priority":
			skill.Priority = value
		}
		if !inFrontMatter && i > 20 {
			break
		}
	}
	return skill
}

func listRoot(root, scope string) ([]Skill, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(root, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		item := ParseHeader(string(data))
		if item.Name == "" {
			item.Name = entry.Name()
		}
		item.Path = path
		item.Scope = scope
		out = append(out, item)
	}
	return out, nil
}

func readSource(source string) ([]byte, string, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		resp, err := http.Get(source)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return nil, "", fmt.Errorf("download failed: %s", resp.Status)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
		return data, strings.TrimSuffix(filepath.Base(resp.Request.URL.Path), filepath.Ext(resp.Request.URL.Path)), err
	}
	info, err := os.Stat(source)
	if err != nil {
		return nil, "", err
	}
	path := source
	if info.IsDir() {
		path = filepath.Join(source, "SKILL.md")
	}
	data, err := os.ReadFile(path)
	return data, strings.TrimSuffix(filepath.Base(filepath.Dir(path)), filepath.Ext(path)), err
}

func cleanWorkspace(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace != "" {
		return workspace
	}
	return filepath.Join(config.DefaultHome(), "workspace")
}

func sanitizeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
			continue
		}
		if r == ' ' || r == '.' || r == '/' {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-_")
}

func defaultCatalogs() []config.SkillCatalogConfig {
	return []config.SkillCatalogConfig{
		{Name: "skills.sh", Enabled: true, BaseURL: "https://skills.sh", SearchURL: "https://skills.sh/?q={query}", InstallURL: "", TrustLevel: "high"},
		{Name: "skillhub.cn", Enabled: false, BaseURL: "https://skillhub.cn", SearchURL: "https://skillhub.cn/search?q={query}", InstallURL: "", TrustLevel: "unknown"},
		{Name: "clawhub.ai", Enabled: false, BaseURL: "https://clawhub.ai", SearchURL: "https://clawhub.ai/search?q={query}", InstallURL: "", TrustLevel: "medium"},
	}
}

func adapterForCatalog(catalog config.SkillCatalogConfig) string {
	if strings.TrimSpace(catalog.InstallURL) != "" {
		return "declared_install_url"
	}
	return "search_url_only"
}
