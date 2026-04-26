package harness

import (
	"context"

	"github.com/cloudwego/eino/adk"
	einoskill "github.com/cloudwego/eino/adk/middlewares/skill"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"

	"github.com/dongping/mateway/internal/capabilities"
	"github.com/dongping/mateway/internal/tools"
)

func (h *Harness) newSkillForkAgent(ctx context.Context, req Request, run Run, name string, opts *einoskill.AgentHubOptions) (adk.Agent, error) {
	agentName := firstNonEmpty(name, run.AgentName, req.AgentName, "default")
	profile, err := h.loadAgentProfile(agentName)
	if err != nil {
		return nil, err
	}
	effective, err := h.compileCapabilities(ctx, tools.Scope{
		UserID:    req.UserID,
		Channel:   req.Channel,
		ThreadID:  firstNonEmpty(req.ThreadID, req.SessionKey, run.ThreadID),
		AgentName: agentName,
	})
	if err != nil {
		return nil, err
	}
	effective = capabilities.Narrow(run.Capabilities, effective)
	forkReq := req
	forkReq.AgentName = agentName
	forkRun := run
	forkRun.AgentName = agentName
	forkRun.Capabilities = effective

	toolPlan, err := h.buildEinoToolPlan(ctx, forkReq, forkRun, profile, effective)
	if err != nil {
		return nil, err
	}
	model := optsModel(opts)
	var modelOpts []einomodel.Option
	if model == nil {
		model, modelOpts, err = h.newNamedEinoModelWithOptions(ctx, "")
		if err != nil {
			return nil, err
		}
	}
	instruction := h.buildAgentInstruction(profile, firstNonEmpty(run.Goal, req.UserText), effective.VisibleSkills, nil, true, nil, false, toolNamesFromPlan(toolPlan))
	handlers, err := h.buildEinoChatMiddlewares(ctx, forkRun, model, modelOpts, toolPlan, nil)
	if err != nil {
		return nil, err
	}
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          profile.Name,
		Description:   firstNonEmpty(profile.Description, profile.Name+" skill worker"),
		Instruction:   instruction,
		Model:         model,
		MaxIterations: 12,
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

func optsModel(opts *einoskill.AgentHubOptions) einomodel.ToolCallingChatModel {
	if opts == nil || opts.Model == nil {
		return nil
	}
	return opts.Model
}
