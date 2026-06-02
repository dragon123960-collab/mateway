package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentprofile"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/observe"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/secret"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"
)

//go:embed static/*
var staticFiles embed.FS

type Server struct {
	Config  *config.Root
	Runtime runtime.Runtime
}

func Serve(ctx context.Context, cfg *config.Root, rt runtime.Runtime) error {
	if cfg == nil {
		return fmt.Errorf("web config is required")
	}
	bind := strings.TrimSpace(cfg.Web.Bind)
	if bind == "" {
		bind = "127.0.0.1:8765"
	}
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: NewServer(cfg, rt).Router()}
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		err := <-errCh
		if err == http.ErrServerClosed {
			return ctx.Err()
		}
		return err
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func NewServer(cfg *config.Root, rt runtime.Runtime) Server {
	if rt.Model == nil {
		rt = runtime.New(cfg)
	}
	return Server{Config: cfg, Runtime: rt}
}

func (s Server) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", s.handleChat)
	mux.HandleFunc("/api/overview", s.handleOverview)
	mux.HandleFunc("/api/skills/cleanup", s.handleSkillCleanup)
	mux.HandleFunc("/api/skills/", s.handleSkillAction)
	mux.HandleFunc("/api/skills", s.handleSkills)
	mux.HandleFunc("/api/schedules/", s.handleScheduleAction)
	mux.HandleFunc("/api/schedules", s.handleSchedules)
	mux.HandleFunc("/api/sessions/", s.handleSession)
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/api/channels/", s.handleChannelAction)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/channels", s.handleChannels)
	mux.HandleFunc("/api/agents", s.handleAgents)
	mux.HandleFunc("/api/memory/report", s.handleMemoryReport)
	mux.HandleFunc("/api/events/ws", s.handleEventsWebSocket)
	mux.HandleFunc("/api/runs/", s.handleRun)
	mux.HandleFunc("/api/runs", s.handleRuns)
	mux.HandleFunc("/watch", s.handleWatch)
	mux.HandleFunc("/", s.handleIndex)
	mux.Handle("/static/", http.FileServer(http.FS(staticFiles)))
	return withJSONErrors(mux)
}

func (s Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s Server) handleWatch(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/watch" {
		http.NotFound(w, r)
		return
	}
	if !s.Config.Web.OfficeWatchEnabled {
		writeError(w, http.StatusNotFound, "office watch is disabled")
		return
	}
	data, err := staticFiles.ReadFile("static/watch.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var input struct {
		Message    string `json:"message"`
		SessionKey string `json:"session_key"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	text := strings.TrimSpace(input.Message)
	if text == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	sessionKey := strings.TrimSpace(input.SessionKey)
	if sessionKey == "" {
		sessionKey = defaultWebSessionKey(s.Config)
	}
	msg := channel.InboundMessage{
		ID:         "web-" + time.Now().Format("20060102150405.000000000"),
		Channel:    "web",
		ThreadID:   sessionKey,
		UserID:     "local",
		SessionKey: sessionKey,
		Text:       text,
	}
	resp, err := s.Runtime.Handle(r.Context(), msg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_key": sessionKey,
		"reply":       resp.Reply,
		"followups":   resp.FollowUps,
		"trace_id":    resp.TraceID,
		"trace_path":  resp.TracePath,
		"failed":      resp.Failed,
	})
}

func (s Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	store := session.NewStore(s.Config.App.Home)
	keys, _ := store.List()
	totalTokens := 0
	totalRequests := 0
	for _, key := range keys {
		state, err := store.Load(key)
		if err != nil {
			continue
		}
		totalTokens += state.Usage.TotalTokens
		totalRequests += state.Usage.Requests
	}
	learning, _ := memory.BuildLearningReport(memory.LearningReportInput{Home: s.Config.App.Home, Workspace: s.Config.App.Workspace})
	writeJSON(w, http.StatusOK, map[string]any{
		"home":                 s.Config.App.Home,
		"workspace":            s.Config.App.Workspace,
		"model":                s.Config.Model.Default,
		"web_bind":             s.Config.Web.Bind,
		"feishu_enabled":       s.Config.Channels.Feishu.Enabled,
		"weixin_enabled":       s.Config.Channels.Weixin.Enabled,
		"sessions":             len(keys),
		"requests":             totalRequests,
		"total_tokens":         totalTokens,
		"learning_tasks":       learning.Tasks,
		"skill_usage":          learning.SkillUsage,
		"skill_issues":         learning.SkillIssues,
		"realtime_enabled":     s.Config.Web.RealtimeEnabled,
		"office_watch_enabled": s.Config.Web.OfficeWatchEnabled,
	})
}

func (s Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	items, err := skill.List(s.Config.App.Workspace)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cleanup, _ := skill.BuildCleanupReport(cleanupInput(s.Config))
	states := map[string]skill.CleanupItem{}
	for _, item := range cleanup.Items {
		states[item.Scope+":"+item.Name] = item
	}
	var out []map[string]any
	for _, item := range items {
		state := states[item.Scope+":"+item.Name]
		out = append(out, map[string]any{
			"id":           state.ID,
			"name":         item.Name,
			"description":  item.Description,
			"scope":        item.Scope,
			"stage":        item.Stage,
			"priority":     item.Priority,
			"path":         item.Path,
			"state":        defaultString(state.State, skill.StateActive),
			"usage_count":  state.UsageCount,
			"last_used_at": state.LastUsedAt,
			"reason":       state.Reason,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": out})
}

func (s Server) handleSkillCleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	report, err := skill.BuildCleanupReport(cleanupInput(s.Config))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s Server) handleSkillAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/restore") {
		methodNotAllowed(w)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/skills/"), "/restore")
	item, err := skill.Restore(cleanupInput(s.Config), id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = writeAudit(s.Config.App.Home, "skill_restore", map[string]any{"id": id, "name": item.Name})
	writeJSON(w, http.StatusOK, item)
}

func (s Server) handleSchedules(w http.ResponseWriter, r *http.Request) {
	store := schedule.Store{Home: s.Config.App.Home}
	switch r.Method {
	case http.MethodGet:
		tasks, err := store.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"schedules": tasks})
	case http.MethodPost:
		var input struct {
			Text        string `json:"text"`
			RunAt       string `json:"run_at"`
			Interval    string `json:"interval"`
			SessionKey  string `json:"session_key"`
			RequireTest *bool  `json:"require_test"`
			Activate    bool   `json:"activate"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		runAt, err := time.Parse(time.RFC3339, strings.TrimSpace(input.RunAt))
		if err != nil {
			writeError(w, http.StatusBadRequest, "run_at must be RFC3339")
			return
		}
		var interval time.Duration
		if strings.TrimSpace(input.Interval) != "" {
			interval, err = time.ParseDuration(strings.TrimSpace(input.Interval))
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		requireTest := true
		if input.RequireTest != nil {
			requireTest = *input.RequireTest
		}
		task, err := store.Create(schedule.CreateInput{
			SessionKey:  input.SessionKey,
			Text:        input.Text,
			RunAt:       runAt,
			Interval:    interval,
			RequireTest: requireTest,
			Activate:    input.Activate,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = writeAudit(s.Config.App.Home, "schedule_create", map[string]any{"id": task.ID})
		writeJSON(w, http.StatusCreated, task)
	default:
		methodNotAllowed(w)
	}
}

func (s Server) handleScheduleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		methodNotAllowed(w)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/schedules/"), "/")
	if len(parts) != 2 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "schedule id and action are required")
		return
	}
	store := schedule.Store{Home: s.Config.App.Home}
	var (
		task schedule.Task
		err  error
	)
	switch parts[1] {
	case "activate":
		task, err = store.Activate(parts[0])
	case "pause":
		task, err = store.Pause(parts[0])
	case "test":
		task, err = store.Read(parts[0])
		if err == nil {
			var record schedule.RunRecord
			record, err = s.runScheduledTask(r.Context(), store, task, "test")
			if err == nil {
				err = store.MarkTested(task, time.Now(), record)
			}
			if err == nil {
				_ = writeAudit(s.Config.App.Home, "schedule_test", map[string]any{"id": task.ID, "run": record.ID})
				writeJSON(w, http.StatusOK, map[string]any{"task": task, "run": record})
				return
			}
		}
	default:
		writeError(w, http.StatusBadRequest, "unsupported schedule action")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = writeAudit(s.Config.App.Home, "schedule_"+parts[1], map[string]any{"id": task.ID})
	writeJSON(w, http.StatusOK, task)
}

func (s Server) runScheduledTask(ctx context.Context, store schedule.Store, task schedule.Task, kind string) (schedule.RunRecord, error) {
	startedAt := time.Now()
	msg := channel.InboundMessage{
		ID:         task.ID,
		Channel:    "schedule",
		SessionKey: defaultString(task.SessionKey, "schedule:"+task.ID),
		Text:       task.Text,
		Metadata:   map[string]string{"scheduled_task_id": task.ID, "scheduled_run_kind": kind},
	}
	resp, err := s.Runtime.Handle(ctx, msg)
	status := "success"
	errText := ""
	output := ""
	tracePath := ""
	if err != nil {
		status = "error"
		errText = err.Error()
	} else {
		output = strings.TrimSpace(resp.Reply.Text)
		tracePath = resp.TracePath
		if resp.Failed {
			status = "error"
		}
	}
	record, recordErr := store.RecordRun(schedule.RunRecord{
		TaskID:     task.ID,
		Kind:       kind,
		Status:     status,
		StartedAt:  startedAt.Format(time.RFC3339),
		FinishedAt: time.Now().Format(time.RFC3339),
		SessionKey: msg.SessionKey,
		Output:     output,
		TracePath:  tracePath,
		Error:      errText,
	})
	if recordErr != nil {
		return record, recordErr
	}
	return record, err
}

func (s Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	store := session.NewStore(s.Config.App.Home)
	keys, err := store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var out []map[string]any
	out = []map[string]any{}
	for _, key := range keys {
		state, err := store.Load(key)
		if err != nil {
			continue
		}
		out = append(out, sessionSummary(state))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return fmt.Sprint(out[i]["updated_at"]) > fmt.Sprint(out[j]["updated_at"])
	})
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	key, err := urlPathValue(strings.TrimPrefix(r.URL.Path, "/api/sessions/"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if key == "" {
		writeError(w, http.StatusBadRequest, "session key is required")
		return
	}
	state, err := session.NewStore(s.Config.App.Home).Load(key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key":          state.Key,
		"messages":     state.Messages,
		"tasks":        state.Tasks,
		"active_task":  state.ActiveTask,
		"pending":      state.Pending,
		"usage":        state.Usage,
		"updated_at":   state.UpdatedAt,
		"latest_trace": latestTraceID(state.Tasks),
	})
}

func (s Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, redactConfig(s.Config))
	case http.MethodPatch:
		if !s.Config.Web.AllowConfigWrite {
			writeError(w, http.StatusForbidden, "config write is disabled")
			return
		}
		var patch map[string]any
		if !readJSON(w, r, &patch) {
			return
		}
		if err := rejectSecretPatch(patch); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := patchMainConfig(s.Config.App.Home, patch); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		applyConfigPatchToRuntime(s.Config, patch)
		_ = writeAudit(s.Config.App.Home, "config_patch", map[string]any{"keys": mapKeys(patch)})
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	default:
		methodNotAllowed(w)
	}
}

func (s Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channels": []map[string]any{
			{"id": "feishu", "enabled": s.Config.Channels.Feishu.Enabled, "bot_name": s.Config.Channels.Feishu.BotName, "config": "channels/feishu.yaml"},
			{"id": "weixin", "enabled": s.Config.Channels.Weixin.Enabled, "config": "channels/weixin.yaml"},
			{"id": "web", "enabled": s.Config.Web.EnabledValue(), "bind": s.Config.Web.Bind, "config": "config.yaml"},
		},
	})
}

func (s Server) handleChannelAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		methodNotAllowed(w)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/channels/"), "/")
	if len(parts) != 2 || parts[1] != "enabled" {
		writeError(w, http.StatusBadRequest, "usage: /api/channels/:id/enabled")
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if err := patchChannelEnabled(s.Config.App.Home, parts[0], input.Enabled); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch parts[0] {
	case "feishu":
		s.Config.Channels.Feishu.Enabled = input.Enabled
	case "weixin":
		s.Config.Channels.Weixin.Enabled = input.Enabled
	}
	_ = writeAudit(s.Config.App.Home, "channel_enabled", map[string]any{"id": parts[0], "enabled": input.Enabled})
	writeJSON(w, http.StatusOK, map[string]any{"id": parts[0], "enabled": input.Enabled})
}

func (s Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	manager := agentprofile.Manager{Config: s.Config}
	writeJSON(w, http.StatusOK, map[string]any{"agents": manager.List()})
}

func (s Server) handleMemoryReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	report, err := memory.BuildReport(memory.ReportInput{Home: s.Config.App.Home, MemoryRoot: memoryRoot(s.Config)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	learning, _ := memory.BuildLearningReport(memory.LearningReportInput{Home: s.Config.App.Home, Workspace: s.Config.App.Workspace})
	writeJSON(w, http.StatusOK, map[string]any{"memory": report, "learning": learning})
}

func (s Server) handleEventsWebSocket(w http.ResponseWriter, r *http.Request) {
	if !s.Config.Web.RealtimeEnabled {
		writeError(w, http.StatusNotFound, "realtime events are disabled")
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		host := strings.Split(r.Host, ":")[0]
		return host == "127.0.0.1" || host == "localhost" || host == "::1"
	}}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	sessionKey := strings.TrimSpace(r.URL.Query().Get("session_key"))
	id, ch := observe.Subscribe(sessionKey)
	defer observe.Unsubscribe(id)
	if err := conn.WriteJSON(observe.Event{Type: "connected", Time: time.Now().Format(time.RFC3339Nano), SessionKey: sessionKey}); err != nil {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			if err := conn.WriteJSON(event); err != nil {
				return
			}
		}
	}
}

func (s Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	sessionFilter := strings.TrimSpace(r.URL.Query().Get("session_key"))
	store := session.NewStore(s.Config.App.Home)
	keys, err := store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	runs := []map[string]any{}
	for _, key := range keys {
		if sessionFilter != "" && key != sessionFilter {
			continue
		}
		state, err := store.Load(key)
		if err != nil {
			continue
		}
		for _, task := range state.Tasks {
			if strings.TrimSpace(task.TraceID) == "" && strings.TrimSpace(task.TracePath) == "" {
				continue
			}
			runs = append(runs, map[string]any{
				"session_key": key,
				"task_id":     task.ID,
				"goal":        task.Goal,
				"summary":     task.Summary,
				"status":      task.Status,
				"trace_id":    task.TraceID,
				"trace_path":  task.TracePath,
				"created_at":  task.CreatedAt,
				"updated_at":  task.UpdatedAt,
			})
		}
	}
	sort.SliceStable(runs, func(i, j int) bool {
		return fmt.Sprint(runs[i]["updated_at"]) > fmt.Sprint(runs[j]["updated_at"])
	})
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	traceID, err := urlPathValue(strings.TrimPrefix(r.URL.Path, "/api/runs/"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tracePath := filepath.Join(s.Config.App.Home, "trace", traceID+".jsonl")
	if !strings.HasSuffix(traceID, ".jsonl") {
		tracePath = filepath.Join(s.Config.App.Home, "trace", traceID+".jsonl")
	}
	if strings.HasSuffix(traceID, ".jsonl") {
		tracePath = filepath.Join(s.Config.App.Home, "trace", filepath.Base(traceID))
	}
	events, err := readTraceEvents(tracePath)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	summary, _ := runtime.SummarizeTrace(tracePath)
	writeJSON(w, http.StatusOK, map[string]any{
		"trace_id":   strings.TrimSuffix(filepath.Base(tracePath), ".jsonl"),
		"trace_path": tracePath,
		"summary":    summary,
		"events":     events,
	})
}

func cleanupInput(cfg *config.Root) skill.CleanupInput {
	return skill.CleanupInput{Home: cfg.App.Home, Workspace: cfg.App.Workspace, Config: cfg.Skills.Cleanup}
}

func defaultWebSessionKey(cfg *config.Root) string {
	name := "default"
	if cfg != nil && strings.TrimSpace(cfg.App.Workspace) != "" {
		name = filepath.Base(strings.TrimSpace(cfg.App.Workspace))
	}
	return "web:" + name
}

func sessionSummary(state session.State) map[string]any {
	return map[string]any{
		"key":          state.Key,
		"messages":     len(state.Messages),
		"tasks":        len(state.Tasks),
		"active_task":  state.ActiveTask,
		"pending":      pendingKind(state.Pending),
		"usage":        state.Usage,
		"updated_at":   state.UpdatedAt,
		"last_summary": lastTaskSummary(state.Tasks),
		"latest_trace": latestTraceID(state.Tasks),
	}
}

func pendingKind(pending *session.PendingAction) string {
	if pending == nil {
		return ""
	}
	return pending.Kind
}

func lastTaskSummary(tasks []session.TaskNode) string {
	for i := len(tasks) - 1; i >= 0; i-- {
		if strings.TrimSpace(tasks[i].Summary) != "" {
			return tasks[i].Summary
		}
	}
	return ""
}

func latestTraceID(tasks []session.TaskNode) string {
	for i := len(tasks) - 1; i >= 0; i-- {
		if strings.TrimSpace(tasks[i].TraceID) != "" {
			return strings.TrimSpace(tasks[i].TraceID)
		}
		if strings.TrimSpace(tasks[i].TracePath) != "" {
			return strings.TrimSuffix(filepath.Base(tasks[i].TracePath), ".jsonl")
		}
	}
	return ""
}

func memoryRoot(cfg *config.Root) string {
	root := strings.TrimSpace(cfg.Memory.Root)
	if root != "" {
		return root
	}
	return filepath.Join(cfg.App.Workspace, "memory")
}

func redactConfig(cfg *config.Root) map[string]any {
	return map[string]any{
		"app": map[string]any{
			"name":      cfg.App.Name,
			"home":      cfg.App.Home,
			"workspace": cfg.App.Workspace,
			"locale":    cfg.App.Locale,
		},
		"model": map[string]any{
			"default":   cfg.Model.Default,
			"fallbacks": cfg.Model.Fallbacks,
			"roles":     cfg.Model.Roles,
		},
		"models":    modelSummaries(cfg.Models),
		"scheduler": cfg.Scheduler,
		"skills":    cfg.Skills,
		"web": map[string]any{
			"enabled":              cfg.Web.EnabledValue(),
			"bind":                 cfg.Web.Bind,
			"open_browser":         cfg.Web.OpenBrowser,
			"allow_config_write":   cfg.Web.AllowConfigWrite,
			"realtime_enabled":     cfg.Web.RealtimeEnabled,
			"office_watch_enabled": cfg.Web.OfficeWatchEnabled,
			"office_watch_assets":  cfg.Web.OfficeWatchAssets,
		},
		"channels": map[string]any{
			"feishu": map[string]any{"enabled": cfg.Channels.Feishu.Enabled, "app_id_env": cfg.Channels.Feishu.AppIDEnv, "app_secret_env": cfg.Channels.Feishu.AppSecretEnv, "bot_name": cfg.Channels.Feishu.BotName},
			"weixin": map[string]any{"enabled": cfg.Channels.Weixin.Enabled},
		},
	}
}

func modelSummaries(models []config.ModelConfig) []map[string]any {
	out := make([]map[string]any, 0, len(models))
	for _, model := range models {
		out = append(out, map[string]any{
			"name":           model.Name,
			"provider":       model.Provider,
			"model":          model.Model,
			"enabled":        model.Enabled,
			"modalities":     model.Modalities,
			"context_window": model.ContextWindow,
			"max_tokens":     model.MaxTokens,
			"description":    model.Description,
		})
	}
	return out
}

func applyConfigPatchToRuntime(cfg *config.Root, patch map[string]any) {
	if cfg == nil {
		return
	}
	if model, ok := patch["model"].(map[string]any); ok {
		if value, ok := model["default"].(string); ok {
			cfg.Model.Default = strings.TrimSpace(value)
		}
	}
	if web, ok := patch["web"].(map[string]any); ok {
		if value, ok := web["bind"].(string); ok {
			cfg.Web.Bind = strings.TrimSpace(value)
		}
		if value, ok := web["enabled"].(bool); ok {
			cfg.Web.Enabled = &value
		}
		if value, ok := web["allow_config_write"].(bool); ok {
			cfg.Web.AllowConfigWrite = value
		}
		if value, ok := web["realtime_enabled"].(bool); ok {
			cfg.Web.RealtimeEnabled = value
		}
		if value, ok := web["office_watch_enabled"].(bool); ok {
			cfg.Web.OfficeWatchEnabled = value
		}
		if value, ok := web["office_watch_assets"].(string); ok {
			cfg.Web.OfficeWatchAssets = strings.TrimSpace(value)
		}
	}
}

func rejectSecretPatch(patch map[string]any) error {
	if key := secretPatchKey(patch); key != "" {
		return fmt.Errorf("refusing to write secret-like config key %q from web patch", key)
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	return secret.RejectIfSecretLike(string(data), "web config patch")
}

func secretPatchKey(value any) string {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if isSecretConfigKey(key) {
				return key
			}
			if found := secretPatchKey(child); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range v {
			if found := secretPatchKey(child); found != "" {
				return found
			}
		}
	}
	return ""
}

func isSecretConfigKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	for _, marker := range []string{"password", "passwd", "secret", "token", "api_key", "apikey", "authorization", "smtp_pass", "imap_pass"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func patchMainConfig(home string, patch map[string]any) error {
	path := filepath.Join(home, "config", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	for _, key := range []string{"app", "model", "scheduler", "skills", "web"} {
		if value, ok := patch[key]; ok {
			root[key] = value
		}
	}
	out, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	var check config.Root
	if err := yaml.Unmarshal(out, &check); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func readTraceEvents(path string) ([]map[string]any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var events []map[string]any
	decoder := json.NewDecoder(file)
	for {
		var event map[string]any
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func patchChannelEnabled(home, id string, enabled bool) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if id != "feishu" && id != "weixin" {
		return fmt.Errorf("unsupported channel %q", id)
	}
	path := filepath.Join(home, "config", "channels", id+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	root["enabled"] = enabled
	out, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func urlPathValue(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	out, err := url.PathUnescape(value)
	if err != nil {
		return "", err
	}
	return out, nil
}

func writeAudit(home, typ string, fields map[string]any) error {
	payload := map[string]any{"type": typ, "time": time.Now().Format(time.RFC3339Nano)}
	for key, value := range fields {
		payload[key] = value
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	path := filepath.Join(home, "observe", "audit", "web.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	return err
}

func withJSONErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if err := json.Unmarshal(data, target); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
