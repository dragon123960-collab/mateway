package runtime

import (
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/tool"
)

type AgentPool struct {
	config *config.Root
	agents map[string]*agentcore.Agent
}

func NewAgentPool(cfg *config.Root) AgentPool {
	pool := AgentPool{config: cfg, agents: map[string]*agentcore.Agent{}}
	if cfg == nil {
		return pool
	}
	cfg.DefaultAgent()
	for _, profile := range cfg.Agents.Profiles {
		agentID := strings.TrimSpace(profile.ID)
		if agentID == "" {
			continue
		}
		agent := agentcore.NewAgent(resolveModelForProfile(cfg, profile), tool.NewRegistry(cfg))
		agent.SystemPrompt = buildRuntimeSystemContext(cfg, profile)
		pool.agents[agentID] = agent
	}
	return pool
}

func (p AgentPool) AgentForSession(sessionKey string) *agentcore.Agent {
	agentID := p.resolveAgentID(sessionKey)
	if agent, ok := p.agents[agentID]; ok {
		return cloneAgent(agent)
	}
	if agent, ok := p.agents[p.config.Agents.Default]; ok {
		return cloneAgent(agent)
	}
	return agentcore.NewAgent(HeuristicModel{}, tool.NewRegistry(p.config))
}

func (p AgentPool) resolveAgentID(sessionKey string) string {
	if p.config == nil {
		return ""
	}
	channelName := sessionKey
	if i := strings.Index(channelName, ":"); i >= 0 {
		channelName = channelName[:i]
	}
	for _, binding := range p.config.Agents.Bindings {
		if strings.EqualFold(binding.Channel, channelName) && strings.TrimSpace(binding.AgentID) != "" {
			return strings.TrimSpace(binding.AgentID)
		}
	}
	return p.config.Agents.Default
}

func cloneAgent(agent *agentcore.Agent) *agentcore.Agent {
	if agent == nil {
		return nil
	}
	return &agentcore.Agent{
		SystemPrompt:  agent.SystemPrompt,
		Model:         agent.Model,
		Tools:         agent.Tools,
		Hooks:         agent.Hooks,
		MaxIterations: agent.MaxIterations,
	}
}

func resolveModelForProfile(cfg *config.Root, profile config.AgentProfileConfig) agentcore.Model {
	var names []string
	if modelName := strings.TrimSpace(profile.Model.Default); modelName != "" {
		names = append(names, modelName)
	}
	if len(names) == 0 {
		if modelName := strings.TrimSpace(cfg.Model.Default); modelName != "" {
			names = append(names, modelName)
		}
	}
	names = append(names, profile.Model.Fallbacks...)
	names = append(names, cfg.Model.Fallbacks...)
	var configs []config.ModelConfig
	seen := map[string]bool{}
	for _, name := range names {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if cfg, ok := enabledModelByName(cfg, key); ok && strings.TrimSpace(cfg.ResolvedAPIKey()) != "" {
			configs = append(configs, cfg)
		}
	}
	if len(configs) > 0 {
		agentModel := model.NewFallbackAgentModel(configs)
		agentModel.SystemPrompt = strings.TrimSpace(agentModel.SystemPrompt + "\n\n" + buildRuntimeSystemContext(cfg, profile))
		return agentModel
	}
	return HeuristicModel{}
}

func enabledModelByName(cfg *config.Root, name string) (config.ModelConfig, bool) {
	for _, candidate := range cfg.Models {
		if candidate.Enabled && strings.EqualFold(candidate.Name, name) {
			return candidate, true
		}
	}
	return config.ModelConfig{}, false
}
