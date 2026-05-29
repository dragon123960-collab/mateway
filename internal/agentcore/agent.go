package agentcore

import (
	"context"
	"fmt"
)

type Agent struct {
	SystemPrompt  string
	Messages      []Message
	Model         Model
	Tools         *ToolRegistry
	Hooks         Hooks
	MaxIterations int
}

func NewAgent(model Model, tools *ToolRegistry) *Agent {
	return &Agent{
		Model:         model,
		Tools:         tools,
		MaxIterations: 8,
	}
}

func (a *Agent) Prompt(ctx context.Context, messages ...Message) (Result, error) {
	if a.Model == nil {
		return Result{}, fmt.Errorf("agentcore model is required")
	}
	a.Messages = append(a.Messages, messages...)
	result, err := Run(ctx, Config{
		SystemPrompt:  a.SystemPrompt,
		Model:         a.Model,
		Tools:         a.Tools,
		MaxIterations: a.MaxIterations,
		Hooks:         a.Hooks,
	}, a.Messages)
	if err != nil {
		return Result{}, err
	}
	a.Messages = result.Messages
	return result, nil
}

func (a *Agent) Continue(ctx context.Context) (Result, error) {
	result, err := Run(ctx, Config{
		SystemPrompt:  a.SystemPrompt,
		Model:         a.Model,
		Tools:         a.Tools,
		MaxIterations: a.MaxIterations,
		Hooks:         a.Hooks,
	}, a.Messages)
	if err != nil {
		return Result{}, err
	}
	a.Messages = result.Messages
	return result, nil
}
