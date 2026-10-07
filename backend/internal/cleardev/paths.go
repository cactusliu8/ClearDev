package cleardev

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidRepositoryPath = errors.New("cleardev: path must be a normalized repository-relative slash path")

// PathRules is one immutable version of a work item's four path authorities.
type PathRules struct {
	WritePaths                 []string `json:"writePaths"`
	GeneratedPaths             []string `json:"generatedPaths"`
	SharedPathsRequireApproval []string `json:"sharedPathsRequireApproval"`
	ForbiddenPaths             []string `json:"forbiddenPaths"`
}

// PathClassification is the deterministic result of applying PathRules.
type PathClassification string

const (
	PathForbidden              PathClassification = "FORBIDDEN"
	PathRequiresApproval       PathClassification = "REQUIRES_APPROVAL"
	PathRequiresGeneratedProof PathClassification = "REQUIRES_GENERATED_PROOF"
	PathAllowed                PathClassification = "ALLOWED"
	PathUnlisted               PathClassification = "UNLISTED"
)

// Validate checks every path pattern before rules are persisted or used.
func (r PathRules) Validate() error {
	for name, patterns := range map[string][]string{
		"write_paths":                   r.WritePaths,
		"generated_paths":               r.GeneratedPaths,
		"shared_paths_require_approval": r.SharedPathsRequireApproval,
		"forbidden_paths":               r.ForbiddenPaths,
	} {
		for _, pattern := range patterns {
			if err := validateRepositoryPath(pattern, true); err != nil {
				return fmt.Errorf("%s pattern %q: %w", name, pattern, err)
			}
		}
	}
	return nil
}

// ClassifyPath applies the frozen S01 priority: forbidden, shared, generated,
// write, then unlisted. The result deliberately does not claim a candidate has
// passed scope evaluation; shared and generated paths still require evidence.
func (r PathRules) ClassifyPath(path string) (PathClassification, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	if err := validateRepositoryPath(path, false); err != nil {
		return "", err
	}
	if matchesAny(r.ForbiddenPaths, path) {
		return PathForbidden, nil
	}
	if matchesAny(r.SharedPathsRequireApproval, path) {
		return PathRequiresApproval, nil
	}
	if matchesAny(r.GeneratedPaths, path) {
		return PathRequiresGeneratedProof, nil
	}
	if matchesAny(r.WritePaths, path) {
		return PathAllowed, nil
	}
	return PathUnlisted, nil
}

func validateRepositoryPath(value string, isPattern bool) error {
	if value == "" || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "!") || isWindowsAbsolutePath(value) {
		return ErrInvalidRepositoryPath
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return ErrInvalidRepositoryPath
		}
		if isPattern && strings.Contains(part, "**") && part != "**" {
			return ErrInvalidRepositoryPath
		}
	}
	return nil
}

func isWindowsAbsolutePath(value string) bool {
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && value[2] == '/'
}

func matchesAny(patterns []string, path string) bool {
	for _, pattern := range patterns {
		if matchRepositoryPattern(strings.Split(pattern, "/"), strings.Split(path, "/")) {
			return true
		}
	}
	return false
}

func matchRepositoryPattern(pattern, path []string) bool {
	type state struct{ pattern, path int }
	memo := make(map[state]bool)
	seen := make(map[state]bool)
	var match func(int, int) bool
	match = func(patternIndex, pathIndex int) bool {
		key := state{patternIndex, pathIndex}
		if seen[key] {
			return memo[key]
		}
		seen[key] = true
		var result bool
		if patternIndex == len(pattern) {
			result = pathIndex == len(path)
		} else if pattern[patternIndex] == "**" {
			if match(patternIndex+1, pathIndex) {
				result = true
			} else if pathIndex < len(path) && match(patternIndex, pathIndex+1) {
				result = true
			}
		} else if pathIndex < len(path) && matchPathSegment(pattern[patternIndex], path[pathIndex]) {
			result = match(patternIndex+1, pathIndex+1)
		}
		memo[key] = result
		return result
	}
	return match(0, 0)
}

func matchPathSegment(pattern, value string) bool {
	patternIndex, valueIndex := 0, 0
	star, afterStar := -1, 0
	for valueIndex < len(value) {
		if patternIndex < len(pattern) && pattern[patternIndex] == '*' {
			star = patternIndex
			patternIndex++
			afterStar = valueIndex
			continue
		}
		if patternIndex < len(pattern) && pattern[patternIndex] == value[valueIndex] {
			patternIndex++
			valueIndex++
			continue
		}
		if star < 0 {
			return false
		}
		patternIndex = star + 1
		afterStar++
		valueIndex = afterStar
	}
	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pattern)
}
