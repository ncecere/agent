package core

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ncecere/agent/pkg/tool"
)

type GlobTool struct{}

func (GlobTool) Definition() tool.Definition {
	return tool.Definition{
		ID:          "core/glob",
		Description: "Find files matching a glob pattern within the workspace",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":    map[string]any{"type": "string"},
				"root":       map[string]any{"type": "string"},
				"maxDepth":   map[string]any{"type": "integer"},
				"maxResults": map[string]any{"type": "integer"},
			},
			"required": []string{"pattern"},
		},
	}
}

func (GlobTool) Run(_ context.Context, call tool.Call) (tool.Result, error) {
	pattern, err := argString(call.Arguments, "pattern")
	if err != nil {
		return tool.Result{}, err
	}
	root := "."
	if r, ok := call.Arguments["root"].(string); ok && r != "" {
		root = r
	}
	maxDepth := 4
	if v, ok := call.Arguments["maxDepth"].(float64); ok && v > 0 {
		maxDepth = int(v)
	}
	maxResults := 200
	if v, ok := call.Arguments["maxResults"].(float64); ok && v > 0 {
		maxResults = int(v)
	}
	matcher := loadIgnoreMatcher(root)
	var matches []string
	broadPattern := isBroadPattern(pattern)
	if broadPattern {
		maxDepth = 1
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path != root && matcher.shouldIgnore(path, d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if rel != "." && depthOf(rel) > maxDepth {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		matched, err := matchGlobPattern(pattern, rel, d.IsDir())
		if err != nil {
			return fmt.Errorf("invalid glob pattern: %w", err)
		}
		if broadPattern && rel == "." {
			matched = false
		}
		if broadPattern && depthOf(rel) > 1 {
			matched = false
		}
		if matched {
			matches = append(matches, rel)
			if len(matches) >= maxResults {
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return tool.Result{}, err
	}
	sort.Strings(matches)
	output := strings.Join(matches, "\n")
	return tool.Result{
		ToolID: call.ToolID,
		Output: output,
		Data:   map[string]any{"matches": matches, "count": len(matches), "maxDepth": maxDepth, "maxResults": maxResults},
	}, nil
}

func isBroadPattern(pattern string) bool {
	trimmed := strings.TrimSpace(pattern)
	switch trimmed {
	case "*", "**", ".", "./*", "*.*":
		return true
	default:
		return false
	}
}

func depthOf(rel string) int {
	if rel == "." || rel == "" {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(rel), "/"))
}

func matchGlobPattern(pattern, rel string, isDir bool) (bool, error) {
	pattern = strings.TrimSpace(pattern)
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)
	if strings.Contains(pattern, "/") {
		return filepath.Match(filepath.ToSlash(pattern), rel)
	}
	if isBroadPattern(pattern) {
		return depthOf(rel) <= 1, nil
	}
	return filepath.Match(pattern, base)
}
