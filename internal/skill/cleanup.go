package skill

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
)

const (
	StateActive    = "active"
	StateCold      = "cold"
	StateHidden    = "hidden"
	StateProtected = "protected"
)

type CleanupInput struct {
	Home      string
	Workspace string
	Config    config.SkillCleanupConfig
	Now       time.Time
}

type CleanupReport struct {
	Items  []CleanupItem
	Active int
	Cold   int
	Hidden int
}

type CleanupItem struct {
	ID         string
	Name       string
	Scope      string
	Path       string
	State      string
	Reason     string
	UsageCount int
	LastUsedAt string
}

type cleanupStateFile struct {
	Restored  map[string]cleanupStateEntry `json:"restored,omitempty"`
	Protected map[string]cleanupStateEntry `json:"protected,omitempty"`
}

type cleanupStateEntry struct {
	Time string `json:"time"`
}

func BuildCleanupReport(input CleanupInput) (CleanupReport, error) {
	cfg := normalizeCleanupConfig(input.Config)
	skills, err := List(input.Workspace)
	if err != nil {
		return CleanupReport{}, err
	}
	usage, err := readUsage(input.Home)
	if err != nil {
		return CleanupReport{}, err
	}
	state, err := readCleanupState(input.Home)
	if err != nil {
		return CleanupReport{}, err
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	protected := protectedSet(cfg.Protected)
	var report CleanupReport
	for _, item := range skills {
		id := SkillID(item.Scope, item.Name)
		stat := usage[usageKey(item.Scope, item.Name)]
		ageTime := stat.LastUsedAt
		if ageTime.IsZero() {
			if info, err := os.Stat(item.Path); err == nil {
				ageTime = info.ModTime()
			}
		}
		cleanupItem := CleanupItem{
			ID:         id,
			Name:       item.Name,
			Scope:      item.Scope,
			Path:       item.Path,
			UsageCount: stat.Count,
			LastUsedAt: formatTime(stat.LastUsedAt),
		}
		key := cleanupKey(item.Scope, item.Name)
		switch {
		case protected[key] || state.Protected[id].Time != "":
			cleanupItem.State = StateProtected
			cleanupItem.Reason = "protected"
			report.Active++
		case state.Restored[id].Time != "":
			cleanupItem.State = StateActive
			cleanupItem.Reason = "restored"
			report.Active++
		case !cfg.EnabledValue():
			cleanupItem.State = StateActive
			cleanupItem.Reason = "cleanup disabled"
			report.Active++
		default:
			cleanupItem.State, cleanupItem.Reason = cleanupStateFor(stat, ageTime, cfg, now)
			switch cleanupItem.State {
			case StateCold:
				report.Cold++
			case StateHidden:
				report.Hidden++
			default:
				report.Active++
			}
		}
		report.Items = append(report.Items, cleanupItem)
	}
	sort.SliceStable(report.Items, func(i, j int) bool {
		left := stateRank(report.Items[i].State)
		right := stateRank(report.Items[j].State)
		if left != right {
			return left < right
		}
		return report.Items[i].Name < report.Items[j].Name
	})
	return report, nil
}

func Restore(input CleanupInput, id string) (CleanupItem, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return CleanupItem{}, fmt.Errorf("skill id is required")
	}
	report, err := BuildCleanupReport(input)
	if err != nil {
		return CleanupItem{}, err
	}
	var found CleanupItem
	for _, item := range report.Items {
		if item.ID == id {
			found = item
			break
		}
	}
	if found.ID == "" {
		return CleanupItem{}, fmt.Errorf("unknown skill id %q", id)
	}
	state, err := readCleanupState(input.Home)
	if err != nil {
		return CleanupItem{}, err
	}
	if state.Restored == nil {
		state.Restored = map[string]cleanupStateEntry{}
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	state.Restored[id] = cleanupStateEntry{Time: now.Format(time.RFC3339Nano)}
	if err := writeCleanupState(input.Home, state); err != nil {
		return CleanupItem{}, err
	}
	found.State = StateActive
	found.Reason = "restored"
	return found, nil
}

func SkillID(scope, name string) string {
	base := cleanupKey(scope, name)
	sum := sha1.Sum([]byte(base))
	return strings.ReplaceAll(base, ":", "-") + "-" + hex.EncodeToString(sum[:])[:8]
}

type usageStat struct {
	Count      int
	LastUsedAt time.Time
}

func cleanupStateFor(stat usageStat, ageTime time.Time, cfg config.SkillCleanupConfig, now time.Time) (string, string) {
	if stat.Count > cfg.MaxUsageCount {
		return StateActive, "usage above threshold"
	}
	if ageTime.IsZero() {
		return StateActive, "no usage age available"
	}
	unusedDays := int(now.Sub(ageTime).Hours() / 24)
	if unusedDays >= cfg.HiddenAfterDays {
		return StateHidden, fmt.Sprintf("unused %d days and usage_count <= %d", unusedDays, cfg.MaxUsageCount)
	}
	if unusedDays >= cfg.ColdAfterDays {
		return StateCold, fmt.Sprintf("unused %d days and usage_count <= %d", unusedDays, cfg.MaxUsageCount)
	}
	return StateActive, fmt.Sprintf("unused fewer than %d days", cfg.ColdAfterDays)
}

func readUsage(home string) (map[string]usageStat, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		home = ".mateway"
	}
	path := filepath.Join(home, "observe", "skill_usage", "events.jsonl")
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string]usageStat{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	out := map[string]usageStat{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event struct {
			Time  string `json:"time"`
			Skill struct {
				Name  string `json:"name"`
				Scope string `json:"scope"`
			} `json:"skill"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		key := usageKey(event.Skill.Scope, event.Skill.Name)
		if key == "" {
			continue
		}
		stat := out[key]
		stat.Count++
		if t, err := time.Parse(time.RFC3339Nano, event.Time); err == nil && t.After(stat.LastUsedAt) {
			stat.LastUsedAt = t
		}
		out[key] = stat
	}
	return out, scanner.Err()
}

func readCleanupState(home string) (cleanupStateFile, error) {
	path := cleanupStatePath(home)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cleanupStateFile{Restored: map[string]cleanupStateEntry{}, Protected: map[string]cleanupStateEntry{}}, nil
	}
	if err != nil {
		return cleanupStateFile{}, err
	}
	var state cleanupStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return cleanupStateFile{}, err
	}
	if state.Restored == nil {
		state.Restored = map[string]cleanupStateEntry{}
	}
	if state.Protected == nil {
		state.Protected = map[string]cleanupStateEntry{}
	}
	return state, nil
}

func writeCleanupState(home string, state cleanupStateFile) error {
	path := cleanupStatePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func cleanupStatePath(home string) string {
	home = strings.TrimSpace(home)
	if home == "" {
		home = ".mateway"
	}
	return filepath.Join(home, "observe", "skill_cleanup", "state.json")
}

func normalizeCleanupConfig(cfg config.SkillCleanupConfig) config.SkillCleanupConfig {
	if cfg.Enabled == nil {
		value := true
		cfg.Enabled = &value
	}
	if cfg.ColdAfterDays <= 0 {
		cfg.ColdAfterDays = 30
	}
	if cfg.HiddenAfterDays <= 0 {
		cfg.HiddenAfterDays = 90
	}
	if cfg.MaxUsageCount <= 0 {
		cfg.MaxUsageCount = 1
	}
	if cfg.HiddenAfterDays < cfg.ColdAfterDays {
		cfg.HiddenAfterDays = cfg.ColdAfterDays
	}
	if strings.TrimSpace(cfg.RestoreMode) == "" {
		cfg.RestoreMode = "permanent"
	}
	return cfg
}

func usageKey(scope, name string) string {
	return cleanupKey(scope, name)
}

func cleanupKey(scope, name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(scope)) + ":" + name
}

func protectedSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		out[value] = true
		out["shared:"+value] = true
		out["agent:"+value] = true
	}
	return out
}

func stateRank(state string) int {
	switch state {
	case StateHidden:
		return 0
	case StateCold:
		return 1
	case StateProtected:
		return 3
	default:
		return 2
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
