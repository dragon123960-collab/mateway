package harness

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	openaiext "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	planexecute "github.com/cloudwego/eino/adk/prebuilt/planexecute"
	"github.com/cloudwego/eino/adk/prebuilt/supervisor"
	einocallbacks "github.com/cloudwego/eino/callbacks"
	einomodel "github.com/cloudwego/eino/components/model"
	einomodelcallback "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	einotoolcallback "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	cbtemplate "github.com/cloudwego/eino/utils/callbacks"
	jsonschema "github.com/eino-contrib/jsonschema"

	"github.com/dongping/mateway/internal/agents"
	"github.com/dongping/mateway/internal/capabilities"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/llm"
	"github.com/dongping/mateway/internal/prompt"
	"github.com/dongping/mateway/internal/skills"
	"github.com/dongping/mateway/internal/tools"
)

type approvalResumeData struct {
	Approved bool `json:"approved"`
}

type tracePlan struct {
	Steps []string `json:"steps"`
}

func (p *tracePlan) FirstStep() string {
	if len(p.Steps) == 0 {
		return ""
	}
	return p.Steps[0]
}

func (p *tracePlan) MarshalJSON() ([]byte, error) {
	type alias tracePlan
	return json.Marshal((*alias)(p))
}

func (p *tracePlan) UnmarshalJSON(data []byte) error {
	type alias tracePlan
	return json.Unmarshal(data, (*alias)(p))
}

type approvalInterruptInfo struct {
	ApprovalID string         `json:"approval_id"`
	RunID      string         `json:"run_id"`
	ToolName   string         `json:"tool_name"`
	Arguments  map[string]any `json:"arguments,omitempty"`
}

func init() {
	gob.Register(approvalInterruptInfo{})
	gob.Register(approvalResumeData{})
	gob.Register(tracePlan{})
	gob.Register(map[string]any{})
	gob.Register([]any{})
}

type einoCheckpointStore struct {
	workspace string
}

func (s einoCheckpointStore) Get(_ context.Context, checkPointID string) ([]byte, bool, error) {
	path := s.path(checkPointID)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (s einoCheckpointStore) Set(_ context.Context, checkPointID string, checkPoint []byte) error {
	if err := os.MkdirAll(filepath.Dir(s.path(checkPointID)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(checkPointID), checkPoint, 0o644)
}

func (s einoCheckpointStore) path(checkPointID string) string {
	return filepath.Join(s.workspace, "memory", "eino_checkpoints", checkPointID+".bin")
}

type einoToolAdapter struct {
	harness *Harness
	run     Run
	req     Request
	spec    tools.Spec
	tool    tools.Tool
}

func (t *einoToolAdapter) Info(_ context.Context) (*schema.ToolInfo, error) {
	var params *schema.ParamsOneOf
	if len(t.spec.InputSchema) > 0 {
		var js jsonschema.Schema
		if err := json.Unmarshal(t.spec.InputSchema, &js); err == nil {
			params = schema.NewParamsOneOfByJSONSchema(&js)
		}
	}
	return &schema.ToolInfo{
		Name:        t.spec.Name,
		Desc:        strings.TrimSpace(firstNonEmpty(t.spec.Description, t.spec.Name)),
		ParamsOneOf: params,
	}, nil
}

func (t *einoToolAdapter) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...einotool.Option) (string, error) {
	t.harness.appendRunStep(t.run.ID, buildToolChoiceStep(t.run, t.req, t.spec, argumentsInJSON))
	if t.harness.requiresApprovalForTool(t.spec) {
		isResumeTarget, hasData, data := einotool.GetResumeContext[approvalResumeData](ctx)
		if !(isResumeTarget && hasData && data.Approved) {
			var arguments map[string]any
			_ = json.Unmarshal([]byte(argumentsInJSON), &arguments)
			pending := PendingApproval{
				ID:         fmt.Sprintf("approval_%d", time.Now().UnixNano()),
				RunID:      t.run.ID,
				SessionKey: t.req.SessionKey,
				AgentName:  t.run.AgentName,
				ToolName:   t.spec.Name,
				Arguments:  arguments,
				Mode:       "chat",
				CreatedAt:  time.Now(),
			}
			t.harness.savePendingApproval(pending)
			t.harness.markRunWaitingApproval(t.run.ID, pending, RunStep{
				Kind:       "tool",
				Status:     "waiting_approval",
				AgentName:  t.run.AgentName,
				ToolName:   t.spec.Name,
				Input:      trim(argumentsInJSON, 400),
				StartedAt:  time.Now(),
				FinishedAt: time.Now(),
			})
			return "", einotool.Interrupt(ctx, approvalInterruptInfo{
				ApprovalID: pending.ID,
				RunID:      t.run.ID,
				ToolName:   t.spec.Name,
				Arguments:  arguments,
			})
		}
	}

	startedAt := time.Now()
	res, err := t.tool.Invoke(ctx, tools.Call{
		RunID:      t.run.ID,
		StepID:     fmt.Sprintf("tool_%d", len(t.run.Steps)+1),
		SessionKey: t.req.SessionKey,
		ThreadID:   t.req.ThreadID,
		AgentName:  t.run.AgentName,
		ToolName:   t.spec.Name,
		Arguments:  json.RawMessage(argumentsInJSON),
	})
	output := stringifyToolResult(string(res.Output))
	if err != nil {
		t.harness.appendRunStep(t.run.ID, RunStep{
			Kind:       "tool",
			Status:     "failed",
			AgentName:  t.run.AgentName,
			ToolName:   t.spec.Name,
			Input:      trim(argumentsInJSON, 400),
			Output:     trim(err.Error(), 400),
			StartedAt:  startedAt,
			FinishedAt: time.Now(),
		})
		return "", err
	}
	t.harness.appendRunStep(t.run.ID, RunStep{
		Kind:       "tool",
		Status:     "completed",
		AgentName:  t.run.AgentName,
		ToolName:   t.spec.Name,
		Input:      trim(argumentsInJSON, 400),
		Output:     trim(output, 400),
		StartedAt:  startedAt,
		FinishedAt: time.Now(),
	})
	return output, nil
}

func buildToolChoiceStep(run Run, req Request, spec tools.Spec, argumentsInJSON string) RunStep {
	reason := buildToolChoiceReason(run, req, spec, argumentsInJSON)
	return RunStep{
		Kind:       "tool_choice",
		Status:     "completed",
		AgentName:  run.AgentName,
		ToolName:   spec.Name,
		Input:      trim(argumentsInJSON, 240),
		Output:     trim(reason, 400),
		StartedAt:  time.Now(),
		FinishedAt: time.Now(),
	}
}

func buildToolChoiceReason(run Run, req Request, spec tools.Spec, argumentsInJSON string) string {
	goal := strings.TrimSpace(firstNonEmpty(run.Goal, req.UserText))
	parts := []string{}
	if goal != "" {
		parts = append(parts, fmt.Sprintf("为推进任务目标“%s”", trimInline(goal, 100)))
	}
	switch spec.Name {
	case "web_search":
		parts = append(parts, "需要先获取外部最新信息，因此优先做网络检索。")
	case "browser_fetch":
		parts = append(parts, "已经有候选网页，需要继续读取正文而不是只看搜索摘要。")
	case "wiki_query", "read_memory", "read_session_summary", "recall_last_task", "search_history", "search_scoped_memory":
		parts = append(parts, "先查现有记忆和历史，避免重复调研或重复执行。")
	case "read_file", "list_files", "search_text":
		parts = append(parts, "当前更需要读取本地工作区上下文，而不是直接对外检索。")
	case "write_file", "write_memory_note", "wiki_ingest":
		parts = append(parts, "当前阶段重点是把结果沉淀下来，便于复用和后续追踪。")
	case "sandbox_exec":
		parts = append(parts, "需要用受控实验验证命令、脚本或最小执行假设。")
	case "spawn", "wait_agent":
		parts = append(parts, "任务需要子 agent 协作或等待并行结果。")
	case "schedule_create", "schedule_enable", "schedule_disable", "schedule_remove":
		parts = append(parts, "当前目标涉及后续自动执行或任务编排，因此需要调整定时任务状态。")
	case "schedule_list", "schedule_get":
		parts = append(parts, "需要先查看现有定时任务，避免重复创建或错误修改。")
	default:
		if len(spec.Tags) > 0 {
			parts = append(parts, fmt.Sprintf("它属于 %s 能力面，和当前步骤最匹配。", strings.Join(spec.Tags, "/")))
		} else {
			parts = append(parts, "它是当前可见工具里最匹配这一阶段需求的选择。")
		}
	}
	if strings.TrimSpace(argumentsInJSON) != "" && argumentsInJSON != "{}" {
		parts = append(parts, "本次输入参数是："+trimInline(argumentsInJSON, 120))
	}
	if len(run.VisibleTools) > 0 {
		parts = append(parts, "这是基于当前可见能力面做出的选择，而不是全量工具盲选。")
	}
	return strings.Join(parts, " ")
}

func (h *Harness) einoChat(ctx context.Context, req Request, run Run, history []HistoryMessage, userText string) (string, error) {
	return h.einoChatWithRoute(ctx, req, run, history, userText)
}

func (h *Harness) einoChatWithRoute(ctx context.Context, req Request, run Run, history []HistoryMessage, userText string) (string, error) {
	runner, modelOpts, err := h.newEinoRunner(ctx, req, run)
	if err != nil {
		return "", err
	}
	messages := make([]adk.Message, 0, len(history)+1)
	for _, item := range history {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(item.Role)) {
		case "system":
			messages = append(messages, schema.SystemMessage(content))
		case "assistant":
			messages = append(messages, schema.AssistantMessage(content, nil))
		case "tool":
			messages = append(messages, schema.ToolMessage(content, "historic-tool"))
		default:
			messages = append(messages, schema.UserMessage(content))
		}
	}
	messages = append(messages, schema.UserMessage(userText))

	iter := runner.Run(ctx, messages,
		adk.WithCheckPointID(run.ID),
		adk.WithCallbacks(h.einoTraceHandler(run)),
		adk.WithSessionValues(map[string]any{
			"Time":  time.Now().Format(time.RFC3339),
			"User":  req.UserID,
			"Agent": run.AgentName,
		}),
		adk.WithChatModelOptions(modelOpts),
	)
	result, err := h.consumeEinoEvents(iter, run, req)
	if err == nil {
		return result, nil
	}
	if strings.EqualFold(run.Route, "plan_execute") && isPlanExecuteToolChoiceIncompatible(err) {
		reason := trim(err.Error(), 320)
		h.appendRunStep(run.ID, RunStep{
			Kind:       "route_fallback",
			Status:     "completed",
			AgentName:  run.AgentName,
			Input:      "plan_execute -> chatmodel",
			Output:     fmt.Sprintf("当前模型不支持 plan_execute 所需的 forced tool_choice，已自动降级为 chatmodel 重试。原因：%s", reason),
			StartedAt:  time.Now(),
			FinishedAt: time.Now(),
		})
		run.Route = "chatmodel"
		run.UpdatedAt = time.Now()
		h.saveRun(run)
		return h.einoChatWithRoute(ctx, req, run, history, userText)
	}
	return "", err
}

func isPlanExecuteToolChoiceIncompatible(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "tool_choice") &&
		(strings.Contains(text, "thinking mode") || strings.Contains(text, "does not support being set to required") || strings.Contains(text, "does not support being set to required or object"))
}

func (h *Harness) resumeEinoRun(ctx context.Context, pending PendingApproval) (Run, error) {
	run, ok := h.GetRun(ctx, pending.RunID)
	if !ok {
		return Run{}, fmt.Errorf("run %q not found", pending.RunID)
	}
	req := Request{
		SessionKey: pending.SessionKey,
		ThreadID:   run.ThreadID,
		AgentName:  run.AgentName,
		Channel:    "feishu",
		Mode:       "chat",
	}
	runner, modelOpts, err := h.newEinoRunner(ctx, req, run)
	if err != nil {
		return Run{}, err
	}
	if strings.TrimSpace(pending.InterruptID) == "" {
		return Run{}, fmt.Errorf("approval %q is missing interrupt id", pending.ID)
	}
	iter, err := runner.ResumeWithParams(ctx, run.ID, &adk.ResumeParams{
		Targets: map[string]any{
			pending.InterruptID: approvalResumeData{Approved: true},
		},
	}, adk.WithChatModelOptions(modelOpts), adk.WithCallbacks(h.einoTraceHandler(run)))
	if err != nil {
		return Run{}, err
	}
	result, err := h.consumeEinoEvents(iter, run, req)
	if err != nil {
		return Run{}, err
	}
	run = h.mustGetRun(run.ID)
	run.Status = "completed"
	run.Result = strings.TrimSpace(result)
	run.UpdatedAt = time.Now()
	h.saveRun(run)
	_ = h.refreshSessionSummary(ctx, run)
	return run, nil
}

func (h *Harness) consumeEinoEvents(iter *adk.AsyncIterator[*adk.AgentEvent], run Run, req Request) (string, error) {
	lastAssistant := ""
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			return "", event.Err
		}
		if appendCustomizedActionStep(h, run, event) && (event.Output == nil || event.Output.MessageOutput == nil) {
			continue
		}
		if event.Action != nil && event.Action.TransferToAgent != nil {
			h.appendRunStep(run.ID, RunStep{
				Kind:       "transfer",
				Status:     "completed",
				AgentName:  event.AgentName,
				Output:     trim(event.Action.TransferToAgent.DestAgentName, 220),
				StartedAt:  time.Now(),
				FinishedAt: time.Now(),
			})
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			h.attachInterruptIDs(event.Action.Interrupted)
			current := h.mustGetRun(run.ID)
			if current.LastApprovalID != "" {
				pending, ok := h.pendingApprovalByID(current.LastApprovalID)
				if ok {
					return approvalMessage(pending), nil
				}
			}
			return "当前任务已中断，等待外部处理。", nil
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		msg, err := event.Output.MessageOutput.GetMessage()
		if err != nil || msg == nil {
			continue
		}
		switch msg.Role {
		case schema.Assistant:
			content := strings.TrimSpace(msg.Content)
			if content != "" {
				if response := extractResponseContent(content); response != "" {
					lastAssistant = response
					h.appendRunStep(run.ID, RunStep{
						Kind:       "respond",
						Status:     "completed",
						AgentName:  event.AgentName,
						Input:      trim(req.UserText, 220),
						Output:     trim(response, 400),
						StartedAt:  time.Now(),
						FinishedAt: time.Now(),
					})
					continue
				}
				if steps := extractPlanSteps(content); len(steps) > 0 {
					kind := "plan"
					if strings.EqualFold(event.AgentName, "replanner") {
						kind = "replan"
					}
					h.appendRunStep(run.ID, RunStep{
						Kind:       kind,
						Status:     "completed",
						AgentName:  event.AgentName,
						Input:      trim(req.UserText, 220),
						Output:     trim(strings.Join(steps, "\n"), 400),
						StartedAt:  time.Now(),
						FinishedAt: time.Now(),
					})
					continue
				}
				lastAssistant = content
				kind := "llm"
				if event.AgentName != "" && !strings.EqualFold(event.AgentName, run.AgentName) {
					kind = "agent_message"
				}
				h.appendRunStep(run.ID, RunStep{
					Kind:       kind,
					Status:     "completed",
					AgentName:  event.AgentName,
					Input:      trim(req.UserText, 220),
					Output:     trim(content, 400),
					StartedAt:  time.Now(),
					FinishedAt: time.Now(),
				})
			}
		case schema.Tool:
			if strings.TrimSpace(msg.Content) != "" {
				if selectedTools, ok := parseToolSearchSelection(msg.Content); ok {
					h.appendRunStep(run.ID, RunStep{
						Kind:       "tool_search",
						Status:     "completed",
						AgentName:  event.AgentName,
						ToolName:   firstNonEmpty(msg.ToolName, event.Output.MessageOutput.ToolName),
						Output:     trim(strings.Join(selectedTools, ", "), 320),
						StartedAt:  time.Now(),
						FinishedAt: time.Now(),
					})
					continue
				}
				if offloadPath := parsePersistedOutputPath(msg.Content); offloadPath != "" {
					h.appendRunStep(run.ID, RunStep{
						Kind:       "tool_offload",
						Status:     "completed",
						AgentName:  event.AgentName,
						ToolName:   firstNonEmpty(msg.ToolName, event.Output.MessageOutput.ToolName),
						Output:     trim(offloadPath, 320),
						StartedAt:  time.Now(),
						FinishedAt: time.Now(),
					})
					continue
				}
				h.appendRunStep(run.ID, RunStep{
					Kind:       "tool_result",
					Status:     "completed",
					AgentName:  event.AgentName,
					ToolName:   firstNonEmpty(msg.ToolName, event.Output.MessageOutput.ToolName),
					Output:     trim(msg.Content, 400),
					StartedAt:  time.Now(),
					FinishedAt: time.Now(),
				})
			}
		}
	}
	return lastAssistant, nil
}

func (h *Harness) einoTraceHandler(run Run) einocallbacks.Handler {
	return cbtemplate.NewHandlerHelper().
		Agent(&cbtemplate.AgentCallbackHandler{
			OnStart: func(ctx context.Context, info *einocallbacks.RunInfo, input *adk.AgentCallbackInput) context.Context {
				agentName := firstNonEmpty(info.Name, run.AgentName)
				mode := "run"
				inputCount := 0
				if input != nil {
					if input.ResumeInfo != nil {
						mode = "resume"
					}
					if input.Input != nil {
						inputCount = len(input.Input.Messages)
					}
				}
				h.appendRunStep(run.ID, RunStep{
					Kind:       "callback_agent_start",
					Status:     "started",
					AgentName:  agentName,
					Output:     trim(fmt.Sprintf("type=%s mode=%s input_messages=%d", info.Type, mode, inputCount), 240),
					StartedAt:  time.Now(),
					FinishedAt: time.Now(),
				})
				return context.WithValue(ctx, callbackAgentContextKey{}, agentName)
			},
			OnEnd: func(ctx context.Context, info *einocallbacks.RunInfo, output *adk.AgentCallbackOutput) context.Context {
				agentName := currentCallbackAgentName(ctx, firstNonEmpty(info.Name, run.AgentName))
				eventIter := output.Events
				go func() {
					h.appendRunStep(run.ID, RunStep{
						Kind:       "callback_agent_end",
						Status:     "completed",
						AgentName:  agentName,
						Output:     trim(fmt.Sprintf("type=%s %s", info.Type, summarizeAgentEvents(eventIter)), 320),
						StartedAt:  time.Now(),
						FinishedAt: time.Now(),
					})
				}()
				return ctx
			},
		}).
		ChatModel(&cbtemplate.ModelCallbackHandler{
			OnStart: func(ctx context.Context, info *einocallbacks.RunInfo, input *einomodelcallback.CallbackInput) context.Context {
				h.appendRunStep(run.ID, RunStep{
					Kind:       "callback_model_start",
					Status:     "started",
					AgentName:  currentCallbackAgentName(ctx, firstNonEmpty(info.Name, run.AgentName)),
					Input:      trim(callbackModelInputSummary(input), 240),
					Output:     trim(callbackModelConfigSummary(input), 240),
					StartedAt:  time.Now(),
					FinishedAt: time.Now(),
				})
				return ctx
			},
			OnEnd: func(ctx context.Context, info *einocallbacks.RunInfo, output *einomodelcallback.CallbackOutput) context.Context {
				h.appendRunStep(run.ID, RunStep{
					Kind:       "callback_model_end",
					Status:     "completed",
					AgentName:  currentCallbackAgentName(ctx, firstNonEmpty(info.Name, run.AgentName)),
					Output:     trim(callbackModelOutputSummary(output), 320),
					StartedAt:  time.Now(),
					FinishedAt: time.Now(),
				})
				return ctx
			},
			OnError: func(ctx context.Context, info *einocallbacks.RunInfo, err error) context.Context {
				h.appendRunStep(run.ID, RunStep{
					Kind:       "callback_model_error",
					Status:     "failed",
					AgentName:  currentCallbackAgentName(ctx, firstNonEmpty(info.Name, run.AgentName)),
					Output:     trim(err.Error(), 320),
					StartedAt:  time.Now(),
					FinishedAt: time.Now(),
				})
				return ctx
			},
		}).
		Tool(&cbtemplate.ToolCallbackHandler{
			OnStart: func(ctx context.Context, info *einocallbacks.RunInfo, input *einotoolcallback.CallbackInput) context.Context {
				h.appendRunStep(run.ID, RunStep{
					Kind:       "callback_tool_start",
					Status:     "started",
					AgentName:  currentCallbackAgentName(ctx, run.AgentName),
					ToolName:   firstNonEmpty(info.Name, run.ToolName),
					Input:      trim(firstNonEmpty(input.ArgumentsInJSON, "{}"), 240),
					StartedAt:  time.Now(),
					FinishedAt: time.Now(),
				})
				return ctx
			},
			OnEnd: func(ctx context.Context, info *einocallbacks.RunInfo, output *einotoolcallback.CallbackOutput) context.Context {
				h.appendRunStep(run.ID, RunStep{
					Kind:       "callback_tool_end",
					Status:     "completed",
					AgentName:  currentCallbackAgentName(ctx, run.AgentName),
					ToolName:   firstNonEmpty(info.Name, run.ToolName),
					Output:     trim(callbackToolOutputSummary(output), 320),
					StartedAt:  time.Now(),
					FinishedAt: time.Now(),
				})
				return ctx
			},
			OnError: func(ctx context.Context, info *einocallbacks.RunInfo, err error) context.Context {
				h.appendRunStep(run.ID, RunStep{
					Kind:       "callback_tool_error",
					Status:     "failed",
					AgentName:  currentCallbackAgentName(ctx, run.AgentName),
					ToolName:   firstNonEmpty(info.Name, run.ToolName),
					Output:     trim(err.Error(), 320),
					StartedAt:  time.Now(),
					FinishedAt: time.Now(),
				})
				return ctx
			},
		}).
		Handler()
}

func callbackModelInputSummary(input *einomodelcallback.CallbackInput) string {
	if input == nil {
		return ""
	}
	lastUser := ""
	for i := len(input.Messages) - 1; i >= 0; i-- {
		msg := input.Messages[i]
		if msg == nil {
			continue
		}
		if msg.Role == schema.User {
			lastUser = strings.TrimSpace(msg.Content)
			if lastUser != "" {
				break
			}
		}
	}
	if lastUser == "" {
		return fmt.Sprintf("messages=%d", len(input.Messages))
	}
	return fmt.Sprintf("messages=%d last_user=%s", len(input.Messages), lastUser)
}

func callbackModelConfigSummary(input *einomodelcallback.CallbackInput) string {
	if input == nil {
		return ""
	}
	parts := []string{}
	if input.Config != nil && strings.TrimSpace(input.Config.Model) != "" {
		parts = append(parts, "model="+input.Config.Model)
	}
	if len(input.Tools) > 0 {
		names := make([]string, 0, len(input.Tools))
		for _, item := range input.Tools {
			if item != nil && strings.TrimSpace(item.Name) != "" {
				names = append(names, item.Name)
			}
		}
		if len(names) > 0 {
			if len(names) > 8 {
				parts = append(parts, fmt.Sprintf("tool_count=%d", len(names)))
			} else {
				parts = append(parts, "tools="+strings.Join(names, ","))
			}
		}
	}
	return strings.Join(parts, " ")
}

func callbackModelOutputSummary(output *einomodelcallback.CallbackOutput) string {
	if output == nil {
		return ""
	}
	parts := []string{}
	if output.Message != nil && strings.TrimSpace(output.Message.Content) != "" {
		parts = append(parts, "message="+strings.TrimSpace(output.Message.Content))
	}
	if output.Message != nil && len(output.Message.ToolCalls) > 0 {
		parts = append(parts, fmt.Sprintf("tool_calls=%d", len(output.Message.ToolCalls)))
	}
	if output.TokenUsage != nil && output.TokenUsage.TotalTokens > 0 {
		parts = append(parts, fmt.Sprintf("tokens=%d", output.TokenUsage.TotalTokens))
	}
	return strings.Join(parts, " ")
}

func callbackToolOutputSummary(output *einotoolcallback.CallbackOutput) string {
	if output == nil {
		return ""
	}
	if strings.TrimSpace(output.Response) != "" {
		return output.Response
	}
	if output.ToolOutput != nil {
		data, _ := json.Marshal(output.ToolOutput)
		return string(data)
	}
	return ""
}

func stringifyToolResult(result string) string {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return `""`
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed
	}
	data, _ := json.Marshal(trimmed)
	return string(data)
}

func (h *Harness) attachInterruptIDs(info *adk.InterruptInfo) {
	if info == nil {
		return
	}
	for _, interruptCtx := range info.InterruptContexts {
		if interruptCtx == nil || !interruptCtx.IsRootCause {
			continue
		}
		payload, ok := interruptCtx.Info.(approvalInterruptInfo)
		if ok {
			h.updatePendingInterruptID(payload.ApprovalID, interruptCtx.ID)
			continue
		}
		if payloadMap, ok := interruptCtx.Info.(map[string]any); ok {
			approvalID := strings.TrimSpace(fmt.Sprint(payloadMap["approval_id"]))
			if approvalID != "" {
				h.updatePendingInterruptID(approvalID, interruptCtx.ID)
			}
		}
	}
}

func (h *Harness) updatePendingInterruptID(approvalID, interruptID string) {
	if strings.TrimSpace(approvalID) == "" || strings.TrimSpace(interruptID) == "" {
		return
	}
	h.approvalMu.Lock()
	defer h.approvalMu.Unlock()
	item, ok := h.pendingApprovalsByID[approvalID]
	if !ok {
		return
	}
	item.InterruptID = interruptID
	h.pendingApprovalsByID[approvalID] = item
	items := h.pendingApprovalsBySession[item.SessionKey]
	for i := range items {
		if items[i].ID == approvalID {
			items[i].InterruptID = interruptID
		}
	}
	h.pendingApprovalsBySession[item.SessionKey] = items
}

func (h *Harness) pendingApprovalByID(approvalID string) (PendingApproval, bool) {
	h.approvalMu.RLock()
	defer h.approvalMu.RUnlock()
	item, ok := h.pendingApprovalsByID[strings.TrimSpace(approvalID)]
	return item, ok
}

func approvalMessage(p PendingApproval) string {
	return fmt.Sprintf("工具 `%s` 需要批准，approval id: `%s`。发送 `/approve %s` 执行，`/deny %s` 拒绝，或 `/approvals` 查看全部待批。", p.ToolName, p.ID, p.ID, p.ID)
}

func (h *Harness) newEinoRunner(ctx context.Context, req Request, run Run) (*adk.Runner, []einomodel.Option, error) {
	rootAgent, subAgents, modelOpts, err := h.buildAgentTree(ctx, req, run)
	if err != nil {
		return nil, nil, err
	}
	var root adk.Agent = rootAgent
	if len(subAgents) > 0 {
		root, err = supervisor.New(ctx, &supervisor.Config{
			Supervisor: rootAgent,
			SubAgents:  subAgents,
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           root,
		EnableStreaming: false,
		CheckPointStore: einoCheckpointStore{workspace: h.Workspace},
	}), modelOpts, nil
}

func (h *Harness) buildAgentTree(ctx context.Context, req Request, run Run) (adk.Agent, []adk.Agent, []einomodel.Option, error) {
	if strings.EqualFold(firstNonEmpty(run.Route, h.selectEinoRoute(req)), "plan_execute") {
		agent, modelOpts, err := h.buildPlanExecuteAgent(ctx, req, run)
		if err != nil {
			return nil, nil, nil, err
		}
		return agent, nil, modelOpts, nil
	}
	model, modelOpts, err := h.newEinoModel(ctx, run.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	profile, err := h.loadAgentProfile(run.AgentName)
	if err != nil {
		return nil, nil, nil, err
	}
	rootToolPlan, err := h.buildEinoToolPlan(ctx, req, run, profile, run.Capabilities)
	if err != nil {
		return nil, nil, nil, err
	}
	rootSkillBundle, err := h.buildEinoSkillBundle(ctx, req, run, run.Capabilities.VisibleSkills, run.SelectedSkills)
	if err != nil {
		return nil, nil, nil, err
	}
	rootHandlers, err := h.buildEinoChatMiddlewares(ctx, run, model, modelOpts, rootToolPlan, rootSkillBundle)
	if err != nil {
		return nil, nil, nil, err
	}
	rootAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          profile.Name,
		Description:   firstNonEmpty(profile.Description, profile.Name+" assistant"),
		Instruction:   h.buildAgentInstruction(profile, firstNonEmpty(run.Goal, req.UserText), run.Capabilities.VisibleSkills, run.SelectedSkills, run.SkillPickerSource != "", rootSkillBundle, false, run.VisibleTools),
		Model:         model,
		MaxIterations: 20,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               rootToolPlan.BaseTools,
				ExecuteSequentially: true,
			},
			EmitInternalEvents: true,
		},
		Handlers: rootHandlers,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	profiles, err := agents.List(filepath.Join(h.Workspace, "agents"))
	if err != nil {
		return nil, nil, nil, err
	}
	subAgents := make([]adk.Agent, 0, len(profiles))
	for _, item := range profiles {
		if item.Name == profile.Name {
			continue
		}
		childProfile, err := h.loadAgentProfile(item.Name)
		if err != nil {
			continue
		}
		childCaps, err := h.compileCapabilities(ctx, tools.Scope{
			UserID:    req.UserID,
			Channel:   req.Channel,
			ThreadID:  firstNonEmpty(req.ThreadID, req.SessionKey),
			AgentName: childProfile.Name,
		})
		if err != nil {
			continue
		}
		childCaps = capabilities.Narrow(run.Capabilities, childCaps)
		childToolPlan, err := h.buildEinoToolPlan(ctx, req, run, childProfile, childCaps)
		if err != nil {
			continue
		}
		childSkillBundle, err := h.buildEinoSkillBundle(ctx, req, run, childCaps.VisibleSkills, run.SelectedSkills)
		if err != nil {
			continue
		}
		childModel, childModelOpts, err := h.newEinoModel(ctx, run.ID)
		if err != nil {
			continue
		}
		childHandlers, err := h.buildEinoChatMiddlewares(ctx, run, childModel, childModelOpts, childToolPlan, childSkillBundle)
		if err != nil {
			continue
		}
		childAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name:          childProfile.Name,
			Description:   firstNonEmpty(childProfile.Description, childProfile.Name+" worker"),
			Instruction:   h.buildAgentInstruction(childProfile, firstNonEmpty(run.Goal, req.UserText), childCaps.VisibleSkills, run.SelectedSkills, false, childSkillBundle, false, toolNamesFromPlan(childToolPlan)),
			Model:         childModel,
			MaxIterations: 12,
			ToolsConfig: adk.ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools:               childToolPlan.BaseTools,
					ExecuteSequentially: true,
				},
				EmitInternalEvents: true,
			},
			Handlers: childHandlers,
		})
		if err != nil {
			continue
		}
		subAgents = append(subAgents, childAgent)
	}
	return rootAgent, subAgents, modelOpts, nil
}

func (h *Harness) buildPlanExecuteAgent(ctx context.Context, req Request, run Run) (adk.Agent, []einomodel.Option, error) {
	if h.shouldAvoidPlanExecuteForConfiguredModel() {
		return nil, nil, fmt.Errorf("configured primary model does not safely support Eino plan_execute forced tool_choice")
	}
	model, modelOpts, err := h.newEinoModel(ctx, run.ID)
	if err != nil {
		return nil, nil, err
	}
	profile, err := h.loadAgentProfile(run.AgentName)
	if err != nil {
		return nil, nil, err
	}
	skillBundle, err := h.buildEinoSkillBundle(ctx, req, run, run.Capabilities.VisibleSkills, run.SelectedSkills)
	if err != nil {
		return nil, nil, err
	}
	instruction := h.buildAgentInstruction(profile, firstNonEmpty(run.Goal, req.UserText), run.Capabilities.VisibleSkills, run.SelectedSkills, run.SkillPickerSource != "", skillBundle, true, run.VisibleTools)
	planner, err := planexecute.NewPlanner(ctx, &planexecute.PlannerConfig{
		ToolCallingChatModel: model,
		GenInputFn:           h.planExecutePlannerInputFn(instruction),
		NewPlan: func(context.Context) planexecute.Plan {
			return &tracePlan{}
		},
	})
	if err != nil {
		return nil, nil, err
	}
	executor, err := h.buildPlanExecuteExecutor(ctx, req, run, profile, model, modelOpts, instruction, skillBundle)
	if err != nil {
		return nil, nil, err
	}
	replanner, err := planexecute.NewReplanner(ctx, &planexecute.ReplannerConfig{
		ChatModel:  model,
		GenInputFn: h.planExecuteReplannerInputFn(instruction),
		NewPlan: func(context.Context) planexecute.Plan {
			return &tracePlan{}
		},
	})
	if err != nil {
		return nil, nil, err
	}
	agent, err := planexecute.New(ctx, &planexecute.Config{
		Planner:       planner,
		Executor:      executor,
		Replanner:     replanner,
		MaxIterations: 8,
	})
	if err != nil {
		return nil, nil, err
	}
	return agent, modelOpts, nil
}

func (h *Harness) buildPlanExecuteExecutor(ctx context.Context, req Request, run Run, profile agents.Profile, model einomodel.ToolCallingChatModel, modelOpts []einomodel.Option, instruction string, skillBundle *einoSkillBundle) (adk.Agent, error) {
	toolPlan, err := h.buildEinoToolPlan(ctx, req, run, profile, run.Capabilities)
	if err != nil {
		return nil, err
	}
	handlers, err := h.buildEinoChatMiddlewares(ctx, run, model, modelOpts, toolPlan, skillBundle)
	if err != nil {
		return nil, err
	}
	genInputFn := h.planExecuteExecutorInputFn(instruction)
	genInput := func(ctx context.Context, _ string, _ *adk.AgentInput) ([]adk.Message, error) {
		planValue, ok := adk.GetSessionValue(ctx, planexecute.PlanSessionKey)
		if !ok {
			panic("impossible: plan not found")
		}
		userInputValue, ok := adk.GetSessionValue(ctx, planexecute.UserInputSessionKey)
		if !ok {
			panic("impossible: user input not found")
		}
		var executedSteps []planexecute.ExecutedStep
		if stepValue, ok := adk.GetSessionValue(ctx, planexecute.ExecutedStepsSessionKey); ok {
			executedSteps = stepValue.([]planexecute.ExecutedStep)
		}
		return genInputFn(ctx, &planexecute.ExecutionContext{
			UserInput:     userInputValue.([]adk.Message),
			Plan:          planValue.(planexecute.Plan),
			ExecutedSteps: executedSteps,
		})
	}
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "executor",
		Description:   "an executor agent",
		Model:         model,
		GenModelInput: genInput,
		MaxIterations: 20,
		OutputKey:     planexecute.ExecutedStepSessionKey,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               toolPlan.BaseTools,
				ExecuteSequentially: true,
			},
			EmitInternalEvents: true,
		},
		Handlers: handlers,
	})
}

func (h *Harness) shouldAvoidPlanExecuteForConfiguredModel() bool {
	candidates := h.Config.CandidateModels()
	if len(candidates) == 0 {
		return false
	}
	return modelLikelyRejectsForcedToolChoice(candidates[0].Model)
}

func modelLikelyRejectsForcedToolChoice(modelName string) bool {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if name == "" {
		return false
	}
	return strings.Contains(name, "qwen3")
}

func (h *Harness) buildAgentInstruction(profile agents.Profile, goal string, visibleSkillNames []string, selectedSkillNames []string, useSelectedSkills bool, skillBundle *einoSkillBundle, includeSkillInstruction bool, visibleToolNames []string) string {
	visibleSkills := h.visibleSkillCatalog(visibleSkillNames)
	systemPrompt := h.Config.Models.SystemPrompt
	useSkillActivation := skillBundle != nil && skillBundle.tool != nil
	if useSkillActivation && includeSkillInstruction && strings.TrimSpace(skillBundle.instruction) != "" {
		if strings.TrimSpace(systemPrompt) == "" {
			systemPrompt = skillBundle.instruction
		} else {
			systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + skillBundle.instruction)
		}
	}
	if cliHint := buildCLIExplorationHint(goal, visibleToolNames); strings.TrimSpace(cliHint) != "" {
		if strings.TrimSpace(systemPrompt) == "" {
			systemPrompt = cliHint
		} else {
			systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + cliHint)
		}
	}
	base := prompt.Assembler{
		Workspace:          h.Workspace,
		SystemPrompt:       systemPrompt,
		Goal:               goal,
		Skills:             visibleSkills,
		SelectedSkills:     skills.FilterVisible(visibleSkills, selectedSkillNames),
		UseSelectedSkills:  useSelectedSkills,
		UseSkillActivation: useSkillActivation,
		SkillToolName:      einoSkillToolName,
	}.Build()
	if strings.TrimSpace(profile.Prompt) == "" {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return strings.TrimSpace(profile.Prompt)
	}
	return strings.TrimSpace(base + "\n\n## AGENT_PROFILE\n" + strings.TrimSpace(profile.Prompt))
}

func toolNamesFromPlan(plan *einoToolPlan) []string {
	if plan == nil {
		return nil
	}
	names := append([]string(nil), plan.DynamicNames...)
	for _, tool := range plan.BaseTools {
		info, err := tool.Info(context.Background())
		if err == nil && info != nil && strings.TrimSpace(info.Name) != "" {
			names = appendUnique(names, info.Name)
		}
	}
	sort.Strings(names)
	return names
}

func (h *Harness) planExecutePlannerInputFn(instruction string) planexecute.GenPlannerModelInputFn {
	return func(ctx context.Context, userInput []adk.Message) ([]adk.Message, error) {
		msgs, err := planexecute.PlannerPrompt.Format(ctx, map[string]any{
			"input": userInput,
		})
		if err != nil {
			return nil, err
		}
		return prependPlanExecuteInstruction(instruction, msgs), nil
	}
}

func (h *Harness) planExecuteExecutorInputFn(instruction string) planexecute.GenModelInputFn {
	return func(ctx context.Context, in *planexecute.ExecutionContext) ([]adk.Message, error) {
		planContent, err := in.Plan.MarshalJSON()
		if err != nil {
			return nil, err
		}
		msgs, err := planexecute.ExecutorPrompt.Format(ctx, map[string]any{
			"input":          formatPlanExecuteInput(in.UserInput),
			"plan":           string(planContent),
			"executed_steps": formatPlanExecuteSteps(in.ExecutedSteps),
			"step":           in.Plan.FirstStep(),
		})
		if err != nil {
			return nil, err
		}
		return prependPlanExecuteInstruction(instruction, msgs), nil
	}
}

func (h *Harness) planExecuteReplannerInputFn(instruction string) planexecute.GenModelInputFn {
	return func(ctx context.Context, in *planexecute.ExecutionContext) ([]adk.Message, error) {
		planContent, err := in.Plan.MarshalJSON()
		if err != nil {
			return nil, err
		}
		msgs, err := planexecute.ReplannerPrompt.Format(ctx, map[string]any{
			"plan":           string(planContent),
			"input":          formatPlanExecuteInput(in.UserInput),
			"executed_steps": formatPlanExecuteSteps(in.ExecutedSteps),
			"plan_tool":      planexecute.PlanToolInfo.Name,
			"respond_tool":   planexecute.RespondToolInfo.Name,
		})
		if err != nil {
			return nil, err
		}
		return prependPlanExecuteInstruction(instruction, msgs), nil
	}
}

func prependPlanExecuteInstruction(instruction string, msgs []adk.Message) []adk.Message {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return msgs
	}
	out := make([]adk.Message, 0, len(msgs)+1)
	out = append(out, schema.SystemMessage(instruction))
	out = append(out, msgs...)
	return out
}

func formatPlanExecuteInput(input []adk.Message) string {
	var sb strings.Builder
	for _, msg := range input {
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

func formatPlanExecuteSteps(results []planexecute.ExecutedStep) string {
	var sb strings.Builder
	for _, result := range results {
		sb.WriteString(fmt.Sprintf("Step: %s\nResult: %s\n\n", result.Step, result.Result))
	}
	return sb.String()
}

func (h *Harness) einoToolsForAgent(ctx context.Context, req Request, run Run, profile agents.Profile, effective capabilities.Effective, includeSubagentTools bool) ([]einotool.BaseTool, error) {
	list, err := h.eligibleToolsForAgent(ctx, req, profile, effective)
	if err != nil {
		return nil, err
	}
	allowedByTask := progressiveToolDisclosure(firstNonEmpty(run.Goal, req.UserText), list)
	if len(allowedByTask) > 0 {
		filtered := make([]tools.Tool, 0, len(list))
		for _, item := range list {
			if allowedByTask[item.Spec().Name] {
				filtered = append(filtered, item)
			}
		}
		list = filtered
	}
	_ = includeSubagentTools
	return h.adaptEinoTools(run, req, list), nil
}

func (h *Harness) newEinoModel(ctx context.Context, runID string) (einomodel.ToolCallingChatModel, []einomodel.Option, error) {
	candidates := h.Config.CandidateModels()
	return h.newEinoModelFromCandidates(ctx, runID, candidates, true)
}

func (h *Harness) newNamedEinoModel(ctx context.Context, name string) (einomodel.ToolCallingChatModel, error) {
	model, _, err := h.newNamedEinoModelWithOptions(ctx, name)
	return model, err
}

func (h *Harness) newNamedEinoModelWithOptions(ctx context.Context, name string) (einomodel.ToolCallingChatModel, []einomodel.Option, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return h.newEinoModel(ctx, "")
	}
	modelCfg := h.Config.ModelByName(name)
	if modelCfg == nil {
		if base := h.Config.DefaultModel(); base != nil {
			derived := *base
			derived.Name = name
			derived.Model = name
			modelCfg = &derived
		}
	}
	if modelCfg == nil {
		return nil, nil, fmt.Errorf("model %q not found", name)
	}
	return h.newEinoModelFromCandidates(ctx, "", []config.ModelConfig{*modelCfg}, false)
}

func (h *Harness) newEinoModelFromCandidates(ctx context.Context, runID string, candidates []config.ModelConfig, updateRun bool) (einomodel.ToolCallingChatModel, []einomodel.Option, error) {
	if len(candidates) == 0 {
		return nil, nil, fmt.Errorf("no enabled model configured under config/models")
	}
	timeout := time.Duration(h.Config.Models.RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	var maxTokens *int
	if h.Config.Models.MaxTokens > 0 {
		maxTokens = &h.Config.Models.MaxTokens
	}
	temperature := float32(h.Config.Models.Temperature)
	out := make([]fallbackEinoCandidate, 0, len(candidates))
	for _, modelCfg := range candidates {
		if strings.TrimSpace(modelCfg.APIBase) == "" || strings.TrimSpace(modelCfg.Model) == "" {
			continue
		}
		chatModel, err := openaiext.NewChatModel(ctx, &openaiext.ChatModelConfig{
			APIKey:      strings.TrimSpace(modelCfg.APIKey),
			BaseURL:     strings.TrimSpace(modelCfg.APIBase),
			Model:       strings.TrimSpace(modelCfg.Model),
			Timeout:     timeout,
			MaxTokens:   maxTokens,
			Temperature: &temperature,
		})
		if err != nil {
			return nil, nil, err
		}
		var opts []einomodel.Option
		if len(modelCfg.Headers) > 0 {
			opts = append(opts, openaiext.WithExtraHeader(modelCfg.Headers))
		}
		out = append(out, fallbackEinoCandidate{
			name:  modelCfg.Name,
			model: chatModel,
			opts:  opts,
		})
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("no valid model configured under config/models")
	}
	var onSuccess func(string)
	if updateRun && strings.TrimSpace(runID) != "" {
		onSuccess = func(modelName string) {
			h.updateRunModel(runID, modelName)
		}
	}
	return &fallbackEinoModel{
		candidates: out,
		onSuccess:  onSuccess,
	}, nil, nil
}

type fallbackEinoCandidate struct {
	name  string
	model *openaiext.ChatModel
	opts  []einomodel.Option
}

type fallbackEinoModel struct {
	candidates []fallbackEinoCandidate
	boundTools []*schema.ToolInfo
	onSuccess  func(string)
}

func (m *fallbackEinoModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	var lastErr error
	attempted := make([]string, 0, len(m.candidates))
	for _, candidate := range m.candidates {
		attempted = append(attempted, candidate.name)
		msg, err := candidate.model.Generate(ctx, input, m.mergeOptions(candidate.opts, opts...)...)
		if err == nil {
			if m.onSuccess != nil {
				m.onSuccess(candidate.name)
			}
			return msg, nil
		}
		lastErr = err
		if !llm.ShouldFallbackModel(err) {
			return nil, err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no candidate model available")
	}
	return nil, fmt.Errorf("llm fallback exhausted after [%s]: %w", strings.Join(attempted, ", "), lastErr)
}

func (m *fallbackEinoModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	var lastErr error
	attempted := make([]string, 0, len(m.candidates))
	for _, candidate := range m.candidates {
		attempted = append(attempted, candidate.name)
		stream, err := candidate.model.Stream(ctx, input, m.mergeOptions(candidate.opts, opts...)...)
		if err == nil {
			if m.onSuccess != nil {
				m.onSuccess(candidate.name)
			}
			return stream, nil
		}
		lastErr = err
		if !llm.ShouldFallbackModel(err) {
			return nil, err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no candidate model available")
	}
	return nil, fmt.Errorf("llm fallback exhausted after [%s]: %w", strings.Join(attempted, ", "), lastErr)
}

func (m *fallbackEinoModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	cloned := &fallbackEinoModel{
		candidates: append([]fallbackEinoCandidate(nil), m.candidates...),
		boundTools: append([]*schema.ToolInfo(nil), tools...),
		onSuccess:  m.onSuccess,
	}
	return cloned, nil
}

func (m *fallbackEinoModel) mergeOptions(base []einomodel.Option, opts ...einomodel.Option) []einomodel.Option {
	out := make([]einomodel.Option, 0, len(base)+len(opts)+1)
	out = append(out, base...)
	if len(m.boundTools) > 0 {
		out = append(out, einomodel.WithTools(m.boundTools))
	}
	out = append(out, opts...)
	return out
}

func extractPlanSteps(content string) []string {
	var payload struct {
		Steps []string `json:"steps"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &payload) == nil && len(payload.Steps) > 0 {
		return payload.Steps
	}
	return nil
}

func extractResponseContent(content string) string {
	var payload struct {
		Response string `json:"response"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &payload) == nil {
		return strings.TrimSpace(payload.Response)
	}
	return ""
}
