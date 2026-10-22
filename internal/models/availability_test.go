package models

import (
	"testing"
	"time"
)

// Defer must move the next probe without rewriting what the last real probe
// learned: a rate-limited probe is not evidence about the model.
func TestDeferKeepsAvailabilityRecord(t *testing.T) {
	reg, err := AvailabilityStore("")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	now := time.Now().UTC()
	if err := reg.Record("m", 0, true, "probe_succeeded", "anonymous", now, EffortProbe{Effort: "high", Protocol: "chat"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	before := reg.Get("m")
	next := now.Add(15 * time.Minute)
	if err := reg.Defer("m", 0, next, now); err != nil {
		t.Fatalf("defer: %v", err)
	}
	after := reg.Get("m")
	if after.Disabled != before.Disabled || after.Reason != before.Reason {
		t.Fatalf("defer changed the record: %+v -> %+v", before, after)
	}
	if after.AutoEffort != before.AutoEffort {
		t.Fatalf("defer changed the effort observation: %+v -> %+v", before.AutoEffort, after.AutoEffort)
	}
	if !after.NextCheck.Equal(next) {
		t.Fatalf("NextCheck = %v, want %v", after.NextCheck, next)
	}
}
