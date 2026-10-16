package service

import "testing"

func TestAPIKeyServiceRejectsV15SnapshotWithoutRPMOverrideResolutionState(t *testing.T) {
	groupID := int64(9)
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-models-list", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{
			Version:  15,
			APIKeyID: 1,
			UserID:   2,
			GroupID:  &groupID,
			Status:   StatusActive,
			User: APIKeyAuthUserSnapshot{
				ID:          2,
				Status:      StatusActive,
				Role:        RoleUser,
				Balance:     10,
				Concurrency: 3,
			},
			Group: &APIKeyAuthGroupSnapshot{
				ID:               groupID,
				Name:             "openai",
				Platform:         PlatformOpenAI,
				Status:           StatusActive,
				SubscriptionType: SubscriptionTypeStandard,
				RateMultiplier:   1,
			},
		},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatalf("expected v15 auth snapshot to be rejected after RPM override resolution state was added")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

func TestAPIKeyServiceRejectsV17SnapshotWithoutBillingModelSource(t *testing.T) {
	groupID := int64(9)
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-billing-source", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{
			Version:  17,
			APIKeyID: 1,
			UserID:   2,
			GroupID:  &groupID,
			Status:   StatusActive,
			User: APIKeyAuthUserSnapshot{
				ID:          2,
				Status:      StatusActive,
				Role:        RoleUser,
				Balance:     10,
				Concurrency: 3,
			},
			Group: &APIKeyAuthGroupSnapshot{
				ID:               groupID,
				Name:             "openai",
				Platform:         PlatformOpenAI,
				Status:           StatusActive,
				SubscriptionType: SubscriptionTypeStandard,
				RateMultiplier:   1,
			},
		},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok || apiKey != nil {
		t.Fatalf("expected v17 auth snapshot to be rejected after billing model source was added")
	}
}

func TestAPIKeyAuthSnapshotPreservesBillingSourceAndGroupFallbackModel(t *testing.T) {
	groupID := int64(9)
	svc := &APIKeyService{}
	snapshot, err := svc.snapshotFromAPIKey(t.Context(), &APIKey{
		ID:      1,
		UserID:  2,
		GroupID: &groupID,
		Status:  StatusActive,
		User: &User{
			ID:          2,
			Status:      StatusActive,
			Role:        RoleUser,
			Balance:     10,
			Concurrency: 3,
		},
		Group: &Group{
			ID:                 groupID,
			Name:               "openai",
			Platform:           PlatformOpenAI,
			Status:             StatusActive,
			SubscriptionType:   SubscriptionTypeStandard,
			RateMultiplier:     1,
			BillingModelSource: BillingModelSourceResponse,
			FallbackModel:      "gpt-5.6-sol",
			FailoverGroupID:    func() *int64 { v := int64(10); return &v }(),
			FailoverStrategy:   FailoverStrategyCurrentGroupFirst,
		},
	})
	if err != nil {
		t.Fatalf("build auth snapshot: %v", err)
	}
if snapshot.Version != 21 {
			t.Fatalf("expected auth snapshot version 21, got %d", snapshot.Version)
	}
	if snapshot.Group == nil || snapshot.Group.BillingModelSource != BillingModelSourceResponse || snapshot.Group.FallbackModel != "gpt-5.6-sol" || snapshot.Group.FailoverGroupID == nil || *snapshot.Group.FailoverGroupID != 10 || snapshot.Group.FailoverStrategy != FailoverStrategyCurrentGroupFirst {
		t.Fatalf("expected billing source and fallback model in snapshot, got %#v", snapshot.Group)
	}

	restored := svc.snapshotToAPIKey("glk-test", snapshot)
	if restored.Group == nil || restored.Group.BillingModelSource != BillingModelSourceResponse || restored.Group.FallbackModel != "gpt-5.6-sol" || restored.Group.FailoverGroupID == nil || *restored.Group.FailoverGroupID != 10 || restored.Group.FailoverStrategy != FailoverStrategyCurrentGroupFirst {
		t.Fatalf("expected billing source and fallback model after snapshot restore, got %#v", restored.Group)
	}
}
