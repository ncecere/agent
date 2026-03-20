package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoaderPrefersEarlierRootsForDuplicatePluginNames(t *testing.T) {
	tempDir := t.TempDir()
	rootA := filepath.Join(tempDir, "a")
	rootB := filepath.Join(tempDir, "b")
	if err := os.MkdirAll(filepath.Join(rootA, "dup"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootB, "dup"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestA := "apiVersion: agent/v1\nkind: Plugin\nmetadata:\n  name: dup\n  version: 1.0.0\n  description: first\nspec:\n  category: asset\n  runtime:\n    type: asset\n  contributes:\n    tools: []\n    prompts: []\n    profileTemplates: []\n    policies: []\n  configSchema:\n    type: object\n    properties: {}\n    required: []\n  permissions: {}\n  requires:\n    framework: \">=0.1.0\"\n    plugins: []\n"
	manifestB := "apiVersion: agent/v1\nkind: Plugin\nmetadata:\n  name: dup\n  version: 2.0.0\n  description: second\nspec:\n  category: asset\n  runtime:\n    type: asset\n  contributes:\n    tools: []\n    prompts: []\n    profileTemplates: []\n    policies: []\n  configSchema:\n    type: object\n    properties: {}\n    required: []\n  permissions: {}\n  requires:\n    framework: \">=0.1.0\"\n    plugins: []\n"
	if err := os.WriteFile(filepath.Join(rootA, "dup", "plugin.yaml"), []byte(manifestA), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "dup", "plugin.yaml"), []byte(manifestB), 0o644); err != nil {
		t.Fatal(err)
	}
	loader := Loader{Roots: []string{rootA, rootB}}
	discovered, err := loader.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(discovered))
	}
	if discovered[0].Manifest.Metadata.Version != "1.0.0" {
		t.Fatalf("expected earlier root to win, got version %s", discovered[0].Manifest.Metadata.Version)
	}
}

func TestLoaderLoadAcceptsPluginDirectory(t *testing.T) {
	tempDir := t.TempDir()
	pluginDir := filepath.Join(tempDir, "example")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "apiVersion: agent/v1\nkind: Plugin\nmetadata:\n  name: example\n  version: 1.0.0\n  description: example\nspec:\n  category: asset\n  runtime:\n    type: asset\n  contributes:\n    tools: []\n    prompts: []\n    profileTemplates: []\n    policies: []\n  configSchema:\n    type: object\n    properties: {}\n    required: []\n  permissions: {}\n  requires:\n    framework: \">=0.1.0\"\n    plugins: []\n"
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	loader := Loader{}
	loaded, path, err := loader.Load(context.Background(), pluginDir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Metadata.Name != "example" {
		t.Fatalf("expected example plugin, got %s", loaded.Metadata.Name)
	}
	if path != filepath.Join(pluginDir, "plugin.yaml") {
		t.Fatalf("expected plugin.yaml path, got %s", path)
	}
}
