package gate

import "testing"

func TestGlobMatches(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"docs/**", "docs/adr/0011-loop-pr-credential-flow.md", true},
		{"docs/**", "internal/gate/pattern.go", false},
		{"internal/order/**", "internal/order/order.go", true},
		{"internal/order/**", "internal/order/nested/deep/file.go", true},
		{"internal/order/**", "internal/orderbook/orderbook.go", false},
		{"**", "anything/at/all.go", true},
		{"*.md", "README.md", true},       // single segment, no "**": matches a root-level file only
		{"*.md", "docs/README.md", false}, // "*" never crosses a "/" and this pattern has no "**"
		{".github/**", ".github/workflows/verdict-gate.yml", true},
		{".github/**", ".githubx/foo", false}, // "**" is anchored after the literal "/", not a bare prefix match
	}
	for _, tt := range tests {
		if got := globMatches(tt.pattern, tt.path); got != tt.want {
			t.Errorf("globMatches(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}
