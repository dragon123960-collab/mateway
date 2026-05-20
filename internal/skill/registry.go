package skill

import "sort"

type Registry struct {
	skills map[string]Definition
}

func NewRegistry() *Registry {
	return &Registry{skills: map[string]Definition{}}
}

func NewBuiltinRegistry() *Registry {
	r := NewRegistry()
	return r
}

func (r *Registry) Register(def Definition) {
	if r.skills == nil {
		r.skills = map[string]Definition{}
	}
	r.skills[def.Name] = def
}

func (r *Registry) Get(name string) (Definition, bool) {
	def, ok := r.skills[name]
	return def, ok
}

func (r *Registry) Definitions() []Definition {
	names := r.Names()
	out := make([]Definition, 0, len(names))
	for _, name := range names {
		out = append(out, r.skills[name])
	}
	return out
}

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.skills))
	for name := range r.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
