package script

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/secret"
)

type Script struct {
	Name            string
	Path            string
	Description     string
	Risk            string
	RequiredSecrets []SecretRef
	Source          string
	Hash            string
	Authorized      bool
}

type Record struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Source     string `json:"source"`
	Risk       string `json:"risk"`
	Hash       string `json:"hash"`
	Authorized bool   `json:"authorized"`
	UpdatedAt  string `json:"updated_at"`
}

type SecretRef struct {
	ID  string
	Env string
}

type RunInput struct {
	Name    string
	Args    []string
	Timeout time.Duration
}

type RunResult struct {
	Script   Script
	Command  []string
	ExitCode int
	Output   string
	Duration time.Duration
}

func List(cfg *config.Root) ([]Script, error) {
	var out []Script
	seen := map[string]bool{}
	for _, dir := range scriptDirs(cfg) {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			info, err := entry.Info()
			if err != nil || info.Mode()&0o111 == 0 {
				continue
			}
			script, err := parseScript(path)
			if err != nil {
				return nil, err
			}
			script.Source = scriptSource(path, cfg)
			script.Hash = fileHash(path)
			script.Authorized = script.Source != "external_skill" || isAuthorized(cfg, script)
			key := strings.ToLower(script.Name)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, script)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func Run(ctx context.Context, cfg *config.Root, input RunInput) (RunResult, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return RunResult{}, fmt.Errorf("script name is required")
	}
	scripts, err := List(cfg)
	if err != nil {
		return RunResult{}, err
	}
	var selected Script
	for _, script := range scripts {
		if script.Name == name {
			selected = script
			break
		}
	}
	if selected.Name == "" {
		return RunResult{}, fmt.Errorf("script %q not found", name)
	}
	if selected.Source == "external_skill" && !selected.Authorized {
		return RunResult{}, fmt.Errorf("script %q requires authorization before first run", name)
	}
	timeout := input.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, selected.Path, input.Args...)
	cmd.Env = os.Environ()
	store := secret.Store{Home: home(cfg)}
	for _, ref := range selected.RequiredSecrets {
		env := strings.TrimSpace(ref.Env)
		if env == "" {
			continue
		}
		entry, ok, err := store.Get(ref.ID)
		if err != nil {
			return RunResult{}, err
		}
		if !ok {
			return RunResult{}, fmt.Errorf("missing required secret %s", ref.ID)
		}
		cmd.Env = append(cmd.Env, env+"="+entry.Value)
	}
	start := time.Now()
	output, err := cmd.CombinedOutput()
	result := RunResult{
		Script:   selected,
		Command:  append([]string{selected.Path}, input.Args...),
		ExitCode: cmd.ProcessState.ExitCode(),
		Output:   strings.TrimSpace(string(output)),
		Duration: time.Since(start),
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func scriptDirs(cfg *config.Root) []string {
	var dirs []string
	home := home(cfg)
	workspace := workspace(cfg)
	agentID := defaultAgentID(cfg)
	if workspace != "" && agentID != "" {
		dirs = append(dirs, skillScriptDirs(filepath.Join(workspace, "agents", agentID, "skills"))...)
	}
	if workspace != "" {
		dirs = append(dirs, skillScriptDirs(filepath.Join(workspace, "skills"))...)
	}
	dirs = append(dirs, filepath.Join(home, "scripts"))
	if workspace != "" {
		dirs = append(dirs, filepath.Join(workspace, "scripts"))
	}
	if cfg != nil {
		for _, dir := range cfg.Scripts.Dirs {
			if strings.TrimSpace(dir) != "" {
				dirs = append(dirs, strings.TrimSpace(dir))
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, dir := range dirs {
		clean, err := filepath.Abs(filepath.Clean(expandHome(dir)))
		if err != nil || seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}

func Authorize(cfg *config.Root, name string) (Record, error) {
	scripts, err := List(cfg)
	if err != nil {
		return Record{}, err
	}
	for _, script := range scripts {
		if script.Name != name {
			continue
		}
		record := Record{Name: script.Name, Path: script.Path, Source: script.Source, Risk: script.Risk, Hash: script.Hash, Authorized: true, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
		records, err := loadRecords(cfg)
		if err != nil {
			return Record{}, err
		}
		records[recordKey(script)] = record
		return record, saveRecords(cfg, records)
	}
	return Record{}, fmt.Errorf("script %q not found", name)
}

func scriptSource(path string, cfg *config.Root) string {
	clean, _ := filepath.Abs(filepath.Clean(path))
	home := home(cfg)
	workspace := workspace(cfg)
	if home != "" && strings.HasPrefix(clean, filepath.Join(home, "scripts")+string(filepath.Separator)) {
		return "local"
	}
	if workspace != "" && strings.Contains(clean, string(filepath.Separator)+"agents"+string(filepath.Separator)) {
		return "agent"
	}
	if workspace != "" && strings.Contains(clean, string(filepath.Separator)+"skills"+string(filepath.Separator)) {
		return "external_skill"
	}
	return "configured"
}

func fileHash(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	sum := sha256.New()
	_, _ = io.Copy(sum, file)
	return hex.EncodeToString(sum.Sum(nil))
}

func recordKey(script Script) string {
	return script.Path + "\x00" + script.Hash
}

func isAuthorized(cfg *config.Root, script Script) bool {
	records, err := loadRecords(cfg)
	if err != nil {
		return false
	}
	record, ok := records[recordKey(script)]
	return ok && record.Authorized
}

func recordsPath(cfg *config.Root) string {
	return filepath.Join(home(cfg), "scripts", "records.json")
}

func loadRecords(cfg *config.Root) (map[string]Record, error) {
	data, err := os.ReadFile(recordsPath(cfg))
	if os.IsNotExist(err) {
		return map[string]Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records map[string]Record
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	if records == nil {
		records = map[string]Record{}
	}
	return records, nil
}

func saveRecords(cfg *config.Root, records map[string]Record) error {
	path := recordsPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func skillScriptDirs(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dirs = append(dirs, filepath.Join(root, entry.Name(), "scripts"))
	}
	sort.Strings(dirs)
	return dirs
}

func parseScript(path string) (Script, error) {
	script := Script{Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Path: path, Risk: "guarded_mutation"}
	file, err := os.Open(path)
	if err != nil {
		return script, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for i := 0; i < 30 && scanner.Scan(); i++ {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimPrefix(line, "#")
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "mateway.") {
			continue
		}
		key = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(key)), "mateway.")
		value = strings.TrimSpace(value)
		switch key {
		case "name":
			if value != "" {
				script.Name = value
			}
		case "description":
			script.Description = value
		case "risk":
			if value != "" {
				script.Risk = value
			}
		case "required_secret":
			if ref := parseSecretRef(value); ref.ID != "" {
				script.RequiredSecrets = append(script.RequiredSecrets, ref)
			}
		}
	}
	return script, scanner.Err()
}

func parseSecretRef(value string) SecretRef {
	var ref SecretRef
	for _, part := range strings.Fields(value) {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "id":
			ref.ID = strings.TrimSpace(val)
		case "env":
			ref.Env = strings.TrimSpace(val)
		}
	}
	return ref
}

func home(cfg *config.Root) string {
	if cfg != nil && strings.TrimSpace(cfg.App.Home) != "" {
		return strings.TrimSpace(cfg.App.Home)
	}
	return config.DefaultHome()
}

func workspace(cfg *config.Root) string {
	if cfg != nil && strings.TrimSpace(cfg.App.Workspace) != "" {
		return strings.TrimSpace(cfg.App.Workspace)
	}
	return filepath.Join(home(cfg), "workspace")
}

func defaultAgentID(cfg *config.Root) string {
	if cfg != nil && strings.TrimSpace(cfg.Agents.Default) != "" {
		return strings.TrimSpace(cfg.Agents.Default)
	}
	return "main"
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if userHome, err := os.UserHomeDir(); err == nil {
			return filepath.Join(userHome, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}
