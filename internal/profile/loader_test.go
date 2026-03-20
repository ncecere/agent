package profile

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoaderLoadAcceptsProfileDirectory(t *testing.T) {
	tempDir := t.TempDir()
	profileDir := filepath.Join(tempDir, "example")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "apiVersion: agent/v1\nkind: Profile\nmetadata:\n  name: example\n  version: 1.0.0\n  description: example\nspec:\n  instructions:\n    system: []\n  provider:\n    default: mock\n    model: echo\n  tools:\n    enabled: []\n  approval:\n    mode: never\n    requireFor: []\n  workspace:\n    required: false\n    writeScope: read-only\n  session:\n    persistence: sqlite\n    compaction: auto\n  policy:\n    overlays: []\n"
	if err := os.WriteFile(filepath.Join(profileDir, "profile.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	loader := Loader{}
	loaded, path, err := loader.Load(context.Background(), profileDir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Metadata.Name != "example" {
		t.Fatalf("expected example profile, got %s", loaded.Metadata.Name)
	}
	if path != filepath.Join(profileDir, "profile.yaml") {
		t.Fatalf("expected profile.yaml path, got %s", path)
	}
}
