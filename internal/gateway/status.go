package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/dongping/mateway/internal/config"
)

type runtimeStatus struct {
	GatewayHost       string `json:"gateway_host"`
	GatewayPort       int    `json:"gateway_port"`
	FeishuEnabled     bool   `json:"feishu_enabled"`
	DefaultModel      string `json:"default_model"`
	ModelName         string `json:"model_name"`
	SessionHistory    int    `json:"session_history_limit"`
	RequestsPerMinute int    `json:"requests_per_minute"`
	CooldownOn429     int    `json:"cooldown_on_429_seconds"`
	UpdatedAt         string `json:"updated_at"`
}

func writeRuntimeStatus(cfg config.Config) error {
	modelAlias := cfg.Models.Default
	modelName := ""
	if model := cfg.DefaultModel(); model != nil {
		modelAlias = model.Name
		modelName = model.Model
	}
	payload := runtimeStatus{
		GatewayHost:       cfg.Gateway.Host,
		GatewayPort:       cfg.Gateway.Port,
		FeishuEnabled:     cfg.Channels.Feishu.Enabled,
		DefaultModel:      modelAlias,
		ModelName:         modelName,
		SessionHistory:    cfg.Sessions.HistoryLimit,
		RequestsPerMinute: cfg.Models.Limits.RequestsPerMinute,
		CooldownOn429:     cfg.Models.Limits.CooldownOn429,
		UpdatedAt:         time.Now().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(cfg.App.Home, "gateway_state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
