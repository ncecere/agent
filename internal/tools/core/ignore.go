package core

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

type ignoreMatcher struct {
	root     string
	patterns []string
}

func loadIgnoreMatcher(root string) ignoreMatcher {
	matcher := ignoreMatcher{
		root: root,
		patterns: []string{
			".git",
			".git/*",
			"bin",
			"bin/*",
			"node_modules",
			"node_modules/*",
			"vendor",
			"vendor/*",
		},
	}
	gitignorePath := filepath.Join(root, ".gitignore")
	file, err := os.Open(gitignorePath)
	if err != nil {
		return matcher
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		matcher.patterns = append(matcher.patterns, line)
	}
	return matcher
}

func (m ignoreMatcher) shouldIgnore(path string, isDir bool) bool {
	rel, err := filepath.Rel(m.root, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	base := filepath.Base(path)
	for _, pattern := range m.patterns {
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		trimmed := strings.TrimSuffix(pattern, "/")
		if rel == trimmed || base == trimmed {
			return true
		}
		if matched, _ := filepath.Match(pattern, rel); matched {
			return true
		}
		if matched, _ := filepath.Match(pattern, base); matched {
			return true
		}
		if isDir && strings.HasPrefix(rel+"/", trimmed+"/") {
			return true
		}
	}
	return false
}
