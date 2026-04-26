package harness

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	einoskill "github.com/cloudwego/eino/adk/middlewares/skill"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"

	mwayskills "github.com/dongping/mateway/internal/skills"
)

const einoSkillToolName = "skill"

type einoSkillBundle struct {
	instruction string
	tool        einotool.BaseTool
	handler     adk.ChatModelAgentMiddleware
	selected    []mwayskills.Skill
}

func (h *Harness) buildEinoSkillBundle(ctx context.Context, req Request, run Run, visibleSkillNames, selectedSkillNames []string) (*einoSkillBundle, error) {
	visible := h.visibleSkillCatalog(visibleSkillNames)
	if len(visible) == 0 {
		return nil, nil
	}
	activated := visible
	if len(selectedSkillNames) > 0 {
		activated = mwayskills.FilterVisible(visible, selectedSkillNames)
	}
	if len(activated) == 0 {
		return nil, nil
	}

	cfg := &einoskill.Config{
		Backend:      &einoSkillBackend{skills: activated},
		BuildContent: buildEinoSkillContent,
		AgentHub:     &harnessSkillAgentHub{harness: h, req: req, run: run},
		ModelHub:     &harnessSkillModelHub{harness: h},
	}
	handler, err := einoskill.NewMiddleware(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build eino skill middleware: %w", err)
	}
	mw, err := einoskill.New(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build eino skill activation tool: %w", err)
	}
	if len(mw.AdditionalTools) == 0 {
		return nil, fmt.Errorf("eino skill middleware returned no tools")
	}
	return &einoSkillBundle{
		instruction: strings.TrimSpace(mw.AdditionalInstruction),
		tool:        mw.AdditionalTools[0],
		handler:     handler,
		selected:    activated,
	}, nil
}

type harnessSkillModelHub struct {
	harness *Harness
}

func (h *harnessSkillModelHub) Get(ctx context.Context, name string) (einomodel.ToolCallingChatModel, error) {
	if h == nil || h.harness == nil {
		return nil, fmt.Errorf("skill model hub is not configured")
	}
	return h.harness.newNamedEinoModel(ctx, name)
}

type harnessSkillAgentHub struct {
	harness *Harness
	req     Request
	run     Run
}

func (h *harnessSkillAgentHub) Get(ctx context.Context, name string, opts *einoskill.AgentHubOptions) (adk.Agent, error) {
	if h == nil || h.harness == nil {
		return nil, fmt.Errorf("skill agent hub is not configured")
	}
	return h.harness.newSkillForkAgent(ctx, h.req, h.run, name, opts)
}

type einoSkillBackend struct {
	skills []mwayskills.Skill
}

func (b *einoSkillBackend) List(context.Context) ([]einoskill.FrontMatter, error) {
	out := make([]einoskill.FrontMatter, 0, len(b.skills))
	for _, skill := range b.skills {
		out = append(out, toEinoFrontMatter(skill))
	}
	return out, nil
}

func (b *einoSkillBackend) Get(_ context.Context, name string) (einoskill.Skill, error) {
	name = strings.TrimSpace(name)
	for _, skill := range b.skills {
		if skill.Manifest.Name == name {
			return einoskill.Skill{
				FrontMatter:   toEinoFrontMatter(skill),
				Content:       appendEinoSkillResources(skill),
				BaseDirectory: skill.Directory,
			}, nil
		}
	}
	return einoskill.Skill{}, fmt.Errorf("skill not found: %s", name)
}

func toEinoFrontMatter(skill mwayskills.Skill) einoskill.FrontMatter {
	return einoskill.FrontMatter{
		Name:        skill.Manifest.Name,
		Description: skill.Manifest.Description,
		Context:     einoskill.ContextMode(strings.TrimSpace(skill.Manifest.Context)),
		Agent:       skill.Manifest.Agent,
		Model:       skill.Manifest.Model,
	}
}

func buildEinoSkillContent(_ context.Context, skill einoskill.Skill, _ string) (string, error) {
	body := strings.TrimSpace(skill.Content)
	if body == "" {
		body = "No additional skill instructions."
	}
	return body, nil
}

func appendEinoSkillResources(skill mwayskills.Skill) string {
	body := strings.TrimSpace(skill.Body)
	lines := []string{}
	for _, name := range skill.Resources.AllowedDirs() {
		if part := renderPromptSkillResourceCategory(name, skillResourceItems(skill.Resources, name)); part != "" {
			lines = append(lines, part)
		}
	}
	if len(lines) == 0 {
		return body
	}
	resourceBlock := strings.Join(append([]string{
		"## Mateway Resources",
	}, append(lines, "Use `read_skill_resource` for on-demand text inspection, or absolute paths rooted at the base directory for execution.")...), "\n")
	if body == "" {
		return resourceBlock
	}
	return strings.TrimSpace(body + "\n\n" + resourceBlock)
}

func skillResourceItems(resources mwayskills.ResourceSet, name string) []string {
	switch name {
	case "scripts":
		return resources.Scripts
	case "references":
		return resources.References
	case "assets":
		return resources.Assets
	default:
		return resources.Extra[name]
	}
}

func renderPromptSkillResourceCategory(label string, items []string) string {
	if len(items) == 0 {
		return ""
	}
	const maxItems = 6
	visible := items
	if len(visible) > maxItems {
		visible = visible[:maxItems]
	}
	line := label + ": " + strings.Join(visible, ", ")
	if len(items) > len(visible) {
		line += fmt.Sprintf(" (+%d more)", len(items)-len(visible))
	}
	return line
}
