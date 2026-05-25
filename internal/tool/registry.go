package tool

import (
	"fmt"
	"sort"
)

type Registry struct {
	tools map[string]Definition
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Definition{}}
}

func NewBuiltinRegistry() *Registry {
	r := NewRegistry()
	RegisterBuiltins(r)
	return r
}

func (r *Registry) Register(def Definition) {
	if r.tools == nil {
		r.tools = map[string]Definition{}
	}
	r.tools[def.Name] = def
}

func (r *Registry) Get(name string) (Definition, bool) {
	def, ok := r.tools[name]
	return def, ok
}

func (r *Registry) MustGet(name string) (Definition, error) {
	def, ok := r.Get(name)
	if !ok {
		return Definition{}, fmt.Errorf("unknown tool %q", name)
	}
	return def, nil
}

func (r *Registry) Definitions() []Definition {
	names := r.Names()
	out := make([]Definition, 0, len(names))
	for _, name := range names {
		def := r.tools[name]
		if def.Hidden {
			continue
		}
		out = append(out, def)
	}
	return out
}

func (r *Registry) AllDefinitions() []Definition {
	names := r.Names()
	out := make([]Definition, 0, len(names))
	for _, name := range names {
		out = append(out, r.tools[name])
	}
	return out
}

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) VisibleNames() []string {
	names := make([]string, 0, len(r.tools))
	for name, def := range r.tools {
		if def.Hidden {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
