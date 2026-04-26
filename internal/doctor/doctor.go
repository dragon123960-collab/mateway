package doctor

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/cmdresolve"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/skills"
)

type Check struct {
	Name    string
	Status  string
	Details string
}

func Run(ctx context.Context, cfg config.Config) []Check {
	_ = ctx
	checks := []Check{
		checkPath("config", config.ConfigPath()),
		checkPath("config_models", config.ModelFragmentsDir()),
		checkPath("config_channels", config.ChannelFragmentsDir()),
		checkPath("workspace", cfg.App.Workspace),
	}
	catalog := skills.NewCatalog(cfg.Skills.Roots)
	err := catalog.Refresh()
	snapshot := catalog.Snapshot()
	status := "ok"
	details := fmt.Sprintf("%d loaded", len(snapshot))
	if err != nil {
		status = "warn"
		details = err.Error()
	}
	checks = append(checks, Check{
		Name:    "skills",
		Status:  status,
		Details: details,
	})
	checks = append(checks, Check{
		Name:    "watch",
		Status:  ternary(cfg.Skills.Watch, "ok", "warn"),
		Details: fmt.Sprintf("roots=%s", strings.Join(cfg.Skills.Roots, ", ")),
	})
	checks = append(checks, Check{
		Name:    "gateway",
		Status:  "ok",
		Details: fmt.Sprintf("%s:%d", cfg.Gateway.Host, cfg.Gateway.Port),
	})
	feishuStatus := "warn"
	feishuDetails := "disabled"
	if cfg.Channels.Feishu.Enabled {
		feishuStatus = "ok"
		feishuDetails = "enabled"
		if cfg.Channels.Feishu.AppID == "" || cfg.Channels.Feishu.AppSecret == "" {
			feishuStatus = "warn"
			feishuDetails = "enabled but app_id/app_secret incomplete"
		}
	}
	checks = append(checks, Check{
		Name:    "feishu",
		Status:  feishuStatus,
		Details: feishuDetails,
	})
	modelStatus := "warn"
	modelDetails := "no enabled model"
	if model := cfg.DefaultModel(); model != nil {
		modelStatus = "ok"
		modelDetails = fmt.Sprintf("%s -> %s", model.Name, model.Model)
		if model.APIBase == "" {
			modelStatus = "warn"
			modelDetails = "default model missing api_base"
		}
	}
	checks = append(checks, Check{
		Name:    "llm",
		Status:  modelStatus,
		Details: modelDetails,
	})
	checks = append(checks, Check{
		Name:    "llm_limits",
		Status:  "ok",
		Details: fmt.Sprintf("rpm=%d cooldown_429=%ds retry=%d", cfg.Models.Limits.RequestsPerMinute, cfg.Models.Limits.CooldownOn429, cfg.Models.Limits.TransientRetryMax),
	})
	checks = append(checks, Check{
		Name:    "sessions",
		Status:  "ok",
		Details: fmt.Sprintf("history_limit=%d", cfg.Sessions.HistoryLimit),
	})
	checks = append(checks, Check{
		Name:    "security",
		Status:  "ok",
		Details: fmt.Sprintf("workspace_paths=%t risky_approval=%t", cfg.Security.EnforceWorkspacePaths, cfg.Security.RequireApprovalForRiskyTools),
	})
	webStatus := ternary(cfg.Integrations.WebSearch.Enabled, "ok", "warn")
	webDetails := "disabled"
	if cfg.Integrations.WebSearch.Enabled {
		switch strings.ToLower(strings.TrimSpace(cfg.Integrations.WebSearch.Provider)) {
		case "tavily":
			webDetails = firstNonEmpty(cfg.Integrations.WebSearch.Tavily.BaseURL, "tavily")
		default:
			webDetails = firstNonEmpty(cfg.Integrations.WebSearch.DuckDuckGo.BaseURL, "duckduckgo")
		}
	}
	checks = append(checks, Check{
		Name:    "web_search",
		Status:  webStatus,
		Details: webDetails,
	})
	commandSnapshot, snapErr := cmdresolve.Default().Snapshot()
	shellStatus := "ok"
	shellDetails := fmt.Sprintf("shell=%s search_paths=%d", firstNonEmpty(commandSnapshot.ShellPath, "(none)"), len(commandSnapshot.SearchPaths))
	if snapErr != nil {
		shellStatus = "warn"
		shellDetails = snapErr.Error()
	}
	checks = append(checks, Check{
		Name:    "command_env",
		Status:  shellStatus,
		Details: shellDetails,
	})
	return checks
}

func Format(checks []Check) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mateway doctor %s\n\n", time.Now().Format(time.RFC3339))
	for _, check := range checks {
		fmt.Fprintf(&b, "[%s] %s - %s\n", strings.ToUpper(check.Status), check.Name, check.Details)
	}
	return b.String()
}

func checkPath(name, path string) Check {
	if _, err := os.Stat(path); err != nil {
		return Check{Name: name, Status: "warn", Details: path}
	}
	return Check{Name: name, Status: "ok", Details: path}
}

func ternary(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
