package doctor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/cmdresolve"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/skills"
	"github.com/dongping/mateway/internal/tools"
)

type Check struct {
	Name    string
	Status  string
	Details string
}

var probeHTTPConnectivity = func(ctx context.Context, target string, headers map[string]string) (int, error) {
	client := &http.Client{Timeout: 4 * time.Second}
	methods := []string{http.MethodHead, http.MethodGet}
	var lastErr error
	for _, method := range methods {
		req, err := http.NewRequestWithContext(ctx, method, target, nil)
		if err != nil {
			return 0, err
		}
		for key, value := range headers {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
				continue
			}
			req.Header.Set(key, value)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		_ = resp.Body.Close()
		if method == http.MethodHead && resp.StatusCode == http.StatusMethodNotAllowed {
			lastErr = fmt.Errorf("head not allowed")
			continue
		}
		return resp.StatusCode, nil
	}
	return 0, lastErr
}

func Run(ctx context.Context, cfg config.Config) []Check {
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
	if model := cfg.DefaultModel(); model != nil {
		checks = append(checks, llmConnectivityCheck(ctx, *model))
	}
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
			if strings.TrimSpace(cfg.Integrations.WebSearch.Tavily.APIKey) == "" {
				webStatus = "warn"
				webDetails += " (api_key missing)"
			}
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
	checks = append(checks, cliProviderChecks(ctx, cfg)...)
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

func llmConnectivityCheck(ctx context.Context, model config.ModelConfig) Check {
	target := strings.TrimSpace(model.APIBase)
	if target == "" {
		return Check{Name: "llm_connectivity", Status: "warn", Details: "default model missing api_base"}
	}
	if _, err := url.ParseRequestURI(target); err != nil {
		return Check{Name: "llm_connectivity", Status: "warn", Details: fmt.Sprintf("%s -> invalid api_base: %v", model.Name, err)}
	}
	headers := map[string]string{"Accept": "application/json"}
	if key := strings.TrimSpace(model.APIKey); key != "" {
		headers["Authorization"] = "Bearer " + key
	}
	for key, value := range model.Headers {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		headers[key] = value
	}
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	statusCode, err := probeHTTPConnectivity(probeCtx, target, headers)
	if err != nil {
		return Check{Name: "llm_connectivity", Status: "warn", Details: fmt.Sprintf("%s -> %v", model.Name, err)}
	}
	status := "ok"
	details := fmt.Sprintf("%s -> %s (http %d)", model.Name, target, statusCode)
	if statusCode == http.StatusTooManyRequests {
		status = "warn"
		details += " rate_limited"
	} else if statusCode >= 500 {
		status = "warn"
		details += " upstream_error"
	}
	return Check{Name: "llm_connectivity", Status: status, Details: details}
}

func cliProviderChecks(ctx context.Context, cfg config.Config) []Check {
	enabledCount := 0
	okCount := 0
	warnCount := 0
	providerChecks := make([]Check, 0, len(cfg.CLIProviders))
	for _, item := range cfg.CLIProviders {
		check := cliProviderCheck(ctx, item)
		if strings.TrimSpace(check.Details) == "disabled" {
			providerChecks = append(providerChecks, check)
			warnCount++
			continue
		}
		if item.Enabled == nil || *item.Enabled {
			enabledCount++
		}
		if check.Status == "ok" {
			okCount++
		} else {
			warnCount++
		}
		providerChecks = append(providerChecks, check)
	}
	summaryStatus := "ok"
	if warnCount > 0 {
		summaryStatus = "warn"
	}
	summaryDetails := fmt.Sprintf("configured=%d enabled=%d ok=%d warn=%d", len(cfg.CLIProviders), enabledCount, okCount, warnCount)
	if len(cfg.CLIProviders) == 0 {
		summaryDetails = "configured=0"
	}
	return append([]Check{{
		Name:    "cli_providers",
		Status:  summaryStatus,
		Details: summaryDetails,
	}}, providerChecks...)
}

func cliProviderCheck(ctx context.Context, item config.CLIProviderConfig) Check {
	name := firstNonEmpty(strings.TrimSpace(item.Name), strings.TrimSpace(item.Binary), "unnamed")
	checkName := "cli_provider:" + name
	if item.Enabled != nil && !*item.Enabled {
		return Check{Name: checkName, Status: "warn", Details: "disabled"}
	}
	binary := strings.TrimSpace(item.Binary)
	if binary == "" {
		return Check{Name: checkName, Status: "warn", Details: "binary missing"}
	}
	resolution, err := cmdresolve.Default().Resolve(binary)
	if err != nil {
		return Check{Name: checkName, Status: "warn", Details: err.Error()}
	}
	provider := tools.ExternalCLIProvider{
		Name:            name,
		BinaryPath:      item.Binary,
		Enabled:         item.Enabled == nil || *item.Enabled,
		Description:     item.Description,
		ListArgs:        item.ListArgs,
		AllowedCommands: item.AllowedCommands,
		BlockedCommands: item.BlockedCommands,
		Env:             item.Env,
		RiskLevel:       item.RiskLevel,
	}
	toolset, err := provider.Tools(ctx, tools.Scope{})
	if err != nil {
		return Check{Name: checkName, Status: "warn", Details: err.Error()}
	}
	if len(toolset) == 0 {
		return Check{Name: checkName, Status: "warn", Details: "resolved but exposed 0 tools"}
	}
	for _, tool := range toolset {
		if reporter, ok := tool.(tools.AvailabilityReporter); ok {
			availability := reporter.Availability(ctx)
			if !availability.Available {
				return Check{Name: checkName, Status: "warn", Details: availability.Reason}
			}
			break
		}
	}
	details := fmt.Sprintf("%s via %s", resolution.Path, resolution.Source)
	if len(item.AllowedCommands) > 0 {
		details += fmt.Sprintf(" allowed=%d", len(item.AllowedCommands))
	}
	return Check{Name: checkName, Status: "ok", Details: details}
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
