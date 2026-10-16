package main

import (
	"testing"
	"time"
)

func TestApplyRuntimeTimezoneDefaultsToShanghai(t *testing.T) {
	previous := time.Local
	t.Cleanup(func() { time.Local = previous })

	if err := applyRuntimeTimezone(""); err != nil {
		t.Fatalf("apply default timezone: %v", err)
	}
	if time.Local.String() != "Asia/Shanghai" {
		t.Fatalf("local timezone=%q, want Asia/Shanghai", time.Local.String())
	}
}

func TestApplyRuntimeTimezoneRejectsUnknownZone(t *testing.T) {
	previous := time.Local
	t.Cleanup(func() { time.Local = previous })

	if err := applyRuntimeTimezone("Mars/Olympus"); err == nil {
		t.Fatal("expected invalid timezone error")
	}
}
