package schedule

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
	"gopkg.in/yaml.v3"
)

const (
	StatusActive = "active"
	StatusPaused = "paused"
)

type Task struct {
	ID           string             `yaml:"id" json:"id"`
	Title        string             `yaml:"title" json:"title"`
	Status       string             `yaml:"status" json:"status"`
	Owner        Endpoint           `yaml:"owner" json:"owner"`
	AgentID      string             `yaml:"agent_id" json:"agent_id"`
	Schedule     ScheduleSpec       `yaml:"schedule" json:"schedule"`
	Prompt       string             `yaml:"prompt" json:"prompt"`
	AllowedTools []string           `yaml:"allowed_tools" json:"allowed_tools"`
	Delivery     DeliverySpec       `yaml:"delivery" json:"delivery"`
	Confirmation ConfirmationPolicy `yaml:"confirmation" json:"confirmation"`
	Limits       Limits             `yaml:"limits" json:"limits"`
	CreatedAt    time.Time          `yaml:"created_at" json:"created_at"`
	UpdatedAt    time.Time          `yaml:"updated_at" json:"updated_at"`
}

type Endpoint struct {
	Channel  string `yaml:"channel" json:"channel"`
	ThreadID string `yaml:"thread_id" json:"thread_id"`
	UserID   string `yaml:"user_id" json:"user_id"`
}

type ScheduleSpec struct {
	Kind       string   `yaml:"kind" json:"kind"`
	DailyAt    string   `yaml:"daily_at,omitempty" json:"daily_at,omitempty"`
	WeeklyAt   string   `yaml:"weekly_at,omitempty" json:"weekly_at,omitempty"`
	Weekday    string   `yaml:"weekday,omitempty" json:"weekday,omitempty"`
	Weekdays   []string `yaml:"weekdays,omitempty" json:"weekdays,omitempty"`
	MonthlyAt  string   `yaml:"monthly_at,omitempty" json:"monthly_at,omitempty"`
	MonthlyDay int      `yaml:"monthly_day,omitempty" json:"monthly_day,omitempty"`
	Interval   string   `yaml:"interval,omitempty" json:"interval,omitempty"`
}

type DeliverySpec struct {
	Channel  string `yaml:"channel" json:"channel"`
	ThreadID string `yaml:"thread_id" json:"thread_id"`
	Mode     string `yaml:"mode" json:"mode"`
	Path     string `yaml:"path" json:"path"`
}

type ConfirmationPolicy struct {
	Create     string `yaml:"create" json:"create"`
	RiskyTools string `yaml:"risky_tools" json:"risky_tools"`
}

type Limits struct {
	MaxRuntimeSeconds int `yaml:"max_runtime_seconds" json:"max_runtime_seconds"`
	MaxOutputChars    int `yaml:"max_output_chars" json:"max_output_chars"`
}

type CreateInput struct {
	ID           string
	Title        string
	Prompt       string
	AgentID      string
	DailyAt      string
	WeeklyAt     string
	Weekday      string
	Weekdays     []string
	MonthlyAt    string
	MonthlyDay   int
	Interval     string
	Channel      string
	ThreadID     string
	UserID       string
	DeliveryMode string
	DeliveryPath string
	AllowedTools []string
	Now          time.Time
}

type UpdateInput struct {
	Title        string
	Prompt       string
	AgentID      string
	Schedule     *ScheduleSpec
	DeliveryMode string
	DeliveryPath string
	AllowedTools []string
}

type RunState struct {
	TaskID    string    `json:"task_id"`
	LastRunAt time.Time `json:"last_run_at"`
	Status    string    `json:"status"`
	LastError string    `json:"last_error,omitempty"`
	TraceID   string    `json:"trace_id,omitempty"`
	Output    string    `json:"output,omitempty"`
}

type State struct {
	Tasks []RunState `json:"tasks"`
}

type Store struct {
	Home string
}

func NewStore(home string) Store {
	if strings.TrimSpace(home) == "" {
		home = config.DefaultHome()
	}
	return Store{Home: home}
}

func (s Store) Create(input CreateInput) (Task, string, error) {
	task, err := buildTask(input)
	if err != nil {
		return Task{}, "", err
	}
	path := s.taskPath(task.ID)
	if _, err := os.Stat(path); err == nil {
		return Task{}, "", fmt.Errorf("schedule task already exists: %s", task.ID)
	} else if !os.IsNotExist(err) {
		return Task{}, "", err
	}
	if err := writeYAML(path, task); err != nil {
		return Task{}, "", err
	}
	return task, path, nil
}

func buildTask(input CreateInput) (Task, error) {
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	task := Task{
		ID:           strings.TrimSpace(input.ID),
		Title:        strings.TrimSpace(input.Title),
		Status:       StatusActive,
		AgentID:      firstNonEmpty(input.AgentID, "main"),
		Schedule:     scheduleSpecFromInput(input),
		Prompt:       strings.TrimSpace(input.Prompt),
		AllowedTools: cleanList(input.AllowedTools),
		Owner: Endpoint{
			Channel:  firstNonEmpty(input.Channel, "cli"),
			ThreadID: strings.TrimSpace(input.ThreadID),
			UserID:   strings.TrimSpace(input.UserID),
		},
		Delivery: DeliverySpec{
			Channel:  firstNonEmpty(input.Channel, "cli"),
			ThreadID: strings.TrimSpace(input.ThreadID),
			Mode:     firstNonEmpty(input.DeliveryMode, "artifact"),
			Path:     strings.TrimSpace(input.DeliveryPath),
		},
		Confirmation: ConfirmationPolicy{
			Create:     "required",
			RiskyTools: "required",
		},
		Limits: Limits{
			MaxRuntimeSeconds: 300,
			MaxOutputChars:    6000,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if task.ID == "" {
		task.ID = slug(task.Title)
	}
	if task.ID == "" {
		task.ID = "task-" + now.Format("20060102-150405")
	}
	if err := validateTask(task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s Store) List() ([]Task, error) {
	entries, err := os.ReadDir(s.taskDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tasks []Task
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		task, err := readTask(filepath.Join(s.taskDir(), entry.Name()))
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].ID < tasks[j].ID
	})
	return tasks, nil
}

func (s Store) Show(id string) (Task, string, error) {
	path := s.taskPath(id)
	task, err := readTask(path)
	if err != nil {
		return Task{}, "", err
	}
	return task, path, nil
}

func (s Store) SetStatus(id, status string) (Task, string, error) {
	status = strings.TrimSpace(status)
	if status != StatusActive && status != StatusPaused {
		return Task{}, "", fmt.Errorf("unsupported schedule status: %s", status)
	}
	task, path, err := s.Show(id)
	if err != nil {
		return Task{}, "", err
	}
	task.Status = status
	task.UpdatedAt = time.Now()
	if err := writeYAML(path, task); err != nil {
		return Task{}, "", err
	}
	return task, path, nil
}

func (s Store) Update(id string, input UpdateInput) (Task, string, error) {
	task, path, err := s.Show(id)
	if err != nil {
		return Task{}, "", err
	}
	if strings.TrimSpace(input.Title) != "" {
		task.Title = strings.TrimSpace(input.Title)
	}
	if strings.TrimSpace(input.Prompt) != "" {
		task.Prompt = strings.TrimSpace(input.Prompt)
	}
	if strings.TrimSpace(input.AgentID) != "" {
		task.AgentID = strings.TrimSpace(input.AgentID)
	}
	if input.Schedule != nil {
		task.Schedule = *input.Schedule
	}
	if strings.TrimSpace(input.DeliveryMode) != "" {
		task.Delivery.Mode = strings.TrimSpace(input.DeliveryMode)
	}
	if strings.TrimSpace(input.DeliveryPath) != "" {
		task.Delivery.Path = strings.TrimSpace(input.DeliveryPath)
	}
	if len(input.AllowedTools) > 0 {
		task.AllowedTools = cleanList(input.AllowedTools)
	}
	task.UpdatedAt = time.Now()
	if err := validateTask(task); err != nil {
		return Task{}, "", err
	}
	if err := writeYAML(path, task); err != nil {
		return Task{}, "", err
	}
	return task, path, nil
}

func (s Store) Delete(id string) (string, error) {
	path := s.taskPath(id)
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s Store) Due(now time.Time) ([]Task, error) {
	if now.IsZero() {
		now = time.Now()
	}
	tasks, err := s.List()
	if err != nil {
		return nil, err
	}
	state, err := s.ReadState()
	if err != nil {
		return nil, err
	}
	var due []Task
	for _, task := range tasks {
		if task.Status != StatusActive {
			continue
		}
		if !isDueNow(task, state, now) {
			continue
		}
		due = append(due, task)
	}
	return due, nil
}

func (s Store) ReadState() (State, error) {
	data, err := os.ReadFile(s.statePath())
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (s Store) WriteRunState(next RunState) error {
	state, err := s.ReadState()
	if err != nil {
		return err
	}
	for i, item := range state.Tasks {
		if item.TaskID == next.TaskID {
			state.Tasks[i] = next
			return s.writeState(state)
		}
	}
	state.Tasks = append(state.Tasks, next)
	return s.writeState(state)
}

func (s Store) writeState(state State) error {
	path := s.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (s Store) taskDir() string {
	return filepath.Join(s.Home, "schedules", "tasks")
}

func (s Store) taskPath(id string) string {
	return filepath.Join(s.taskDir(), filepath.Base(strings.TrimSpace(id))+".yaml")
}

func (s Store) statePath() string {
	return filepath.Join(s.Home, "run", "scheduler", "user_tasks_state.json")
}

func readTask(path string) (Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Task{}, err
	}
	var task Task
	if err := yaml.Unmarshal(data, &task); err != nil {
		return Task{}, err
	}
	if err := validateTask(task); err != nil {
		return Task{}, fmt.Errorf("%s: %w", path, err)
	}
	return task, nil
}

func writeYAML(path string, task Task) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(task)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func validateTask(task Task) error {
	if strings.TrimSpace(task.ID) == "" {
		return fmt.Errorf("task id is required")
	}
	if strings.TrimSpace(task.Title) == "" {
		return fmt.Errorf("task title is required")
	}
	if strings.TrimSpace(task.Prompt) == "" {
		return fmt.Errorf("task prompt is required")
	}
	switch normalizeScheduleKind(task.Schedule) {
	case "daily":
		if _, _, ok := parseClock(task.Schedule.DailyAt); !ok {
			return fmt.Errorf("schedule.daily_at must be HH:MM")
		}
	case "weekly":
		if _, _, ok := parseClock(task.Schedule.WeeklyAt); !ok {
			return fmt.Errorf("schedule.weekly_at must be HH:MM")
		}
		if len(scheduleWeekdays(task.Schedule)) == 0 {
			return fmt.Errorf("schedule.weekday is required for weekly schedules")
		}
	case "monthly":
		if _, _, ok := parseClock(task.Schedule.MonthlyAt); !ok {
			return fmt.Errorf("schedule.monthly_at must be HH:MM")
		}
		if task.Schedule.MonthlyDay < 1 || task.Schedule.MonthlyDay > 31 {
			return fmt.Errorf("schedule.monthly_day must be between 1 and 31")
		}
	case "interval":
		if _, err := time.ParseDuration(task.Schedule.Interval); err != nil {
			return fmt.Errorf("schedule.interval must be a duration")
		}
	default:
		return fmt.Errorf("unsupported schedule kind: %s", task.Schedule.Kind)
	}
	return nil
}

func scheduleSpecFromInput(input CreateInput) ScheduleSpec {
	if strings.TrimSpace(input.Interval) != "" {
		return ScheduleSpec{Kind: "interval", Interval: strings.TrimSpace(input.Interval)}
	}
	if strings.TrimSpace(input.MonthlyAt) != "" || input.MonthlyDay > 0 {
		return ScheduleSpec{Kind: "monthly", MonthlyAt: firstNonEmpty(input.MonthlyAt, input.DailyAt, "09:00"), MonthlyDay: input.MonthlyDay}
	}
	if strings.TrimSpace(input.WeeklyAt) != "" || strings.TrimSpace(input.Weekday) != "" || len(input.Weekdays) > 0 {
		weekdays := normalizeWeekdays(input.Weekdays)
		weekday := strings.TrimSpace(input.Weekday)
		if len(weekdays) == 0 && weekday != "" {
			weekdays = []string{weekday}
		}
		return ScheduleSpec{Kind: "weekly", WeeklyAt: firstNonEmpty(input.WeeklyAt, input.DailyAt, "09:00"), Weekday: weekday, Weekdays: weekdays}
	}
	return ScheduleSpec{Kind: "daily", DailyAt: firstNonEmpty(input.DailyAt, "09:00")}
}

func normalizeScheduleKind(spec ScheduleSpec) string {
	if strings.TrimSpace(spec.Kind) != "" {
		return strings.ToLower(strings.TrimSpace(spec.Kind))
	}
	if strings.TrimSpace(spec.Interval) != "" {
		return "interval"
	}
	if strings.TrimSpace(spec.MonthlyAt) != "" || spec.MonthlyDay > 0 {
		return "monthly"
	}
	if strings.TrimSpace(spec.WeeklyAt) != "" || strings.TrimSpace(spec.Weekday) != "" || len(spec.Weekdays) > 0 {
		return "weekly"
	}
	return "daily"
}

func isDueNow(task Task, state State, now time.Time) bool {
	switch normalizeScheduleKind(task.Schedule) {
	case "daily":
		return pastDailyTime(now, task.Schedule.DailyAt) && !ranToday(state, task.ID, now)
	case "weekly":
		if !matchesAnyWeekday(now.Weekday(), scheduleWeekdays(task.Schedule)) || !pastDailyTime(now, task.Schedule.WeeklyAt) {
			return false
		}
		if len(scheduleWeekdays(task.Schedule)) > 1 {
			return !ranToday(state, task.ID, now)
		}
		return !ranThisWeek(state, task.ID, now)
	case "monthly":
		return now.Day() == task.Schedule.MonthlyDay && pastDailyTime(now, task.Schedule.MonthlyAt) && !ranThisMonth(state, task.ID, now)
	case "interval":
		interval, err := time.ParseDuration(task.Schedule.Interval)
		if err != nil || interval <= 0 {
			return false
		}
		last, ok := lastRunAt(state, task.ID)
		return !ok || !now.Before(last.Add(interval))
	default:
		return false
	}
}

func pastDailyTime(now time.Time, dailyAt string) bool {
	hour, minute, ok := parseClock(dailyAt)
	if !ok {
		return false
	}
	dueAt := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	return !now.Before(dueAt)
}

func ranToday(state State, taskID string, now time.Time) bool {
	last, ok := lastRunAt(state, taskID)
	if !ok {
		return false
	}
	last = last.In(now.Location())
	return last.Year() == now.Year() && last.YearDay() == now.YearDay()
}

func ranThisWeek(state State, taskID string, now time.Time) bool {
	last, ok := lastRunAt(state, taskID)
	if !ok {
		return false
	}
	last = last.In(now.Location())
	nowYear, nowWeek := now.ISOWeek()
	lastYear, lastWeek := last.ISOWeek()
	return nowYear == lastYear && nowWeek == lastWeek
}

func ranThisMonth(state State, taskID string, now time.Time) bool {
	last, ok := lastRunAt(state, taskID)
	if !ok {
		return false
	}
	last = last.In(now.Location())
	return last.Year() == now.Year() && last.Month() == now.Month()
}

func lastRunAt(state State, taskID string) (time.Time, bool) {
	for _, item := range state.Tasks {
		if item.TaskID != taskID || item.LastRunAt.IsZero() {
			continue
		}
		return item.LastRunAt, true
	}
	return time.Time{}, false
}

func scheduleWeekdays(spec ScheduleSpec) []string {
	if len(spec.Weekdays) > 0 {
		return normalizeWeekdays(spec.Weekdays)
	}
	if strings.TrimSpace(spec.Weekday) != "" {
		return normalizeWeekdays([]string{spec.Weekday})
	}
	return nil
}

func normalizeWeekdays(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		weekday, ok := parseWeekday(value)
		if !ok {
			continue
		}
		name := weekdayName(weekday)
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func matchesAnyWeekday(current time.Weekday, values []string) bool {
	for _, value := range values {
		target, ok := parseWeekday(value)
		if ok && current == target {
			return true
		}
	}
	return false
}

func weekdayName(day time.Weekday) string {
	switch day {
	case time.Monday:
		return "monday"
	case time.Tuesday:
		return "tuesday"
	case time.Wednesday:
		return "wednesday"
	case time.Thursday:
		return "thursday"
	case time.Friday:
		return "friday"
	case time.Saturday:
		return "saturday"
	default:
		return "sunday"
	}
}

func parseWeekday(value string) (time.Weekday, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "sunday", "sun", "0":
		return time.Sunday, true
	case "monday", "mon", "1":
		return time.Monday, true
	case "tuesday", "tue", "2":
		return time.Tuesday, true
	case "wednesday", "wed", "3":
		return time.Wednesday, true
	case "thursday", "thu", "4":
		return time.Thursday, true
	case "friday", "fri", "5":
		return time.Friday, true
	case "saturday", "sat", "6":
		return time.Saturday, true
	default:
		return time.Sunday, false
	}
}

func Summary(spec ScheduleSpec) string {
	switch normalizeScheduleKind(spec) {
	case "weekly":
		return "weekly:" + strings.Join(scheduleWeekdays(spec), ",") + "@" + spec.WeeklyAt
	case "monthly":
		return fmt.Sprintf("monthly:%d@%s", spec.MonthlyDay, spec.MonthlyAt)
	case "interval":
		return "interval:" + spec.Interval
	default:
		return "daily@" + spec.DailyAt
	}
}

func parseClock(value string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	hour, okHour := parseClockPart(parts[0], 23)
	minute, okMinute := parseClockPart(parts[1], 59)
	return hour, minute, okHour && okMinute
}

func parseClockPart(value string, max int) (int, bool) {
	if value == "" || len(value) > 2 {
		return 0, false
	}
	n := 0
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	return n, n <= max
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, ch := range value {
		ok := (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')
		if ok {
			b.WriteRune(ch)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func cleanList(values []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
