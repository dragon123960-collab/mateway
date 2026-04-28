package scheduler

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	ScheduleKindInterval = "interval"
	ScheduleKindCron     = "cron"
	storeVersion         = 2

	TargetSessionCurrent  = "current"
	TargetSessionExplicit = "explicit"
	TargetSessionIsolated = "isolated"

	TargetAgentCurrent  = "current"
	TargetAgentExplicit = "explicit"
	TargetAgentDefault  = "default"
)

type Schedule struct {
	Kind            string `json:"kind,omitempty"`
	IntervalMinutes int    `json:"interval_minutes,omitempty"`
	Expr            string `json:"expr,omitempty"`
	TZ              string `json:"tz,omitempty"`
}

type Target struct {
	SessionMode string `json:"session_mode,omitempty"`
	SessionKey  string `json:"session_key,omitempty"`
	AgentMode   string `json:"agent_mode,omitempty"`
	AgentName   string `json:"agent_name,omitempty"`
}

type JobState struct {
	NextRunAt         time.Time `json:"next_run_at,omitempty"`
	LastRunAt         time.Time `json:"last_run_at,omitempty"`
	LastRunStatus     string    `json:"last_run_status,omitempty"`
	LastError         string    `json:"last_error,omitempty"`
	LastDurationMs    int64     `json:"last_duration_ms,omitempty"`
	ConsecutiveErrors int       `json:"consecutive_errors,omitempty"`
	LastRunID         string    `json:"last_run_id,omitempty"`
	LastTaskID        string    `json:"last_task_id,omitempty"`
}

type Job struct {
	ID         string         `json:"id,omitempty"`
	Name       string         `json:"name"`
	SessionKey string         `json:"session_key"`
	AgentName  string         `json:"agent_name,omitempty"`
	Mode       string         `json:"mode"`
	Prompt     string         `json:"prompt,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	Enabled    bool           `json:"enabled"`
	Schedule   Schedule       `json:"schedule"`
	Target     Target         `json:"target,omitempty"`
	State      JobState       `json:"state,omitempty"`
	CreatedAt  time.Time      `json:"created_at,omitempty"`
	UpdatedAt  time.Time      `json:"updated_at,omitempty"`
}

type runRecord struct {
	AtMs        int64  `json:"at_ms"`
	JobName     string `json:"job_name"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	DurationMs  int64  `json:"duration_ms,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	NextRunAtMs int64  `json:"next_run_at_ms,omitempty"`
}

type Store struct {
	Workspace string
}

type persistedStore struct {
	Version int   `json:"version"`
	Jobs    []Job `json:"jobs"`
}

type legacyJob struct {
	Name            string         `json:"name"`
	SessionKey      string         `json:"session_key"`
	AgentName       string         `json:"agent_name,omitempty"`
	Mode            string         `json:"mode"`
	Prompt          string         `json:"prompt,omitempty"`
	ToolName        string         `json:"tool_name,omitempty"`
	Arguments       map[string]any `json:"arguments,omitempty"`
	IntervalMinutes int            `json:"interval_minutes"`
	NextRunAt       time.Time      `json:"next_run_at"`
}

func (s Store) Save(job Job) error {
	_, _, err := s.Upsert(job)
	return err
}

func (s Store) SaveRuntimeState(job Job) error {
	now := time.Now()
	if err := job.Normalize(now); err != nil {
		return err
	}
	jobs, err := s.List()
	if err != nil {
		return err
	}
	for i := range jobs {
		if sameJob(jobs[i], job) {
			job.ID = jobs[i].ID
			job.CreatedAt = jobs[i].CreatedAt
			jobs[i] = job
			return s.writeAll(jobs)
		}
	}
	return fmt.Errorf("schedule %q not found", firstNonEmpty(job.Name, job.ID))
}

func (s Store) Upsert(job Job) (Job, string, error) {
	now := time.Now()
	if err := job.Normalize(now); err != nil {
		return Job{}, "", err
	}
	jobs, err := s.List()
	if err != nil {
		return Job{}, "", err
	}
	action := "create"
	for i := range jobs {
		if sameJob(jobs[i], job) {
			action = scheduleUpsertAction(jobs[i], job)
			job = mergeScheduleJob(jobs[i], job, now, action)
			jobs[i] = job
			if err := s.writeAll(jobs); err != nil {
				return Job{}, "", err
			}
			return job, action, nil
		}
	}
	jobs = append(jobs, job)
	if err := s.writeAll(jobs); err != nil {
		return Job{}, "", err
	}
	return job, action, nil
}

func (s Store) Get(name string) (Job, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Job{}, false, nil
	}
	jobs, err := s.List()
	if err != nil {
		return Job{}, false, err
	}
	for _, job := range jobs {
		if job.Name == name || (!job.CreatedAt.IsZero() && job.ID == name) || job.ID == name {
			return job, true, nil
		}
	}
	return Job{}, false, nil
}

func (s Store) List() ([]Job, error) {
	path := s.path()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var envelope persistedStore
	if err := json.Unmarshal(data, &envelope); err == nil && len(envelope.Jobs) >= 0 && envelope.Version > 0 {
		now := time.Now()
		for i := range envelope.Jobs {
			if err := envelope.Jobs[i].Normalize(now); err != nil {
				return nil, err
			}
		}
		sortJobs(envelope.Jobs)
		return envelope.Jobs, nil
	}

	var legacy []legacyJob
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(legacy))
	now := time.Now()
	for _, item := range legacy {
		job := Job{
			ID:         buildJobID(item.Name, now),
			Name:       item.Name,
			SessionKey: item.SessionKey,
			AgentName:  item.AgentName,
			Mode:       item.Mode,
			Prompt:     item.Prompt,
			ToolName:   item.ToolName,
			Arguments:  item.Arguments,
			Enabled:    true,
			Schedule: Schedule{
				Kind:            ScheduleKindInterval,
				IntervalMinutes: item.IntervalMinutes,
			},
			State: JobState{
				NextRunAt: item.NextRunAt,
			},
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := job.Normalize(now); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	sortJobs(jobs)
	return jobs, nil
}

func (s Store) Due(now time.Time) ([]Job, error) {
	jobs, err := s.List()
	if err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(jobs))
	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		if !job.State.NextRunAt.After(now) {
			out = append(out, job)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].State.NextRunAt.Before(out[j].State.NextRunAt)
	})
	return out, nil
}

func (s Store) Enable(name string, enabled bool) (Job, error) {
	job, ok, err := s.Get(name)
	if err != nil {
		return Job{}, err
	}
	if !ok {
		return Job{}, fmt.Errorf("schedule %q not found", name)
	}
	job.Enabled = enabled
	job.UpdatedAt = time.Now()
	if enabled && job.State.NextRunAt.IsZero() {
		next, err := job.Schedule.NextRunAfter(time.Now())
		if err != nil {
			return Job{}, err
		}
		job.State.NextRunAt = next
	}
	return job, s.Save(job)
}

func (s Store) Remove(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("schedule name is required")
	}
	jobs, err := s.List()
	if err != nil {
		return err
	}
	filtered := make([]Job, 0, len(jobs))
	var removed *Job
	for i := range jobs {
		if jobs[i].Name == name || jobs[i].ID == name {
			job := jobs[i]
			removed = &job
			continue
		}
		filtered = append(filtered, jobs[i])
	}
	if removed == nil {
		return fmt.Errorf("schedule %q not found", name)
	}
	if err := s.writeAll(filtered); err != nil {
		return err
	}
	_ = os.Remove(s.runLogPath(*removed))
	return nil
}

func (s Store) AppendRun(job Job, status, runID, taskID string, duration time.Duration, runErr error) error {
	if strings.TrimSpace(job.Name) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.runLogPath(job)), 0o755); err != nil {
		return err
	}
	record := runRecord{
		AtMs:        time.Now().UnixMilli(),
		JobName:     job.Name,
		Status:      firstNonEmpty(strings.TrimSpace(status), "unknown"),
		Error:       trimInline(firstNonEmpty(errorString(runErr), job.State.LastError), 600),
		DurationMs:  duration.Milliseconds(),
		RunID:       strings.TrimSpace(runID),
		TaskID:      strings.TrimSpace(taskID),
		NextRunAtMs: job.State.NextRunAt.UnixMilli(),
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.runLogPath(job), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func (s Store) ReadRuns(name string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 20
	}
	job, ok, err := s.Get(name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("schedule %q not found", name)
	}
	f, err := os.Open(s.runLogPath(job))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines, nil
}

func (s Store) writeAll(jobs []Job) error {
	if err := os.MkdirAll(filepath.Dir(s.path()), 0o755); err != nil {
		return err
	}
	sortJobs(jobs)
	data, err := json.MarshalIndent(persistedStore{
		Version: storeVersion,
		Jobs:    jobs,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(), data, 0o644)
}

func (s Store) path() string {
	return filepath.Join(s.Workspace, "memory", "schedules", "jobs.json")
}

func (s Store) runLogPath(job Job) string {
	name := sanitizeScheduleName(firstNonEmpty(job.ID, job.Name))
	return filepath.Join(s.Workspace, "memory", "schedules", "runs", name+".jsonl")
}

func NewJob(name, sessionKey, prompt string, intervalMinutes int) (Job, error) {
	return NewIntervalJob(name, sessionKey, prompt, intervalMinutes)
}

func NewIntervalJob(name, sessionKey, prompt string, intervalMinutes int) (Job, error) {
	if strings.TrimSpace(name) == "" || intervalMinutes <= 0 {
		return Job{}, fmt.Errorf("name and positive interval are required")
	}
	now := time.Now()
	job := Job{
		ID:         buildJobID(name, now),
		Name:       strings.TrimSpace(name),
		SessionKey: strings.TrimSpace(sessionKey),
		Mode:       "chat",
		Prompt:     strings.TrimSpace(prompt),
		Enabled:    true,
		Schedule: Schedule{
			Kind:            ScheduleKindInterval,
			IntervalMinutes: intervalMinutes,
		},
		Target: Target{
			SessionMode: TargetSessionIsolated,
			SessionKey:  strings.TrimSpace(sessionKey),
			AgentMode:   TargetAgentDefault,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	return job, job.Normalize(now)
}

func NewCronJob(name, sessionKey, prompt, expr, tz string) (Job, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(expr) == "" {
		return Job{}, fmt.Errorf("name and cron expr are required")
	}
	now := time.Now()
	job := Job{
		ID:         buildJobID(name, now),
		Name:       strings.TrimSpace(name),
		SessionKey: strings.TrimSpace(sessionKey),
		Mode:       "chat",
		Prompt:     strings.TrimSpace(prompt),
		Enabled:    true,
		Schedule: Schedule{
			Kind: ScheduleKindCron,
			Expr: strings.TrimSpace(expr),
			TZ:   strings.TrimSpace(tz),
		},
		Target: Target{
			SessionMode: TargetSessionIsolated,
			SessionKey:  strings.TrimSpace(sessionKey),
			AgentMode:   TargetAgentDefault,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	return job, job.Normalize(now)
}

func (j *Job) Normalize(now time.Time) error {
	if strings.TrimSpace(j.Name) == "" {
		return fmt.Errorf("schedule name is required")
	}
	if strings.TrimSpace(j.Mode) == "" {
		j.Mode = "chat"
	}
	if strings.TrimSpace(j.ID) == "" {
		j.ID = buildJobID(j.Name, now)
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now
	}
	j.UpdatedAt = now
	if j.Schedule.Kind == "" {
		j.Schedule.Kind = ScheduleKindInterval
	}
	if err := j.Schedule.Validate(); err != nil {
		return err
	}
	resolvedSession, resolvedAgent, target, err := resolveStoredTarget(j.Name, j.SessionKey, j.AgentName, j.Target)
	if err != nil {
		return err
	}
	j.Target = target
	j.SessionKey = resolvedSession
	j.AgentName = resolvedAgent
	if !j.Enabled && j.State.NextRunAt.IsZero() {
		return nil
	}
	if j.State.NextRunAt.IsZero() {
		next, err := j.Schedule.NextRunAfter(now)
		if err != nil {
			return err
		}
		j.State.NextRunAt = next
	}
	return nil
}

func (j Job) Description() string {
	switch strings.ToLower(strings.TrimSpace(j.Schedule.Kind)) {
	case ScheduleKindCron:
		return fmt.Sprintf("cron=%q tz=%s target=%s", j.Schedule.Expr, firstNonEmpty(j.Schedule.TZ, time.Local.String()), j.TargetSummary())
	default:
		return fmt.Sprintf("every %d min target=%s", j.Schedule.IntervalMinutes, j.TargetSummary())
	}
}

func (j Job) TargetSummary() string {
	return fmt.Sprintf("session[%s:%s] agent[%s:%s]",
		firstNonEmpty(j.Target.SessionMode, TargetSessionExplicit),
		firstNonEmpty(j.SessionKey, j.Target.SessionKey, "-"),
		firstNonEmpty(j.Target.AgentMode, TargetAgentDefault),
		firstNonEmpty(j.AgentName, j.Target.AgentName, "default"),
	)
}

func (j Job) LastStatus() string {
	return firstNonEmpty(strings.TrimSpace(j.State.LastRunStatus), "never")
}

func (s Schedule) Validate() error {
	switch strings.ToLower(strings.TrimSpace(s.Kind)) {
	case ScheduleKindInterval:
		if s.IntervalMinutes <= 0 {
			return fmt.Errorf("interval schedule requires positive interval_minutes")
		}
		return nil
	case ScheduleKindCron:
		if strings.TrimSpace(s.Expr) == "" {
			return fmt.Errorf("cron schedule requires expr")
		}
		_, err := parseCronExpr(s.Expr)
		if err != nil {
			return err
		}
		_, err = s.Location()
		return err
	default:
		return fmt.Errorf("unsupported schedule kind %q", s.Kind)
	}
}

func (s Schedule) NextRunAfter(base time.Time) (time.Time, error) {
	switch strings.ToLower(strings.TrimSpace(s.Kind)) {
	case ScheduleKindInterval:
		return base.Add(time.Duration(s.IntervalMinutes) * time.Minute), nil
	case ScheduleKindCron:
		expr, err := parseCronExpr(s.Expr)
		if err != nil {
			return time.Time{}, err
		}
		loc, err := s.Location()
		if err != nil {
			return time.Time{}, err
		}
		return expr.NextAfter(base, loc), nil
	default:
		return time.Time{}, fmt.Errorf("unsupported schedule kind %q", s.Kind)
	}
}

func (s Schedule) Location() (*time.Location, error) {
	tz := strings.TrimSpace(s.TZ)
	if tz == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", tz, err)
	}
	return loc, nil
}

func sameJob(left, right Job) bool {
	if left.Name != "" && right.Name != "" && left.Name == right.Name {
		return true
	}
	return left.ID != "" && right.ID != "" && left.ID == right.ID
}

func scheduleUpsertAction(existing, desired Job) string {
	if jobsEquivalent(existing, desired) {
		return "noop"
	}
	return "update"
}

func mergeScheduleJob(existing, desired Job, now time.Time, action string) Job {
	desired.ID = existing.ID
	desired.CreatedAt = existing.CreatedAt
	desired.State.LastRunAt = existing.State.LastRunAt
	desired.State.LastRunStatus = existing.State.LastRunStatus
	desired.State.LastError = existing.State.LastError
	desired.State.LastDurationMs = existing.State.LastDurationMs
	desired.State.ConsecutiveErrors = existing.State.ConsecutiveErrors
	desired.State.LastRunID = existing.State.LastRunID
	if action == "noop" {
		desired.State.NextRunAt = existing.State.NextRunAt
		return desired
	}
	if schedulesEquivalent(existing.Schedule, desired.Schedule) && existing.Enabled == desired.Enabled {
		desired.State.NextRunAt = existing.State.NextRunAt
	} else {
		next, err := desired.Schedule.NextRunAfter(now)
		if err == nil {
			desired.State.NextRunAt = next
		} else {
			desired.State.NextRunAt = existing.State.NextRunAt
		}
	}
	return desired
}

func jobsEquivalent(left, right Job) bool {
	return left.Name == right.Name &&
		left.SessionKey == right.SessionKey &&
		left.AgentName == right.AgentName &&
		left.Mode == right.Mode &&
		left.Prompt == right.Prompt &&
		left.ToolName == right.ToolName &&
		left.Enabled == right.Enabled &&
		schedulesEquivalent(left.Schedule, right.Schedule) &&
		targetsEquivalent(left.Target, right.Target) &&
		jsonMapsEqual(left.Arguments, right.Arguments)
}

func schedulesEquivalent(left, right Schedule) bool {
	return left.Kind == right.Kind &&
		left.IntervalMinutes == right.IntervalMinutes &&
		left.Expr == right.Expr &&
		left.TZ == right.TZ
}

func targetsEquivalent(left, right Target) bool {
	return left.SessionMode == right.SessionMode &&
		left.SessionKey == right.SessionKey &&
		left.AgentMode == right.AgentMode &&
		left.AgentName == right.AgentName
}

func jsonMapsEqual(left, right map[string]any) bool {
	if len(left) != len(right) {
		return false
	}
	leftData, _ := json.Marshal(left)
	rightData, _ := json.Marshal(right)
	return string(leftData) == string(rightData)
}

func sortJobs(jobs []Job) {
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
}

func buildJobID(name string, now time.Time) string {
	return fmt.Sprintf("job_%d_%s", now.UnixNano(), sanitizeScheduleName(name))
}

func sanitizeScheduleName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return "schedule"
	}
	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", ":", "-", ".", "-", "@", "-", "(", "", ")", "")
	name = replacer.Replace(name)
	name = strings.Trim(name, "-_")
	if name == "" {
		return "schedule"
	}
	return name
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func trimInline(value string, max int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if max > 0 && len(value) > max {
		return value[:max]
	}
	return value
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func resolveStoredTarget(name, sessionKey, agentName string, target Target) (string, string, Target, error) {
	name = strings.TrimSpace(name)
	target.SessionMode = strings.ToLower(strings.TrimSpace(target.SessionMode))
	target.AgentMode = strings.ToLower(strings.TrimSpace(target.AgentMode))
	if target.SessionMode == "" {
		switch {
		case strings.TrimSpace(target.SessionKey) != "":
			target.SessionMode = TargetSessionExplicit
		case strings.TrimSpace(sessionKey) == "" || strings.TrimSpace(sessionKey) == "schedule:"+name:
			target.SessionMode = TargetSessionIsolated
		default:
			target.SessionMode = TargetSessionExplicit
			target.SessionKey = strings.TrimSpace(sessionKey)
		}
	}
	if target.AgentMode == "" {
		switch {
		case strings.TrimSpace(target.AgentName) != "":
			target.AgentMode = TargetAgentExplicit
		case strings.TrimSpace(agentName) == "" || strings.TrimSpace(agentName) == "default":
			target.AgentMode = TargetAgentDefault
		default:
			target.AgentMode = TargetAgentExplicit
			target.AgentName = strings.TrimSpace(agentName)
		}
	}
	resolvedSession, resolvedAgent, normalized, err := ResolveTarget(name, strings.TrimSpace(sessionKey), strings.TrimSpace(agentName), target)
	if err != nil {
		return "", "", Target{}, err
	}
	if normalized.SessionMode == TargetSessionExplicit && strings.TrimSpace(normalized.SessionKey) == "" {
		normalized.SessionKey = strings.TrimSpace(sessionKey)
		resolvedSession = normalized.SessionKey
	}
	if normalized.AgentMode == TargetAgentExplicit && strings.TrimSpace(normalized.AgentName) == "" {
		normalized.AgentName = strings.TrimSpace(agentName)
		resolvedAgent = normalized.AgentName
	}
	if strings.TrimSpace(resolvedSession) == "" {
		resolvedSession = firstNonEmpty(strings.TrimSpace(sessionKey), "schedule:"+name)
	}
	if strings.TrimSpace(resolvedAgent) == "" {
		resolvedAgent = firstNonEmpty(strings.TrimSpace(agentName), "default")
	}
	return resolvedSession, resolvedAgent, normalized, nil
}

func ResolveTarget(name, currentSessionKey, currentAgentName string, target Target) (string, string, Target, error) {
	name = strings.TrimSpace(name)
	currentSessionKey = strings.TrimSpace(currentSessionKey)
	currentAgentName = strings.TrimSpace(currentAgentName)
	target.SessionMode = strings.ToLower(strings.TrimSpace(target.SessionMode))
	target.AgentMode = strings.ToLower(strings.TrimSpace(target.AgentMode))

	if target.SessionMode == "" {
		if currentSessionKey != "" {
			target.SessionMode = TargetSessionCurrent
		} else {
			target.SessionMode = TargetSessionIsolated
		}
	}
	if target.AgentMode == "" {
		if currentAgentName != "" {
			target.AgentMode = TargetAgentCurrent
		} else {
			target.AgentMode = TargetAgentDefault
		}
	}

	var resolvedSession string
	switch target.SessionMode {
	case TargetSessionCurrent:
		if currentSessionKey == "" {
			return "", "", Target{}, fmt.Errorf("current session target requires an active session")
		}
		resolvedSession = currentSessionKey
		target.SessionKey = currentSessionKey
	case TargetSessionExplicit:
		if strings.TrimSpace(target.SessionKey) == "" {
			return "", "", Target{}, fmt.Errorf("explicit session target requires session_key")
		}
		resolvedSession = strings.TrimSpace(target.SessionKey)
	case TargetSessionIsolated:
		resolvedSession = "schedule:" + name
		target.SessionKey = resolvedSession
	default:
		return "", "", Target{}, fmt.Errorf("unsupported target session mode %q", target.SessionMode)
	}

	var resolvedAgent string
	switch target.AgentMode {
	case TargetAgentCurrent:
		resolvedAgent = firstNonEmpty(currentAgentName, "default")
		target.AgentName = resolvedAgent
	case TargetAgentExplicit:
		if strings.TrimSpace(target.AgentName) == "" {
			return "", "", Target{}, fmt.Errorf("explicit agent target requires agent_name")
		}
		resolvedAgent = strings.TrimSpace(target.AgentName)
	case TargetAgentDefault:
		resolvedAgent = "default"
		target.AgentName = resolvedAgent
	default:
		return "", "", Target{}, fmt.Errorf("unsupported target agent mode %q", target.AgentMode)
	}

	return resolvedSession, resolvedAgent, target, nil
}
