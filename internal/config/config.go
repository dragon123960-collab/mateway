package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Root struct {
	App       AppConfig       `yaml:"app"`
	Security  SecurityConfig  `yaml:"security"`
	Search    SearchConfig    `yaml:"search"`
	Model     ModelSelection  `yaml:"model"`
	Execution ExecutionConfig `yaml:"execution"`
	Memory    MemoryConfig    `yaml:"memory"`
	Learning  LearningConfig  `yaml:"learning"`
	Skills    SkillsConfig    `yaml:"skills"`
	Remote    RemoteConfig    `yaml:"remote"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
	Agents    AgentsConfig    `yaml:"agents"`
	Models    []ModelConfig   `yaml:"-"`
	Channels  ChannelsConfig  `yaml:"-"`
}

func DefaultRoot() Root {
	root := Root{
		App: AppConfig{Name: "mateway"},
		Model: ModelSelection{
			Default:   "minimax",
			Fallbacks: []string{},
		},
		Execution: ExecutionConfig{
			MaxParallelTools:  4,
			MaxIterations:     intPtr(50),
			InactivityTimeout: "5m",
			ContextBudget: ContextBudgetConfig{
				Enabled:                boolPtr(true),
				SoftRatio:              0.65,
				HardRatio:              0.90,
				RecentTurns:            8,
				ToolResultTargetTokens: 1200,
				MaxVisibleTools:        8,
				DefaultVisible: []string{
					"file.read",
					"file.write",
					"file.edit",
					"terminal.run",
					"web.search",
					"web.fetch",
					"toolresult.read",
				},
				TraceTelemetry: boolPtr(true),
			},
		},
		Memory: MemoryConfig{
			Enabled:           true,
			RecentDays:        3,
			AutoPropose:       true,
			AutoCommitLowRisk: false,
			ProposalNudge: ProposalNudgeConfig{
				Enabled:      boolPtr(true),
				Interval:     "24h",
				Channels:     []string{"cli"},
				MaxProposals: 3,
			},
		},
		Learning: LearningConfig{Enabled: true, SkillCrystallization: SkillCrystallizationConfig{
			Enabled:          true,
			SuccessThreshold: 3,
			MinConfidence:    "medium",
		}},
		Scheduler: SchedulerConfig{
			Enabled:  false,
			Timezone: "Asia/Shanghai",
			Interval: "30s",
		},
		Remote: RemoteConfig{
			Profiles: []RemoteProfileConfig{},
		},
		Security: SecurityConfig{
			EnforceWorkspacePaths: true,
			AccessiblePaths:       []string{},
			TerminalSandbox: TerminalSandboxConfig{
				Enabled:        false,
				Mode:           "restricted",
				TimeoutSeconds: 20,
				CommandPrefix:  []string{},
			},
		},
		Search: SearchConfig{
			DefaultTool:        "tavily",
			ProviderOrder:      []string{"tavily", "searxng", "duckduckgo"},
			CacheEnabled:       true,
			CacheTTLHours:      168,
			FreshCacheTTLHours: 6,
			Providers: SearchProvidersConfig{
				Tavily:     SearchProviderConfig{Enabled: false, BaseURL: "https://api.tavily.com/search", APIKeyEnv: "TAVILY_API_KEY", TimeoutSeconds: 8, MaxResults: 5, DailyBudget: 20, MonthlyBudget: 900, SearchDepth: "basic", Topic: "general"},
				SearXNG:    SearchProviderConfig{Enabled: false, BaseURL: "http://127.0.0.1:8088", TimeoutSeconds: 8, MaxResults: 5},
				DuckDuckGo: SearchProviderConfig{Enabled: true, TimeoutSeconds: 4, MaxResults: 5, Region: "cn-zh"},
			},
		},
	}
	root.Skills.Catalogs = []SkillCatalogConfig{
		{Name: "skills.sh", Enabled: true, BaseURL: "https://skills.sh", SearchURL: "https://skills.sh/?q={query}", InstallURL: "", TrustLevel: "high"},
		{Name: "skillhub.cn", Enabled: false, BaseURL: "https://skillhub.cn", SearchURL: "https://skillhub.cn/search?q={query}", InstallURL: "", TrustLevel: "unknown"},
		{Name: "clawhub.ai", Enabled: false, BaseURL: "https://clawhub.ai", SearchURL: "https://clawhub.ai/search?q={query}", InstallURL: "", TrustLevel: "medium"},
	}
	root.Agents = AgentsConfig{
		Default: "main",
		Profiles: []AgentProfileConfig{{
			ID:               "main",
			Name:             "Main Assistant",
			Default:          true,
			SessionNamespace: "main",
			Model:            root.Model,
			Heartbeat: HeartbeatConfig{
				Enabled:  false,
				Interval: "30m",
				Schedule: HeartbeatSchedule{DailyAt: "03:30"},
				Jobs:     []string{"memory_lint", "memory_index_rebuild", "memory_distill", "learning_distill", "skill_learning"},
				QuietHours: HeartbeatQuietHours{
					Start: "23:00",
					End:   "08:00",
				},
			},
		}},
		Bindings: []AgentBindingConfig{{Channel: "cli", AgentID: "main"}, {Channel: "feishu", AgentID: "main"}},
	}
	return root
}

func DefaultTimezone() string {
	return DefaultRoot().Scheduler.Timezone
}

func TimezoneLocation(timezone string) (*time.Location, string) {
	name := strings.TrimSpace(timezone)
	if name == "" {
		name = DefaultTimezone()
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		name = DefaultTimezone()
		loc, err = time.LoadLocation(name)
		if err != nil {
			return time.Local, time.Local.String()
		}
	}
	return loc, name
}

func (r *Root) TimezoneLocation() (*time.Location, string) {
	if r == nil {
		return TimezoneLocation("")
	}
	return TimezoneLocation(r.Scheduler.Timezone)
}

type AppConfig struct {
	Name      string `yaml:"name"`
	Home      string `yaml:"home"`
	Workspace string `yaml:"workspace"`
}

type SecurityConfig struct {
	EnforceWorkspacePaths bool                  `yaml:"enforce_workspace_paths"`
	AccessiblePaths       []string              `yaml:"accessible_paths"`
	TerminalSandbox       TerminalSandboxConfig `yaml:"terminal_sandbox"`
}

type TerminalSandboxConfig struct {
	Enabled        bool     `yaml:"enabled"`
	Mode           string   `yaml:"mode"`
	WorkDir        string   `yaml:"workdir"`
	TimeoutSeconds int      `yaml:"timeout_seconds"`
	CommandPrefix  []string `yaml:"command_prefix"`
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
	SearXNG    SearchProviderConfig `yaml:"searxng"`
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
	Default   string     `yaml:"default"`
	Fallbacks []string   `yaml:"fallbacks"`
	Roles     ModelRoles `yaml:"roles"`
}

type ModelRoles map[string][]string

func (r ModelRoles) Models(role string) []string {
	if len(r) == 0 {
		return nil
	}
	values := r[strings.TrimSpace(role)]
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func (r *ModelRoles) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
		*r = nil
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("model roles must be a mapping")
	}
	out := ModelRoles{}
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := strings.TrimSpace(value.Content[i].Value)
		if key == "" {
			continue
		}
		values, err := stringOrStringList(value.Content[i+1])
		if err != nil {
			return fmt.Errorf("model role %s: %w", key, err)
		}
		out[key] = values
	}
	*r = out
	return nil
}

func (r ModelRoles) MarshalYAML() (any, error) {
	out := map[string]any{}
	for key, values := range r {
		if len(values) == 1 {
			out[key] = values[0]
		} else {
			out[key] = values
		}
	}
	return out, nil
}

func (r *ModelRoles) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := ModelRoles{}
	for key, value := range raw {
		switch v := value.(type) {
		case string:
			out[key] = []string{v}
		case []any:
			for _, item := range v {
				text, ok := item.(string)
				if !ok {
					return fmt.Errorf("model role %s must contain strings", key)
				}
				out[key] = append(out[key], text)
			}
		default:
			return fmt.Errorf("model role %s must be string or string list", key)
		}
	}
	*r = out
	return nil
}

func stringOrStringList(node *yaml.Node) ([]string, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!null" || strings.TrimSpace(node.Value) == "" {
			return nil, nil
		}
		return []string{node.Value}, nil
	case yaml.SequenceNode:
		values := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("must contain scalar strings")
			}
			values = append(values, item.Value)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("must be string or string list")
	}
}

type ModelConfig struct {
	Name           string   `yaml:"name"`
	Provider       string   `yaml:"provider"`
	API            string   `yaml:"api"`
	Model          string   `yaml:"model"`
	APIBase        string   `yaml:"api_base"`
	APIKey         string   `yaml:"api_key"`
	APIKeyEnv      string   `yaml:"api_key_env"`
	Modalities     []string `yaml:"modalities"`
	ContextWindow  int      `yaml:"context_window"`
	MaxTokens      int      `yaml:"max_tokens"`
	StripReasoning bool     `yaml:"strip_reasoning"`
	Enabled        bool     `yaml:"enabled"`
	Description    string   `yaml:"description"`
}

type ExecutionConfig struct {
	MaxParallelTools  int                 `yaml:"max_parallel_tools"`
	MaxIterations     *int                `yaml:"max_iterations"`
	InactivityTimeout string              `yaml:"inactivity_timeout"`
	ContextBudget     ContextBudgetConfig `yaml:"context_budget"`
}

type ContextBudgetConfig struct {
	Enabled                *bool    `yaml:"enabled"`
	SoftRatio              float64  `yaml:"soft_ratio"`
	HardRatio              float64  `yaml:"hard_ratio"`
	RecentTurns            int      `yaml:"recent_turns"`
	ToolResultTargetTokens int      `yaml:"tool_result_target_tokens"`
	MaxVisibleTools        int      `yaml:"max_visible_tools"`
	DefaultVisible         []string `yaml:"default_visible"`
	TraceTelemetry         *bool    `yaml:"trace_telemetry"`
}

func (c ContextBudgetConfig) EnabledValue() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func (c ContextBudgetConfig) TraceTelemetryValue() bool {
	if c.TraceTelemetry == nil {
		return true
	}
	return *c.TraceTelemetry
}

func (c ContextBudgetConfig) SoftRatioValue() float64 {
	if c.SoftRatio <= 0 || c.SoftRatio > 1 {
		return 0.65
	}
	return c.SoftRatio
}

func (c ContextBudgetConfig) HardRatioValue() float64 {
	if c.HardRatio <= 0 || c.HardRatio > 1 {
		return 0.90
	}
	return c.HardRatio
}

func (c ContextBudgetConfig) RecentTurnsValue() int {
	if c.RecentTurns <= 0 {
		return 8
	}
	return c.RecentTurns
}

func (c ContextBudgetConfig) ToolResultTargetTokensValue() int {
	if c.ToolResultTargetTokens <= 0 {
		return 1200
	}
	return c.ToolResultTargetTokens
}

func (c ContextBudgetConfig) MaxVisibleToolsValue() int {
	if c.MaxVisibleTools <= 0 {
		return 8
	}
	return c.MaxVisibleTools
}

var defaultVisibleTools = []string{
	"file.read",
	"file.write",
	"file.edit",
	"terminal.run",
	"web.search",
	"web.fetch",
	"toolresult.read",
}

func (c ContextBudgetConfig) DefaultVisibleValue() []string {
	if len(c.DefaultVisible) == 0 {
		return append([]string(nil), defaultVisibleTools...)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(c.DefaultVisible))
	for _, name := range c.DefaultVisible {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return append([]string(nil), defaultVisibleTools...)
	}
	return out
}

func (c ExecutionConfig) MaxIterationsValue() int {
	if c.MaxIterations == nil {
		return 50
	}
	if *c.MaxIterations < 0 {
		return 50
	}
	return *c.MaxIterations
}

func (c ExecutionConfig) InactivityTimeoutDuration() time.Duration {
	timeout, err := time.ParseDuration(strings.TrimSpace(c.InactivityTimeout))
	if err != nil || timeout < 0 {
		return 0
	}
	return timeout
}

type MemoryConfig struct {
	Enabled           bool                `yaml:"enabled"`
	Root              string              `yaml:"root"`
	RecentDays        int                 `yaml:"recent_days"`
	AutoPropose       bool                `yaml:"auto_propose"`
	AutoCommitLowRisk bool                `yaml:"auto_commit_low_risk"`
	ProposalNudge     ProposalNudgeConfig `yaml:"proposal_nudge"`
}

type ProposalNudgeConfig struct {
	Enabled      *bool    `yaml:"enabled"`
	Interval     string   `yaml:"interval"`
	Channels     []string `yaml:"channels"`
	MaxProposals int      `yaml:"max_proposals"`
}

func (c ProposalNudgeConfig) EnabledValue() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type LearningConfig struct {
	Enabled              bool                       `yaml:"enabled"`
	SkillCrystallization SkillCrystallizationConfig `yaml:"skill_crystallization"`
}

type SkillCrystallizationConfig struct {
	Enabled          bool   `yaml:"enabled"`
	SuccessThreshold int    `yaml:"success_threshold"`
	MinConfidence    string `yaml:"min_confidence"`
}

type SkillsConfig struct {
	Catalogs []SkillCatalogConfig `yaml:"catalogs"`
}

type RemoteConfig struct {
	Profiles []RemoteProfileConfig `yaml:"profiles"`
}

type RemoteProfileConfig struct {
	Alias          string   `yaml:"alias"`
	Host           string   `yaml:"host"`
	User           string   `yaml:"user"`
	Port           int      `yaml:"port"`
	AuthSecretID   string   `yaml:"auth_secret_id"`
	AllowedClasses []string `yaml:"allowed_classes"`
}

type SkillCatalogConfig struct {
	Name       string `yaml:"name"`
	Enabled    bool   `yaml:"enabled"`
	BaseURL    string `yaml:"base_url"`
	SearchURL  string `yaml:"search_url"`
	InstallURL string `yaml:"install_url"`
	TrustLevel string `yaml:"trust_level"`
}

type SchedulerConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Timezone string `yaml:"timezone"`
	StateDir string `yaml:"state_dir"`
	Interval string `yaml:"interval"`
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
	Weixin WeixinConfig `yaml:"weixin"`
}

type FeishuConfig struct {
	Enabled              bool                  `yaml:"enabled"`
	DefaultAccount       string                `yaml:"default_account"`
	Accounts             []FeishuAccountConfig `yaml:"accounts"`
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

type FeishuAccountConfig struct {
	ID                   string                `yaml:"id"`
	Enabled              *bool                 `yaml:"enabled"`
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
	AutoReply            *bool                 `yaml:"auto_reply"`
	MentionRequiredGroup *bool                 `yaml:"mention_required_in_group"`
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

type WeixinConfig struct {
	Enabled              bool   `yaml:"enabled"`
	BaseURL              string `yaml:"base_url"`
	BaseURLEnv           string `yaml:"base_url_env"`
	AccountID            string `yaml:"account_id"`
	AccountIDEnv         string `yaml:"account_id_env"`
	Token                string `yaml:"token"`
	TokenEnv             string `yaml:"token_env"`
	AccountDir           string `yaml:"account_dir"`
	MediaDir             string `yaml:"media_dir"`
	BotAgent             string `yaml:"bot_agent"`
	PollTimeoutMS        int    `yaml:"poll_timeout_ms"`
	RetryInterval        string `yaml:"retry_interval"`
	MentionRequiredGroup bool   `yaml:"mention_required_in_group"`
}

func (c WeixinConfig) ResolveSecrets() WeixinConfig {
	c.BaseURL = firstNonEmpty(c.BaseURL, getenv(c.BaseURLEnv))
	c.AccountID = firstNonEmpty(c.AccountID, getenv(c.AccountIDEnv))
	c.Token = firstNonEmpty(c.Token, getenv(c.TokenEnv))
	return c
}

func (c FeishuConfig) ResolveSecrets() FeishuConfig {
	c.AppID = firstNonEmpty(c.AppID, getenv(c.AppIDEnv))
	c.AppSecret = firstNonEmpty(c.AppSecret, getenv(c.AppSecretEnv))
	c.VerificationToken = firstNonEmpty(c.VerificationToken, getenv(c.VerificationTokenEnv))
	c.EncryptKey = firstNonEmpty(c.EncryptKey, getenv(c.EncryptKeyEnv))
	return c
}

func (c FeishuConfig) AccountConfigs() []FeishuConfig {
	base := c
	base.Accounts = nil
	if strings.TrimSpace(base.DefaultAccount) == "" {
		base.DefaultAccount = "default"
	}
	if len(c.Accounts) == 0 {
		if strings.TrimSpace(base.AppID) == "" && strings.TrimSpace(base.AppIDEnv) == "" &&
			strings.TrimSpace(base.AppSecret) == "" && strings.TrimSpace(base.AppSecretEnv) == "" {
			return nil
		}
		return []FeishuConfig{base}
	}
	out := make([]FeishuConfig, 0, len(c.Accounts))
	for _, account := range c.Accounts {
		cfg := base
		cfg.Accounts = nil
		if id := strings.TrimSpace(account.ID); id != "" {
			cfg.DefaultAccount = id
		}
		if account.Enabled != nil {
			cfg.Enabled = *account.Enabled
		}
		cfg.AppID, cfg.AppIDEnv = overlaySecretRef(cfg.AppID, cfg.AppIDEnv, account.AppID, account.AppIDEnv)
		cfg.AppSecret, cfg.AppSecretEnv = overlaySecretRef(cfg.AppSecret, cfg.AppSecretEnv, account.AppSecret, account.AppSecretEnv)
		cfg.VerificationToken, cfg.VerificationTokenEnv = overlaySecretRef(cfg.VerificationToken, cfg.VerificationTokenEnv, account.VerificationToken, account.VerificationTokenEnv)
		cfg.EncryptKey, cfg.EncryptKeyEnv = overlaySecretRef(cfg.EncryptKey, cfg.EncryptKeyEnv, account.EncryptKey, account.EncryptKeyEnv)
		cfg.BaseURL = overlayString(cfg.BaseURL, account.BaseURL)
		cfg.BotName = overlayString(cfg.BotName, account.BotName)
		if account.AutoReply != nil {
			cfg.AutoReply = *account.AutoReply
		}
		if account.MentionRequiredGroup != nil {
			cfg.MentionRequiredGroup = *account.MentionRequiredGroup
		}
		cfg.Webhook = overlayFeishuWebhook(cfg.Webhook, account.Webhook)
		cfg.WebSocket = overlayFeishuWebSocket(cfg.WebSocket, account.WebSocket)
		out = append(out, cfg)
	}
	return out
}

func (c SearchProviderConfig) ResolvedAPIKey() string {
	return firstNonEmpty(c.APIKey, getenvWithMatewayFallback(c.APIKeyEnv))
}

func (c ModelConfig) ResolvedAPIKey() string {
	return firstNonEmpty(c.APIKey, getenvWithMatewayFallback(c.APIKeyEnv))
}

func (c ModelConfig) SupportsModality(modality string) bool {
	modality = strings.TrimSpace(strings.ToLower(modality))
	if modality == "" {
		return false
	}
	modalities := c.Modalities
	if len(modalities) == 0 {
		modalities = []string{"text"}
	}
	for _, candidate := range modalities {
		if strings.EqualFold(strings.TrimSpace(candidate), modality) {
			return true
		}
	}
	return false
}

func (c ModelConfig) MaxTokensValue() int {
	if c.MaxTokens > 0 {
		return c.MaxTokens
	}
	return 4096
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
	root.NormalizeForUse()
	channels, err := l.loadChannels()
	if err != nil {
		return nil, err
	}
	root.Channels = channels
	return root, nil
}

func (r *Root) NormalizeForUse() {
	if r == nil {
		return
	}
	r.applyDefaults()
	r.normalizeSearch()
	r.normalizeSkills()
	r.normalizeRemote()
	r.normalizeAgents()
}

func (r *Root) applyDefaults() {
	defaults := DefaultRoot()
	if strings.TrimSpace(r.App.Name) == "" {
		r.App.Name = defaults.App.Name
	}
	if strings.TrimSpace(r.Model.Default) == "" {
		r.Model.Default = defaults.Model.Default
	}
	if r.Model.Fallbacks == nil {
		r.Model.Fallbacks = defaults.Model.Fallbacks
	}
	if r.Model.Roles == nil {
		r.Model.Roles = ModelRoles{}
	}
	if r.Execution.MaxParallelTools <= 0 {
		r.Execution.MaxParallelTools = defaults.Execution.MaxParallelTools
	}
	if r.Execution.MaxIterations == nil {
		r.Execution.MaxIterations = defaults.Execution.MaxIterations
	} else if *r.Execution.MaxIterations < 0 {
		r.Execution.MaxIterations = defaults.Execution.MaxIterations
	}
	if strings.TrimSpace(r.Execution.InactivityTimeout) == "" {
		r.Execution.InactivityTimeout = defaults.Execution.InactivityTimeout
	}
	if r.Execution.ContextBudget.Enabled == nil {
		r.Execution.ContextBudget.Enabled = defaults.Execution.ContextBudget.Enabled
	}
	if r.Execution.ContextBudget.SoftRatio <= 0 {
		r.Execution.ContextBudget.SoftRatio = defaults.Execution.ContextBudget.SoftRatio
	}
	if r.Execution.ContextBudget.HardRatio <= 0 {
		r.Execution.ContextBudget.HardRatio = defaults.Execution.ContextBudget.HardRatio
	}
	if r.Execution.ContextBudget.HardRatio < r.Execution.ContextBudget.SoftRatio {
		r.Execution.ContextBudget.HardRatio = defaults.Execution.ContextBudget.HardRatio
	}
	if r.Execution.ContextBudget.RecentTurns <= 0 {
		r.Execution.ContextBudget.RecentTurns = defaults.Execution.ContextBudget.RecentTurns
	}
	if r.Execution.ContextBudget.ToolResultTargetTokens <= 0 {
		r.Execution.ContextBudget.ToolResultTargetTokens = defaults.Execution.ContextBudget.ToolResultTargetTokens
	}
	if r.Execution.ContextBudget.MaxVisibleTools <= 0 {
		r.Execution.ContextBudget.MaxVisibleTools = defaults.Execution.ContextBudget.MaxVisibleTools
	}
	if r.Execution.ContextBudget.TraceTelemetry == nil {
		r.Execution.ContextBudget.TraceTelemetry = defaults.Execution.ContextBudget.TraceTelemetry
	}
	if r.Memory.RecentDays <= 0 {
		r.Memory.RecentDays = defaults.Memory.RecentDays
	}
	proposalNudgeUnset := strings.TrimSpace(r.Memory.ProposalNudge.Interval) == "" && len(r.Memory.ProposalNudge.Channels) == 0 && r.Memory.ProposalNudge.MaxProposals == 0 && r.Memory.ProposalNudge.Enabled == nil
	if proposalNudgeUnset {
		r.Memory.ProposalNudge.Enabled = defaults.Memory.ProposalNudge.Enabled
	}
	if strings.TrimSpace(r.Memory.ProposalNudge.Interval) == "" {
		r.Memory.ProposalNudge.Interval = defaults.Memory.ProposalNudge.Interval
	}
	if len(r.Memory.ProposalNudge.Channels) == 0 {
		r.Memory.ProposalNudge.Channels = defaults.Memory.ProposalNudge.Channels
	}
	if r.Memory.ProposalNudge.MaxProposals <= 0 {
		r.Memory.ProposalNudge.MaxProposals = defaults.Memory.ProposalNudge.MaxProposals
	}
	if strings.TrimSpace(r.Learning.SkillCrystallization.MinConfidence) == "" {
		r.Learning.SkillCrystallization.MinConfidence = defaults.Learning.SkillCrystallization.MinConfidence
	}
	if r.Learning.SkillCrystallization.SuccessThreshold <= 0 {
		r.Learning.SkillCrystallization.SuccessThreshold = defaults.Learning.SkillCrystallization.SuccessThreshold
	}
	if strings.TrimSpace(r.Scheduler.Timezone) == "" {
		r.Scheduler.Timezone = defaults.Scheduler.Timezone
	}
	if strings.TrimSpace(r.Scheduler.Interval) == "" {
		r.Scheduler.Interval = defaults.Scheduler.Interval
	}
	if r.Security.AccessiblePaths == nil {
		r.Security.AccessiblePaths = []string{}
	}
	if strings.TrimSpace(r.Security.TerminalSandbox.Mode) == "" {
		r.Security.TerminalSandbox.Mode = defaults.Security.TerminalSandbox.Mode
	}
	if r.Security.TerminalSandbox.TimeoutSeconds <= 0 {
		r.Security.TerminalSandbox.TimeoutSeconds = defaults.Security.TerminalSandbox.TimeoutSeconds
	}
	if r.Security.TerminalSandbox.CommandPrefix == nil {
		r.Security.TerminalSandbox.CommandPrefix = []string{}
	}
	if strings.TrimSpace(r.Search.DefaultTool) == "" {
		r.Search.DefaultTool = defaults.Search.DefaultTool
	}
	if r.Search.CacheTTLHours <= 0 {
		r.Search.CacheTTLHours = defaults.Search.CacheTTLHours
	}
	if r.Search.FreshCacheTTLHours <= 0 {
		r.Search.FreshCacheTTLHours = defaults.Search.FreshCacheTTLHours
	}
	mergeSearchProviderDefaults(&r.Search.Providers.Tavily, defaults.Search.Providers.Tavily)
	mergeSearchProviderDefaults(&r.Search.Providers.SearXNG, defaults.Search.Providers.SearXNG)
	mergeSearchProviderDefaults(&r.Search.Providers.DuckDuckGo, defaults.Search.Providers.DuckDuckGo)
}

func mergeSearchProviderDefaults(dst *SearchProviderConfig, defaults SearchProviderConfig) {
	if strings.TrimSpace(dst.BaseURL) == "" {
		dst.BaseURL = defaults.BaseURL
	}
	if strings.TrimSpace(dst.APIKeyEnv) == "" {
		dst.APIKeyEnv = defaults.APIKeyEnv
	}
	if dst.TimeoutSeconds <= 0 {
		dst.TimeoutSeconds = defaults.TimeoutSeconds
	}
	if dst.MaxResults <= 0 {
		dst.MaxResults = defaults.MaxResults
	}
	if dst.DailyBudget <= 0 {
		dst.DailyBudget = defaults.DailyBudget
	}
	if dst.MonthlyBudget <= 0 {
		dst.MonthlyBudget = defaults.MonthlyBudget
	}
	if strings.TrimSpace(dst.SearchDepth) == "" {
		dst.SearchDepth = defaults.SearchDepth
	}
	if strings.TrimSpace(dst.Topic) == "" {
		dst.Topic = defaults.Topic
	}
	if strings.TrimSpace(dst.Region) == "" {
		dst.Region = defaults.Region
	}
}

func (r *Root) normalizeSkills() {
	if len(r.Skills.Catalogs) > 0 {
		return
	}
	r.Skills.Catalogs = []SkillCatalogConfig{
		{Name: "skills.sh", Enabled: true, BaseURL: "https://skills.sh", SearchURL: "https://skills.sh/?q={query}", InstallURL: "", TrustLevel: "high"},
		{Name: "skillhub.cn", Enabled: false, BaseURL: "https://skillhub.cn", SearchURL: "https://skillhub.cn/search?q={query}", InstallURL: "", TrustLevel: "unknown"},
		{Name: "clawhub.ai", Enabled: false, BaseURL: "https://clawhub.ai", SearchURL: "https://clawhub.ai/search?q={query}", InstallURL: "", TrustLevel: "medium"},
	}
}

func (r *Root) normalizeRemote() {
	if r.Remote.Profiles == nil {
		r.Remote.Profiles = []RemoteProfileConfig{}
	}
}

func (r *Root) normalizeSearch() {
	if len(r.Search.ProviderOrder) == 0 {
		if r.Search.Providers.Tavily.Enabled {
			r.Search.ProviderOrder = append(r.Search.ProviderOrder, "tavily")
		}
		if r.Search.Providers.SearXNG.Enabled {
			r.Search.ProviderOrder = append(r.Search.ProviderOrder, "searxng")
		}
		if r.Search.Providers.DuckDuckGo.Enabled {
			r.Search.ProviderOrder = append(r.Search.ProviderOrder, "duckduckgo")
		}
	}
	if len(r.Search.ProviderOrder) == 0 {
		r.Search.ProviderOrder = []string{"tavily", "searxng", "duckduckgo"}
	}
	if strings.TrimSpace(r.Search.DefaultTool) == "" {
		r.Search.DefaultTool = "web.search"
	}
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
	for _, name := range []string{"feishu.yaml", "weixin.yaml"} {
		path := filepath.Join(l.ConfigDir(), "channels", name)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := readYAML(path, &channels); err != nil {
			return channels, err
		}
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

func overlayString(base, value string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return base
}

func overlaySecretRef(baseValue, baseEnv, value, env string) (string, string) {
	if strings.TrimSpace(value) != "" {
		return value, env
	}
	if strings.TrimSpace(env) != "" {
		return "", env
	}
	return baseValue, baseEnv
}

func overlayFeishuWebhook(base, value FeishuWebhookConfig) FeishuWebhookConfig {
	if value.Enabled {
		base.Enabled = true
	}
	if strings.TrimSpace(value.Addr) != "" {
		base.Addr = value.Addr
	}
	if strings.TrimSpace(value.Path) != "" {
		base.Path = value.Path
	}
	return base
}

func overlayFeishuWebSocket(base, value FeishuWebSocketConfig) FeishuWebSocketConfig {
	if value.Enabled {
		base.Enabled = true
	}
	return base
}

func boolPtr(value bool) *bool {
	return &value
}

func intPtr(value int) *int {
	return &value
}

func getenv(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(strings.TrimSpace(name)))
}

func getenvWithMatewayFallback(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if value := getenv(name); value != "" {
		return value
	}
	if strings.HasPrefix(name, "MATEWAY_") {
		return ""
	}
	return getenv("MATEWAY_" + name)
}
