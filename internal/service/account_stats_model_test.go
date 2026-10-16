//go:build unit

package service

import "testing"

func TestResolveAccountStatsModel_UsesKnownUpstreamModel(t *testing.T) {
	got := resolveAccountStatsModel(&UsageLog{RequestedModel: "gpt-5.6-sol"}, "gpt-5.6-luna", "gpt-5.6-sol")
	if got != "gpt-5.6-luna" {
		t.Fatalf("expected final upstream model, got %q", got)
	}
}

func TestResolveAccountStatsModel_FallsBackWithoutMappingEvidence(t *testing.T) {
	got := resolveAccountStatsModel(&UsageLog{RequestedModel: "gpt-5.6-sol", ChannelMappedModel: "gpt-5.6-sol"}, "", " gpt-5.6-sol ")
	if got != "gpt-5.6-sol" {
		t.Fatalf("expected requested model fallback, got %q", got)
	}
}

func TestResolveAccountStatsModel_FailsClosedWhenMappingEvidenceExists(t *testing.T) {
	chain := "gpt-5.6-sol→gpt-5.6-luna"
	got := resolveAccountStatsModel(&UsageLog{
		RequestedModel:     "gpt-5.6-sol",
		ChannelMappedModel: "gpt-5.6-luna",
		ModelMappingChain:  &chain,
	}, "", "gpt-5.6-sol")
	if got != "" {
		t.Fatalf("expected empty model for mapped request with missing upstream evidence, got %q", got)
	}
}

func TestResolveAccountStatsModel_AccountMappingEvidenceAloneFailsClosed(t *testing.T) {
	chain := "gpt-5.6-sol→gpt-5.6-luna→gpt-5.6-terra"
	got := resolveAccountStatsModel(&UsageLog{ModelMappingChain: &chain}, "", "gpt-5.6-sol")
	if got != "" {
		t.Fatalf("expected empty model when mapping chain is present, got %q", got)
	}
}

func TestUsageLogHasModelMappingEvidence_NilAndNoop(t *testing.T) {
	if usageLogHasModelMappingEvidence(nil) {
		t.Fatal("nil usage log must not imply mapping evidence")
	}
	if usageLogHasModelMappingEvidence(&UsageLog{RequestedModel: "model", ChannelMappedModel: "model"}) {
		t.Fatal("no-op mapping fields must not imply mapping evidence")
	}
}
