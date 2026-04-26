package tools

import (
	"context"
	"sort"
	"strings"
	"sync"
)

type Registry struct {
	mu        sync.RWMutex
	providers []Provider
}

func NewRegistry() *Registry {
	return &Registry{}
}

func (r *Registry) Register(provider Provider) {
	if provider == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = append(r.providers, provider)
}

func (r *Registry) List(ctx context.Context, scope Scope) ([]Tool, error) {
	r.mu.RLock()
	providers := append([]Provider(nil), r.providers...)
	r.mu.RUnlock()

	out := make([]Tool, 0, 16)
	for _, provider := range providers {
		tools, err := provider.Tools(ctx, scope)
		if err != nil {
			return nil, err
		}
		out = append(out, tools...)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Spec().Name < out[j].Spec().Name
	})
	return out, nil
}

func (r *Registry) Specs(ctx context.Context, scope Scope) ([]Spec, error) {
	tools, err := r.List(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := make([]Spec, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Spec())
	}
	return out, nil
}

func (r *Registry) Find(ctx context.Context, scope Scope, name string) (Tool, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false, nil
	}
	tools, err := r.List(ctx, scope)
	if err != nil {
		return nil, false, err
	}
	for _, tool := range tools {
		if tool.Spec().Name == name {
			return tool, true, nil
		}
	}
	return nil, false, nil
}
