package agentcore

import (
	"context"
	"fmt"
)

type Agent struct {
	SystemPrompt     string
	Messages         []Message
	Model            Model
	Tools            *ToolRegistry
	Hooks            Hooks
	MaxParallelTools int
}

func NewAgent(model Model, tools *ToolRegistry) *Agent {
	return &Agent{
		Model:            model,
		Tools:            tools,
		MaxParallelTools: 4,
	}
}

func (a *Agent) Prompt(ctx context.Context, messages ...Message) (Result, error) {
	if a.Model == nil {
		return Result{}, fmt.Errorf("agentcore model is required")
	}
	a.Messages = append(a.Messages, messages...)
	result, err := Run(ctx, Config{
		SystemPrompt:     a.SystemPrompt,
		Model:            a.Model,
		Tools:            a.Tools,
		MaxParallelTools: a.MaxParallelTools,
		Hooks:            a.Hooks,
	}, a.Messages)
	if err != nil {
		return Result{}, err
	}
	a.Messages = result.Messages
	return result, nil
}

func (a *Agent) Continue(ctx context.Context) (Result, error) {
	result, err := Run(ctx, Config{
		SystemPrompt:     a.SystemPrompt,
		Model:            a.Model,
		Tools:            a.Tools,
		MaxParallelTools: a.MaxParallelTools,
		Hooks:            a.Hooks,
	}, a.Messages)
	if err != nil {
		return Result{}, err
	}
	a.Messages = result.Messages
	return result, nil
}
