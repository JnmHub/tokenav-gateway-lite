//go:build unit

package service

import (
	"errors"
	"testing"
)

func TestNormalizeMonitorPrimaryModel_GrokDefaultsToGrok45(t *testing.T) {
	if got := normalizeMonitorPrimaryModel(MonitorProviderGrok, "  "); got != MonitorDefaultGrokModel {
		t.Fatalf("empty Grok monitor model should default to %q, got %q", MonitorDefaultGrokModel, got)
	}
	if got := normalizeMonitorPrimaryModel(MonitorProviderGrok, " grok-4 "); got != "grok-4" {
		t.Fatalf("explicit Grok monitor model should be preserved, got %q", got)
	}
	if got := normalizeMonitorPrimaryModel(MonitorProviderOpenAI, "  "); got != "" {
		t.Fatalf("non-Grok monitor should keep the existing required-model behavior, got %q", got)
	}
}

func TestValidateAPIMode_GrokOnlyAllowsChatCompletions(t *testing.T) {
	if err := validateAPIMode(MonitorProviderGrok, MonitorAPIModeChatCompletions); err != nil {
		t.Fatalf("Grok chat completions mode should be valid: %v", err)
	}
	if err := validateAPIMode(MonitorProviderGrok, MonitorAPIModeResponses); !errors.Is(err, ErrChannelMonitorInvalidAPIMode) {
		t.Fatalf("Grok responses mode should be rejected, got %v", err)
	}
}

func TestApplyMonitorUpdate_SwitchToGrokResetsProtocolDefaults(t *testing.T) {
	existing := &ChannelMonitor{
		Provider:         MonitorProviderOpenAI,
		APIMode:          MonitorAPIModeResponses,
		PrimaryModel:     "gpt-5.5",
		IntervalSeconds:  300,
		JitterSeconds:    0,
		BodyOverrideMode: MonitorBodyOverrideModeOff,
	}
	provider := MonitorProviderGrok

	if err := applyMonitorUpdate(existing, ChannelMonitorUpdateParams{Provider: &provider}); err != nil {
		t.Fatalf("switching a monitor to Grok should succeed: %v", err)
	}
	if existing.PrimaryModel != MonitorDefaultGrokModel {
		t.Fatalf("switching to Grok should reset the primary model to %q, got %q", MonitorDefaultGrokModel, existing.PrimaryModel)
	}
	if existing.APIMode != MonitorAPIModeChatCompletions {
		t.Fatalf("switching to Grok should reset API mode to chat completions, got %q", existing.APIMode)
	}
}

func TestApplyMonitorUpdate_GrokBlankModelUsesDefault(t *testing.T) {
	existing := &ChannelMonitor{
		Provider:         MonitorProviderGrok,
		APIMode:          MonitorAPIModeChatCompletions,
		PrimaryModel:     "grok-4",
		IntervalSeconds:  300,
		JitterSeconds:    0,
		BodyOverrideMode: MonitorBodyOverrideModeOff,
	}
	blank := "  "

	if err := applyMonitorUpdate(existing, ChannelMonitorUpdateParams{PrimaryModel: &blank}); err != nil {
		t.Fatalf("blank Grok monitor model should use the default: %v", err)
	}
	if existing.PrimaryModel != MonitorDefaultGrokModel {
		t.Fatalf("blank Grok monitor model should become %q, got %q", MonitorDefaultGrokModel, existing.PrimaryModel)
	}
}

func TestValidateBodyModeForProtocol_GrokReplaceRequiresMessages(t *testing.T) {
	err := validateBodyModeForProtocol(
		MonitorProviderGrok,
		MonitorAPIModeChatCompletions,
		MonitorBodyOverrideModeReplace,
		map[string]any{"model": MonitorDefaultGrokModel},
	)
	if !errors.Is(err, ErrChannelMonitorInvalidRequestBody) {
		t.Fatalf("Grok replace mode without messages should be rejected, got %v", err)
	}
}
