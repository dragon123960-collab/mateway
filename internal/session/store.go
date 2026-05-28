package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
)

type State struct {
	Key        string              `json:"key"`
	Messages   []agentcore.Message `json:"messages"`
	Tasks      []TaskNode          `json:"tasks,omitempty"`
	ActiveTask string              `json:"active_task,omitempty"`
	Pending    *PendingAction      `json:"pending,omitempty"`
	UpdatedAt  time.Time           `json:"updated_at"`
}

type TaskNode struct {
	ID        string     `json:"id"`
	ParentID  string     `json:"parent_id,omitempty"`
	Goal      string     `json:"goal"`
	Status    string     `json:"status"`
	Steps     []TaskStep `json:"steps,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type TaskStep struct {
	ID       string         `json:"id"`
	Tool     string         `json:"tool"`
	Status   string         `json:"status"`
	Summary  string         `json:"summary,omitempty"`
	Evidence map[string]any `json:"evidence,omitempty"`
}

type PendingAction struct {
	Kind       string             `json:"kind"`
	TaskID     string             `json:"task_id"`
	Question   string             `json:"question,omitempty"`
	ToolCall   agentcore.ToolCall `json:"tool_call,omitempty"`
	ResumeText string             `json:"resume_text,omitempty"`
}

type Store struct {
	dir string
}

func NewStore(home string) Store {
	return Store{dir: filepath.Join(home, "sessions")}
}

func (s Store) Load(key string) (State, error) {
	path := s.path(key)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{Key: key}, nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	if state.Key == "" {
		state.Key = key
	}
	return state, nil
}

func (s Store) Save(state State) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	state.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(state.Key), data, 0o644)
}

func (s *State) EnsureTask(goal string) *TaskNode {
	now := time.Now()
	if s.ActiveTask != "" {
		for i := range s.Tasks {
			if s.Tasks[i].ID == s.ActiveTask && IsOpenTaskStatus(s.Tasks[i].Status) {
				s.Tasks[i].UpdatedAt = now
				return &s.Tasks[i]
			}
		}
	}
	return s.StartTask(goal)
}

func (s *State) StartTask(goal string) *TaskNode {
	now := time.Now()
	task := TaskNode{
		ID:        nextTaskID(len(s.Tasks) + 1),
		Goal:      strings.TrimSpace(goal),
		Status:    "running",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if task.Goal == "" {
		task.Goal = "Untitled task"
	}
	s.Tasks = append(s.Tasks, task)
	s.ActiveTask = task.ID
	return &s.Tasks[len(s.Tasks)-1]
}

func (s *State) ActivateTask(taskID string) *TaskNode {
	now := time.Now()
	for i := range s.Tasks {
		if s.Tasks[i].ID == taskID {
			s.ActiveTask = taskID
			s.Tasks[i].Status = "running"
			s.Tasks[i].UpdatedAt = now
			return &s.Tasks[i]
		}
	}
	return nil
}

func IsOpenTaskStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "", "running", "await_confirm", "await_user_input":
		return true
	default:
		return false
	}
}

func (s *State) AddStep(taskID string, step TaskStep) {
	now := time.Now()
	for i := range s.Tasks {
		if s.Tasks[i].ID == taskID {
			if step.ID == "" {
				step.ID = nextStepID(len(s.Tasks[i].Steps) + 1)
			}
			s.Tasks[i].Steps = append(s.Tasks[i].Steps, step)
			s.Tasks[i].UpdatedAt = now
			return
		}
	}
}

func (s *State) CompleteActiveTask() {
	for i := range s.Tasks {
		if s.Tasks[i].ID == s.ActiveTask {
			s.Tasks[i].Status = "completed"
			s.Tasks[i].UpdatedAt = time.Now()
			return
		}
	}
}

func (s *State) BlockActiveTask(kind string) {
	for i := range s.Tasks {
		if s.Tasks[i].ID == s.ActiveTask {
			s.Tasks[i].Status = kind
			s.Tasks[i].UpdatedAt = time.Now()
			return
		}
	}
}

func (s Store) path(key string) string {
	name := strings.NewReplacer("/", "_", ":", "_", "\\", "_").Replace(key)
	if name == "" {
		name = "default"
	}
	return filepath.Join(s.dir, name+".json")
}

func nextTaskID(n int) string {
	return "task-" + time.Now().Format("20060102150405") + "-" + strings.TrimLeft(strings.ReplaceAll(time.Duration(n).String(), "ns", ""), "0")
}

func nextStepID(n int) string {
	return "step-" + time.Now().Format("150405") + "-" + strings.TrimLeft(strings.ReplaceAll(time.Duration(n).String(), "ns", ""), "0")
}
