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
	Summary    SessionSummary      `json:"summary,omitempty"`
	ActiveTask string              `json:"active_task,omitempty"`
	Pending    *PendingAction      `json:"pending,omitempty"`
	Usage      Usage               `json:"usage,omitempty"`
	UpdatedAt  time.Time           `json:"updated_at"`
}

type SessionSummary struct {
	Text      string    `json:"text,omitempty"`
	Tasks     []string  `json:"tasks,omitempty"`
	OpenItems []string  `json:"open_items,omitempty"`
	Evidence  []string  `json:"evidence,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type TaskNode struct {
	ID        string         `json:"id"`
	ParentID  string         `json:"parent_id,omitempty"`
	Goal      string         `json:"goal"`
	Summary   string         `json:"summary,omitempty"`
	Status    string         `json:"status"`
	Execution ExecutionFrame `json:"execution,omitempty"`
	Steps     []TaskStep     `json:"steps,omitempty"`
	TraceID   string         `json:"trace_id,omitempty"`
	TracePath string         `json:"trace_path,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type TaskStep struct {
	ID                 string         `json:"id"`
	Tool               string         `json:"tool"`
	Status             string         `json:"status"`
	Summary            string         `json:"summary,omitempty"`
	Evidence           map[string]any `json:"evidence,omitempty"`
	Risk               string         `json:"risk,omitempty"`
	AcceptanceCriteria string         `json:"acceptance_criteria,omitempty"`
	EvidenceContract   string         `json:"evidence_contract,omitempty"`
	Accepted           bool           `json:"accepted,omitempty"`
	Mutation           bool           `json:"mutation,omitempty"`
}

type ExecutionFrame struct {
	ID            string           `json:"id,omitempty"`
	Mode          string           `json:"mode,omitempty"`
	Status        string           `json:"status,omitempty"`
	OriginalTask  string           `json:"original_task,omitempty"`
	Contract      *TaskContract    `json:"contract,omitempty"`
	TraceRefs     []TraceRef       `json:"trace_refs,omitempty"`
	CurrentStepID string           `json:"current_step_id,omitempty"`
	CurrentNodeID string           `json:"current_node_id,omitempty"`
	Events        []ExecutionEvent `json:"events,omitempty"`
	UpdatedAt     time.Time        `json:"updated_at,omitempty"`
}

type TaskContract struct {
	Summary          string                 `json:"summary,omitempty"`
	RequiresTools    bool                   `json:"requires_tools,omitempty"`
	RequiredTools    []string               `json:"required_tools,omitempty"`
	RequiredEvidence []TaskEvidenceContract `json:"required_evidence,omitempty"`
	PlanItems        []TaskPlanItem         `json:"plan_items,omitempty"`
	ExpectedOutcome  string                 `json:"expected_outcome,omitempty"`
	CompletionPolicy string                 `json:"completion_policy,omitempty"`
	CreatedAt        time.Time              `json:"created_at,omitempty"`
}

type TaskPlanItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Tool      string    `json:"tool,omitempty"`
	Criteria  string    `json:"criteria,omitempty"`
	Evidence  string    `json:"evidence,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type TaskEvidenceContract struct {
	Kind        string `json:"kind,omitempty"`
	Tool        string `json:"tool,omitempty"`
	Description string `json:"description,omitempty"`
}

type TraceRef struct {
	TraceID   string    `json:"trace_id,omitempty"`
	TracePath string    `json:"trace_path,omitempty"`
	Phase     string    `json:"phase,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

const (
	PendingKindMemoryProposalReview = "memory_proposal_review"
	PendingKindTaskPlanConfirm      = "task_plan_confirm"
)

type ExecutionEvent struct {
	ID        string         `json:"id,omitempty"`
	Type      string         `json:"type"`
	Status    string         `json:"status,omitempty"`
	Tool      string         `json:"tool,omitempty"`
	StepID    string         `json:"step_id,omitempty"`
	Summary   string         `json:"summary,omitempty"`
	Evidence  map[string]any `json:"evidence,omitempty"`
	CreatedAt time.Time      `json:"created_at,omitempty"`
}

type PendingAction struct {
	Kind        string `json:"kind"`
	TaskID      string `json:"task_id"`
	ProposalID  string `json:"proposal_id,omitempty"`
	Question    string `json:"question,omitempty"`
	Feedback    string `json:"feedback,omitempty"`
	ReplanCount int    `json:"replan_count,omitempty"`
}

type Usage struct {
	Requests             int     `json:"requests,omitempty"`
	InputTokens          int     `json:"input_tokens,omitempty"`
	OutputTokens         int     `json:"output_tokens,omitempty"`
	TotalTokens          int     `json:"total_tokens,omitempty"`
	EstimatedInputTokens int     `json:"estimated_input_tokens,omitempty"`
	SavedEstimatedTokens int     `json:"saved_estimated_tokens,omitempty"`
	CompactedMessages    int     `json:"compacted_messages,omitempty"`
	CompactedToolResults int     `json:"compacted_tool_results,omitempty"`
	CacheHits            int     `json:"cache_hits,omitempty"`
	CacheReadTokens      int     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens     int     `json:"cache_write_tokens,omitempty"`
	CacheInputTokens     int     `json:"cache_input_tokens,omitempty"`
	CacheOutputTokens    int     `json:"cache_output_tokens,omitempty"`
	Cost                 float64 `json:"cost,omitempty"`
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

func (s Store) Archive(state State) (string, error) {
	if strings.TrimSpace(state.Key) == "" {
		state.Key = "default"
	}
	dir := filepath.Join(s.dir, "archive", safeName(state.Key))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	state.UpdatedAt = time.Now()
	name := time.Now().Format("20060102-150405.000000") + ".json"
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(data, '\n'), 0o644)
}

func (s Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}
		var state State
		if json.Unmarshal(data, &state) == nil && strings.TrimSpace(state.Key) != "" {
			keys = append(keys, state.Key)
		}
	}
	return keys, nil
}

func (s Store) ListArchives(key string) ([]string, error) {
	dir := filepath.Join(s.dir, "archive", safeName(key))
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		ids = append(ids, strings.TrimSuffix(entry.Name(), ".json"))
	}
	return ids, nil
}

func (s Store) LoadArchive(key, id string) (State, string, error) {
	id = strings.TrimSuffix(strings.TrimSpace(id), ".json")
	path := filepath.Join(s.dir, "archive", safeName(key), id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, path, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, path, err
	}
	if state.Key == "" {
		state.Key = key
	}
	return state, path, nil
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
	task.Execution = newExecutionFrame(task.ID, task.Goal, now)
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
			ensureExecutionFrame(&s.Tasks[i], now)
			s.Tasks[i].Execution.Status = "running"
			s.Tasks[i].Execution.UpdatedAt = now
			s.Tasks[i].UpdatedAt = now
			return &s.Tasks[i]
		}
	}
	return nil
}

func IsOpenTaskStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "", "running", "await_user_input", "resuming", "failed":
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
			ensureExecutionFrame(&s.Tasks[i], now)
			s.Tasks[i].Execution.CurrentStepID = step.ID
			s.Tasks[i].Execution.UpdatedAt = now
			s.Tasks[i].UpdatedAt = now
			return
		}
	}
}

func (s *State) CompleteActiveTask() {
	for i := range s.Tasks {
		if s.Tasks[i].ID == s.ActiveTask {
			s.Tasks[i].Status = "completed"
			now := time.Now()
			ensureExecutionFrame(&s.Tasks[i], now)
			s.Tasks[i].Execution.Status = "completed"
			s.Tasks[i].Execution.UpdatedAt = now
			s.Tasks[i].UpdatedAt = now
			s.ActiveTask = ""
			return
		}
	}
}

func (s *State) CompleteActiveTaskWithSummary(summary, traceID, tracePath string) {
	for i := range s.Tasks {
		if s.Tasks[i].ID == s.ActiveTask {
			s.Tasks[i].Status = "completed"
			s.Tasks[i].Summary = strings.TrimSpace(summary)
			s.Tasks[i].TraceID = strings.TrimSpace(traceID)
			s.Tasks[i].TracePath = strings.TrimSpace(tracePath)
			now := time.Now()
			ensureExecutionFrame(&s.Tasks[i], now)
			s.Tasks[i].Execution.Status = "completed"
			s.Tasks[i].Execution.UpdatedAt = now
			s.Tasks[i].UpdatedAt = now
			s.ActiveTask = ""
			return
		}
	}
}

func (s *State) AwaitUserInputActiveTaskWithSummary(summary, traceID, tracePath string) {
	for i := range s.Tasks {
		if s.Tasks[i].ID == s.ActiveTask {
			s.Tasks[i].Status = "await_user_input"
			s.Tasks[i].Summary = strings.TrimSpace(summary)
			s.Tasks[i].TraceID = strings.TrimSpace(traceID)
			s.Tasks[i].TracePath = strings.TrimSpace(tracePath)
			now := time.Now()
			ensureExecutionFrame(&s.Tasks[i], now)
			s.Tasks[i].Execution.Status = executionStatusForTaskStatus("await_user_input")
			s.Tasks[i].Execution.UpdatedAt = now
			s.Tasks[i].UpdatedAt = now
			return
		}
	}
}

func (s *State) SetTaskTrace(taskID, traceID, tracePath string) {
	now := time.Now()
	for i := range s.Tasks {
		if s.Tasks[i].ID == taskID {
			s.Tasks[i].TraceID = strings.TrimSpace(traceID)
			s.Tasks[i].TracePath = strings.TrimSpace(tracePath)
			ensureExecutionFrame(&s.Tasks[i], now)
			s.Tasks[i].Execution.UpdatedAt = now
			s.Tasks[i].UpdatedAt = now
			return
		}
	}
}

func (s *State) BlockActiveTask(kind string) {
	for i := range s.Tasks {
		if s.Tasks[i].ID == s.ActiveTask {
			s.Tasks[i].Status = kind
			now := time.Now()
			ensureExecutionFrame(&s.Tasks[i], now)
			s.Tasks[i].Execution.Status = executionStatusForTaskStatus(kind)
			s.Tasks[i].Execution.UpdatedAt = now
			s.Tasks[i].UpdatedAt = now
			return
		}
	}
}

func (s *State) EnsureExecutionFrame(taskID string) *ExecutionFrame {
	now := time.Now()
	for i := range s.Tasks {
		if s.Tasks[i].ID == taskID {
			ensureExecutionFrame(&s.Tasks[i], now)
			s.Tasks[i].UpdatedAt = now
			return &s.Tasks[i].Execution
		}
	}
	return nil
}

func (s *State) SetExecutionStatus(taskID, status string) {
	now := time.Now()
	for i := range s.Tasks {
		if s.Tasks[i].ID == taskID {
			ensureExecutionFrame(&s.Tasks[i], now)
			s.Tasks[i].Execution.Status = strings.TrimSpace(status)
			s.Tasks[i].Execution.UpdatedAt = now
			s.Tasks[i].UpdatedAt = now
			return
		}
	}
}

func (s *State) AddExecutionEvent(taskID string, event ExecutionEvent) {
	now := time.Now()
	for i := range s.Tasks {
		if s.Tasks[i].ID == taskID {
			ensureExecutionFrame(&s.Tasks[i], now)
			if event.ID == "" {
				event.ID = nextEventID(len(s.Tasks[i].Execution.Events) + 1)
			}
			if event.CreatedAt.IsZero() {
				event.CreatedAt = now
			}
			s.Tasks[i].Execution.Events = append(s.Tasks[i].Execution.Events, event)
			s.Tasks[i].Execution.UpdatedAt = now
			s.Tasks[i].UpdatedAt = now
			return
		}
	}
}

func (s *State) AddTraceRef(taskID string, ref TraceRef) {
	now := time.Now()
	for i := range s.Tasks {
		if s.Tasks[i].ID == taskID {
			ensureExecutionFrame(&s.Tasks[i], now)
			if ref.CreatedAt.IsZero() {
				ref.CreatedAt = now
			}
			ref.TraceID = strings.TrimSpace(ref.TraceID)
			ref.TracePath = strings.TrimSpace(ref.TracePath)
			ref.Phase = strings.TrimSpace(ref.Phase)
			ref.MessageID = strings.TrimSpace(ref.MessageID)
			for j := range s.Tasks[i].Execution.TraceRefs {
				existing := &s.Tasks[i].Execution.TraceRefs[j]
				if existing.TraceID != "" && existing.TraceID == ref.TraceID {
					if ref.TracePath != "" {
						existing.TracePath = ref.TracePath
					}
					if ref.Phase != "" {
						existing.Phase = ref.Phase
					}
					if ref.MessageID != "" {
						existing.MessageID = ref.MessageID
					}
					s.Tasks[i].TraceID = ref.TraceID
					s.Tasks[i].TracePath = ref.TracePath
					s.Tasks[i].Execution.UpdatedAt = now
					s.Tasks[i].UpdatedAt = now
					return
				}
			}
			s.Tasks[i].Execution.TraceRefs = append(s.Tasks[i].Execution.TraceRefs, ref)
			if ref.TraceID != "" {
				s.Tasks[i].TraceID = ref.TraceID
			}
			if ref.TracePath != "" {
				s.Tasks[i].TracePath = ref.TracePath
			}
			s.Tasks[i].Execution.UpdatedAt = now
			s.Tasks[i].UpdatedAt = now
			return
		}
	}
}

func (s *State) SetTaskContract(taskID string, contract TaskContract) {
	now := time.Now()
	for i := range s.Tasks {
		if s.Tasks[i].ID == taskID {
			ensureExecutionFrame(&s.Tasks[i], now)
			if contract.CreatedAt.IsZero() {
				contract.CreatedAt = now
			}
			s.Tasks[i].Execution.Contract = &contract
			s.Tasks[i].Execution.UpdatedAt = now
			s.Tasks[i].UpdatedAt = now
			return
		}
	}
}

func (s State) TaskByID(taskID string) *TaskNode {
	for i := range s.Tasks {
		if s.Tasks[i].ID == taskID {
			return &s.Tasks[i]
		}
	}
	return nil
}

func newExecutionFrame(taskID, goal string, now time.Time) ExecutionFrame {
	return ExecutionFrame{
		ID:           "frame-" + taskID,
		Mode:         "agent_loop",
		Status:       "running",
		OriginalTask: strings.TrimSpace(goal),
		UpdatedAt:    now,
	}
}

func ensureExecutionFrame(task *TaskNode, now time.Time) {
	if strings.TrimSpace(task.Execution.ID) == "" {
		task.Execution = newExecutionFrame(task.ID, task.Goal, now)
		task.Execution.Status = executionStatusForTaskStatus(task.Status)
		return
	}
	if strings.TrimSpace(task.Execution.Mode) == "" {
		task.Execution.Mode = "agent_loop"
	}
	if strings.TrimSpace(task.Execution.Status) == "" {
		task.Execution.Status = executionStatusForTaskStatus(task.Status)
	}
	if strings.TrimSpace(task.Execution.OriginalTask) == "" {
		task.Execution.OriginalTask = strings.TrimSpace(task.Goal)
	}
	if task.Execution.UpdatedAt.IsZero() {
		task.Execution.UpdatedAt = now
	}
}

func executionStatusForTaskStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "await_confirm":
		return "awaiting_confirmation"
	case "await_user_input", "await_schedule_test":
		return "awaiting_user_input"
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "cancelled":
		return "cancelled"
	case "resuming":
		return "resuming"
	default:
		return "running"
	}
}

func (s Store) path(key string) string {
	name := safeName(key)
	if name == "" {
		name = "default"
	}
	return filepath.Join(s.dir, name+".json")
}

func safeName(key string) string {
	return strings.NewReplacer("/", "_", ":", "_", "\\", "_").Replace(key)
}

func nextTaskID(n int) string {
	return "task-" + time.Now().Format("20060102150405.000000") + "-" + strings.TrimLeft(strings.ReplaceAll(time.Duration(n).String(), "ns", ""), "0")
}

func nextStepID(n int) string {
	return "step-" + time.Now().Format("150405") + "-" + strings.TrimLeft(strings.ReplaceAll(time.Duration(n).String(), "ns", ""), "0")
}

func nextEventID(n int) string {
	return "event-" + time.Now().Format("150405") + "-" + strings.TrimLeft(strings.ReplaceAll(time.Duration(n).String(), "ns", ""), "0")
}
