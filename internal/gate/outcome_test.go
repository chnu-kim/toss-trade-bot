package gate

import "testing"

func TestOutcome_NonApprove(t *testing.T) {
	tests := []struct {
		name string
		o    Outcome
		want bool
	}{
		{"approve does not count as non-approve", OutcomeApprove, false},
		{"reject counts as non-approve", OutcomeReject, true},
		{"indeterminate counts as non-approve", OutcomeIndeterminate, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.o.NonApprove(); got != tt.want {
				t.Errorf("Outcome(%v).NonApprove() = %v, want %v", tt.o, got, tt.want)
			}
		})
	}
}

func TestOutcome_String(t *testing.T) {
	tests := []struct {
		o    Outcome
		want string
	}{
		{OutcomeApprove, "approve"},
		{OutcomeReject, "reject"},
		{OutcomeIndeterminate, "indeterminate"},
	}
	for _, tt := range tests {
		if got := tt.o.String(); got != tt.want {
			t.Errorf("Outcome.String() = %q, want %q", got, tt.want)
		}
	}
}
