package tool

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dongping/mateway/internal/config"
)

var destructiveCommandPattern = regexp.MustCompile(`(?i)(^|\s|&&|\|\||;|` + "`" + `)(rm(\s|$)|rmdir(\s|$)|shred(\s|$)|git\s+(reset|clean)(\s|$))`)
var shellControlPattern = regexp.MustCompile(`[;&|` + "`" + `$<>]`)

func IsDangerousCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	return cmd != "" && destructiveCommandPattern.MatchString(cmd)
}

type TerminalDecision struct {
	Allow          bool
	Class          string
	Reason         string
	RemoteProfile  string
	RequireConfirm bool
}

func CheckTerminalCommand(command string, cfg *config.Root) TerminalDecision {
	command = strings.TrimSpace(command)
	if command == "" {
		return TerminalDecision{Class: "empty", Reason: "terminal command is required"}
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return TerminalDecision{Class: "empty", Reason: "terminal command is required"}
	}
	if IsDangerousCommand(command) {
		return TerminalDecision{Class: "destructive", Reason: "destructive terminal command is blocked"}
	}
	if looksLikeNetworkCommand(fields[0]) {
		if profile, ok := matchRemoteProfile(fields, cfg); ok {
			return TerminalDecision{Allow: true, Class: "remote", RemoteProfile: profile.Alias, RequireConfirm: profile.RequireConfirm}
		}
		return TerminalDecision{Class: "network", Reason: "network terminal command requires a configured remote profile or dedicated tool"}
	}
	if strings.EqualFold(fields[0], "cat") && len(fields) > 1 {
		for _, raw := range fields[1:] {
			if strings.HasPrefix(raw, "-") {
				continue
			}
			if _, err := ResolveAllowedPath(raw, cfg); err != nil {
				return TerminalDecision{Class: "path_escape", Reason: err.Error()}
			}
		}
	}
	if isSafeReadOnlyPipeline(command, cfg) {
		return TerminalDecision{Allow: true, Class: "read_only_pipeline"}
	}
	if isProjectInternalCommand(command, fields) {
		return TerminalDecision{Allow: true, Class: "project_internal"}
	}
	if shellControlPattern.MatchString(command) {
		return TerminalDecision{Class: "unknown_shell", Reason: "compound shell syntax is blocked; use a dedicated tool or a simple allowlisted command"}
	}
	if isAllowlistedLocalCommand(fields) {
		return TerminalDecision{Allow: true, Class: "local_read_only"}
	}
	return TerminalDecision{Class: "unknown", Reason: "terminal command is not in the local read-only allowlist"}
}

func isSafeReadOnlyPipeline(command string, cfg *config.Root) bool {
	if strings.ContainsAny(command, ";&`$<>") {
		return false
	}
	main := strings.TrimSpace(command)
	if strings.Contains(main, "||") {
		parts := strings.Split(main, "||")
		if len(parts) != 2 || !isEchoFallback(parts[1]) {
			return false
		}
		main = strings.TrimSpace(parts[0])
	}
	if !strings.Contains(main, "|") {
		return false
	}
	for _, segment := range strings.Split(main, "|") {
		if !isSafeReadOnlyCommandSegment(segment, cfg) {
			return false
		}
	}
	return true
}

func isEchoFallback(segment string) bool {
	fields := strings.Fields(strings.TrimSpace(segment))
	return len(fields) >= 1 && fields[0] == "echo"
}

func isSafeReadOnlyCommandSegment(segment string, cfg *config.Root) bool {
	fields := strings.Fields(strings.TrimSpace(segment))
	if len(fields) == 0 {
		return false
	}
	if looksLikeNetworkCommand(fields[0]) || IsDangerousCommand(strings.Join(fields, " ")) {
		return false
	}
	if !isAllowlistedLocalCommand(fields) {
		return false
	}
	return commandPathsAllowed(fields, cfg)
}

func commandPathsAllowed(fields []string, cfg *config.Root) bool {
	if len(fields) == 0 {
		return false
	}
	cmd := filepath.Base(fields[0])
	switch cmd {
	case "cat", "ls", "find":
		for i := 1; i < len(fields); i++ {
			raw := fields[i]
			if raw == "" || strings.HasPrefix(raw, "-") || isOptionValue(fields, i) || looksLikePattern(raw) {
				continue
			}
			if _, err := ResolveAllowedPath(raw, cfg); err != nil {
				return false
			}
		}
	}
	for i := 1; i < len(fields); i++ {
		raw := fields[i]
		if raw == "" || strings.HasPrefix(raw, "-") || isOptionValue(fields, i) || looksLikePattern(raw) || !looksLikePathArg(raw) {
			continue
		}
		if _, err := ResolveAllowedPath(raw, cfg); err != nil {
			return false
		}
	}
	return true
}

func isOptionValue(fields []string, index int) bool {
	if index == 0 {
		return false
	}
	prev := fields[index-1]
	switch prev {
	case "-type", "-name", "-iname", "-maxdepth", "-mindepth", "-mtime", "-size", "-path", "-not", "-print", "-exec", "-e", "-A", "-B", "-C", "-n":
		return true
	default:
		return false
	}
}

func looksLikePattern(value string) bool {
	return strings.ContainsAny(value, "*?[]{}()")
}

func looksLikePathArg(value string) bool {
	return strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.Contains(value, string(filepath.Separator))
}

func isProjectInternalCommand(command string, fields []string) bool {
	if len(fields) == 0 || strings.ContainsAny(command, ";&|`$<>") {
		return false
	}
	exe := strings.TrimSpace(fields[0])
	if exe == "" || filepath.Base(exe) != "mateway" {
		return false
	}
	root, ok := currentMatewayProjectRoot()
	if !ok {
		return false
	}
	if !looksLikePathArg(exe) {
		return true
	}
	resolved, err := filepath.Abs(filepath.Clean(exe))
	if err != nil {
		return false
	}
	return pathInsideRoot(resolved, root)
}

func currentMatewayProjectRoot() (string, bool) {
	wd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		modPath := filepath.Join(wd, "go.mod")
		data, err := os.ReadFile(modPath)
		if err == nil && strings.Contains(string(data), "module github.com/dongping/mateway") {
			return wd, true
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", false
		}
		wd = parent
	}
}

func pathInsideRoot(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func looksLikeNetworkCommand(name string) bool {
	base := filepath.Base(strings.TrimSpace(name))
	switch base {
	case "ssh", "scp", "rsync", "curl", "wget", "nc", "ncat", "telnet":
		return true
	default:
		return false
	}
}

func isAllowlistedLocalCommand(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	cmd := filepath.Base(fields[0])
	switch cmd {
	case "pwd", "ls", "find", "grep", "rg", "head", "tail", "wc", "sed":
		return true
	case "go":
		return len(fields) >= 2 && fields[1] == "test"
	case "git":
		if len(fields) < 2 {
			return false
		}
		switch fields[1] {
		case "status", "diff", "log", "show", "branch", "remote", "rev-parse":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func matchRemoteProfile(fields []string, cfg *config.Root) (config.RemoteProfileConfig, bool) {
	if cfg == nil || len(fields) == 0 {
		return config.RemoteProfileConfig{}, false
	}
	target := remoteTarget(fields)
	if target == "" {
		return config.RemoteProfileConfig{}, false
	}
	host := target
	if strings.Contains(host, "@") {
		parts := strings.Split(host, "@")
		host = parts[len(parts)-1]
	}
	host = strings.Trim(host, "[]")
	for _, profile := range cfg.Remote.Profiles {
		if strings.EqualFold(strings.TrimSpace(profile.Alias), target) ||
			strings.EqualFold(strings.TrimSpace(profile.Host), host) ||
			strings.EqualFold(strings.TrimSpace(profile.Alias), host) {
			return profile, true
		}
	}
	return config.RemoteProfileConfig{}, false
}

func remoteTarget(fields []string) string {
	if len(fields) < 2 {
		return ""
	}
	cmd := filepath.Base(fields[0])
	switch cmd {
	case "ssh":
		for i := 1; i < len(fields); i++ {
			if strings.HasPrefix(fields[i], "-") {
				if fields[i] == "-p" && i+1 < len(fields) {
					i++
				}
				continue
			}
			return fields[i]
		}
	case "scp", "rsync":
		for _, field := range fields[1:] {
			if strings.Contains(field, ":") && !strings.HasPrefix(field, "-") {
				return strings.SplitN(field, ":", 2)[0]
			}
		}
	}
	return ""
}

func IsBlockedFetchURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || host == "localhost.localdomain" {
		return "localhost", true
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		if ip := net.ParseIP(host); ip != nil {
			ips = []net.IP{ip}
		}
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return ip.String(), true
		}
	}
	return "", false
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	return ip.Equal(net.ParseIP("169.254.169.254"))
}

func IsValidSecretID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 128 {
		return false
	}
	if strings.HasPrefix(id, "system/") || strings.HasPrefix(id, "mateway/") || strings.HasPrefix(id, "internal/") {
		return false
	}
	for i, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' || r == '/'
		if !ok {
			return false
		}
		if i == 0 && !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func ResolveAllowedPath(raw string, cfg *config.Root) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("path is required")
	}
	expanded := strings.TrimSpace(raw)
	if strings.HasPrefix(expanded, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			expanded = filepath.Join(home, strings.TrimPrefix(expanded, "~/"))
		}
	}
	base := defaultPathBase(cfg)
	if !filepath.IsAbs(expanded) {
		expanded = filepath.Join(base, expanded)
	}
	clean, err := filepath.Abs(filepath.Clean(expanded))
	if err != nil {
		return "", err
	}
	if cfg == nil || !cfg.Security.EnforceWorkspacePaths {
		return clean, nil
	}
	for _, root := range allowedRoots(cfg) {
		rootAbs, err := filepath.Abs(filepath.Clean(root))
		if err != nil || rootAbs == "" {
			continue
		}
		if clean == rootAbs || strings.HasPrefix(clean, rootAbs+string(filepath.Separator)) {
			return clean, nil
		}
	}
	return "", fmt.Errorf("path %s is outside allowed roots", clean)
}

func defaultPathBase(cfg *config.Root) string {
	if cfg != nil && strings.TrimSpace(cfg.App.Home) != "" {
		return cfg.App.Home
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".mateway")
	}
	return "."
}

func allowedRoots(cfg *config.Root) []string {
	if cfg == nil {
		return nil
	}
	var roots []string
	if strings.TrimSpace(cfg.App.Home) != "" {
		roots = append(roots, cfg.App.Home)
	}
	if strings.TrimSpace(cfg.App.Workspace) != "" {
		roots = append(roots, cfg.App.Workspace)
	}
	roots = append(roots, cfg.Security.AccessiblePaths...)
	return roots
}
