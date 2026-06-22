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
	"gopkg.in/yaml.v3"
)

type Skill struct {
	Name         string
	Description  string
	Stage        string
	GraphType    string
	Granularity  string
	AllowedTools []string
	Inputs       []string
	Outputs      []string
	Usage        string
	Entrypoints  []string
	Success      []string
	Priority     string
	Path         string
	Scope        string
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
	AdapterVersion string        `yaml:"adapter_version"`
	Source         string        `yaml:"source"`
	InstalledAt    time.Time     `yaml:"installed_at"`
	ToolRuntime    string        `yaml:"tool_runtime"`
	Notes          []string      `yaml:"notes,omitempty"`
	Graph          GraphMetadata `yaml:"graph"`
}

type GraphMetadata struct {
	Mode            string   `yaml:"mode"`
	Type            string   `yaml:"type"`
	Stage           string   `yaml:"stage"`
	Granularity     string   `yaml:"granularity"`
	Inputs          []string `yaml:"inputs,omitempty"`
	Outputs         []string `yaml:"outputs,omitempty"`
	AllowedTools    []string `yaml:"allowed_tools,omitempty"`
	Usage           string   `yaml:"usage,omitempty"`
	Entrypoints     []string `yaml:"entrypoints,omitempty"`
	SuccessCriteria []string `yaml:"success_criteria,omitempty"`
	SafetyNotes     []string `yaml:"safety_notes,omitempty"`
}

type RegisterInput struct {
	Workspace string
	Path      string
	Name      string
	Source    string
	Force     bool
}

type RegisterResult struct {
	Name         string
	Path         string
	MetadataPath string
}

type DoctorReport struct {
	Orphans []OrphanSkill
}

type OrphanSkill struct {
	Name   string
	Path   string
	Reason string
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
	if err := ValidateSkillContent("SKILL.md", string(data)); err != nil {
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
	metadataPath, err := WriteMetadata(filepath.Dir(target), DefaultMetadata(DefaultMetadataInput{
		Source: source,
		Header: header,
		Body:   string(data),
		Notes: []string{
			"Original SKILL.md is preserved. Mateway-specific adaptation lives in this metadata directory.",
			"Use terminal.run for command execution; use file.read/write/delete for local files; use secret.set and terminal.run.env_secrets for credentials.",
		},
	}))
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
	if err := ValidateMetadata(metadata); err != nil {
		return Metadata{}, true, err
	}
	return metadata, true, nil
}

func WriteMetadata(skillDir string, metadata Metadata) (string, error) {
	if err := ValidateMetadata(metadata); err != nil {
		return "", err
	}
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

type DefaultMetadataInput struct {
	Source string
	Header Skill
	Body   string
	Notes  []string
}

func DefaultMetadata(input DefaultMetadataInput) Metadata {
	stage := strings.TrimSpace(input.Header.Stage)
	if !validMetadataValue(stage, "planning", "execution", "synthesis") {
		stage = "execution"
	}
	graphType := "prompt"
	allowedTools := []string(nil)
	usage := defaultSkillUsage(input.Header, input.Body)
	entrypoints := inferSkillEntrypoints(input.Body)
	successCriteria := defaultSuccessCriteria(input.Header, input.Body)
	nameLower := strings.ToLower(strings.TrimSpace(input.Header.Name))
	bodyLower := strings.ToLower(input.Body)
	switch nameLower {
	case "fresh-search":
		graphType = "react"
		allowedTools = []string{"web.search", "web.fetch"}
	}
	if graphType == "prompt" && (strings.Contains(bodyLower, "terminal.run") || len(entrypoints) > 0) {
		graphType = "react"
		allowedTools = appendUniqueStrings(allowedTools, "terminal.run")
	}
	return Metadata{
		AdapterVersion: "2",
		Source:         firstNonEmpty(input.Source, "local"),
		InstalledAt:    time.Now().UTC(),
		ToolRuntime:    "mateway",
		Notes:          input.Notes,
		Graph: GraphMetadata{
			Mode:            "adapted",
			Type:            graphType,
			Stage:           stage,
			Granularity:     "subtask",
			AllowedTools:    allowedTools,
			Usage:           usage,
			Entrypoints:     entrypoints,
			SuccessCriteria: successCriteria,
		},
	}
}

func defaultSkillUsage(header Skill, body string) string {
	var parts []string
	if desc := strings.TrimSpace(header.Description); desc != "" {
		parts = append(parts, desc)
	}
	bodyLower := strings.ToLower(body)
	switch {
	case strings.Contains(bodyLower, "terminal.run"):
		parts = append(parts, "Execute this skill through a node-local ReAct loop. Follow SKILL.md exactly, use terminal.run for command examples, and do not claim completion until command evidence satisfies success_criteria.")
	case strings.Contains(bodyLower, "lark-cli"):
		parts = append(parts, "Use lark-cli commands described in SKILL.md and return the created resource URL, token, or command output as node output.")
	default:
		parts = append(parts, "Use SKILL.md as node-local instruction. Produce output that satisfies this metadata's outputs and success_criteria.")
	}
	return strings.Join(parts, " ")
}

func inferSkillEntrypoints(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lower, "python ") || strings.HasPrefix(lower, "python3 "):
			out = appendUniqueStrings(out, trimmed)
		case strings.HasPrefix(lower, "lark-cli "):
			out = appendUniqueStrings(out, trimmed)
		case strings.Contains(lower, "/scripts/") && !strings.HasPrefix(trimmed, "#"):
			out = appendUniqueStrings(out, trimmed)
		}
		if len(out) >= 5 {
			break
		}
	}
	return out
}

func defaultSuccessCriteria(header Skill, body string) []string {
	bodyLower := strings.ToLower(body)
	var out []string
	if strings.Contains(bodyLower, "do not claim") {
		out = append(out, "Do not claim success unless the required command or API call returns successful evidence.")
	}
	if strings.Contains(bodyLower, "url") || strings.Contains(bodyLower, "token") {
		out = append(out, "Return the created resource URL, token, or verifiable command output.")
	}
	if len(out) == 0 {
		out = append(out, "Return a concise result that satisfies the node goal and SKILL.md instructions.")
	}
	return out
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	var out []string
	for _, value := range append(values, additions...) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func Register(input RegisterInput) (RegisterResult, error) {
	workspace := cleanWorkspace(input.Workspace)
	target, err := resolveRegisterTarget(workspace, input.Path, input.Name)
	if err != nil {
		return RegisterResult{}, err
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return RegisterResult{}, err
	}
	if err := ValidateSkillContent(target, string(data)); err != nil {
		return RegisterResult{}, err
	}
	skillDir := filepath.Dir(target)
	metadataPath := filepath.Join(skillDir, ".mateway", "metadata.yaml")
	if _, err := os.Stat(metadataPath); err == nil && !input.Force {
		return RegisterResult{}, fmt.Errorf("skill metadata already exists for %s; use force to overwrite", target)
	} else if err != nil && !os.IsNotExist(err) {
		return RegisterResult{}, err
	}
	header := ParseHeader(string(data))
	name := sanitizeName(firstNonEmpty(input.Name, header.Name, filepath.Base(skillDir)))
	if name == "" {
		return RegisterResult{}, fmt.Errorf("skill name is required")
	}
	source := strings.TrimSpace(input.Source)
	if source == "" {
		source = "local"
	}
	written, err := WriteMetadata(skillDir, DefaultMetadata(DefaultMetadataInput{
		Source: source,
		Header: header,
		Body:   string(data),
		Notes: []string{
			"Registered from an existing local SKILL.md.",
		},
	}))
	if err != nil {
		return RegisterResult{}, err
	}
	return RegisterResult{Name: name, Path: target, MetadataPath: written}, nil
}

func Doctor(workspace string) (DoctorReport, error) {
	workspace = cleanWorkspace(workspace)
	var report DoctorReport
	for _, root := range []string{
		filepath.Join(workspace, "agents", "main", "skills"),
		filepath.Join(workspace, "skills"),
	} {
		orphans, err := doctorRoot(root)
		if err != nil {
			return DoctorReport{}, err
		}
		report.Orphans = append(report.Orphans, orphans...)
	}
	return report, nil
}

func doctorRoot(root string) ([]OrphanSkill, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []OrphanSkill
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		skillPath := filepath.Join(root, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillPath); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		if _, ok, err := ReadMetadata(filepath.Dir(skillPath)); err != nil {
			out = append(out, OrphanSkill{Name: entry.Name(), Path: skillPath, Reason: err.Error()})
		} else if !ok {
			out = append(out, OrphanSkill{Name: entry.Name(), Path: skillPath, Reason: "missing .mateway/metadata.yaml"})
		}
	}
	return out, nil
}

func ValidateMetadata(metadata Metadata) error {
	if strings.TrimSpace(metadata.AdapterVersion) == "" {
		return fmt.Errorf("metadata adapter_version is required")
	}
	if strings.TrimSpace(metadata.Source) == "" {
		return fmt.Errorf("metadata source is required")
	}
	if strings.TrimSpace(metadata.ToolRuntime) != "mateway" {
		return fmt.Errorf("metadata tool_runtime must be mateway")
	}
	graph := metadata.Graph
	if !validMetadataValue(graph.Mode, "native", "adapted", "legacy") {
		return fmt.Errorf("metadata graph.mode must be native, adapted, or legacy")
	}
	if !validMetadataValue(graph.Type, "prompt", "react", "script") {
		return fmt.Errorf("metadata graph.type must be prompt, react, or script")
	}
	if !validMetadataValue(graph.Stage, "planning", "execution", "synthesis") {
		return fmt.Errorf("metadata graph.stage must be planning, execution, or synthesis")
	}
	if !validMetadataValue(graph.Granularity, "subtask", "workflow") {
		return fmt.Errorf("metadata graph.granularity must be subtask or workflow")
	}
	return nil
}

func validMetadataValue(value string, allowed ...string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func resolveRegisterTarget(workspace, path, name string) (string, error) {
	path = strings.TrimSpace(path)
	if path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			path = filepath.Join(path, "SKILL.md")
		}
		return filepath.Abs(filepath.Clean(path))
	}
	name = sanitizeName(name)
	if name == "" {
		return "", fmt.Errorf("skill path or name is required")
	}
	return filepath.Join(workspace, "skills", name, "SKILL.md"), nil
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
		metadata, ok, err := ReadMetadata(filepath.Dir(path))
		if err != nil || !ok {
			continue
		}
		item.Stage = firstNonEmpty(metadata.Graph.Stage, item.Stage)
		item.GraphType = metadata.Graph.Type
		item.Granularity = metadata.Graph.Granularity
		item.AllowedTools = append([]string(nil), metadata.Graph.AllowedTools...)
		item.Inputs = append([]string(nil), metadata.Graph.Inputs...)
		item.Outputs = append([]string(nil), metadata.Graph.Outputs...)
		item.Usage = metadata.Graph.Usage
		item.Entrypoints = append([]string(nil), metadata.Graph.Entrypoints...)
		item.Success = append([]string(nil), metadata.Graph.SuccessCriteria...)
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
