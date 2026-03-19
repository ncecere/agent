package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	internalapproval "github.com/ncecere/agent/internal/approval"
	internalhost "github.com/ncecere/agent/internal/host"
	internalmcp "github.com/ncecere/agent/internal/mcp"
	"github.com/ncecere/agent/internal/plugin"
	internalpolicy "github.com/ncecere/agent/internal/policy"
	profileloader "github.com/ncecere/agent/internal/profile"
	"github.com/ncecere/agent/internal/providers/mock"
	"github.com/ncecere/agent/internal/providers/openai"
	"github.com/ncecere/agent/internal/registry"
	internalruntime "github.com/ncecere/agent/internal/runtime"
	store "github.com/ncecere/agent/internal/store/sqlite"
	coretools "github.com/ncecere/agent/internal/tools/core"
	"github.com/ncecere/agent/pkg/approval"
	"github.com/ncecere/agent/pkg/config"
	"github.com/ncecere/agent/pkg/policy"
	"github.com/ncecere/agent/pkg/profile"
	"github.com/ncecere/agent/pkg/provider"
	pkgruntime "github.com/ncecere/agent/pkg/runtime"
	"github.com/ncecere/agent/pkg/session"
	"github.com/ncecere/agent/pkg/tool"
	"github.com/ncecere/agent/pkg/workspace"
)

type App struct {
	Paths            config.Paths
	Config           config.Config
	Profiles         profileloader.Loader
	Plugins          plugin.Loader
	Tools            *registry.ToolRegistry
	Providers        *registry.ProviderRegistry
	PluginManifests  *registry.PluginRegistry
	Prompts          *registry.PromptRegistry
	ProfileTemplates *registry.ProfileTemplateRegistry
	Policies         *registry.PolicyRegistry
	MCPManager       *internalmcp.Manager
	Runner           pkgruntime.Runner
	Sessions         session.Store
}

func Bootstrap(cwd string) (App, error) {
	paths, err := config.DefaultPaths(cwd)
	if err != nil {
		return App{}, err
	}
	cfg, err := config.Load(paths)
	if err != nil {
		return App{}, err
	}
	toolRegistry := registry.NewToolRegistry()
	for _, t := range []tool.Tool{coretools.ReadTool{}, coretools.WriteTool{}, coretools.EditTool{}, coretools.BashTool{}, coretools.GlobTool{}, coretools.GrepTool{}} {
		if err := toolRegistry.Register(t); err != nil {
			return App{}, err
		}
	}
	providerRegistry := registry.NewProviderRegistry()
	if err := providerRegistry.Register(mock.Provider{}); err != nil {
		return App{}, err
	}
	if err := providerRegistry.Register(openai.Provider{
		BaseURL: cfg.Providers["openai"].BaseURL,
		APIKey:  cfg.Providers["openai"].APIKey,
		APIMode: cfg.Providers["openai"].APIMode,
	}); err != nil {
		return App{}, err
	}
	pluginRegistry := registry.NewPluginRegistry()
	promptRegistry := registry.NewPromptRegistry()
	profileTemplateRegistry := registry.NewProfileTemplateRegistry()
	policyRegistry := registry.NewPolicyRegistry()
	profileLoaderInst := profileloader.Loader{Roots: []string{
		paths.LocalProfilesDir,
		paths.UserProfilesDir,
	}}
	hostCaps := &internalhost.RuntimeCapabilities{
		Profiles:   profileLoaderInst,
		Tools:      toolRegistry,
		Providers:  providerRegistry,
		DefaultCWD: paths.CWD,
		MaxDepth:   2,
	}
	pluginLoader := plugin.Loader{Roots: []string{
		paths.LocalPluginsDir,
		paths.UserPluginsDir,
	}, Enable: func(name string) bool {
		return cfg.IsPluginEnabled(name)
	}}
	mcpManager := internalmcp.NewManager()
	if err := plugin.RegisterDiscovered(context.Background(), pluginLoader, plugin.Registries{
		Plugins:          pluginRegistry,
		Tools:            toolRegistry,
		Prompts:          promptRegistry,
		ProfileTemplates: profileTemplateRegistry,
		Policies:         policyRegistry,
		PluginConfigs:    cfg.Plugins,
		HostCapabilities: hostCaps,
		MCPManager:       mcpManager,
	}); err != nil {
		return App{}, err
	}
	app := App{
		Paths:            paths,
		Config:           cfg,
		Profiles:         profileLoaderInst,
		Plugins:          pluginLoader,
		Tools:            toolRegistry,
		Providers:        providerRegistry,
		PluginManifests:  pluginRegistry,
		Prompts:          promptRegistry,
		ProfileTemplates: profileTemplateRegistry,
		Policies:         policyRegistry,
		MCPManager:       mcpManager,
		Runner:           internalruntime.Runner{},
		Sessions:         store.Store{Path: filepath.Join(paths.SessionsDir, "sessions.db")},
	}
	return app, nil
}

func (a App) ResolveProvider(name string) (provider.Provider, error) {
	providerImpl, ok := a.Providers.Get(name)
	if !ok {
		return nil, fmt.Errorf("provider %q is not registered", name)
	}
	return providerImpl, nil
}

func (a App) ResolveTools(enabled []string) ([]tool.Tool, error) {
	tools := make([]tool.Tool, 0, len(enabled))
	for _, id := range enabled {
		toolImpl, ok := a.Tools.Get(id)
		if !ok {
			return nil, fmt.Errorf("tool %q is not registered", id)
		}
		tools = append(tools, toolImpl)
	}
	return tools, nil
}

func (a App) BuildPolicy(workspaceRef workspace.Workspace, manifest profile.Manifest, profilePath string) policy.Engine {
	overrides, err := internalpolicy.LoadToolOverrides(profilePath, manifest.Spec.Policy.Overlays)
	if err != nil {
		overrides = internalpolicy.OverlayDecisions{Tools: map[string]policy.DecisionKind{}}
	}
	return internalpolicy.Engine{
		Workspace:      workspaceRef,
		ReadOnly:       internalpolicy.IsReadOnly(manifest.Spec.Workspace.WriteScope, manifest.Spec.Tools.Enabled),
		SensitiveTools: a.sensitiveToolsFor(manifest.Spec.Tools.Enabled),
		ToolOverrides:  overrides.Tools,
		ShellOverride:  overrides.Shell,
		NetOverride:    overrides.Network,
	}
}

func (a App) BuildApprovalResolver(mode string) approval.Resolver {
	resolved := approval.Mode(mode)
	if resolved == "" {
		resolved = approval.Mode(a.Config.ApprovalMode)
	}
	if resolved == "" {
		resolved = approval.ModeOnRequest
	}
	return internalapproval.CLIResolver{Mode: resolved, Reader: os.Stdin, Writer: os.Stdout}
}

func (a App) sensitiveToolsFor(enabled []string) map[string]policy.RiskLevel {
	allowed := make(map[string]struct{}, len(enabled))
	for _, toolID := range enabled {
		allowed[toolID] = struct{}{}
	}
	sensitive := make(map[string]policy.RiskLevel)
	for _, pluginName := range a.PluginManifests.List() {
		manifest, ok := a.PluginManifests.Get(pluginName)
		if !ok {
			continue
		}
		for _, toolID := range manifest.Spec.Permissions.SensitiveActions {
			if _, ok := allowed[toolID]; ok {
				sensitive[toolID] = policy.RiskHigh
			}
		}
	}
	return sensitive
}
