package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalpolicy "github.com/ncecere/agent/internal/policy"
	"github.com/ncecere/agent/internal/providers/mock"
	"github.com/ncecere/agent/internal/registry"
	internalruntime "github.com/ncecere/agent/internal/runtime"
	store "github.com/ncecere/agent/internal/store/sqlite"
	coretools "github.com/ncecere/agent/internal/tools/core"
	"github.com/ncecere/agent/pkg/approval"
	"github.com/ncecere/agent/pkg/events"
	"github.com/ncecere/agent/pkg/profile"
	"github.com/ncecere/agent/pkg/provider"
	pkgruntime "github.com/ncecere/agent/pkg/runtime"
	"github.com/ncecere/agent/pkg/tool"
	"github.com/ncecere/agent/pkg/workspace"
)

func testProfile(name string, enabledTools []string) profile.Manifest {
	return profile.Manifest{
		APIVersion: "agent/v1",
		Kind:       "Profile",
		Metadata:   profile.Metadata{Name: name, Version: "0.1.0"},
		Spec: profile.Spec{
			Provider:  profile.ProviderSpec{Default: "mock", Model: "echo"},
			Tools:     profile.ToolSpec{Enabled: enabledTools},
			Approval:  profile.ApprovalSpec{Mode: "never"},
			Workspace: profile.WorkspaceSpec{Required: false, WriteScope: "workspace"},
		},
	}
}

func toolRegistry(t *testing.T) *registry.ToolRegistry {
	t.Helper()
	reg := registry.NewToolRegistry()
	for _, impl := range []tool.Tool{
		coretools.ReadTool{},
		coretools.WriteTool{},
		coretools.EditTool{},
		coretools.GlobTool{},
		coretools.GrepTool{},
	} {
		if err := reg.Register(impl); err != nil {
			t.Fatalf("register tool: %v", err)
		}
	}
	return reg
}

func TestRunFullToolCallCycle(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(file, []byte("hello integration test"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := toolRegistry(t)
	readTool, _ := reg.Get("core/read")
	ws, _ := workspace.Resolve(dir)
	pol := internalpolicy.Engine{Workspace: ws}

	var seen []events.Type
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		seen = append(seen, event.Type)
		return nil
	})

	runner := internalruntime.Runner{}
	result, err := runner.Run(context.Background(), pkgruntime.RunRequest{
		Prompt:    "read " + file,
		Profile:   testProfile("test", []string{"core/read"}),
		Provider:  mock.Provider{},
		Tools:     []tool.Tool{readTool},
		Policy:    pol,
		Approvals: allowAllResolver{},
		Events:    sink,
		Execution: pkgruntime.ExecutionContext{CWD: dir, Workspace: ws},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(result.Output, "hello integration test") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	assertEventSeen(t, seen, events.TypeRunStarted)
	assertEventSeen(t, seen, events.TypeToolRequested)
	assertEventSeen(t, seen, events.TypeToolFinished)
	assertEventSeen(t, seen, events.TypeRunFinished)
}

func TestPolicyBlocksWriteOutsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	reg := toolRegistry(t)
	writeTool, _ := reg.Get("core/write")
	ws, _ := workspace.Resolve(dir)
	pol := internalpolicy.Engine{Workspace: ws}

	runner := internalruntime.Runner{}
	_, err := runner.Run(context.Background(), pkgruntime.RunRequest{
		Prompt:    "write /tmp/evil.txt ::: bad",
		Profile:   testProfile("test", []string{"core/write"}),
		Provider:  mock.Provider{},
		Tools:     []tool.Tool{writeTool},
		Policy:    pol,
		Approvals: allowAllResolver{},
		Events:    events.NopSink{},
		Execution: pkgruntime.ExecutionContext{CWD: dir, Workspace: ws},
	})
	if err == nil {
		t.Fatal("expected policy denial, got nil")
	}
	if !strings.Contains(err.Error(), "outside workspace") && !strings.Contains(err.Error(), "policy denied") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSessionCreateAndResume(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")
	sessions := store.Store{Path: dbPath}
	ws, _ := workspace.Resolve(dir)
	pol := internalpolicy.Engine{Workspace: ws}
	runner := internalruntime.Runner{}

	first, err := runner.Run(context.Background(), pkgruntime.RunRequest{
		Prompt:    "hello first",
		Profile:   testProfile("test", nil),
		Provider:  mock.Provider{},
		Policy:    pol,
		Approvals: allowAllResolver{},
		Events:    events.NopSink{},
		Sessions:  sessions,
		Execution: pkgruntime.ExecutionContext{CWD: dir, Workspace: ws},
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.SessionID == "" {
		t.Fatal("expected first session id")
	}

	loaded, err := sessions.Load(context.Background(), first.SessionID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if len(loaded.Entries) == 0 {
		t.Fatal("expected persisted entries")
	}

	second, err := runner.Run(context.Background(), pkgruntime.RunRequest{
		Prompt:     "hello second",
		Profile:    testProfile("test", nil),
		Provider:   mock.Provider{},
		Policy:     pol,
		Approvals:  allowAllResolver{},
		Events:     events.NopSink{},
		Sessions:   sessions,
		Execution:  pkgruntime.ExecutionContext{CWD: dir, SessionID: first.SessionID, Workspace: ws},
		Transcript: first.Transcript,
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.SessionID != first.SessionID {
		t.Fatalf("expected same session id, got %s vs %s", second.SessionID, first.SessionID)
	}

	metas, err := sessions.List(context.Background(), dir, 10)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("expected 1 session metadata row, got %d", len(metas))
	}
	count, err := sessions.Count(context.Background(), dir)
	if err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected session count 1, got %d", count)
	}
}

func TestGlobAndGrepTools(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored/\nignored-file.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "util.go"), []byte("package main\n\nfunc helper() string { return \"hello\" }"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test project"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored-file.txt"), []byte("skip me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored", "nested.go"), []byte("package ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	globResult, err := coretools.GlobTool{}.Run(context.Background(), tool.Call{
		ToolID:    "core/glob",
		Arguments: map[string]any{"pattern": "*.go", "root": dir},
	})
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if !strings.Contains(globResult.Output, "main.go") || !strings.Contains(globResult.Output, "util.go") {
		t.Fatalf("unexpected glob output: %q", globResult.Output)
	}
	if strings.Contains(globResult.Output, "README.md") {
		t.Fatalf("README.md should not match *.go: %q", globResult.Output)
	}
	if strings.Contains(globResult.Output, "ignored") {
		t.Fatalf("ignored paths should be skipped: %q", globResult.Output)
	}

	broadResult, err := coretools.GlobTool{}.Run(context.Background(), tool.Call{
		ToolID:    "core/glob",
		Arguments: map[string]any{"pattern": "*", "root": dir},
	})
	if err != nil {
		t.Fatalf("broad glob: %v", err)
	}
	if strings.Contains(broadResult.Output, "ignored") {
		t.Fatalf("broad glob should respect ignore rules: %q", broadResult.Output)
	}

	grepResult, err := coretools.GrepTool{}.Run(context.Background(), tool.Call{
		ToolID:    "core/grep",
		Arguments: map[string]any{"pattern": "func helper", "path": dir, "filePattern": "*.go"},
	})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(grepResult.Output, "util.go") {
		t.Fatalf("unexpected grep output: %q", grepResult.Output)
	}
}

func TestWriteEditAndBashTools(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")

	writeResult, err := coretools.WriteTool{}.Run(context.Background(), tool.Call{
		ToolID:    "core/write",
		Arguments: map[string]any{"path": target, "content": "hello world"},
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if writeResult.Output != "wrote file" {
		t.Fatalf("unexpected write output: %q", writeResult.Output)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("unexpected file content after write: %q", string(data))
	}

	editResult, err := coretools.EditTool{}.Run(context.Background(), tool.Call{
		ToolID:    "core/edit",
		Arguments: map[string]any{"path": target, "old": "world", "new": "agent"},
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if editResult.Output != "edited file" {
		t.Fatalf("unexpected edit output: %q", editResult.Output)
	}
	data, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if string(data) != "hello agent" {
		t.Fatalf("unexpected file content after edit: %q", string(data))
	}

	readResult, err := coretools.ReadTool{}.Run(context.Background(), tool.Call{
		ToolID:    "core/read",
		Arguments: map[string]any{"path": target},
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if readResult.Output != "hello agent" {
		t.Fatalf("unexpected read output: %q", readResult.Output)
	}

	bashResult, err := coretools.BashTool{}.Run(context.Background(), tool.Call{
		ToolID:    "core/bash",
		Arguments: map[string]any{"command": "printf 'bash ok'"},
	})
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if strings.TrimSpace(bashResult.Output) != "bash ok" {
		t.Fatalf("unexpected bash output: %q", bashResult.Output)
	}
}

type allowAllResolver struct{}

func (allowAllResolver) Resolve(_ context.Context, _ approval.Request) (approval.Decision, error) {
	return approval.Decision{Approved: true, Reason: "test allow"}, nil
}

func assertEventSeen(t *testing.T, seen []events.Type, target events.Type) {
	t.Helper()
	for _, eventType := range seen {
		if eventType == target {
			return
		}
	}
	t.Fatalf("expected event %s to be emitted", target)
}

var _ provider.Provider = mock.Provider{}
