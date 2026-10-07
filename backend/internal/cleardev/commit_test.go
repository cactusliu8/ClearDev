package cleardev

import (
	"strings"
	"testing"
)

func TestNormalizeCommitSHA(t *testing.T) {
	sha40 := strings.Repeat("A", 40)
	sha64 := strings.Repeat("b", 64)
	tests := []struct {
		name  string
		value string
		want  string
		ok    bool
	}{
		{"sha1", sha40, strings.ToLower(sha40), true},
		{"sha256", sha64, sha64, true},
		{"short", strings.Repeat("a", 39), "", false},
		{"long", strings.Repeat("a", 65), "", false},
		{"not hex", strings.Repeat("g", 40), "", false},
		{"space", " " + strings.Repeat("a", 39), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeCommitSHA(tt.value)
			if (err == nil) != tt.ok || got != tt.want {
				t.Fatalf("NormalizeCommitSHA(%q) = %q, %v; want %q, ok=%v", tt.value, got, err, tt.want, tt.ok)
			}
			if IsFullCommitSHA(tt.value) != tt.ok {
				t.Fatalf("IsFullCommitSHA(%q) = %v, want %v", tt.value, IsFullCommitSHA(tt.value), tt.ok)
			}
		})
	}
}
