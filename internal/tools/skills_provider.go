package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	hostruntime "github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/skills"
)

type SkillsProvider struct {
	Catalog *skills.Catalog
	Invoker hostruntime.Invoker
}

func (p SkillsProvider) Tools(_ context.Context, _ Scope) ([]Tool, error) {
	if p.Catalog == nil {
		return nil, nil
	}
	snapshot := p.Catalog.Snapshot()
	out := make([]Tool, 0, len(snapshot))
	for _, skill := range snapshot {
		if !skill.Executable || skill.Manifest.Type == "" {
			continue
		}
		out = append(out, skillTool{
			skill:   skill,
			invoker: p.Invoker,
		})
	}
	return out, nil
}

type skillTool struct {
	skill   skills.Skill
	invoker hostruntime.Invoker
}

func (t skillTool) Spec() Spec {
	tags := []string{"skill"}
	if t.skill.Manifest.Type != "" {
		tags = append(tags, string(t.skill.Manifest.Type))
	}
	tags = append(tags, t.skill.Manifest.Tags...)
	for key, value := range t.skill.Manifest.Metadata {
		key = strings.TrimSpace(strings.ToLower(key))
		if key == "" || value == nil {
			continue
		}
		if key == "tag" || key == "tags" || key == "keyword" || key == "keywords" || key == "category" {
			switch v := value.(type) {
			case string:
				for _, item := range strings.FieldsFunc(strings.ToLower(strings.TrimSpace(v)), func(r rune) bool { return r == ',' || r == ';' || r == '|' || r == ' ' }) {
					item = strings.TrimSpace(item)
					if item != "" {
						tags = append(tags, item)
					}
				}
			case []any:
				for _, item := range v {
					s, _ := item.(string)
					s = strings.TrimSpace(strings.ToLower(s))
					if s != "" {
						tags = append(tags, s)
					}
				}
			}
		}
	}
	return Spec{
		Name:        t.skill.Manifest.Name,
		Description: firstNonEmpty(t.skill.Manifest.Description, "Workspace skill"),
		Kind:        KindSkill,
		ReadOnly:    t.skill.Manifest.ReadOnly,
		RiskLevel:   t.skill.Manifest.RiskLevel,
		Tags:        tags,
	}
}

func (t skillTool) Invoke(ctx context.Context, _ Call) (Result, error) {
	res, err := t.invoker.Invoke(ctx, t.skill)
	payload := strings.TrimSpace(res.Stdout)
	if payload == "" {
		payload = strings.TrimSpace(res.Stderr)
	}
	if payload == "" {
		payload = fmt.Sprintf("skill %s completed", t.skill.Manifest.Name)
	}
	data, _ := json.Marshal(payload)
	return Result{
		Output: data,
		Meta: map[string]any{
			"exit_code": res.ExitCode,
			"status":    res.Status,
		},
	}, err
}
