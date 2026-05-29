package schedule

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Task struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	SessionKey    string `json:"session_key"`
	Text          string `json:"text"`
	RunAt         string `json:"run_at"`
	Interval      string `json:"interval,omitempty"`
	RequireTest   bool   `json:"require_test"`
	TestedAt      string `json:"tested_at,omitempty"`
	LastRunAt     string `json:"last_run_at,omitempty"`
	LastRunStatus string `json:"last_run_status,omitempty"`
	LastRunID     string `json:"last_run_id,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type Store struct {
	Home string
	Now  func() time.Time
}

type CreateInput struct {
	SessionKey  string
	Text        string
	RunAt       time.Time
	Interval    time.Duration
	RequireTest bool
	Activate    bool
}

type RunRecord struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	SessionKey string `json:"session_key"`
	Output     string `json:"output,omitempty"`
	TracePath  string `json:"trace_path,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (s Store) Create(input CreateInput) (Task, error) {
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return Task{}, fmt.Errorf("schedule text is required")
	}
	if input.RunAt.IsZero() {
		return Task{}, fmt.Errorf("run_at is required")
	}
	now := s.now()
	status := "pending"
	if input.Activate || !input.RequireTest {
		status = "active"
	}
	task := Task{
		ID:          "sch_" + now.Format("20060102150405.000000000"),
		Status:      status,
		SessionKey:  firstNonEmpty(input.SessionKey, "schedule:"+now.Format("20060102150405")),
		Text:        text,
		RunAt:       input.RunAt.Format(time.RFC3339),
		RequireTest: input.RequireTest,
		CreatedAt:   now.Format(time.RFC3339),
		UpdatedAt:   now.Format(time.RFC3339),
	}
	if input.Interval > 0 {
		task.Interval = input.Interval.String()
	}
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return Task{}, err
	}
	return task, s.write(task)
}

func (s Store) List() ([]Task, error) {
	entries, err := os.ReadDir(s.dir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tasks []Task
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		task, err := s.read(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].RunAt < tasks[j].RunAt
	})
	return tasks, nil
}

func (s Store) Due(now time.Time) ([]Task, error) {
	tasks, err := s.List()
	if err != nil {
		return nil, err
	}
	var due []Task
	for _, task := range tasks {
		if task.Status != "active" {
			continue
		}
		runAt, err := time.Parse(time.RFC3339, task.RunAt)
		if err != nil {
			continue
		}
		if !runAt.After(now) {
			due = append(due, task)
		}
	}
	return due, nil
}

func (s Store) Activate(id string) (Task, error) {
	task, err := s.read(id)
	if err != nil {
		return Task{}, err
	}
	task.Status = "active"
	task.UpdatedAt = s.now().Format(time.RFC3339)
	if err := s.write(task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s Store) Pause(id string) (Task, error) {
	task, err := s.read(id)
	if err != nil {
		return Task{}, err
	}
	task.Status = "paused"
	task.UpdatedAt = s.now().Format(time.RFC3339)
	if err := s.write(task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s Store) MarkTested(task Task, finishedAt time.Time, record RunRecord) error {
	task.TestedAt = finishedAt.Format(time.RFC3339)
	task.LastRunAt = task.TestedAt
	task.LastRunStatus = record.Status
	task.LastRunID = record.ID
	task.UpdatedAt = task.TestedAt
	if record.Status == "success" {
		task.Status = "active"
	} else {
		task.Status = "error"
	}
	return s.write(task)
}

func (s Store) MarkError(task Task, finishedAt time.Time, record RunRecord) error {
	task.LastRunAt = finishedAt.Format(time.RFC3339)
	task.LastRunStatus = record.Status
	task.LastRunID = record.ID
	task.UpdatedAt = task.LastRunAt
	task.Status = "error"
	return s.write(task)
}

func (s Store) MarkRan(task Task, finishedAt time.Time, record RunRecord) error {
	task.LastRunAt = finishedAt.Format(time.RFC3339)
	task.LastRunStatus = record.Status
	task.LastRunID = record.ID
	task.UpdatedAt = task.LastRunAt
	if task.Interval == "" {
		task.Status = "done"
		return s.write(task)
	}
	interval, err := time.ParseDuration(task.Interval)
	if err != nil || interval <= 0 {
		task.Status = "error"
		return s.write(task)
	}
	next, err := time.Parse(time.RFC3339, task.RunAt)
	if err != nil {
		next = finishedAt
	}
	for !next.After(finishedAt) {
		next = next.Add(interval)
	}
	task.RunAt = next.Format(time.RFC3339)
	return s.write(task)
}

func (s Store) RecordRun(record RunRecord) (RunRecord, error) {
	now := s.now()
	if record.ID == "" {
		record.ID = "run_" + now.Format("20060102150405.000000000")
	}
	if record.StartedAt == "" {
		record.StartedAt = now.Format(time.RFC3339)
	}
	if record.FinishedAt == "" {
		record.FinishedAt = now.Format(time.RFC3339)
	}
	if err := os.MkdirAll(s.runsDir(), 0o755); err != nil {
		return RunRecord{}, err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return RunRecord{}, err
	}
	path := filepath.Join(s.runsDir(), record.ID+".json")
	return record, os.WriteFile(path, append(data, '\n'), 0o644)
}

func (s Store) Read(id string) (Task, error) {
	return s.read(id)
}

func (s Store) read(id string) (Task, error) {
	data, err := os.ReadFile(filepath.Join(s.dir(), id+".json"))
	if err != nil {
		return Task{}, err
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s Store) write(task Task) error {
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir(), task.ID+".json"), append(data, '\n'), 0o644)
}

func (s Store) dir() string {
	home := strings.TrimSpace(s.Home)
	if home == "" {
		home = ".mateway"
	}
	return filepath.Join(home, "schedules")
}

func (s Store) runsDir() string {
	return filepath.Join(s.dir(), "runs")
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
