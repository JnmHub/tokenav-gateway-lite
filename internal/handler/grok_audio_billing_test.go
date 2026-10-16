package handler

import (
	"testing"
	"time"
)

func TestGrokRealtimeBillingResultRequiresObservedAudio(t *testing.T) {
	if grokRealtimeBillingResult("grok-voice-latest", "grok-voice-latest", time.Second, false) != nil {
		t.Fatal("没有观察到音频的会话不能计费")
	}
	if grokRealtimeBillingResult("grok-voice-latest", "grok-voice-latest", 0, true) != nil {
		t.Fatal("零时长会话不能计费")
	}
	result := grokRealtimeBillingResult("grok-voice-latest", "grok-voice-latest", 90*time.Second, true)
	if result == nil || result.AudioUsage == nil || result.AudioUsage.DurationOrUnits != 1.5 {
		t.Fatalf("真实音频会话应按分钟计费: %#v", result)
	}
	if result.UpstreamModel != "grok-voice-latest" {
		t.Fatalf("应记录最终上游模型: %#v", result)
	}
}

func TestGrokRealtimeBillingResultKeepsMappedUpstreamModel(t *testing.T) {
	result := grokRealtimeBillingResult("grok-public", "grok-upstream", time.Second, true)
	if result == nil || result.Model != "grok-public" || result.UpstreamModel != "grok-upstream" {
		t.Fatalf("应分别保留客户模型和上游模型: %#v", result)
	}
}
