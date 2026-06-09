package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/skill"
	"gopkg.in/yaml.v3"
)

type doctorStatus string

const (
	doctorOK   doctorStatus = "OK"
	doctorWARN doctorStatus = "WARN"
	doctorFAIL doctorStatus = "FAIL"
)

type doctorCheck struct {
	Status  doctorStatus
	Name    string
	Message string
}

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("mateway doctor", flag.ContinueOnError)
	homeFlag := fs.String("home", "", "override MATEWAY_HOME for diagnostics")
	if err := fs.Parse(args); err != nil {
		return err
	}
	home := strings.TrimSpace(*homeFlag)
	if home == "" {
		home = config.DefaultHome()
	}
	checks := []doctorCheck{{Status: doctorOK, Name: "home", Message: home}}
	if err := config.EnsureDefaultConfigFiles(home); err != nil {
		checks = append(checks, doctorCheck{Status: doctorFAIL, Name: "init_defaults", Message: err.Error()})
		printDoctorChecks(checks)
		return fmt.Errorf("doctor found failures")
	}
	cfg, loadErr := config.NewLoader(home).Load()
	if loadErr != nil {
		checks = append(checks, doctorCheck{Status: doctorFAIL, Name: "config_load", Message: loadErr.Error()})
		cfg = &config.Root{App: config.AppConfig{Home: home, Workspace: filepath.Join(home, "workspace")}}
		cfg.NormalizeForUse()
	} else {
		checks = append(checks, doctorCheck{Status: doctorOK, Name: "config_load", Message: filepath.Join(home, "config", "config.yaml")})
	}
	checks = append(checks, doctorConfigFiles(home)...)
	checks = append(checks, doctorConfigShape(home, cfg)...)
	checks = append(checks, doctorToolRegistry(cfg)...)
	checks = append(checks, doctorSkills(cfg)...)
	checks = append(checks, doctorRuntimeDirs(cfg)...)
	printDoctorChecks(checks)
	if countDoctorStatus(checks, doctorFAIL) > 0 {
		return fmt.Errorf("doctor found failures")
	}
	return nil
}

func printDoctorChecks(checks []doctorCheck) {
	for _, check := range checks {
		fmt.Printf("%s\t%s\t%s\n", check.Status, check.Name, check.Message)
	}
	fmt.Printf("summary\tok=%d\twarn=%d\tfail=%d\n", countDoctorStatus(checks, doctorOK), countDoctorStatus(checks, doctorWARN), countDoctorStatus(checks, doctorFAIL))
}

func countDoctorStatus(checks []doctorCheck, status doctorStatus) int {
	count := 0
	for _, check := range checks {
		if check.Status == status {
			count++
		}
	}
	return count
}

func doctorConfigFiles(home string) []doctorCheck {
	paths := map[string]string{
		"config_file": filepath.Join(home, "config", "config.yaml"),
		"workspace":   filepath.Join(home, "workspace"),
		"skills_dir":  filepath.Join(home, "workspace", "skills"),
		"models_dir":  filepath.Join(home, "config", "models"),
	}
	checks := make([]doctorCheck, 0, len(paths))
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := paths[name]
		if _, err := os.Stat(path); err != nil {
			checks = append(checks, doctorCheck{Status: doctorFAIL, Name: name, Message: err.Error()})
			continue
		}
		checks = append(checks, doctorCheck{Status: doctorOK, Name: name, Message: path})
	}
	return checks
}

func doctorConfigShape(home string, cfg *config.Root) []doctorCheck {
	var checks []doctorCheck
	if strings.TrimSpace(cfg.App.Home) == "" {
		checks = append(checks, doctorCheck{Status: doctorFAIL, Name: "app.home", Message: "empty"})
	} else {
		checks = append(checks, doctorCheck{Status: doctorOK, Name: "app.home", Message: cfg.App.Home})
	}
	if strings.TrimSpace(cfg.App.Workspace) == "" {
		checks = append(checks, doctorCheck{Status: doctorFAIL, Name: "app.workspace", Message: "empty"})
	} else {
		checks = append(checks, doctorCheck{Status: doctorOK, Name: "app.workspace", Message: cfg.App.Workspace})
	}
	if cfg.Execution.MaxIterationsValue() <= 0 {
		checks = append(checks, doctorCheck{Status: doctorFAIL, Name: "execution.max_iterations", Message: "must be positive"})
	} else {
		checks = append(checks, doctorCheck{Status: doctorOK, Name: "execution.max_iterations", Message: fmt.Sprint(cfg.Execution.MaxIterationsValue())})
	}
	if cfg.Execution.InactivityTimeoutDuration() == 0 {
		checks = append(checks, doctorCheck{Status: doctorWARN, Name: "execution.inactivity_timeout", Message: "invalid or disabled"})
	} else {
		checks = append(checks, doctorCheck{Status: doctorOK, Name: "execution.inactivity_timeout", Message: cfg.Execution.InactivityTimeout})
	}
	for _, key := range obsoleteConfigKeys(filepath.Join(home, "config", "config.yaml")) {
		checks = append(checks, doctorCheck{Status: doctorWARN, Name: "obsolete_config_key", Message: key})
	}
	if len(cfg.Models) == 0 {
		checks = append(checks, doctorCheck{Status: doctorWARN, Name: "models", Message: "no model files loaded"})
	} else {
		checks = append(checks, doctorCheck{Status: doctorOK, Name: "models", Message: fmt.Sprintf("%d loaded", len(cfg.Models))})
	}
	if strings.TrimSpace(cfg.Model.Default) == "" {
		checks = append(checks, doctorCheck{Status: doctorFAIL, Name: "model.default", Message: "empty"})
	} else if !modelNameExists(cfg.Model.Default, cfg.Models) {
		checks = append(checks, doctorCheck{Status: doctorWARN, Name: "model.default", Message: cfg.Model.Default + " is not enabled in model files"})
	} else {
		checks = append(checks, doctorCheck{Status: doctorOK, Name: "model.default", Message: cfg.Model.Default})
	}
	if _, err := cfg.DefaultAgentStrict(); err != nil {
		checks = append(checks, doctorCheck{Status: doctorFAIL, Name: "agents.default", Message: err.Error()})
	} else {
		checks = append(checks, doctorCheck{Status: doctorOK, Name: "agents.default", Message: cfg.Agents.Default})
	}
	for i, provider := range cfg.Search.ProviderOrder {
		if !searchProviderKnown(provider) {
			checks = append(checks, doctorCheck{Status: doctorWARN, Name: "search.provider_order", Message: fmt.Sprintf("unknown provider %q at index %d", provider, i)})
		}
	}
	return checks
}

func obsoleteConfigKeys(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return []string{"config.yaml: " + err.Error()}
	}
	var found []string
	for _, key := range []string{
		"app.locale",
		"app.message_catalog_dir",
		"model.roles.followup",
		"model.roles.review",
		"execution.max_no_progress_turns",
		"execution.max_repeated_tool_failures",
		"memory.require_confirm_for",
		"learning.skill_crystallization.require_user_confirm",
		"learning.skill_crystallization.ask_timing",
		"security.require_approval_for_risky_tools",
		"scripts",
		"remote.require_confirm",
	} {
		if yamlPathExists(&node, strings.Split(key, ".")) {
			found = append(found, key)
		}
	}
	return found
}

func yamlPathExists(node *yaml.Node, parts []string) bool {
	if node == nil || len(parts) == 0 {
		return false
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return yamlPathExists(node.Content[0], parts)
	}
	if node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == parts[0] {
			if len(parts) == 1 {
				return true
			}
			return yamlPathExists(node.Content[i+1], parts[1:])
		}
	}
	return false
}

func modelNameExists(name string, models []config.ModelConfig) bool {
	name = strings.TrimSpace(name)
	for _, model := range models {
		if model.Enabled && strings.EqualFold(model.Name, name) {
			return true
		}
	}
	return false
}

func searchProviderKnown(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "tavily", "searxng", "duckduckgo":
		return true
	default:
		return false
	}
}

func doctorToolRegistry(cfg *config.Root) []doctorCheck {
	registry := runtime.New(cfg).Tools
	defaultNames := []string{
		"file.read", "file.write", "file.delete", "project.index", "terminal.run", "toolresult.read", "web.search", "web.fetch", "secret.set",
		"schedule.create", "schedule.list", "schedule.update", "schedule.pause", "schedule.resume", "schedule.delete", "schedule.run_now",
		"task.search", "task.resume",
	}
	var checks []doctorCheck
	for _, name := range defaultNames {
		if _, ok := registry.Get(name); !ok {
			checks = append(checks, doctorCheck{Status: doctorFAIL, Name: "tool.missing", Message: name})
		}
	}
	for _, name := range []string{"script.run"} {
		if _, ok := registry.Get(name); ok {
			checks = append(checks, doctorCheck{Status: doctorFAIL, Name: "tool.unexpected", Message: name})
		}
	}
	for _, tool := range registry.List() {
		contract := agentcore.ContractFor(tool)
		if strings.TrimSpace(tool.Description()) == "" {
			checks = append(checks, doctorCheck{Status: doctorFAIL, Name: "tool.description", Message: tool.Name() + " has empty description"})
		}
		if strings.TrimSpace(contract.WhenToUse) == "" || strings.TrimSpace(contract.WhenNotToUse) == "" || strings.TrimSpace(contract.OutputContract) == "" {
			checks = append(checks, doctorCheck{Status: doctorWARN, Name: "tool.contract", Message: tool.Name() + " has sparse model-facing contract"})
		}
	}
	if countDoctorStatus(checks, doctorFAIL) == 0 {
		checks = append(checks, doctorCheck{Status: doctorOK, Name: "tools", Message: fmt.Sprintf("%d default tools", len(registry.List()))})
	}
	return checks
}

func doctorSkills(cfg *config.Root) []doctorCheck {
	workspace := strings.TrimSpace(cfg.App.Workspace)
	if workspace == "" {
		workspace = filepath.Join(config.DefaultHome(), "workspace")
	}
	skillDir := filepath.Join(workspace, "skills")
	entries, err := os.ReadDir(skillDir)
	if err != nil {
		return []doctorCheck{{Status: doctorWARN, Name: "skills", Message: err.Error()}}
	}
	var checks []doctorCheck
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(skillDir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			checks = append(checks, doctorCheck{Status: doctorWARN, Name: "skill.read", Message: err.Error()})
			continue
		}
		count++
		text := string(data)
		metadata, hasMetadata, metadataErr := skill.ReadMetadata(filepath.Dir(path))
		if metadataErr != nil {
			checks = append(checks, doctorCheck{Status: doctorWARN, Name: "skill.metadata", Message: metadataErr.Error()})
		} else if hasMetadata && strings.TrimSpace(metadata.ToolRuntime) == "mateway" {
			checks = append(checks, doctorCheck{Status: doctorOK, Name: "skill.metadata", Message: filepath.Join(filepath.Dir(path), ".mateway", "metadata.yaml")})
		}
		if strings.Contains(text, "script.run") || strings.Contains(text, "mateway script") || strings.Contains(text, "confirm_tool") {
			checks = append(checks, doctorCheck{Status: doctorWARN, Name: "skill.stale_tooling", Message: path})
		}
		if !hasMetadata && (strings.Contains(text, "allowed-tools: Bash") || strings.Contains(text, "Bash(")) {
			checks = append(checks, doctorCheck{Status: doctorWARN, Name: "skill.external_metadata_missing", Message: path})
		}
		if strings.Contains(strings.ToLower(text), "approval pending") || strings.Contains(strings.ToLower(text), "completion review") || strings.Contains(strings.ToLower(text), "followup router") {
			checks = append(checks, doctorCheck{Status: doctorWARN, Name: "skill.stale_runtime", Message: path})
		}
	}
	if count == 0 {
		checks = append(checks, doctorCheck{Status: doctorWARN, Name: "skills", Message: "no workspace skills found"})
	} else {
		checks = append(checks, doctorCheck{Status: doctorOK, Name: "skills", Message: fmt.Sprintf("%d workspace skills", count)})
	}
	return checks
}

func doctorRuntimeDirs(cfg *config.Root) []doctorCheck {
	home := strings.TrimSpace(cfg.App.Home)
	if home == "" {
		home = config.DefaultHome()
	}
	var checks []doctorCheck
	for _, rel := range []string{"config", "workspace"} {
		path := filepath.Join(home, rel)
		if _, err := os.Stat(path); err != nil {
			checks = append(checks, doctorCheck{Status: doctorWARN, Name: "runtime_dir", Message: err.Error()})
		} else {
			checks = append(checks, doctorCheck{Status: doctorOK, Name: "runtime_dir", Message: path})
		}
	}
	return checks
}
