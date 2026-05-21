package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/observer"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/skill"
	"github.com/dongping/mateway/internal/tool"
)

type App struct {
	Config  *config.Root
	Runtime runtime.Runtime
	Model   model.Client
	Tools   *tool.Registry
}

func Build(home string, quiet bool) (*App, error) {
	loader := config.NewLoader(home)
	if err := ensureLayout(loader.Home); err != nil {
		return nil, err
	}
	cfg, err := loader.Load()
	if err != nil {
		return nil, err
	}
	defaultAgent, err := cfg.DefaultAgentStrict()
	if err != nil {
		return nil, err
	}
	modelCfg, err := selectModel(cfg.Models, defaultAgent.Model)
	if err != nil {
		return nil, err
	}
	client := model.NewClient(modelCfg)
	reg := tool.NewBuiltinRegistry()
	projectRoot, _ := filepath.Abs(".")
	rt := runtime.New(cfg, client, reg, observer.Logger{Quiet: quiet, TraceDir: filepath.Join(cfg.App.Home, "trace")}, projectRoot)
	return &App{Config: cfg, Runtime: rt, Model: client, Tools: reg}, nil
}

func Init(home string) (string, error) {
	loader := config.NewLoader(home)
	if err := ensureLayout(loader.Home); err != nil {
		return "", err
	}
	return loader.Home, nil
}

func ensureLayout(home string) error {
	if err := skill.EnsureWorkspaceLayout(home, filepath.Join(home, "workspace")); err != nil {
		return err
	}
	return config.EnsureDefaultConfigFiles(home)
}

func selectModel(models []config.ModelConfig, selection config.ModelSelection) (config.ModelConfig, error) {
	if name := strings.TrimSpace(selection.Default); name != "" {
		item, ok := modelByName(models, name)
		if !ok {
			return config.ModelConfig{}, fmt.Errorf("configured default model %q is not enabled or not found under ~/.mateway/config/models", name)
		}
		return item, nil
	}
	for _, item := range models {
		if strings.EqualFold(item.Name, "minimax") && item.Enabled {
			return item, nil
		}
	}
	for _, item := range models {
		if item.Enabled {
			return item, nil
		}
	}
	return config.ModelConfig{}, fmt.Errorf("no enabled model found under ~/.mateway/config/models")
}

func modelByName(models []config.ModelConfig, name string) (config.ModelConfig, bool) {
	for _, item := range models {
		if !item.Enabled {
			continue
		}
		if strings.EqualFold(item.Name, name) {
			return item, true
		}
	}
	return config.ModelConfig{}, false
}

func Doctor(home string) (string, error) {
	app, err := Build(home, true)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"Mateway doctor OK",
		"home: " + app.Config.App.Home,
		"workspace: " + app.Config.App.Workspace,
		"model: " + app.Model.Config.Name + " / " + app.Model.Config.Model,
		fmt.Sprintf("feishu enabled: %t websocket: %t", app.Config.Channels.Feishu.Enabled, app.Config.Channels.Feishu.WebSocket.Enabled),
		"tools: " + strings.Join(app.Tools.Names(), ", "),
	}, "\n"), nil
}
