package cleardev

import "testing"

func TestPathRulesClassifyPathPriorityAndGlobs(t *testing.T) {
	rules := PathRules{
		WritePaths:                 []string{"src/*", "docs/**"},
		GeneratedPaths:             []string{"src/generated/**"},
		SharedPathsRequireApproval: []string{"src/shared/**"},
		ForbiddenPaths:             []string{"src/shared/secrets/**", ".git/**"},
	}
	tests := []struct {
		name string
		path string
		want PathClassification
	}{
		{"write", "src/main.go", PathAllowed},
		{"double star zero segments", "docs", PathAllowed},
		{"double star nested", "docs/design/plan.md", PathAllowed},
		{"generated wins write", "src/generated/api.go", PathRequiresGeneratedProof},
		{"shared wins generated write", "src/shared/generated.go", PathRequiresApproval},
		{"forbidden wins shared", "src/shared/secrets/key.txt", PathForbidden},
		{"unlisted", "go.mod", PathUnlisted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rules.ClassifyPath(tt.path)
			if err != nil || got != tt.want {
				t.Fatalf("ClassifyPath(%q) = %q, %v; want %q", tt.path, got, err, tt.want)
			}
		})
	}
}

func TestPathRulesRejectInvalidPatternsAndPaths(t *testing.T) {
	invalid := []string{"", "/absolute", "C:/absolute", ".", "..", "a/../b", "a\\b", "a\x00b", "a//b"}
	for _, value := range invalid {
		t.Run("path "+value, func(t *testing.T) {
			if _, err := (PathRules{}).ClassifyPath(value); err == nil {
				t.Fatalf("ClassifyPath(%q) succeeded", value)
			}
		})
	}
	for _, pattern := range append(invalid, "a/***/b", "!a/**") {
		t.Run("pattern "+pattern, func(t *testing.T) {
			rules := PathRules{WritePaths: []string{pattern}}
			if err := rules.Validate(); err == nil {
				t.Fatalf("Validate(%q) succeeded", pattern)
			}
		})
	}
}
