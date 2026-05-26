package session

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const recentTurnLimit = 8

const (
	TaskOpen           = "open"
	TaskAwaitConfirm   = "await_confirm"
	TaskAwaitUserInput = "await_user_input"
	TaskCompleted      = "completed"
	TaskFailed         = "failed"
	TaskAbandoned      = "abandoned"
)

type Turn struct {
	Role string    `json:"role"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type PendingApproval struct {
	ApprovalType    string   `json:"approval_type"`
	Prompt          string   `json:"prompt"`
	Options         []string `json:"options,omitempty"`
	RequestedAction string   `json:"requested_action,omitempty"`
}

type Artifact struct {
	Kind      string `json:"kind"`
	Path      string `json:"path,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Label     string `json:"label,omitempty"`
	SourceURL string `json:"source_url,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

type StepState struct {
	ID               string         `json:"id"`
	Goal             string         `json:"goal,omitempty"`
	Tool             string         `json:"tool"`
	Status           string         `json:"status"`
	AttemptCount     int            `json:"attempt_count"`
	DependsOn        []string       `json:"depends_on,omitempty"`
	ResultOK         bool           `json:"result_ok"`
	ResultError      string         `json:"result_error,omitempty"`
	ResultSummary    string         `json:"result_summary,omitempty"`
	Evidence         map[string]any `json:"evidence,omitempty"`
	AcceptanceStatus string         `json:"acceptance_status,omitempty"`
	AcceptanceReason string         `json:"acceptance_reason,omitempty"`
	StartedAt        time.Time      `json:"started_at,omitempty"`
	FinishedAt       time.Time      `json:"finished_at,omitempty"`
	UpdatedAt        time.Time      `json:"updated_at,omitempty"`
}

type TaskState struct {
	ID                   string               `json:"id"`
	TraceID              string               `json:"trace_id"`
	ParentTaskID         string               `json:"parent_task_id,omitempty"`
	ContinuationOfTaskID string               `json:"continuation_of_task_id,omitempty"`
	Topic                string               `json:"topic"`
	UserText             string               `json:"user_text"`
	ResolvedQuery        string               `json:"resolved_query"`
	PlanSummary          string               `json:"plan_summary"`
	ToolNames            []string             `json:"tool_names"`
	SelectedSkills       []string             `json:"selected_skills,omitempty"`
	Status               string               `json:"status"`
	Failed               bool                 `json:"failed"`
	ResultCount          int                  `json:"result_count"`
	ReplyPreview         string               `json:"reply_preview"`
	LastAnswer           string               `json:"last_answer,omitempty"`
	PendingFields        map[string]string    `json:"pending_fields,omitempty"`
	PendingQuestions     []string             `json:"pending_questions,omitempty"`
	PendingApproval      *PendingApproval     `json:"pending_approval,omitempty"`
	ExecutionStatus      string               `json:"execution_status,omitempty"`
	StepOrder            []string             `json:"step_order,omitempty"`
	StepStates           map[string]StepState `json:"step_states,omitempty"`
	Artifacts            []Artifact           `json:"artifacts,omitempty"`
	StartedAt            time.Time            `json:"started_at"`
	FinishedAt           time.Time            `json:"finished_at"`
	UpdatedAt            time.Time            `json:"updated_at"`
}

func (t TaskState) AwaitConfirm() bool {
	return t.Status == TaskAwaitConfirm
}

func (t TaskState) AwaitUserInput() bool {
	return t.Status == TaskAwaitUserInput
}

func (t TaskState) IsOpenLike() bool {
	switch t.Status {
	case TaskOpen, TaskAwaitConfirm, TaskAwaitUserInput:
		return true
	default:
		return false
	}
}

type State struct {
	SessionKey   string               `json:"session_key"`
	Channel      string               `json:"channel"`
	UserID       string               `json:"user_id"`
	ThreadID     string               `json:"thread_id"`
	TurnCount    int                  `json:"turn_count"`
	ActiveTaskID string               `json:"active_task_id,omitempty"`
	TaskOrder    []string             `json:"task_order,omitempty"`
	Tasks        map[string]TaskState `json:"tasks,omitempty"`
	CreatedAt    time.Time            `json:"created_at"`
	UpdatedAt    time.Time            `json:"updated_at"`
	RecentTurns  []Turn               `json:"recent_turns"`

	// Backward-compat mirror for older code/tests and persisted state migration.
	LastTask *TaskState `json:"last_task,omitempty"`
}

type Store interface {
	Load(sessionKey string) (State, error)
	Save(state State) error
}

type FileStore struct {
	root string
	mu   sync.Mutex
}

func NewFileStore(root string) *FileStore {
	return &FileStore{root: root}
}

func (s *FileStore) Load(sessionKey string) (State, error) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return State{}, fmt.Errorf("session_key is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.pathFor(sessionKey)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{SessionKey: sessionKey, Tasks: map[string]TaskState{}}, nil
		}
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, err
	}
	if strings.TrimSpace(st.SessionKey) == "" {
		st.SessionKey = sessionKey
	}
	st = normalizeState(st)
	return st, nil
}

func (s *FileStore) Save(state State) error {
	if strings.TrimSpace(state.SessionKey) == "" {
		return fmt.Errorf("session_key is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return err
	}
	state = normalizeState(state)
	state.RecentTurns = trimTurns(state.RecentTurns, recentTurnLimit)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.pathFor(state.SessionKey), append(data, '\n'), 0o644)
}

type StateMeta struct {
	SessionKey string
	Channel    string
	UserID     string
	ThreadID   string
}

type AppendTaskInput struct {
	Task           TaskState
	AssistantReply string
	At             time.Time
	Activate       bool
}

func ApplyTask(existing State, meta StateMeta, in AppendTaskInput) State {
	now := in.At
	if now.IsZero() {
		now = time.Now()
	}
	st := normalizeState(existing)
	if st.CreatedAt.IsZero() {
		st.CreatedAt = now
	}
	st.SessionKey = strings.TrimSpace(meta.SessionKey)
	st.Channel = strings.TrimSpace(meta.Channel)
	st.UserID = strings.TrimSpace(meta.UserID)
	st.ThreadID = strings.TrimSpace(meta.ThreadID)
	st.UpdatedAt = now
	st.TurnCount++

	task := in.Task
	if strings.TrimSpace(task.ID) == "" {
		task.ID = strings.TrimSpace(task.TraceID)
	}
	task.UpdatedAt = now
	st.Tasks[task.ID] = task
	st.TaskOrder = upsertTaskOrder(st.TaskOrder, task.ID)
	if in.Activate {
		st.ActiveTaskID = task.ID
		if !task.IsOpenLike() {
			st.ActiveTaskID = ""
		}
	}
	if strings.TrimSpace(task.UserText) != "" {
		st.RecentTurns = append(st.RecentTurns, Turn{Role: "user", Text: strings.TrimSpace(task.UserText), At: firstNonZero(task.StartedAt, now)})
	}
	if strings.TrimSpace(in.AssistantReply) != "" {
		st.RecentTurns = append(st.RecentTurns, Turn{Role: "assistant", Text: strings.TrimSpace(in.AssistantReply), At: now})
	}
	st.RecentTurns = trimTurns(st.RecentTurns, recentTurnLimit)
	refreshLastTask(&st)
	return st
}

func AppendConversation(existing State, meta StateMeta, userText, assistantText string, at time.Time) State {
	now := at
	if now.IsZero() {
		now = time.Now()
	}
	st := normalizeState(existing)
	if st.CreatedAt.IsZero() {
		st.CreatedAt = now
	}
	st.SessionKey = strings.TrimSpace(meta.SessionKey)
	st.Channel = strings.TrimSpace(meta.Channel)
	st.UserID = strings.TrimSpace(meta.UserID)
	st.ThreadID = strings.TrimSpace(meta.ThreadID)
	st.UpdatedAt = now
	st.TurnCount++
	if strings.TrimSpace(userText) != "" {
		st.RecentTurns = append(st.RecentTurns, Turn{Role: "user", Text: strings.TrimSpace(userText), At: now})
	}
	if strings.TrimSpace(assistantText) != "" {
		st.RecentTurns = append(st.RecentTurns, Turn{Role: "assistant", Text: strings.TrimSpace(assistantText), At: now})
	}
	st.RecentTurns = trimTurns(st.RecentTurns, recentTurnLimit)
	refreshLastTask(&st)
	return st
}

func ActiveTask(st State) *TaskState {
	if strings.TrimSpace(st.ActiveTaskID) == "" {
		return nil
	}
	task, ok := st.Tasks[st.ActiveTaskID]
	if !ok {
		return nil
	}
	copy := task
	return &copy
}

func OpenTasks(st State) []TaskState {
	out := make([]TaskState, 0, len(st.Tasks))
	for _, id := range st.TaskOrder {
		task, ok := st.Tasks[id]
		if ok && task.IsOpenLike() {
			out = append(out, task)
		}
	}
	return out
}

func HistoricalTasks(st State, limit int) []TaskState {
	ordered := orderedTasks(st)
	if limit > 0 && len(ordered) > limit {
		ordered = ordered[len(ordered)-limit:]
	}
	return ordered
}

func normalizeState(st State) State {
	if st.Tasks == nil {
		st.Tasks = map[string]TaskState{}
	}
	if st.LastTask != nil && strings.TrimSpace(st.LastTask.ID) != "" {
		if _, ok := st.Tasks[st.LastTask.ID]; !ok {
			st.Tasks[st.LastTask.ID] = *st.LastTask
			st.TaskOrder = upsertTaskOrder(st.TaskOrder, st.LastTask.ID)
		}
		if strings.TrimSpace(st.ActiveTaskID) == "" && st.LastTask.IsOpenLike() {
			st.ActiveTaskID = st.LastTask.ID
		}
	}
	for id, task := range st.Tasks {
		if strings.TrimSpace(task.ID) == "" {
			task.ID = id
		}
		if strings.TrimSpace(task.Status) == "" {
			task.Status = TaskCompleted
		}
		st.Tasks[id] = task
		st.TaskOrder = upsertTaskOrder(st.TaskOrder, id)
	}
	if strings.TrimSpace(st.ActiveTaskID) != "" {
		if _, ok := st.Tasks[st.ActiveTaskID]; !ok {
			st.ActiveTaskID = ""
		}
	}
	if strings.TrimSpace(st.ActiveTaskID) == "" {
		for i := len(st.TaskOrder) - 1; i >= 0; i-- {
			task, ok := st.Tasks[st.TaskOrder[i]]
			if ok && task.IsOpenLike() {
				st.ActiveTaskID = st.TaskOrder[i]
				break
			}
		}
	}
	st.TaskOrder = dedupeStrings(st.TaskOrder)
	refreshLastTask(&st)
	return st
}

func refreshLastTask(st *State) {
	if st == nil {
		return
	}
	if task := ActiveTask(*st); task != nil {
		st.LastTask = task
		return
	}
	ordered := orderedTasks(*st)
	if len(ordered) == 0 {
		st.LastTask = nil
		return
	}
	last := ordered[len(ordered)-1]
	st.LastTask = &last
}

func orderedTasks(st State) []TaskState {
	out := make([]TaskState, 0, len(st.TaskOrder))
	for _, id := range st.TaskOrder {
		task, ok := st.Tasks[id]
		if ok {
			out = append(out, task)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].UpdatedAt.Before(out[j].UpdatedAt)
	})
	return out
}

func trimTurns(turns []Turn, limit int) []Turn {
	if limit <= 0 || len(turns) <= limit {
		return turns
	}
	out := make([]Turn, limit)
	copy(out, turns[len(turns)-limit:])
	return out
}

func upsertTaskOrder(order []string, id string) []string {
	id = strings.TrimSpace(id)
	if id == "" {
		return order
	}
	filtered := make([]string, 0, len(order)+1)
	for _, existing := range order {
		if existing != id {
			filtered = append(filtered, existing)
		}
	}
	return append(filtered, id)
}

func dedupeStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func firstNonZero(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func (s *FileStore) pathFor(sessionKey string) string {
	filename := base64.RawURLEncoding.EncodeToString([]byte(sessionKey)) + ".json"
	return filepath.Join(s.root, filename)
}
