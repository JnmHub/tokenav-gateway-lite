package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestShouldRunSetupWizardSkipsGatewayLiteMode(t *testing.T) {
	if shouldRunSetupWizard(config.RunModeGatewayLite, true) {
		t.Fatal("gateway-lite mode must not enter setup wizard")
	}
	if shouldRunSetupWizard("GATEWAY-LITE", true) {
		t.Fatal("gateway-lite mode normalization must not enter setup wizard")
	}
}

func TestShouldRunSetupWizardKeepsStandardSetup(t *testing.T) {
	if !shouldRunSetupWizard(config.RunModeStandard, true) {
		t.Fatal("standard mode should still enter setup wizard when setup is needed")
	}
	if shouldRunSetupWizard(config.RunModeStandard, false) {
		t.Fatal("standard mode should skip setup wizard when setup is not needed")
	}
}

func TestResolveRunModeForSetupDefaultsToGatewayLite(t *testing.T) {
	t.Setenv("RUN_MODE", "")
	if got := resolveRunModeForSetup(); got != config.RunModeGatewayLite {
		t.Fatalf("default run mode = %q, want gateway-lite", got)
	}
}

func TestShutdownAndCleanupRunsCleanupAfterSuccessfulShutdown(t *testing.T) {
	steps := make([]string, 0, 2)
	cleanup := onceCleanup(func() { steps = append(steps, "cleanup") })
	err := shutdownAndCleanup(context.Background(), func(context.Context) error {
		steps = append(steps, "shutdown")
		return nil
	}, cleanup)
	if err != nil {
		t.Fatalf("shutdownAndCleanup() error = %v", err)
	}
	if got, want := len(steps), 2; got != want || steps[0] != "shutdown" || steps[1] != "cleanup" {
		t.Fatalf("steps = %v, want [shutdown cleanup]", steps)
	}
}

func TestShutdownAndCleanupStillCleansOnceOnTimeout(t *testing.T) {
	timeoutErr := context.DeadlineExceeded
	cleanupCalls := 0
	cleanup := onceCleanup(func() { cleanupCalls++ })

	err := shutdownAndCleanup(context.Background(), func(context.Context) error {
		return timeoutErr
	}, cleanup)
	// 模拟 runMainServer 返回时 defer 再调用一次。
	cleanup()

	if !errors.Is(err, timeoutErr) {
		t.Fatalf("shutdownAndCleanup() error = %v, want deadline exceeded", err)
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", cleanupCalls)
	}
}
