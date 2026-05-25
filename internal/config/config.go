package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Root struct {
	App       AppConfig       `yaml:"app"`
	Security  SecurityConfig  `yaml:"security"`
	Search    SearchConfig    `yaml:"search"`
	Model     ModelSelection  `yaml:"model"`
	Memory    MemoryConfig    `yaml:"memory"`
	Learning  LearningConfig  `yaml:"learning"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
	Agents    AgentsConfig    `yaml:"agents"`
	Models    []ModelConfig   `yaml:"-"`
	Channels  ChannelsConfig  `yaml:"-"`
}

type AppConfig struct {
	Name      string `yaml:"name"`
	Home      string `yaml:"home"`
	Workspace string `yaml:"workspace"`
}

type SecurityConfig struct {
	EnforceWorkspacePaths       bool     `yaml:"enforce_workspace_paths"`
	RequireApprovalForRiskyTool bool     `yaml:"require_approval_for_risky_tools"`
	AccessiblePaths             []string `yaml:"accessible_paths"`
}

type SearchConfig struct {
	DefaultTool        string                `yaml:"default_tool"`
	ProviderOrder      []string              `yaml:"provider_order"`
	CacheEnabled       bool                  `yaml:"cache_enabled"`
	CacheTTLHours      int                   `yaml:"cache_ttl_hours"`
	FreshCacheTTLHours int                   `yaml:"fresh_cache_ttl_hours"`
	Providers          SearchProvidersConfig `yaml:"providers"`
}

type SearchProvidersConfig struct {
	Tavily     SearchProviderConfig `yaml:"tavily"`
	DuckDuckGo SearchProviderConfig `yaml:"duckduckgo"`
}

type SearchProviderConfig struct {
	Enabled        bool     `yaml:"enabled"`
	BaseURL        string   `yaml:"base_url"`
	APIKey         string   `yaml:"api_key"`
	APIKeyEnv      string   `yaml:"api_key_env"`
	TimeoutSeconds int      `yaml:"timeout_seconds"`
	MaxResults     int      `yaml:"max_results"`
	DailyBudget    int      `yaml:"daily_budget"`
	MonthlyBudget  int      `yaml:"monthly_budget"`
	SearchDepth    string   `yaml:"search_depth"`
	Topic          string   `yaml:"topic"`
	IncludeDomains []string `yaml:"include_domains"`
	ExcludeDomains []string `yaml:"exclude_domains"`
	Region         string   `yaml:"region"`
}

type ModelSelection struct {
	Default   string            `yaml:"default"`
	Fallbacks []string          `yaml:"fallbacks"`
	Roles     map[string]string `yaml:"roles"`
}

type ModelConfig struct {
	Name           string `yaml:"name"`
	Provider       string `yaml:"provider"`
	API            string `yaml:"api"`
	Model          string `yaml:"model"`
	APIBase        string `yaml:"api_base"`
	APIKey         string `yaml:"api_key"`
	APIKeyEnv      string `yaml:"api_key_env"`
	StripReasoning bool   `yaml:"strip_reasoning"`
	Enabled        bool   `yaml:"enabled"`
	Description    string `yaml:"description"`
}

type MemoryConfig struct {
	Enabled           bool     `yaml:"enabled"`
	Root              string   `yaml:"root"`
	RecentDays        int      `yaml:"recent_days"`
	AutoPropose       bool     `yaml:"auto_propose"`
	AutoCommitLowRisk bool     `yaml:"auto_commit_low_risk"`
	RequireConfirmFor []string `yaml:"require_confirm_for"`
}

type LearningConfig struct {
	Enabled              bool                       `yaml:"enabled"`
	SkillCrystallization SkillCrystallizationConfig `yaml:"skill_crystallization"`
}

type SkillCrystallizationConfig struct {
	Enabled            bool   `yaml:"enabled"`
	SuccessThreshold   int    `yaml:"success_threshold"`
	MinConfidence      string `yaml:"min_confidence"`
	RequireUserConfirm bool   `yaml:"require_user_confirm"`
	AskTiming          string `yaml:"ask_timing"`
}

type SchedulerConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Timezone string `yaml:"timezone"`
	StateDir string `yaml:"state_dir"`
}

type AgentsConfig struct {
	Default  string               `yaml:"default"`
	Profiles []AgentProfileConfig `yaml:"profiles"`
	Bindings []AgentBindingConfig `yaml:"bindings"`
}

type AgentProfileConfig struct {
	ID               string            `yaml:"id"`
	Name             string            `yaml:"name"`
	Default          bool              `yaml:"default"`
	WorkspaceRoot    string            `yaml:"workspace_root"`
	AgentDir         string            `yaml:"agent_dir"`
	SessionNamespace string            `yaml:"session_namespace"`
	Model            ModelSelection    `yaml:"model"`
	Heartbeat        HeartbeatConfig   `yaml:"heartbeat"`
	Skills           AccessListConfig  `yaml:"skills"`
	Tools            AccessListConfig  `yaml:"tools"`
	Metadata         map[string]string `yaml:"metadata"`
}

type HeartbeatConfig struct {
	Enabled         bool                `yaml:"enabled"`
	Interval        string              `yaml:"interval"`
	Schedule        HeartbeatSchedule   `yaml:"schedule"`
	Jobs            []string            `yaml:"jobs"`
	AutoSendSummary bool                `yaml:"auto_send_summary"`
	QuietHours      HeartbeatQuietHours `yaml:"quiet_hours"`
}

type HeartbeatSchedule struct {
	DailyAt string `yaml:"daily_at"`
}

type HeartbeatQuietHours struct {
	Start string `yaml:"start"`
	End   string `yaml:"end"`
}

type AccessListConfig struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

type AgentBindingConfig struct {
	Channel   string `yaml:"channel"`
	AccountID string `yaml:"account_id"`
	PeerID    string `yaml:"peer_id"`
	AgentID   string `yaml:"agent_id"`
}

type ChannelsConfig struct {
	Feishu FeishuConfig `yaml:"feishu"`
}

type FeishuConfig struct {
	Enabled              bool                  `yaml:"enabled"`
	AppID                string                `yaml:"app_id"`
	AppIDEnv             string                `yaml:"app_id_env"`
	AppSecret            string                `yaml:"app_secret"`
	AppSecretEnv         string                `yaml:"app_secret_env"`
	VerificationToken    string                `yaml:"verification_token"`
	VerificationTokenEnv string                `yaml:"verification_token_env"`
	EncryptKey           string                `yaml:"encrypt_key"`
	EncryptKeyEnv        string                `yaml:"encrypt_key_env"`
	BaseURL              string                `yaml:"base_url"`
	BotName              string                `yaml:"bot_name"`
	AutoReply            bool                  `yaml:"auto_reply"`
	MentionRequiredGroup bool                  `yaml:"mention_required_in_group"`
	Webhook              FeishuWebhookConfig   `yaml:"webhook"`
	WebSocket            FeishuWebSocketConfig `yaml:"websocket"`
}

type FeishuWebhookConfig struct {
	Enabled bool   `yaml:"enabled"`
	Addr    string `yaml:"addr"`
	Path    string `yaml:"path"`
}

type FeishuWebSocketConfig struct {
	Enabled bool `yaml:"enabled"`
}

func (c FeishuConfig) ResolveSecrets() FeishuConfig {
	c.AppID = firstNonEmpty(c.AppID, getenv(c.AppIDEnv))
	c.AppSecret = firstNonEmpty(c.AppSecret, getenv(c.AppSecretEnv))
	c.VerificationToken = firstNonEmpty(c.VerificationToken, getenv(c.VerificationTokenEnv))
	c.EncryptKey = firstNonEmpty(c.EncryptKey, getenv(c.EncryptKeyEnv))
	return c
}

func (c SearchProviderConfig) ResolvedAPIKey() string {
	return firstNonEmpty(c.APIKey, getenv(c.APIKeyEnv))
}

func (c ModelConfig) ResolvedAPIKey() string {
	return firstNonEmpty(c.APIKey, getenv(c.APIKeyEnv))
}

func DefaultHome() string {
	if home := strings.TrimSpace(os.Getenv("MATEWAY_HOME")); home != "" {
		return home
	}
	if userHome, err := os.UserHomeDir(); err == nil {
		return filepath.Join(userHome, ".mateway")
	}
	return ".mateway"
}

type Loader struct {
	Home string
}

func NewLoader(home string) Loader {
	if strings.TrimSpace(home) == "" {
		home = DefaultHome()
	}
	return Loader{Home: home}
}

func (l Loader) ConfigDir() string {
	return filepath.Join(l.Home, "config")
}

func (l Loader) Load() (*Root, error) {
	if err := l.loadEnvFile(); err != nil {
		return nil, err
	}
	root := &Root{}
	if err := readYAML(filepath.Join(l.ConfigDir(), "config.yaml"), root); err != nil {
		return nil, err
	}
	if root.App.Home == "" {
		root.App.Home = l.Home
	}
	if root.App.Workspace == "" {
		root.App.Workspace = filepath.Join(root.App.Home, "workspace")
	}
	models, err := l.loadModels()
	if err != nil {
		return nil, err
	}
	root.Models = models
	root.normalizeAgents()
	channels, err := l.loadChannels()
	if err != nil {
		return nil, err
	}
	root.Channels = channels
	return root, nil
}

func (r *Root) DefaultAgent() AgentProfileConfig {
	r.normalizeAgents()
	for _, profile := range r.Agents.Profiles {
		if strings.EqualFold(profile.ID, r.Agents.Default) {
			return profile
		}
	}
	return r.Agents.Profiles[0]
}

func (r *Root) DefaultAgentStrict() (AgentProfileConfig, error) {
	r.normalizeAgents()
	for _, profile := range r.Agents.Profiles {
		if strings.EqualFold(profile.ID, r.Agents.Default) {
			return profile, nil
		}
	}
	return AgentProfileConfig{}, fmt.Errorf("configured default agent %q is not defined", r.Agents.Default)
}

func (r *Root) normalizeAgents() {
	if strings.TrimSpace(r.Agents.Default) == "" {
		for _, profile := range r.Agents.Profiles {
			if profile.Default && strings.TrimSpace(profile.ID) != "" {
				r.Agents.Default = strings.TrimSpace(profile.ID)
				break
			}
		}
	}
	if strings.TrimSpace(r.Agents.Default) == "" {
		r.Agents.Default = "main"
	}
	if len(r.Agents.Profiles) == 0 {
		r.Agents.Profiles = []AgentProfileConfig{{
			ID:               r.Agents.Default,
			Name:             "Main Assistant",
			Default:          true,
			SessionNamespace: r.Agents.Default,
			Model:            r.Model,
		}}
	}
	for i := range r.Agents.Profiles {
		profile := &r.Agents.Profiles[i]
		if strings.TrimSpace(profile.ID) == "" {
			profile.ID = fmt.Sprintf("agent-%d", i+1)
		}
		profile.ID = strings.TrimSpace(profile.ID)
		if strings.TrimSpace(profile.Name) == "" {
			profile.Name = profile.ID
		}
		if strings.TrimSpace(profile.SessionNamespace) == "" {
			profile.SessionNamespace = profile.ID
		}
		if profile.Model.Empty() {
			profile.Model = r.Model
		}
		if strings.EqualFold(profile.ID, r.Agents.Default) {
			profile.Default = true
		}
	}
}

func (m ModelSelection) Empty() bool {
	return strings.TrimSpace(m.Default) == "" && len(m.Fallbacks) == 0 && len(m.Roles) == 0
}

func (l Loader) loadEnvFile() error {
	path := filepath.Join(l.ConfigDir(), "mateway.env")
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open env file %s: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		value = os.ExpandEnv(value)
		if key == "" || value == "" {
			continue
		}
		if existing := strings.TrimSpace(os.Getenv(key)); existing != "" {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set env %s: %w", key, err)
		}
	}
	return scanner.Err()
}

func (l Loader) loadModels() ([]ModelConfig, error) {
	dir := filepath.Join(l.ConfigDir(), "models")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read model config dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if shouldSkipConfigFile(entry, name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	models := make([]ModelConfig, 0, len(names))
	seen := map[string]string{}
	for _, name := range names {
		var model ModelConfig
		if err := readYAML(filepath.Join(dir, name), &model); err != nil {
			return nil, err
		}
		if model.Enabled {
			key := strings.ToLower(strings.TrimSpace(model.Name))
			if key == "" {
				return nil, fmt.Errorf("model config %s has empty name", filepath.Join(dir, name))
			}
			if previous := seen[key]; previous != "" {
				return nil, fmt.Errorf("duplicate enabled model name %q in %s and %s", model.Name, previous, filepath.Join(dir, name))
			}
			seen[key] = filepath.Join(dir, name)
			models = append(models, model)
		}
	}
	return models, nil
}

func shouldSkipConfigFile(entry os.DirEntry, name string) bool {
	if entry.IsDir() {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" || strings.HasPrefix(lower, "_") {
		return true
	}
	if !strings.HasSuffix(lower, ".yaml") {
		return true
	}
	base := strings.TrimSuffix(lower, ".yaml")
	return strings.HasSuffix(base, ".sample") || strings.HasSuffix(base, ".example")
}

func (l Loader) loadChannels() (ChannelsConfig, error) {
	var channels ChannelsConfig
	path := filepath.Join(l.ConfigDir(), "channels", "feishu.yaml")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return channels, nil
	}
	if err := readYAML(path, &channels); err != nil {
		return channels, err
	}
	return channels, nil
}

func readYAML(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func getenv(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(strings.TrimSpace(name)))
}
