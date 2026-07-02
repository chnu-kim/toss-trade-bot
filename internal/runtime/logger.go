package runtime

import (
	"io"
	"log/slog"
)

// NewLogger builds the process-wide structured logger. Output is JSON so an
// unattended process leaves machine-parseable, greppable diagnostics — the only
// post-mortem surface when no operator is watching.
//
// The writer is injected (os.Stdout in production, a buffer in tests) so the
// logging foundation stays free of any file/rotation/external-sink concern;
// those belong to the future durable audit-sink issue (ADR-0005 point 6), not
// here.
func NewLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, nil))
}
