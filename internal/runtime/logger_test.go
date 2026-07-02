package runtime

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestNewLogger_EmitsJSON verifies the factory produces a structured JSON
// logger: the sole post-mortem diagnosis surface for an unattended process
// must be machine-parseable.
func TestNewLogger_EmitsJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf)

	logger.Info("startup", "base_url", "https://example.test")

	line := strings.TrimSpace(buf.String())
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("log line is not valid JSON: %v\nline: %s", err, line)
	}
	if record["msg"] != "startup" {
		t.Fatalf("msg = %v, want startup", record["msg"])
	}
	if record["base_url"] != "https://example.test" {
		t.Fatalf("base_url = %v, want the injected value", record["base_url"])
	}
}
