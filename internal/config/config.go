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
	Skills    SkillsConfig    `yaml:"skills"`
	Scripts   ScriptsConfig   `yaml:"scripts"`
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
		Memory: MemoryConfig{
			Enabled:           true,
			RecentDays:        3,
			AutoPropose:       true,
			AutoCommitLowRisk: false,
			RequireConfirmFor: []string{"user_preference", "org_knowledge", "long_memory", "skill_candidate"},
			ProposalNudge: ProposalNudgeConfig{
				Enabled:      boolPtr(true),
				Interval:     "24h",
				Channels:     []string{"cli"},
				MaxProposals: 3,
			},
		},
		Learning: LearningConfig{Enabled: true, SkillCrystallization: SkillCrystallizationConfig{
			Enabled:            true,
			SuccessThreshold:   3,
			MinConfidence:      "medium",
			RequireUserConfirm: true,
			AskTiming:          "next_interaction",
		}},
		Scheduler: SchedulerConfig{
			Enabled:  false,
			Timezone: "Asia/Shanghai",
			Interval: "30s",
		},
		Security: SecurityConfig{
			EnforceWorkspacePaths:       true,
			RequireApprovalForRiskyTool: true,
			AccessiblePaths:             []string{},
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

type AppConfig struct {
	Name              string `yaml:"name"`
	Home              string `yaml:"home"`
	Workspace         string `yaml:"workspace"`
	Locale            string `yaml:"locale"`
	MessageCatalogDir string `yaml:"message_catalog_dir"`
}

type SecurityConfig struct {
	EnforceWorkspacePaths       bool                  `yaml:"enforce_workspace_paths"`
	RequireApprovalForRiskyTool bool                  `yaml:"require_approval_for_risky_tools"`
	AccessiblePaths             []string              `yaml:"accessible_paths"`
	TerminalSandbox             TerminalSandboxConfig `yaml:"terminal_sandbox"`
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
	Enabled           bool                `yaml:"enabled"`
	Root              string              `yaml:"root"`
	RecentDays        int                 `yaml:"recent_days"`
	AutoPropose       bool                `yaml:"auto_propose"`
	AutoCommitLowRisk bool                `yaml:"auto_commit_low_risk"`
	RequireConfirmFor []string            `yaml:"require_confirm_for"`
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
	Enabled            bool   `yaml:"enabled"`
	SuccessThreshold   int    `yaml:"success_threshold"`
	MinConfidence      string `yaml:"min_confidence"`
	RequireUserConfirm bool   `yaml:"require_user_confirm"`
	AskTiming          string `yaml:"ask_timing"`
}

type SkillsConfig struct {
	Catalogs []SkillCatalogConfig `yaml:"catalogs"`
}

type ScriptsConfig struct {
	Dirs []string `yaml:"dirs"`
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

func (c SearchProviderConfig) ResolvedAPIKey() string {
	return firstNonEmpty(c.APIKey, getenvWithMatewayFallback(c.APIKeyEnv))
}

func (c ModelConfig) ResolvedAPIKey() string {
	return firstNonEmpty(c.APIKey, getenvWithMatewayFallback(c.APIKeyEnv))
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
	r.normalizeScripts()
	r.normalizeAgents()
}

func (r *Root) applyDefaults() {
	defaults := DefaultRoot()
	if strings.TrimSpace(r.App.Name) == "" {
		r.App.Name = defaults.App.Name
	}
	if strings.TrimSpace(r.App.Locale) == "" {
		r.App.Locale = "auto"
	}
	if strings.TrimSpace(r.Model.Default) == "" {
		r.Model.Default = defaults.Model.Default
	}
	if r.Model.Fallbacks == nil {
		r.Model.Fallbacks = defaults.Model.Fallbacks
	}
	if r.Model.Roles == nil {
		r.Model.Roles = map[string]string{}
	}
	if r.Memory.RecentDays <= 0 {
		r.Memory.RecentDays = defaults.Memory.RecentDays
	}
	if len(r.Memory.RequireConfirmFor) == 0 {
		r.Memory.RequireConfirmFor = defaults.Memory.RequireConfirmFor
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
	if strings.TrimSpace(r.Learning.SkillCrystallization.AskTiming) == "" {
		r.Learning.SkillCrystallization.AskTiming = defaults.Learning.SkillCrystallization.AskTiming
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

func (r *Root) normalizeScripts() {
	if r.Scripts.Dirs == nil {
		r.Scripts.Dirs = []string{}
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

func boolPtr(value bool) *bool {
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
