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
	App      AppConfig      `yaml:"app"`
	Security SecurityConfig `yaml:"security"`
	Search   SearchConfig   `yaml:"search"`
	Models   []ModelConfig  `yaml:"-"`
	Channels ChannelsConfig `yaml:"-"`
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
	DefaultTool string                `yaml:"default_tool"`
	Providers   SearchProvidersConfig `yaml:"providers"`
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
	SearchDepth    string   `yaml:"search_depth"`
	Topic          string   `yaml:"topic"`
	IncludeDomains []string `yaml:"include_domains"`
	ExcludeDomains []string `yaml:"exclude_domains"`
	Region         string   `yaml:"region"`
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
	channels, err := l.loadChannels()
	if err != nil {
		return nil, err
	}
	root.Channels = channels
	return root, nil
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
		if entry.IsDir() || !strings.HasSuffix(name, ".yaml") || strings.HasPrefix(name, "_") || strings.Contains(name, ".example.") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	models := make([]ModelConfig, 0, len(names))
	for _, name := range names {
		var model ModelConfig
		if err := readYAML(filepath.Join(dir, name), &model); err != nil {
			return nil, err
		}
		if model.Enabled {
			models = append(models, model)
		}
	}
	return models, nil
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
