package core

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ncecere/agent/pkg/tool"
)

type GrepTool struct{}

func (GrepTool) Definition() tool.Definition {
	return tool.Definition{
		ID:          "core/grep",
		Description: "Search for a pattern in files within the workspace",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":     map[string]any{"type": "string"},
				"path":        map[string]any{"type": "string"},
				"filePattern": map[string]any{"type": "string"},
				"maxResults":  map[string]any{"type": "integer"},
			},
			"required": []string{"pattern"},
		},
	}
}

func (GrepTool) Run(_ context.Context, call tool.Call) (tool.Result, error) {
	pattern, err := argString(call.Arguments, "pattern")
	if err != nil {
		return tool.Result{}, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return tool.Result{}, fmt.Errorf("invalid regex pattern: %w", err)
	}
	searchPath := "."
	if p, ok := call.Arguments["path"].(string); ok && p != "" {
		searchPath = p
	}
	filePattern := "*"
	if fp, ok := call.Arguments["filePattern"].(string); ok && fp != "" {
		filePattern = fp
	}
	matcher := loadIgnoreMatcher(searchPath)
	maxResults := 50
	if mr, ok := call.Arguments["maxResults"].(float64); ok && mr > 0 {
		maxResults = int(mr)
	}
	type match struct {
		File string `json:"file"`
		Line int    `json:"line"`
		Text string `json:"text"`
	}
	var matches []match
	var outputLines []string
	err = filepath.WalkDir(searchPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path != searchPath && matcher.shouldIgnore(path, d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if len(matches) >= maxResults {
			return fs.SkipAll
		}
		if filePattern != "*" {
			matched, _ := filepath.Match(filePattern, d.Name())
			if !matched {
				return nil
			}
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		rel, _ := filepath.Rel(searchPath, path)
		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			if len(matches) >= maxResults {
				break
			}
			text := scanner.Text()
			if re.MatchString(text) {
				matches = append(matches, match{File: rel, Line: lineNum, Text: strings.TrimSpace(text)})
				outputLines = append(outputLines, fmt.Sprintf("%s:%d: %s", rel, lineNum, strings.TrimSpace(text)))
			}
		}
		return nil
	})
	if err != nil {
		return tool.Result{}, err
	}
	data := make([]any, len(matches))
	for i, m := range matches {
		data[i] = map[string]any{"file": m.File, "line": m.Line, "text": m.Text}
	}
	return tool.Result{
		ToolID: call.ToolID,
		Output: strings.Join(outputLines, "\n"),
		Data:   map[string]any{"matches": data, "count": len(matches)},
	}, nil
}
