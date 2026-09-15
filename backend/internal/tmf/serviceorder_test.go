package tmf

import "testing"

func TestServiceOrderState(t *testing.T) {
	tests := []struct {
		name      string
		cancelled bool
		states    []string
		want      string
	}{
		{"cancelled", true, []string{"PENDING"}, "cancelled"},
		{"acknowledged", false, []string{"PENDING", "PENDING"}, "acknowledged"},
		{"completed", false, []string{"COMPLETED", "SKIPPED"}, "completed"},
		{"failed", false, []string{"FAILED"}, "failed"},
		{"partial", false, []string{"COMPLETED", "FAILED"}, "partial"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ServiceOrderState(tt.cancelled, tt.states); got != tt.want {
				t.Fatalf("ServiceOrderState() = %q, want %q", got, tt.want)
			}
		})
	}
}
