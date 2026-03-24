package fswatcher

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Filter decides whether a file path should be ignored.
type Filter struct {
	mu    sync.RWMutex
	rules []filterRule
}

type filterRule struct {
	re     *regexp.Regexp
	negate bool
}

// NewFilter creates a Filter from inline --ignore patterns and an optional filter file.
func NewFilter(ignorePatterns []string, filterFile string) (*Filter, error) {
	f := &Filter{}
	if err := f.Reload(ignorePatterns, filterFile); err != nil {
		return nil, err
	}
	return f, nil
}

// Reload re-reads the filter file (called on SIGHUP).
func (f *Filter) Reload(ignorePatterns []string, filterFile string) error {
	var rules []filterRule
	for _, p := range ignorePatterns {
		re, err := patternToRegex(p)
		if err == nil {
			rules = append(rules, filterRule{re: re})
		}
	}

	if filterFile != "" {
		fileRules, err := loadFilterFile(filterFile)
		if err != nil {
			return err
		}
		rules = append(rules, fileRules...)
	}

	f.mu.Lock()
	f.rules = rules
	f.mu.Unlock()
	return nil
}

func loadFilterFile(path string) ([]filterRule, error) {
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
		negate := false
		if strings.HasPrefix(line, "!") {
			negate = true
			line = line[1:]
		}
		re, err := patternToRegex(line)
		if err == nil {
			rules = append(rules, filterRule{re: re, negate: negate})
		}
	}
	return rules, scanner.Err()
}

// ShouldIgnore returns true if the given relative path matches an ignore pattern
// and is not re-included by a negation pattern.
func (f *Filter) ShouldIgnore(relPath string) bool {
	// Standardize to use / even on Windows for matching.
	relPath = filepath.ToSlash(relPath)

	f.mu.RLock()
	defer f.mu.RUnlock()

	ignored := false
	for _, rule := range f.rules {
		if rule.re.MatchString(relPath) {
			ignored = !rule.negate
		}
	}
	return ignored
}

func patternToRegex(pattern string) (*regexp.Regexp, error) {
	// This is a simplified gitignore-to-regex converter.
	p := pattern
	isRooted := strings.HasPrefix(p, "/")
	if isRooted {
		p = p[1:]
	}

	// Escape special regex characters.
	p = regexp.QuoteMeta(p)

	// Replace gitignore globs with regex.
	// Order matters: ** first.
	// fmt.Fprintf(os.Stderr, "DEBUG: before replaces p=%q\n", p)
	p = strings.ReplaceAll(p, "\\*\\*/", "(.*/)?")
	// fmt.Fprintf(os.Stderr, "DEBUG: after ** / p=%q\n", p)
	p = strings.ReplaceAll(p, "/\\*\\*", "(/.*)?")
	// fmt.Fprintf(os.Stderr, "DEBUG: after / ** p=%q\n", p)
	p = strings.ReplaceAll(p, "\\*\\*", ".*")
	// fmt.Fprintf(os.Stderr, "DEBUG: after ** p=%q\n", p)
	p = strings.ReplaceAll(p, "\\*", "[^/]*")
	p = strings.ReplaceAll(p, "\\?", ".")

	if isRooted {
		p = "^" + p
	} else if !strings.Contains(pattern, "/") {
		// Pattern with no slashes matches anywhere (at start or after a slash).
		p = "(^|/)" + p
	} else {
		// Pattern with slashes but not at the start is relative to root.
		p = "^" + p
	}

	// Ensure we match the whole string or a directory prefix.
	p = p + "($|/)"

	return regexp.Compile(p)
}
