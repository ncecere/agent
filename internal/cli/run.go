package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	internalplugin "github.com/ncecere/agent/internal/plugin"
	"github.com/ncecere/agent/internal/service"
	"github.com/ncecere/agent/pkg/config"
	"github.com/ncecere/agent/pkg/events"
	pkgplugin "github.com/ncecere/agent/pkg/plugin"
	"github.com/ncecere/agent/pkg/profile"
	"github.com/ncecere/agent/pkg/provider"
	pkgruntime "github.com/ncecere/agent/pkg/runtime"
	"github.com/ncecere/agent/pkg/session"
	"github.com/ncecere/agent/pkg/tool"
	"github.com/ncecere/agent/pkg/workspace"
)

func Run(ctx context.Context, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	app, err := service.Bootstrap(cwd)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		printUsage()
		return nil
	}
	return dispatch(ctx, app, args)
}

func dispatch(ctx context.Context, app service.App, args []string) error {
	switch args[0] {
	case "help", "--help", "-h":
		printUsage()
		return nil
	case "chat":
		return chatCommand(ctx, app, args[1:])
	case "run":
		return runCommand(ctx, app, args[1:])
	case "resume":
		return resumeCommand(ctx, app, args[1:])
	case "profiles":
		return runProfiles(ctx, app, args[1:])
	case "plugins":
		return runPlugins(ctx, app, args[1:])
	case "sessions":
		return runSessions(ctx, app, args[1:])
	case "config":
		return runConfig(app, args[1:])
	case "doctor":
		return runDoctor(ctx, app)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runCommand(ctx context.Context, app service.App, args []string) error {
	profileRef := app.Config.DefaultProfile
	approvalMode := ""
	noSession := false
	var promptParts []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--profile":
			if i+1 >= len(args) {
				return errors.New("--profile requires a value")
			}
			profileRef = args[i+1]
			i++
		case "--approval":
			if i+1 >= len(args) {
				return errors.New("--approval requires a value")
			}
			approvalMode = args[i+1]
			i++
		case "--no-session":
			noSession = true
		default:
			promptParts = append(promptParts, args[i])
		}
	}
	if profileRef == "" {
		profileRef = "coding"
	}
	if len(promptParts) == 0 {
		return errors.New("run requires a prompt")
	}
	prompt := strings.Join(promptParts, " ")
	manifest, path, err := app.Profiles.Load(ctx, profileRef)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("profile %q not found", profileRef)
		}
		return err
	}
	providerImpl, err := app.ResolveProvider(manifest.Spec.Provider.Default)
	if err != nil {
		return err
	}
	tools, err := app.ResolveTools(manifest.Spec.Tools.Enabled)
	if err != nil {
		return err
	}
	workspaceRef, err := workspace.Resolve(app.Paths.CWD)
	if err != nil {
		return err
	}
	result, err := executeRun(ctx, app, runInput{
		Prompt:       prompt,
		Manifest:     manifest,
		ProfilePath:  path,
		ProviderImpl: providerImpl,
		Tools:        tools,
		Workspace:    workspaceRef,
		ApprovalMode: approvalMode,
		NoSession:    noSession,
		CWD:          app.Paths.CWD,
	})
	if err != nil {
		return err
	}
	return printRunResult(result, noSession)
}

func chatCommand(ctx context.Context, app service.App, args []string) error {
	profileRef := app.Config.DefaultProfile
	approvalMode := ""
	noSession := false
	sessionID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--profile":
			if i+1 >= len(args) {
				return errors.New("--profile requires a value")
			}
			profileRef = args[i+1]
			i++
		case "--approval":
			if i+1 >= len(args) {
				return errors.New("--approval requires a value")
			}
			approvalMode = args[i+1]
			i++
		case "--session":
			if i+1 >= len(args) {
				return errors.New("--session requires a value")
			}
			sessionID = args[i+1]
			i++
		case "--no-session":
			noSession = true
		default:
			return fmt.Errorf("unknown chat argument %q", args[i])
		}
	}

	state, err := initializeChatState(ctx, app, profileRef, sessionID, approvalMode, noSession)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "Chat started with profile %s\n", state.Manifest.Metadata.Name)
	if state.SessionID != "" && !state.NoSession {
		fmt.Fprintf(os.Stdout, "Session: %s\n", state.SessionID)
	}
	fmt.Fprintln(os.Stdout, "Type /help for commands. Type /quit to exit.")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Fprint(os.Stdout, "> ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout)
			return nil
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			done, err := handleChatCommand(state, line)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
			continue
		}
		result, err := executeRun(ctx, app, runInput{
			Prompt:       line,
			Manifest:     state.Manifest,
			ProfilePath:  state.ProfilePath,
			ProviderImpl: state.ProviderImpl,
			Tools:        state.Tools,
			Workspace:    state.Workspace,
			ApprovalMode: state.ApprovalMode,
			SessionID:    state.SessionID,
			Transcript:   state.Transcript,
			NoSession:    state.NoSession,
			CWD:          state.CWD,
		})
		if err != nil {
			return err
		}
		if state.SessionID == "" && result.SessionID != "" && !state.NoSession {
			fmt.Fprintf(os.Stdout, "\nSession: %s\n", result.SessionID)
		}
		state.SessionID = result.SessionID
		state.Transcript = result.Transcript
		fmt.Fprintln(os.Stdout)
	}
}

func resumeCommand(ctx context.Context, app service.App, args []string) error {
	if app.Sessions == nil {
		return errors.New("session store is not configured")
	}
	approvalMode := ""
	sessionID := ""
	var promptParts []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session":
			if i+1 >= len(args) {
				return errors.New("--session requires a value")
			}
			sessionID = args[i+1]
			i++
		case "--approval":
			if i+1 >= len(args) {
				return errors.New("--approval requires a value")
			}
			approvalMode = args[i+1]
			i++
		default:
			promptParts = append(promptParts, args[i])
		}
	}
	if len(promptParts) == 0 {
		return errors.New("resume requires a prompt")
	}
	var existingSession sessionView
	var err error
	if sessionID != "" {
		existingSession, err = loadSessionByID(ctx, app, sessionID)
	} else {
		existingSession, err = loadMostRecentSession(ctx, app)
	}
	if err != nil {
		return err
	}
	manifest, path, err := app.Profiles.Load(ctx, existingSession.Profile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("profile %q from session %s is no longer available", existingSession.Profile, existingSession.ID)
		}
		return err
	}
	providerImpl, err := app.ResolveProvider(manifest.Spec.Provider.Default)
	if err != nil {
		return err
	}
	tools, err := app.ResolveTools(manifest.Spec.Tools.Enabled)
	if err != nil {
		return err
	}
	workspaceRef, err := workspace.Resolve(existingSession.CWD)
	if err != nil {
		return err
	}
	result, err := executeRun(ctx, app, runInput{
		Prompt:       strings.Join(promptParts, " "),
		Manifest:     manifest,
		ProfilePath:  path,
		ProviderImpl: providerImpl,
		Tools:        tools,
		Workspace:    workspaceRef,
		ApprovalMode: approvalMode,
		SessionID:    existingSession.ID,
		Transcript:   transcriptFromEntries(existingSession.Entries),
		CWD:          existingSession.CWD,
	})
	if err != nil {
		return err
	}
	return printRunResult(result, false)
}

func runProfiles(ctx context.Context, app service.App, args []string) error {
	if len(args) == 0 {
		return errors.New("profiles command requires a subcommand")
	}
	switch args[0] {
	case "list":
		profiles, err := app.Profiles.Discover(ctx)
		if err != nil {
			return err
		}
		for _, p := range profiles {
			fmt.Printf("%s\t%s\t%s\n", p.Manifest.Metadata.Name, p.Manifest.Metadata.Version, p.Reference.Path)
		}
		return nil
	case "show", "validate", "config", "validate-config":
		if len(args) < 2 {
			return fmt.Errorf("profiles %s requires a profile name or path", args[0])
		}
		manifest, path, err := app.Profiles.Load(ctx, args[1])
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("profile %q not found", args[1])
			}
			return err
		}
		if args[0] == "validate" {
			fmt.Printf("valid\t%s\t%s\n", manifest.Metadata.Name, path)
			return nil
		}
		fmt.Printf("name: %s\nversion: %s\npath: %s\nprovider: %s/%s\ntools: %s\napproval: %s\n",
			manifest.Metadata.Name,
			manifest.Metadata.Version,
			path,
			manifest.Spec.Provider.Default,
			manifest.Spec.Provider.Model,
			strings.Join(manifest.Spec.Tools.Enabled, ", "),
			manifest.Spec.Approval.Mode,
		)
		return nil
	default:
		return fmt.Errorf("unknown profiles subcommand %q", args[0])
	}
}

func runPlugins(ctx context.Context, app service.App, args []string) error {
	if len(args) == 0 {
		return errors.New("plugins command requires a subcommand")
	}
	switch args[0] {
	case "list":
		plugins, err := app.Plugins.Discover(ctx)
		if err != nil {
			return err
		}
		for _, p := range plugins {
			fmt.Printf("%s\t%s\t%s/%s\tenabled=%t\t%s\n", p.Manifest.Metadata.Name, p.Manifest.Metadata.Version, p.Manifest.Spec.Category, p.Manifest.Spec.Runtime.Type, p.Reference.Enabled, p.Reference.Path)
		}
		return nil
	case "show", "validate", "config", "validate-config":
		if args[0] == "config" && len(args) >= 2 && (args[1] == "set" || args[1] == "unset") {
			return runPluginConfigMutation(ctx, app, args[1:])
		}
		if len(args) < 2 {
			return fmt.Errorf("plugins %s requires a plugin name or path", args[0])
		}
		manifest, path, err := app.Plugins.Load(ctx, args[1])
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("plugin %q not found", args[1])
			}
			return err
		}
		if args[0] == "validate-config" {
			pluginCfg := app.Config.Plugins[manifest.Metadata.Name]
			if err := internalplugin.ValidateConfig(manifest, pluginCfg); err != nil {
				return err
			}
			fmt.Printf("valid-config\t%s\n", manifest.Metadata.Name)
			return nil
		}
		if args[0] == "config" {
			pluginCfg := app.Config.Plugins[manifest.Metadata.Name]
			fmt.Printf("name: %s\nenabled: %t\nconfig:\n", manifest.Metadata.Name, app.Config.IsPluginEnabled(manifest.Metadata.Name))
			printPluginConfig(manifest, pluginCfg)
			return nil
		}
		if args[0] == "validate" {
			fmt.Printf("valid\t%s\t%s\n", manifest.Metadata.Name, path)
			return nil
		}
		fmt.Printf("name: %s\nversion: %s\ncategory: %s\nruntime: %s\npath: %s\ntools: %d\nprompts: %d\nprofiles: %d\npolicies: %d\n",
			manifest.Metadata.Name,
			manifest.Metadata.Version,
			manifest.Spec.Category,
			manifest.Spec.Runtime.Type,
			path,
			len(manifest.Spec.Contributes.Tools),
			len(manifest.Spec.Contributes.Prompts),
			len(manifest.Spec.Contributes.ProfileTemplates),
			len(manifest.Spec.Contributes.Policies),
		)
		return nil
	case "install", "enable", "disable":
		return runPluginLifecycle(ctx, app, args)
	case "remove":
		return runPluginLifecycle(ctx, app, args)
	default:
		return fmt.Errorf("unknown plugins subcommand %q", args[0])
	}
}

func runPluginLifecycle(ctx context.Context, app service.App, args []string) error {
	subcommand := args[0]
	switch subcommand {
	case "install":
		if len(args) < 2 {
			return errors.New("plugins install requires a source path")
		}
		link := false
		source := ""
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--link":
				link = true
			default:
				source = args[i]
			}
		}
		if source == "" {
			return errors.New("plugins install requires a source path")
		}
		manifest, destination, err := internalplugin.InstallLocal(source, app.Paths.UserPluginsDir, link)
		if err != nil {
			return err
		}
		fmt.Printf("installed\t%s\t%s\n", manifest.Metadata.Name, destination)
		return nil
	case "enable", "disable":
		if len(args) < 2 {
			return fmt.Errorf("plugins %s requires a plugin name", subcommand)
		}
		name := args[1]
		manifest, _, err := app.Plugins.Load(ctx, name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("plugin %q not found", name)
			}
			return err
		}
		cfg, err := config.Load(app.Paths)
		if err != nil {
			return err
		}
		cfg.SetPluginEnabled(name, subcommand == "enable")
		if subcommand == "enable" {
			if err := internalplugin.ValidateConfig(manifest, cfg.Plugins[name]); err != nil {
				return err
			}
		}
		if err := config.Save(app.Paths, cfg); err != nil {
			return err
		}
		fmt.Printf("%s\t%s\n", subcommand, name)
		return nil
	case "remove":
		if len(args) < 2 {
			return errors.New("plugins remove requires a plugin name")
		}
		name := args[1]
		removedPath, err := internalplugin.RemoveInstalled(name, app.Paths.UserPluginsDir)
		if err != nil {
			return err
		}
		cfg, err := config.Load(app.Paths)
		if err != nil {
			return err
		}
		cfg.RemovePlugin(name)
		if err := config.Save(app.Paths, cfg); err != nil {
			return err
		}
		fmt.Printf("removed\t%s\t%s\n", name, removedPath)
		return nil
	default:
		return fmt.Errorf("unsupported plugin lifecycle command %q", subcommand)
	}
}

func runPluginConfigMutation(ctx context.Context, app service.App, args []string) error {
	if len(args) == 0 {
		return errors.New("plugins config requires a subcommand")
	}
	switch args[0] {
	case "set":
		if len(args) < 4 {
			return errors.New("plugins config set requires <plugin> <key> <value>")
		}
		name, key, rawValue := args[1], args[2], args[3]
		manifest, _, err := app.Plugins.Load(ctx, name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("plugin %q not found", name)
			}
			return err
		}
		cfg, err := config.Load(app.Paths)
		if err != nil {
			return err
		}
		property, ok := manifest.Spec.ConfigSchema.Properties[key]
		value := any(rawValue)
		if ok {
			value, err = internalplugin.ParseConfigValue(property, rawValue)
			if err != nil {
				return fmt.Errorf("plugin %s config %q invalid: %w", name, key, err)
			}
		}
		cfg.SetPluginConfigValue(name, key, value)
		cfg.SetPluginEnabled(name, cfg.IsPluginEnabled(name))
		if cfg.IsPluginEnabled(name) {
			if err := internalplugin.ValidateConfig(manifest, cfg.Plugins[name]); err != nil {
				return err
			}
		}
		if err := config.Save(app.Paths, cfg); err != nil {
			return err
		}
		fmt.Printf("set-config\t%s\t%s\n", name, key)
		return nil
	case "unset":
		if len(args) < 3 {
			return errors.New("plugins config unset requires <plugin> <key>")
		}
		name, key := args[1], args[2]
		manifest, _, err := app.Plugins.Load(ctx, name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("plugin %q not found", name)
			}
			return err
		}
		cfg, err := config.Load(app.Paths)
		if err != nil {
			return err
		}
		cfg.UnsetPluginConfigValue(name, key)
		if cfg.IsPluginEnabled(name) {
			if err := internalplugin.ValidateConfig(manifest, cfg.Plugins[name]); err != nil {
				return err
			}
		}
		if err := config.Save(app.Paths, cfg); err != nil {
			return err
		}
		fmt.Printf("unset-config\t%s\t%s\n", name, key)
		return nil
	default:
		return fmt.Errorf("unknown plugins config subcommand %q", args[0])
	}
}

func runConfig(app service.App, args []string) error {
	if len(args) == 0 {
		return errors.New("config command requires a subcommand")
	}
	switch args[0] {
	case "show":
		fmt.Printf("config_file: %s\ndefault_profile: %s\nenabled_plugins: %s\napproval_mode: %s\n",
			app.Paths.ConfigFile,
			app.Config.DefaultProfile,
			strings.Join(app.Config.EnabledPlugins, ", "),
			app.Config.ApprovalMode,
		)
		return nil
	case "paths":
		fmt.Printf("cwd: %s\nconfig_dir: %s\nconfig_file: %s\nlocal_profiles: %s\nuser_profiles: %s\nlocal_plugins: %s\nuser_plugins: %s\nsessions: %s\n",
			app.Paths.CWD,
			app.Paths.ConfigDir,
			app.Paths.ConfigFile,
			app.Paths.LocalProfilesDir,
			app.Paths.UserProfilesDir,
			app.Paths.LocalPluginsDir,
			app.Paths.UserPluginsDir,
			app.Paths.SessionsDir,
		)
		return nil
	default:
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
}

func runDoctor(ctx context.Context, app service.App) error {
	profiles, err := app.Profiles.Discover(ctx)
	if err != nil {
		return err
	}
	plugins, err := app.Plugins.Discover(ctx)
	if err != nil {
		return err
	}
	checks := []struct {
		label string
		value string
	}{
		{"config_file", statusPath(app.Paths.ConfigFile)},
		{"local_profiles", statusDir(app.Paths.LocalProfilesDir)},
		{"user_profiles", statusDir(app.Paths.UserProfilesDir)},
		{"local_plugins", statusDir(app.Paths.LocalPluginsDir)},
		{"user_plugins", statusDir(app.Paths.UserPluginsDir)},
		{"sessions", statusDir(app.Paths.SessionsDir)},
		{"profiles_found", fmt.Sprintf("%d", len(profiles))},
		{"plugins_found", fmt.Sprintf("%d", len(plugins))},
		{"registered_plugins", fmt.Sprintf("%d", len(app.PluginManifests.List()))},
		{"registered_prompts", fmt.Sprintf("%d", len(app.Prompts.List()))},
		{"registered_profile_templates", fmt.Sprintf("%d", len(app.ProfileTemplates.List()))},
		{"registered_policies", fmt.Sprintf("%d", len(app.Policies.List()))},
		{"registered_tools", fmt.Sprintf("%d", len(app.Tools.List()))},
	}
	for _, check := range checks {
		fmt.Printf("%s\t%s\n", check.label, check.value)
	}
	return nil
}

func runSessions(ctx context.Context, app service.App, args []string) error {
	if len(args) == 0 {
		return errors.New("sessions command requires a subcommand")
	}
	if app.Sessions == nil {
		return errors.New("session store is not configured")
	}
	switch args[0] {
	case "show":
		if len(args) < 2 {
			return errors.New("sessions show requires a session id")
		}
		loaded, err := app.Sessions.Load(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Printf("id: %s\nprofile: %s\ncwd: %s\ncreated_at: %s\nupdated_at: %s\nentries: %d\n",
			loaded.Metadata.ID,
			loaded.Metadata.Profile,
			loaded.Metadata.CWD,
			loaded.Metadata.CreatedAt.Format(time.RFC3339),
			loaded.Metadata.UpdatedAt.Format(time.RFC3339),
			len(loaded.Entries),
		)
		return nil
	case "list":
		limit := 20
		filterCWD := true
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--all":
				filterCWD = false
			case "--limit":
				if i+1 < len(args) {
					if n := parseIntArg(args[i+1]); n > 0 {
						limit = n
						i++
					}
				}
			}
		}
		cwd := ""
		if filterCWD {
			cwd = app.Paths.CWD
		}
		metas, err := app.Sessions.List(ctx, cwd, limit+1)
		if err != nil {
			return err
		}
		total, err := app.Sessions.Count(ctx, cwd)
		if err != nil {
			return err
		}
		if len(metas) == 0 {
			fmt.Println("no sessions found")
			return nil
		}
		hasMore := len(metas) > limit
		if hasMore {
			metas = metas[:limit]
		}
		w := newTabWriter()
		fmt.Fprintln(w, "ID\tPROFILE\tCWD\tUPDATED")
		for _, meta := range metas {
			cwdDisplay := meta.CWD
			if len(cwdDisplay) > 40 {
				cwdDisplay = "..." + cwdDisplay[len(cwdDisplay)-37:]
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", meta.ID, meta.Profile, cwdDisplay, meta.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
		w.Flush()
		fmt.Printf("\nshowing %d of %d session(s)", len(metas), total)
		if hasMore {
			fmt.Printf(" (more available — use --limit to see more)")
		}
		fmt.Println()
		return nil
	case "export":
		if len(args) < 2 {
			return errors.New("sessions export requires a session id")
		}
		loaded, err := app.Sessions.Load(ctx, args[1])
		if err != nil {
			return err
		}
		for _, entry := range loaded.Entries {
			if entry.Kind != "message" {
				continue
			}
			fmt.Printf("[%s] %s: %s\n", entry.CreatedAt.Format("15:04:05"), entry.Role, strings.TrimSpace(entry.Content))
		}
		return nil
	default:
		return fmt.Errorf("unknown sessions subcommand %q", args[0])
	}
}

func statusPath(path string) string {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "missing"
		}
		return "error"
	}
	return "ok"
}

func statusDir(path string) string {
	if info, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "missing"
		}
		return "error"
	} else if !info.IsDir() {
		return "not-a-dir"
	}
	return "ok"
}

func printUsage() {
	prog := filepath.Base(os.Args[0])
	fmt.Printf("%s <command>\n\n", prog)
	fmt.Println("Commands:")
	fmt.Println("  chat                    Start an interactive session")
	fmt.Println("  run                     Execute a one-shot run")
	fmt.Println("  resume                  Resume a previous session with a new prompt")
	fmt.Println("  profiles list           List discoverable profiles")
	fmt.Println("  profiles show <ref>     Show one profile")
	fmt.Println("  profiles validate <ref> Validate one profile")
	fmt.Println("  plugins list            List discoverable plugins")
	fmt.Println("  plugins show <ref>      Show one plugin")
	fmt.Println("  plugins validate <ref>  Validate one plugin")
	fmt.Println("  plugins config <name>   Show one plugin config")
	fmt.Println("  plugins config set <plugin> <key> <value>  Set plugin config")
	fmt.Println("  plugins config unset <plugin> <key>        Unset plugin config")
	fmt.Println("  plugins validate-config <name> Validate one plugin config")
	fmt.Println("  plugins install <path>  Install a local plugin bundle")
	fmt.Println("  plugins enable <name>   Enable an installed plugin")
	fmt.Println("  plugins disable <name>  Disable an installed plugin")
	fmt.Println("  plugins remove <name>   Remove a user-installed plugin")
	fmt.Println("  sessions list           List recent sessions for this cwd")
	fmt.Println("  sessions list --all     List all sessions across all directories")
	fmt.Println("  sessions list --limit N Limit to N sessions")
	fmt.Println("  sessions show <id>      Show one session")
	fmt.Println("  sessions export <id>    Print session message history")
	fmt.Println("  config show             Show resolved config")
	fmt.Println("  config paths            Show config-related paths")
	fmt.Println("  doctor                  Run local diagnostics")
}

type streamSink struct {
	Writer *os.File
}

func (s streamSink) Publish(_ context.Context, event events.Event) error {
	switch event.Type {
	case events.TypeAssistantDelta:
		_, err := fmt.Fprint(s.Writer, event.Message)
		return err
	case events.TypeToolRequested:
		_, err := fmt.Fprintf(s.Writer, "\n[tool request] %s\n", event.Message)
		return err
	case events.TypeToolFinished:
		_, err := fmt.Fprintf(s.Writer, "[tool finished] %s\n", summarizeToolEvent(event))
		return err
	case events.TypePolicyDecision:
		_, err := fmt.Fprintf(s.Writer, "[policy] %s\n", event.Message)
		return err
	case events.TypeApprovalRequest:
		_, err := fmt.Fprintf(s.Writer, "[approval] %s\n", event.Message)
		return err
	case events.TypeError:
		_, err := fmt.Fprintf(s.Writer, "[error] %s\n", event.Message)
		return err
	case events.TypeRunStarted:
		_, err := fmt.Fprintf(s.Writer, "Running at %s\n", event.Time.Format(time.RFC3339))
		return err
	default:
		return nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func summarizeToolEvent(event events.Event) string {
	result, ok := event.Data.(tool.Result)
	if !ok {
		return compactText(event.Message, 200)
	}
	switch result.ToolID {
	case "core/glob":
		count := extractCount(result.Data)
		preview := extractStringSlice(result.Data, "matches")
		return summarizeList("matches", count, preview)
	case "core/grep":
		count := extractCount(result.Data)
		preview := extractMatchPreview(result.Output, 3)
		if count > 0 {
			return fmt.Sprintf("%d match(es)%s", count, preview)
		}
		return "0 matches"
	case "core/read":
		return fmt.Sprintf("read %d bytes", len(result.Output))
	case "core/write", "core/edit":
		if path, _ := result.Data["path"].(string); path != "" {
			return fmt.Sprintf("%s (%s)", result.Output, path)
		}
		return result.Output
	case "core/bash":
		return compactText(strings.TrimSpace(result.Output), 160)
	default:
		return compactText(strings.TrimSpace(result.Output), 200)
	}
}

func extractCount(data map[string]any) int {
	if data == nil {
		return 0
	}
	switch v := data["count"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return 0
	}
}

func extractStringSlice(data map[string]any, key string) []string {
	if data == nil {
		return nil
	}
	raw, ok := data[key]
	if !ok {
		return nil
	}
	items, ok := raw.([]string)
	if ok {
		return items
	}
	anyItems, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(anyItems))
	for _, item := range anyItems {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func summarizeList(label string, count int, items []string) string {
	if count == 0 || len(items) == 0 {
		return fmt.Sprintf("0 %s", label)
	}
	maxPreview := 3
	if len(items) < maxPreview {
		maxPreview = len(items)
	}
	preview := strings.Join(items[:maxPreview], ", ")
	if count > maxPreview {
		return fmt.Sprintf("%d %s: %s, ...", count, label, preview)
	}
	return fmt.Sprintf("%d %s: %s", count, label, preview)
}

func extractMatchPreview(output string, maxLines int) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			filtered = append(filtered, compactText(line, 80))
		}
		if len(filtered) >= maxLines {
			break
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	return ": " + strings.Join(filtered, " | ")
}

func compactText(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "ok"
	}
	if len(text) <= max {
		return text
	}
	if max < 4 {
		return text[:max]
	}
	return text[:max-3] + "..."
}

func loadSystemInstructions(profilePath string, refs []string) string {
	baseDir := filepath.Dir(profilePath)
	chunks := make([]string, 0, len(refs))
	for _, ref := range refs {
		candidate := ref
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(baseDir, candidate)
		}
		data, err := os.ReadFile(candidate)
		if err != nil {
			chunks = append(chunks, ref)
			continue
		}
		chunks = append(chunks, strings.TrimSpace(string(data)))
	}
	return strings.Join(chunks, "\n\n")
}

type runInput struct {
	Prompt       string
	Manifest     profile.Manifest
	ProfilePath  string
	ProviderImpl provider.Provider
	Tools        []tool.Tool
	Workspace    workspace.Workspace
	ApprovalMode string
	SessionID    string
	Transcript   []provider.Message
	NoSession    bool
	CWD          string
}

type chatState struct {
	Manifest     profile.Manifest
	ProfilePath  string
	ProviderImpl provider.Provider
	Tools        []tool.Tool
	Workspace    workspace.Workspace
	ApprovalMode string
	SessionID    string
	Transcript   []provider.Message
	NoSession    bool
	CWD          string
}

type sessionView struct {
	ID      string
	Profile string
	CWD     string
	Entries []session.Entry
}

func executeRun(ctx context.Context, app service.App, input runInput) (pkgruntime.RunResult, error) {
	eventSink := streamSink{Writer: os.Stdout}
	runReq := pkgruntime.RunRequest{
		Prompt:       input.Prompt,
		SystemPrompt: loadSystemInstructions(input.ProfilePath, input.Manifest.Spec.Instructions.System),
		Profile:      input.Manifest,
		Provider:     input.ProviderImpl,
		Tools:        input.Tools,
		Policy:       app.BuildPolicy(input.Workspace, input.Manifest, input.ProfilePath),
		Approvals:    app.BuildApprovalResolver(firstNonEmpty(input.ApprovalMode, input.Manifest.Spec.Approval.Mode)),
		Events:       eventSink,
		Execution:    pkgruntime.ExecutionContext{CWD: input.CWD, SessionID: input.SessionID, ProfileRef: input.ProfilePath, Workspace: input.Workspace},
		Transcript:   input.Transcript,
	}
	if !input.NoSession {
		runReq.Sessions = app.Sessions
	}
	return app.Runner.Run(ctx, runReq)
}

func initializeChatState(ctx context.Context, app service.App, profileRef, sessionID, approvalMode string, noSession bool) (*chatState, error) {
	if sessionID != "" {
		existingSession, err := loadSessionByID(ctx, app, sessionID)
		if err != nil {
			return nil, err
		}
		manifest, path, err := app.Profiles.Load(ctx, existingSession.Profile)
		if err != nil {
			return nil, err
		}
		providerImpl, err := app.ResolveProvider(manifest.Spec.Provider.Default)
		if err != nil {
			return nil, err
		}
		tools, err := app.ResolveTools(manifest.Spec.Tools.Enabled)
		if err != nil {
			return nil, err
		}
		workspaceRef, err := workspace.Resolve(existingSession.CWD)
		if err != nil {
			return nil, err
		}
		return &chatState{
			Manifest:     manifest,
			ProfilePath:  path,
			ProviderImpl: providerImpl,
			Tools:        tools,
			Workspace:    workspaceRef,
			ApprovalMode: approvalMode,
			SessionID:    existingSession.ID,
			Transcript:   transcriptFromEntries(existingSession.Entries),
			NoSession:    noSession,
			CWD:          existingSession.CWD,
		}, nil
	}
	if profileRef == "" {
		profileRef = "coding"
	}
	manifest, path, err := app.Profiles.Load(ctx, profileRef)
	if err != nil {
		return nil, err
	}
	providerImpl, err := app.ResolveProvider(manifest.Spec.Provider.Default)
	if err != nil {
		return nil, err
	}
	tools, err := app.ResolveTools(manifest.Spec.Tools.Enabled)
	if err != nil {
		return nil, err
	}
	workspaceRef, err := workspace.Resolve(app.Paths.CWD)
	if err != nil {
		return nil, err
	}
	return &chatState{
		Manifest:     manifest,
		ProfilePath:  path,
		ProviderImpl: providerImpl,
		Tools:        tools,
		Workspace:    workspaceRef,
		ApprovalMode: approvalMode,
		NoSession:    noSession,
		CWD:          app.Paths.CWD,
	}, nil
}

func handleChatCommand(state *chatState, line string) (bool, error) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return false, nil
	}
	switch parts[0] {
	case "/help":
		fmt.Fprintln(os.Stdout, "/help     Show chat commands")
		fmt.Fprintln(os.Stdout, "/profile  Show current profile")
		fmt.Fprintln(os.Stdout, "/session  Show current session")
		fmt.Fprintln(os.Stdout, "/tools    List enabled tools")
		fmt.Fprintln(os.Stdout, "/approve  Show approval mode")
		fmt.Fprintln(os.Stdout, "/quit     Exit chat")
		return false, nil
	case "/profile":
		fmt.Fprintf(os.Stdout, "profile: %s\nprovider: %s/%s\n", state.Manifest.Metadata.Name, state.Manifest.Spec.Provider.Default, state.Manifest.Spec.Provider.Model)
		return false, nil
	case "/session":
		fmt.Fprintf(os.Stdout, "session: %s\ncwd: %s\n", state.SessionID, state.CWD)
		return false, nil
	case "/tools":
		for _, t := range state.Tools {
			def := t.Definition()
			fmt.Fprintf(os.Stdout, "%s\t%s\n", def.ID, def.Description)
		}
		return false, nil
	case "/approve":
		mode := state.ApprovalMode
		if mode == "" {
			mode = state.Manifest.Spec.Approval.Mode
		}
		fmt.Fprintf(os.Stdout, "approval: %s\n", mode)
		return false, nil
	case "/quit", "/exit":
		return true, nil
	default:
		fmt.Fprintf(os.Stdout, "unknown command: %s\n", parts[0])
		return false, nil
	}
}

func printRunResult(result pkgruntime.RunResult, noSession bool) error {
	if result.Output != "" {
		fmt.Fprintf(os.Stdout, "\nFinal Output:\n%s\n", result.Output)
	}
	if result.SessionID != "" && !noSession {
		fmt.Fprintf(os.Stdout, "\nSession: %s\n", result.SessionID)
	}
	return nil
}

func loadSessionByID(ctx context.Context, app service.App, id string) (sessionView, error) {
	loaded, err := app.Sessions.Load(ctx, id)
	if err != nil {
		return sessionView{}, err
	}
	return sessionView{ID: loaded.Metadata.ID, Profile: loaded.Metadata.Profile, CWD: loaded.Metadata.CWD, Entries: loaded.Entries}, nil
}

func loadMostRecentSession(ctx context.Context, app service.App) (sessionView, error) {
	loaded, err := app.Sessions.MostRecent(ctx, app.Paths.CWD)
	if err != nil {
		return sessionView{}, err
	}
	return sessionView{ID: loaded.Metadata.ID, Profile: loaded.Metadata.Profile, CWD: loaded.Metadata.CWD, Entries: loaded.Entries}, nil
}

func transcriptFromEntries(entries []session.Entry) []provider.Message {
	transcript := make([]provider.Message, 0, len(entries))
	for _, entry := range entries {
		if entry.Kind != session.EntryMessage {
			continue
		}
		meta := decodeSessionMetadata(entry.Metadata)
		switch entry.Role {
		case "user", "assistant", "tool":
			transcript = append(transcript, provider.Message{
				Role:       entry.Role,
				Content:    entry.Content,
				ToolCallID: meta.ToolCallID,
				ToolName:   meta.ToolName,
				ToolCalls:  meta.ToolCalls,
			})
		}
	}
	return transcript
}

func decodeSessionMetadata(raw string) session.MessageMetadata {
	if strings.TrimSpace(raw) == "" {
		return session.MessageMetadata{}
	}
	var meta session.MessageMetadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return session.MessageMetadata{}
	}
	return meta
}

func newTabWriter() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

func parseIntArg(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func printPluginConfig(manifest pkgplugin.Manifest, pluginCfg config.PluginConfig) {
	if len(pluginCfg.Config) == 0 {
		fmt.Println("  <empty>")
		return
	}
	keys := make([]string, 0, len(pluginCfg.Config))
	for key := range pluginCfg.Config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := pluginCfg.Config[key]
		if property, ok := manifest.Spec.ConfigSchema.Properties[key]; ok && property.Secret {
			value = "***"
		}
		fmt.Printf("  %s: %v\n", key, value)
	}
}
