package fswatcher

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Filter decides whether a file path should be ignored.
type Filter struct {
	mu       sync.RWMutex
	patterns []filterRule
}

type filterRule struct {
	pattern string
	negate  bool
}

// NewFilter creates a Filter from inline --ignore patterns and an optional filter file.
func NewFilter(ignorePatterns []string, filterFile string) (*Filter, error) {
	f := &Filter{}
	for _, p := range ignorePatterns {
		f.patterns = append(f.patterns, filterRule{pattern: p})
	}
	if filterFile != "" {
		if err := f.loadFile(filterFile); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// Reload re-reads the filter file (called on SIGHUP).
func (f *Filter) Reload(ignorePatterns []string, filterFile string) error {
	rules := make([]filterRule, 0, len(ignorePatterns))
	for _, p := range ignorePatterns {
		rules = append(rules, filterRule{pattern: p})
	}

	if filterFile != "" {
		fileRules, err := parseFilterFile(filterFile)
		if err != nil {
			return err
		}
		rules = append(rules, fileRules...)
	}

	f.mu.Lock()
	f.patterns = rules
	f.mu.Unlock()
	return nil
}

func (f *Filter) loadFile(path string) error {
	rules, err := parseFilterFile(path)
	if err != nil {
		return err
	}
	f.patterns = append(f.patterns, rules...)
	return nil
}

func parseFilterFile(path string) ([]filterRule, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var rules []filterRule
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "!") {
			rules = append(rules, filterRule{pattern: line[1:], negate: true})
		} else {
			rules = append(rules, filterRule{pattern: line})
		}
	}
	return rules, scanner.Err()
}

// ShouldIgnore returns true if the given relative path matches an ignore pattern
// and is not re-included by a negation pattern.
func (f *Filter) ShouldIgnore(relPath string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()

	ignored := false
	for _, rule := range f.patterns {
		matched, err := filepath.Match(rule.pattern, relPath)
		if err != nil {
			continue
		}
		if !matched {
			// Also try matching just the base name for non-path patterns.
			if !strings.Contains(rule.pattern, "/") && !strings.Contains(rule.pattern, string(filepath.Separator)) {
				matched, _ = filepath.Match(rule.pattern, filepath.Base(relPath))
			}
		}
		if !matched {
			// Try matching with doublestar-style prefix match for ** patterns.
			// filepath.Match doesn't support **, so we do a simplified check:
			// "dir/**" matches anything starting with "dir/".
			if strings.HasSuffix(rule.pattern, "/**") {
				prefix := strings.TrimSuffix(rule.pattern, "/**")
				matched = strings.HasPrefix(relPath, prefix+"/") || relPath == prefix
			}
		}
		if matched {
			ignored = !rule.negate
		}
	}
	return ignored
}
