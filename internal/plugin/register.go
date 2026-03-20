package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	loaderutil "github.com/ncecere/agent/internal/loader"
	"github.com/ncecere/agent/internal/mcp"
	"github.com/ncecere/agent/internal/registry"
	"github.com/ncecere/agent/pkg/config"
	pkghost "github.com/ncecere/agent/pkg/host"
	plg "github.com/ncecere/agent/pkg/plugin"
	"github.com/ncecere/agent/pkg/tool"
)

type Registries struct {
	Plugins          *registry.PluginRegistry
	Tools            *registry.ToolRegistry
	Prompts          *registry.PromptRegistry
	ProfileTemplates *registry.ProfileTemplateRegistry
	Policies         *registry.PolicyRegistry
	PluginConfigs    map[string]config.PluginConfig
	HostCapabilities pkghost.Capabilities
	MCPManager       *mcp.Manager
}

func RegisterDiscovered(ctx context.Context, loader Loader, regs Registries) error {
	discovered, err := loader.Discover(ctx)
	if err != nil {
		return err
	}
	for _, item := range discovered {
		if !item.Reference.Enabled {
			continue
		}
		if err := ValidateManifest(item.Manifest); err != nil {
			return fmt.Errorf("validate plugin manifest %s: %w", item.Manifest.Metadata.Name, err)
		}
		if err := ValidateConfig(item.Manifest, regs.PluginConfigs[item.Manifest.Metadata.Name]); err != nil {
			return err
		}
		if err := registerOne(ctx, item, regs); err != nil {
			return fmt.Errorf("register plugin %s: %w", item.Manifest.Metadata.Name, err)
		}
	}
	return nil
}

func registerOne(ctx context.Context, item Discovered, regs Registries) error {
	if regs.Plugins != nil {
		if err := regs.Plugins.Register(item.Manifest); err != nil {
			return err
		}
	}
	baseDir := filepath.Dir(item.Reference.Path)
	registeredTool := false
	for _, contribution := range item.Manifest.Spec.Contributes.Tools {
		if regs.Tools == nil {
			continue
		}
		descriptorPath := contribution.Path
		if descriptorPath == "" {
			descriptorPath = contribution.Entrypoint
		}
		if descriptorPath == "" {
			return fmt.Errorf("tool contribution %s missing path", contribution.ID)
		}
		descriptor, err := loaderutil.LoadYAML[plg.ToolDescriptor](filepath.Join(baseDir, descriptorPath))
		if err != nil {
			return fmt.Errorf("load tool descriptor %s: %w", contribution.ID, err)
		}
		if descriptor.ID == "" {
			descriptor.ID = contribution.ID
		}
		if _, exists := regs.Tools.Get(descriptor.ID); exists {
			continue
		}
		if err := regs.Tools.Register(DescriptorTool{PluginName: item.Manifest.Metadata.Name, Descriptor: descriptor, Runtime: item.Manifest.Spec.Runtime, Config: regs.PluginConfigs[item.Manifest.Metadata.Name], HostCaps: regs.HostCapabilities, MCPManager: regs.MCPManager, Manifest: item.Manifest}); err != nil {
			return err
		}
		registeredTool = true
	}
	if regs.Tools != nil && item.Manifest.Spec.Runtime.Type == plg.RuntimeMCP && !registeredTool {
		if regs.MCPManager == nil {
			return fmt.Errorf("mcp plugin %s: mcp manager not configured", item.Manifest.Metadata.Name)
		}
		tools, err := regs.MCPManager.Tools(ctx, item.Manifest, regs.PluginConfigs[item.Manifest.Metadata.Name])
		if err != nil {
			return err
		}
		for _, discoveredTool := range tools {
			if _, exists := regs.Tools.Get(discoveredTool.Definition().ID); exists {
				continue
			}
			if err := regs.Tools.Register(discoveredTool); err != nil {
				return err
			}
		}
	}
	for _, contribution := range item.Manifest.Spec.Contributes.Prompts {
		if regs.Prompts != nil {
			if err := regs.Prompts.Register(assetRef(item, baseDir, contribution)); err != nil {
				return err
			}
		}
	}
	for _, contribution := range item.Manifest.Spec.Contributes.ProfileTemplates {
		if regs.ProfileTemplates != nil {
			if err := regs.ProfileTemplates.Register(assetRef(item, baseDir, contribution)); err != nil {
				return err
			}
		}
	}
	for _, contribution := range item.Manifest.Spec.Contributes.Policies {
		if regs.Policies != nil {
			if err := regs.Policies.Register(assetRef(item, baseDir, contribution)); err != nil {
				return err
			}
		}
	}
	return nil
}

func assetRef(item Discovered, baseDir string, contribution plg.Contribution) registry.AssetReference {
	path := contribution.Path
	if path == "" {
		path = contribution.Entrypoint
	}
	if path != "" {
		path = filepath.Join(baseDir, path)
	}
	return registry.AssetReference{PluginName: item.Manifest.Metadata.Name, ID: contribution.ID, Path: path}
}

type DescriptorTool struct {
	PluginName string
	Descriptor plg.ToolDescriptor
	Runtime    plg.Runtime
	Config     config.PluginConfig
	HostCaps   pkghost.Capabilities
	MCPManager *mcp.Manager
	Manifest   plg.Manifest
}

func (t DescriptorTool) Definition() tool.Definition {
	return tool.Definition{ID: t.Descriptor.ID, Description: t.Descriptor.Description, Schema: t.Descriptor.InputSchema}
}

func (t DescriptorTool) Run(ctx context.Context, call tool.Call) (tool.Result, error) {
	switch t.Runtime.Type {
	case plg.RuntimeHTTP:
		return t.runHTTP(ctx, call)
	case plg.RuntimeHost:
		return t.runHost(ctx, call)
	case plg.RuntimeMCP:
		return t.runMCP(ctx, call)
	case plg.RuntimeCommand:
		return t.runCommand(ctx, call)
	default:
		return tool.Result{}, fmt.Errorf("plugin tool %s from %s is registered but runtime mode %s execution is not implemented yet", t.Descriptor.ID, t.PluginName, t.Runtime.Type)
	}
}

func (t DescriptorTool) runMCP(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t.MCPManager == nil {
		return tool.Result{}, fmt.Errorf("plugin tool %s: mcp manager not configured", t.Descriptor.ID)
	}
	tools, err := t.MCPManager.Tools(ctx, t.Manifest, t.Config)
	if err != nil {
		return tool.Result{}, err
	}
	for _, mcpTool := range tools {
		if mcpTool.Definition().ID == t.Descriptor.ID {
			return mcpTool.Run(ctx, call)
		}
	}
	return tool.Result{}, fmt.Errorf("mcp tool %s not found on server", t.Descriptor.ID)
}

func (t DescriptorTool) runHost(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t.HostCaps == nil {
		return tool.Result{}, fmt.Errorf("plugin tool %s requires host capabilities but none are configured", t.Descriptor.ID)
	}
	switch t.Descriptor.Execution.Operation {
	case "spawn-sub-agent":
		task, _ := call.Arguments["task"].(string)
		profile, _ := call.Arguments["profile"].(string)
		maxTurns := 4
		if mt, ok := call.Arguments["maxTurns"].(float64); ok && mt > 0 {
			maxTurns = int(mt)
		}
		result, err := t.HostCaps.SpawnSubRun(ctx, pkghost.SubRunRequest{
			Task:     task,
			Profile:  profile,
			MaxTurns: maxTurns,
		})
		if err != nil {
			return tool.Result{}, err
		}
		return tool.Result{
			ToolID: call.ToolID,
			Output: result.Output,
			Data:   map[string]any{"sessionId": result.SessionID, "turns": result.Turns},
		}, nil
	default:
		return tool.Result{}, fmt.Errorf("plugin tool %s: unsupported host operation %q", t.Descriptor.ID, t.Descriptor.Execution.Operation)
	}
}

func (t DescriptorTool) runHTTP(ctx context.Context, call tool.Call) (tool.Result, error) {
	baseURL, _ := t.Config.Config["baseURL"].(string)
	if baseURL == "" {
		baseURL = t.Runtime.Endpoint
	}
	if strings.TrimSpace(baseURL) == "" {
		return tool.Result{}, fmt.Errorf("plugin %s requires config.baseURL or runtime.endpoint for tool %s", t.PluginName, t.Descriptor.ID)
	}
	url := strings.TrimRight(baseURL, "/")
	operation := strings.TrimSpace(t.Descriptor.Execution.Operation)
	if operation != "" {
		url += "/" + strings.TrimLeft(operation, "/")
	}
	body := map[string]any{
		"plugin":    t.PluginName,
		"tool":      t.Descriptor.ID,
		"operation": t.Descriptor.Execution.Operation,
		"arguments": call.Arguments,
		"config":    t.Config.Config,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return tool.Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(data)))
	if err != nil {
		return tool.Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token, _ := t.Config.Config["apiKey"].(string); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return tool.Result{}, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return tool.Result{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tool.Result{}, fmt.Errorf("plugin %s HTTP tool %s failed: %s: %s", t.PluginName, t.Descriptor.ID, resp.Status, strings.TrimSpace(string(responseBody)))
	}
	var decoded struct {
		Output string         `json:"output"`
		Data   map[string]any `json:"data"`
		Error  string         `json:"error"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err == nil && (decoded.Output != "" || decoded.Data != nil || decoded.Error != "") {
		if decoded.Error != "" {
			return tool.Result{}, errors.New(decoded.Error)
		}
		return tool.Result{ToolID: call.ToolID, Output: decoded.Output, Data: decoded.Data}, nil
	}
	return tool.Result{ToolID: call.ToolID, Output: string(responseBody)}, nil
}

func (t DescriptorTool) runCommand(ctx context.Context, call tool.Call) (tool.Result, error) {
	if len(t.Runtime.Command) == 0 {
		return tool.Result{}, fmt.Errorf("plugin %s: command runtime requires runtime.command", t.PluginName)
	}

	timeout := 30 * time.Second
	if t.Descriptor.Execution.Timeout > 0 {
		timeout = time.Duration(t.Descriptor.Execution.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build environment variables from plugin config.
	var env []string
	for k, v := range t.Config.Config {
		envKey := "AGENT_PLUGIN_" + strings.ToUpper(strings.ReplaceAll(k, ".", "_"))
		env = append(env, envKey+"="+fmt.Sprint(v))
	}

	if len(t.Descriptor.Execution.Argv) > 0 {
		return t.runCommandArgv(ctx, call, env)
	}
	return t.runCommandJSON(ctx, call, env)
}

// runCommandArgv handles argv-template mode for wrapping existing CLIs.
// Template variables {{name}} are expanded from call.Arguments.
// If a value is empty/missing and the preceding element is a flag (--flag),
// both the flag and placeholder are omitted.
func (t DescriptorTool) runCommandArgv(ctx context.Context, call tool.Call, env []string) (tool.Result, error) {
	expanded := expandArgvTemplate(t.Descriptor.Execution.Argv, call.Arguments, t.Config.Config)

	args := make([]string, 0, len(t.Runtime.Command)-1+len(expanded))
	args = append(args, t.Runtime.Command[1:]...)
	args = append(args, expanded...)

	cmd := exec.CommandContext(ctx, t.Runtime.Command[0], args...)
	cmd.Env = append(cmd.Environ(), env...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return tool.Result{}, fmt.Errorf("plugin %s tool %s command failed: %w: %s", t.PluginName, t.Descriptor.ID, err, detail)
		}
		return tool.Result{}, fmt.Errorf("plugin %s tool %s command failed: %w", t.PluginName, t.Descriptor.ID, err)
	}

	return tool.Result{
		ToolID: call.ToolID,
		Output: strings.TrimSpace(stdout.String()),
	}, nil
}

// runCommandJSON handles JSON-stdin/stdout mode for custom binaries and scripts.
// Input: JSON {"plugin","tool","operation","arguments","config"} on stdin
// Output: JSON {"output","data","error"} on stdout, or raw text fallback
func (t DescriptorTool) runCommandJSON(ctx context.Context, call tool.Call, env []string) (tool.Result, error) {
	payload := map[string]any{
		"plugin":    t.PluginName,
		"tool":      t.Descriptor.ID,
		"operation": t.Descriptor.Execution.Operation,
		"arguments": call.Arguments,
		"config":    t.Config.Config,
	}
	input, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, fmt.Errorf("plugin %s: marshal command input: %w", t.PluginName, err)
	}

	args := t.Runtime.Command[1:]
	cmd := exec.CommandContext(ctx, t.Runtime.Command[0], args...)
	cmd.Env = append(cmd.Environ(), env...)
	cmd.Stdin = bytes.NewReader(input)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return tool.Result{}, fmt.Errorf("plugin %s tool %s command failed: %w: %s", t.PluginName, t.Descriptor.ID, err, detail)
		}
		return tool.Result{}, fmt.Errorf("plugin %s tool %s command failed: %w", t.PluginName, t.Descriptor.ID, err)
	}

	// Try to parse structured JSON output.
	raw := stdout.Bytes()
	var decoded struct {
		Output string         `json:"output"`
		Data   map[string]any `json:"data"`
		Error  string         `json:"error"`
	}
	if err := json.Unmarshal(raw, &decoded); err == nil && (decoded.Output != "" || decoded.Data != nil || decoded.Error != "") {
		if decoded.Error != "" {
			return tool.Result{}, errors.New(decoded.Error)
		}
		return tool.Result{ToolID: call.ToolID, Output: decoded.Output, Data: decoded.Data}, nil
	}

	// Fallback: treat stdout as plain text output.
	return tool.Result{ToolID: call.ToolID, Output: strings.TrimSpace(string(raw))}, nil
}

// expandArgvTemplate replaces {{name}} placeholders in an argv template.
// Values are resolved from args first, then from cfg with a "config." prefix.
// If a placeholder resolves to empty and the preceding element looks like a flag
// (starts with "-"), both the flag and the placeholder are omitted.
func expandArgvTemplate(argv []string, args map[string]any, cfg map[string]any) []string {
	result := make([]string, 0, len(argv))
	skip := false

	for i, elem := range argv {
		if skip {
			skip = false
			continue
		}

		if !isTemplatePlaceholder(elem) {
			result = append(result, elem)
			continue
		}

		key := elem[2 : len(elem)-2] // strip {{ and }}
		val := resolveTemplateValue(key, args, cfg)

		if val == "" {
			// Check if next element is also a placeholder (this element is a flag).
			// Actually check if THIS element's predecessor is a flag.
			// Re-check: if the PREVIOUS result element is a flag and val is empty, pop it.
			if len(result) > 0 && isFlag(result[len(result)-1]) {
				result = result[:len(result)-1]
			}
			continue
		}

		result = append(result, val)

		// Look ahead: if the next element is a template that resolves to empty,
		// and current element is a flag, we'll handle it when we get there.
		_ = i
	}

	return result
}

func isTemplatePlaceholder(s string) bool {
	return len(s) > 4 && strings.HasPrefix(s, "{{") && strings.HasSuffix(s, "}}")
}

func isFlag(s string) bool {
	return strings.HasPrefix(s, "-")
}

func resolveTemplateValue(key string, args map[string]any, cfg map[string]any) string {
	if strings.HasPrefix(key, "config.") {
		cfgKey := strings.TrimPrefix(key, "config.")
		if v, ok := cfg[cfgKey]; ok {
			return fmt.Sprint(v)
		}
		return ""
	}
	if v, ok := args[key]; ok {
		s := fmt.Sprint(v)
		if s == "<nil>" {
			return ""
		}
		return s
	}
	return ""
}
