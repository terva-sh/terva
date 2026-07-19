package config

import "testing"

func TestNativeOutputConfigHelpers(t *testing.T) {
	if (*NativeOutputConfig)(nil).IsEnabled() {
		t.Error("nil config must not be enabled")
	}
	if (&NativeOutputConfig{}).IsEnabled() {
		t.Error("nil Enabled must not be enabled")
	}
	no := false
	if (&NativeOutputConfig{Enabled: &no}).IsEnabled() {
		t.Error("Enabled=false must not be enabled")
	}
	yes := true
	if !(&NativeOutputConfig{Enabled: &yes}).IsEnabled() {
		t.Error("Enabled=true must be enabled")
	}

	if got := (*NativeOutputConfig)(nil).EditHistoryOr(1); got != 1 {
		t.Errorf("nil EditHistoryOr(1) = %d, want 1", got)
	}
	if got := (&NativeOutputConfig{}).EditHistoryOr(1); got != 1 {
		t.Errorf("unset EditHistoryOr(1) = %d, want 1", got)
	}
	zero := 0
	if got := (&NativeOutputConfig{EditHistory: &zero}).EditHistoryOr(1); got != 0 {
		t.Errorf("EditHistory=0 EditHistoryOr(1) = %d, want 0 (editing disabled)", got)
	}
	three := 3
	if got := (&NativeOutputConfig{EditHistory: &three}).EditHistoryOr(1); got != 3 {
		t.Errorf("EditHistory=3 EditHistoryOr(1) = %d, want 3", got)
	}
}
