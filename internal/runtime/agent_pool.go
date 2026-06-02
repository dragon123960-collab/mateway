package runtime

import (
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
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
	return p.agentForID(agentID)
}

func (p AgentPool) AgentForMessage(msg channel.InboundMessage) *agentcore.Agent {
	agentID := p.resolveAgentIDForMessage(msg)
	return p.agentForID(agentID)
}

func (p AgentPool) agentForID(agentID string) *agentcore.Agent {
	if agent, ok := p.agents[agentID]; ok {
		return cloneAgent(agent)
	}
	if agent, ok := p.agents[p.config.Agents.Default]; ok {
		return cloneAgent(agent)
	}
	return agentcore.NewAgent(HeuristicModel{}, tool.NewRegistry(p.config))
}

func (p AgentPool) ProfileForSession(sessionKey string) config.AgentProfileConfig {
	agentID := p.resolveAgentID(sessionKey)
	return p.profileForID(agentID)
}

func (p AgentPool) ProfileForMessage(msg channel.InboundMessage) config.AgentProfileConfig {
	agentID := p.resolveAgentIDForMessage(msg)
	return p.profileForID(agentID)
}

func (p AgentPool) profileForID(agentID string) config.AgentProfileConfig {
	if profile, ok := p.profileByID(agentID); ok {
		return profile
	}
	if p.config != nil {
		if profile, ok := p.profileByID(p.config.Agents.Default); ok {
			return profile
		}
	}
	return config.AgentProfileConfig{ID: "main"}
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

func (p AgentPool) resolveAgentIDForMessage(msg channel.InboundMessage) string {
	if p.config == nil {
		return ""
	}
	channelName := strings.TrimSpace(msg.Channel)
	if channelName == "" {
		channelName = msg.SessionKey
		if i := strings.Index(channelName, ":"); i >= 0 {
			channelName = channelName[:i]
		}
	}
	accountID := strings.TrimSpace(msg.Metadata["account_id"])
	peerID := strings.TrimSpace(msg.Metadata["peer_id"])
	if peerID == "" {
		peerID = strings.TrimSpace(msg.ThreadID)
	}
	for _, binding := range p.config.Agents.Bindings {
		if !bindingMatches(binding, channelName, accountID, peerID) {
			continue
		}
		if strings.TrimSpace(binding.AgentID) != "" {
			return strings.TrimSpace(binding.AgentID)
		}
	}
	return p.config.Agents.Default
}

func bindingMatches(binding config.AgentBindingConfig, channelName, accountID, peerID string) bool {
	if !strings.EqualFold(strings.TrimSpace(binding.Channel), strings.TrimSpace(channelName)) {
		return false
	}
	if strings.TrimSpace(binding.AccountID) != "" && !strings.EqualFold(strings.TrimSpace(binding.AccountID), strings.TrimSpace(accountID)) {
		return false
	}
	if strings.TrimSpace(binding.PeerID) != "" && !strings.EqualFold(strings.TrimSpace(binding.PeerID), strings.TrimSpace(peerID)) {
		return false
	}
	return true
}

func (p AgentPool) profileByID(agentID string) (config.AgentProfileConfig, bool) {
	if p.config == nil {
		return config.AgentProfileConfig{}, false
	}
	for _, profile := range p.config.Agents.Profiles {
		if strings.EqualFold(strings.TrimSpace(profile.ID), strings.TrimSpace(agentID)) {
			return profile, true
		}
	}
	return config.AgentProfileConfig{}, false
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
	visionNames := append([]string{}, profile.Model.Roles.Models("vision")...)
	visionNames = append(visionNames, cfg.Model.Roles.Models("vision")...)
	var configs []config.ModelConfig
	var visionConfigs []config.ModelConfig
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
	for _, name := range visionNames {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if cfg, ok := enabledModelByName(cfg, key); ok && strings.TrimSpace(cfg.ResolvedAPIKey()) != "" && cfg.SupportsModality("image") {
			visionConfigs = append(visionConfigs, cfg)
		}
	}
	if len(configs) > 0 {
		return model.NewRoutedAgentModel(configs, visionConfigs)
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
