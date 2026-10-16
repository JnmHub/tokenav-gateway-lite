package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type grokVideoUsageReporterStub struct {
	mu     sync.Mutex
	events []gatewaylite.UsageEvent
}

type grokVideoConcurrentBillingRepo struct {
	UsageBillingRepository
	mu    sync.Mutex
	calls int
}

func (r *grokVideoConcurrentBillingRepo) Apply(_ context.Context, _ *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	// Keep the settlement lock occupied briefly so the competing goroutine
	// exercises both the lock-busy and post-unlock reload paths.
	time.Sleep(10 * time.Millisecond)
	return &UsageBillingApplyResult{Applied: true}, nil
}

func (r *grokVideoConcurrentBillingRepo) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (s *grokVideoUsageReporterStub) ReportUsage(_ context.Context, event gatewaylite.UsageEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *grokVideoUsageReporterStub) snapshot() []gatewaylite.UsageEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]gatewaylite.UsageEvent(nil), s.events...)
}

type grokVideoTaskMemoryStore struct {
	mu       sync.Mutex
	task     *GrokVideoTask
	locked   bool
	saveErr  error
	lockErr  error
	saveCall int
}

func (s *grokVideoTaskMemoryStore) Ready(_ context.Context) error {
	return nil
}

func (s *grokVideoTaskMemoryStore) ListDue(_ context.Context, now time.Time, _ int) ([]*GrokVideoTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task == nil || !s.task.NeedsBackgroundWork() || (s.task.NextPollAt != nil && s.task.NextPollAt.After(now)) {
		return nil, nil
	}
	copyTask := *s.task
	return []*GrokVideoTask{&copyTask}, nil
}

func (s *grokVideoTaskMemoryStore) TryLockPolling(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return true, nil
}

func (s *grokVideoTaskMemoryStore) UnlockPolling(_ context.Context, _ string) error {
	return nil
}

func (s *grokVideoTaskMemoryStore) Save(_ context.Context, task *GrokVideoTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	copyTask := *task
	s.task = &copyTask
	s.saveCall++
	return nil
}

func (s *grokVideoTaskMemoryStore) Get(_ context.Context, requestID string) (*GrokVideoTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task == nil || s.task.RequestID != requestID {
		return nil, ErrGrokVideoTaskNotFound
	}
	copyTask := *s.task
	return &copyTask, nil
}

func (s *grokVideoTaskMemoryStore) TryLockSettlement(_ context.Context, _ string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lockErr != nil {
		return false, s.lockErr
	}
	if s.locked {
		return false, nil
	}
	s.locked = true
	return true, nil
}

func (s *grokVideoTaskMemoryStore) UnlockSettlement(_ context.Context, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locked = false
	return nil
}

func (s *grokVideoTaskMemoryStore) CleanupFinalized(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

func TestParseGrokVideoTaskStatus(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		state string
		dur   int
		res   string
	}{
		{name: "pending", body: `{"status":"processing"}`, state: GrokVideoTaskStateRunning},
		{name: "success with metadata", body: `{"status":"completed","video":{"url":"https://cdn.example/v.mp4","duration_seconds":12,"resolution":"720p"}}`, state: GrokVideoTaskStateSucceeded, dur: 12, res: "720p"},
		{name: "url implies success", body: `{"data":{"video":{"url":"https://cdn.example/v.mp4"}}}`, state: GrokVideoTaskStateSucceeded},
		{name: "failure", body: `{"status":"failed","error":{"message":"blocked"}}`, state: GrokVideoTaskStateFailed},
		{name: "expired", body: `{"status":"expired","error":{"message":"result expired"}}`, state: GrokVideoTaskStateFailed},
		{name: "cancelled", body: `{"status":"cancelled","error":{"message":"cancelled by user"}}`, state: GrokVideoTaskStateCancelled},
		{name: "unknown remains non terminal", body: `{"status":"new_future_state","id":"v_1"}`, state: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseGrokVideoTaskStatus([]byte(tt.body))
			require.Equal(t, tt.state, got.State)
			require.Equal(t, tt.dur, got.DurationSeconds)
			require.Equal(t, tt.res, got.Resolution)
		})
	}
}

func TestParseGrokVideoTaskStatusForAccountSeparatesOfficialAndCompatibleUpstreams(t *testing.T) {
	official := &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey}
	compatible := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://relay.example.com/v1",
		},
	}

	require.Equal(t, GrokVideoTaskStateSucceeded, ParseGrokVideoTaskStatusForAccount(
		official,
		[]byte(`{"status":"done","video":{"url":"https://vidgen.x.ai/v.mp4"}}`),
	).State)
	require.Empty(t, ParseGrokVideoTaskStatusForAccount(
		official,
		[]byte(`{"status":"done"}`),
	).State)
	require.Empty(t, ParseGrokVideoTaskStatusForAccount(
		official,
		[]byte(`{"status":"completed","video":{"url":"https://vidgen.x.ai/v.mp4"}}`),
	).State)

	require.Equal(t, GrokVideoTaskStateSucceeded, ParseGrokVideoTaskStatusForAccount(
		compatible,
		[]byte(`{"status":"completed","video":{"url":"https://cdn.example/v.mp4"}}`),
	).State)
	require.Equal(t, GrokVideoTaskStateSucceeded, ParseGrokVideoTaskStatusForAccount(
		compatible,
		[]byte(`{"data":{"video":{"url":"https://cdn.example/v.mp4"}}}`),
	).State)
}

func TestRegisterGrokVideoTaskStoresNonSensitiveBillingContext(t *testing.T) {
	store := &grokVideoTaskMemoryStore{}
	svc := &OpenAIGatewayService{grokVideoTaskStore: store}
	groupID := int64(8)
	subscriptionID := int64(11)
	task, err := svc.RegisterGrokVideoTask(context.Background(), GrokVideoTaskRegistration{
		APIKey:       &APIKey{ID: 2, User: &User{ID: 3}, GroupID: &groupID},
		Account:      &Account{ID: 4, Platform: PlatformGrok, Credentials: map[string]any{"access_token": "must-not-copy"}},
		Subscription: &UserSubscription{ID: subscriptionID},
		Result: &OpenAIForwardResult{
			ResponseID:           "video-123",
			Model:                "grok-imagine-video-1.5",
			BillingModel:         "grok-imagine-video-1.5",
			UpstreamModel:        "grok-imagine-video-1.5",
			VideoCount:           1,
			VideoResolution:      "1080p",
			VideoDurationSeconds: 10,
		},
		RequestedModel:     "grok-imagine-video-1.5",
		RequestPayloadHash: "sha256:payload",
	})
	require.NoError(t, err)
	require.Equal(t, "video-123", task.RequestID)
	require.Equal(t, int64(4), task.AccountID)
	require.Equal(t, &subscriptionID, task.SubscriptionID)
	require.NotContains(t, task.RequestPayloadHash, "access_token")
	require.Equal(t, GrokVideoTaskStatePending, store.task.State)
}

func TestRegisterGrokVideoTaskPreservesCompositeDecisionForAsyncSettlement(t *testing.T) {
	store := &grokVideoTaskMemoryStore{}
	svc := &OpenAIGatewayService{grokVideoTaskStore: store}
	ctx := WithCompositeRouteDecision(context.Background(), CompositeRouteDecision{
		Matched: true, Source: CompositeRouteSourceExplicit, PublicModel: "studio-video",
		TargetPlatform: PlatformGrok, UpstreamModel: "grok-imagine-video-1.5",
	})
	task, err := svc.RegisterGrokVideoTask(ctx, GrokVideoTaskRegistration{
		APIKey:  &APIKey{ID: 2, User: &User{ID: 3}},
		Account: &Account{ID: 4, Platform: PlatformGrok},
		Result: &OpenAIForwardResult{
			ResponseID: "video-composite-1", Model: "grok-imagine-video-1.5",
			BillingModel: "grok-imagine-video-1.5", UpstreamModel: "grok-imagine-video-1.5",
		},
		RequestedModel: "studio-video",
	})
	require.NoError(t, err)
	require.Equal(t, "studio-video", task.RequestedModel)
	require.Equal(t, PlatformGrok, task.TargetPlatform)
	require.Equal(t, CompositeRouteSourceExplicit, task.CompositeRouteSource)

	restored := task.usageContext(context.Background())
	platform, ok := ResolvedTargetPlatformFromContext(restored)
	require.True(t, ok)
	require.Equal(t, PlatformGrok, platform)
	requested, ok := RequestedPublicModelFromContext(restored)
	require.True(t, ok)
	require.Equal(t, "studio-video", requested)
	source, ok := CompositeRouteSourceFromContext(restored)
	require.True(t, ok)
	require.Equal(t, CompositeRouteSourceExplicit, source)
}

func TestGrokVideoTransformTaskDurationUsesOperationSemantics(t *testing.T) {
	store := &grokVideoTaskMemoryStore{}
	svc := &OpenAIGatewayService{grokVideoTaskStore: store}
	apiKey := &APIKey{ID: 2, User: &User{ID: 3}}
	account := &Account{ID: 4, Platform: PlatformGrok}

	edit, err := svc.RegisterGrokVideoTask(context.Background(), GrokVideoTaskRegistration{
		APIKey: apiKey, Account: account, Operation: GrokVideoOperationEdit,
		Result: &OpenAIForwardResult{
			ResponseID: "video-edit-1", Model: "grok-imagine-video", BillingModel: "grok-imagine-video",
			VideoCount: 1, VideoResolution: "720p", VideoDurationSeconds: 0,
		},
	})
	require.NoError(t, err)
	require.Equal(t, GrokVideoOperationEdit, edit.Operation)
	require.Zero(t, edit.DurationSeconds, "edit must not inherit the generic eight second default")
	require.NoError(t, svc.UpdateGrokVideoTaskStatus(context.Background(), edit, GrokVideoTaskStatus{
		State: "done", DurationSeconds: 90, Resolution: "720p",
	}))
	require.Equal(t, 90, edit.DurationSeconds, "edit uses the exact inherited source duration instead of the 15 second generation clamp")

	extension := &GrokVideoTask{
		RequestID: "video-extension-1", Operation: GrokVideoOperationExtension,
		DurationSeconds: 5, Resolution: "720p", VideoCount: 1, State: GrokVideoTaskStatePending,
	}
	require.NoError(t, store.Save(context.Background(), extension))
	require.NoError(t, svc.UpdateGrokVideoTaskStatus(context.Background(), extension, GrokVideoTaskStatus{
		State: "done", DurationSeconds: 15, Resolution: "720p",
	}))
	require.Equal(t, 5, extension.DurationSeconds, "extension bills only the generated segment, not source plus extension")

	snapshot := &GrokVideoTaskBillingSnapshot{Rates: map[string]GrokVideoTaskRateQuote{
		"720p": {TotalUnitCost: 0.05, ActualUnitCost: 0.05, ScaleByDuration: true},
	}}
	require.InDelta(t, 4.5, snapshot.costForOperation(GrokVideoOperationEdit, "720p", 90, 1).ActualCost, 1e-9)
	require.InDelta(t, 0.25, snapshot.costForOperation(GrokVideoOperationExtension, "720p", 5, 1).ActualCost, 1e-9)
	require.InDelta(t, 0.75, snapshot.CostFor("720p", 90, 1).ActualCost, 1e-9, "normal generation remains capped by its 15 second request contract")
	require.Nil(t, snapshot.costForOperation(GrokVideoOperationEdit, "720p", GrokVideoTransformMaxBillableDurationSeconds+1, 1))
}

func TestPrepareGrokVideoTaskBillingExpandsGatewayReservation(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	quota := gatewaylite.NewRedisQuota(client, "grok-video-reservation")

	oldQuota := gatewaylite.DefaultRedisQuota()
	oldRuntimeRef := gatewaylite.DefaultRuntimeConfigRef()
	oldControlRef := gatewaylite.DefaultControlPlaneClientRef()
	gatewaylite.SetDefaultRedisQuota(quota)
	gatewaylite.SetDefaultRuntimeConfigRef(nil)
	gatewaylite.SetDefaultControlPlaneClientRef(nil)
	t.Cleanup(func() {
		gatewaylite.SetDefaultRedisQuota(oldQuota)
		gatewaylite.SetDefaultRuntimeConfigRef(oldRuntimeRef)
		gatewaylite.SetDefaultControlPlaneClientRef(oldControlRef)
	})

	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID:        "lease-video",
		UserID:         42,
		Region:         "jp",
		AllocatedCents: 100,
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
	}))
	ok, err := quota.Reserve(ctx, gatewaylite.ReserveRequest{
		RequestID:          "request-video",
		KeyID:              "key-video",
		UserID:             42,
		Region:             "jp",
		LeaseID:            "lease-video",
		EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, ok)

	reservation := gatewaylite.NewRequestReservation(1, "lease-video")
	ctx = gatewaylite.ContextWithRequestMeta(ctx, gatewaylite.RequestMeta{
		RequestID:   "request-video",
		KeyID:       "key-video",
		UserID:      42,
		LeaseID:     "lease-video",
		Region:      "jp",
		GatewayID:   "gateway-jp",
		Reservation: reservation,
	})
	price := 0.10
	groupID := int64(7)
	apiKey := &APIKey{
		ID:      9,
		UserID:  42,
		User:    &User{ID: 42},
		GroupID: &groupID,
		Group: &Group{
			ID:             groupID,
			Platform:       PlatformGrok,
			Status:         StatusActive,
			Hydrated:       true,
			RateMultiplier: 1,
			VideoPrice720P: &price,
		},
	}
	cfg := &config.Config{}
	svc := &OpenAIGatewayService{cfg: cfg, billingService: NewBillingService(cfg, nil)}

	snapshot, reservedCents, err := svc.PrepareGrokVideoTaskBilling(ctx, apiKey, "grok-imagine-video", "720p", 5, 1)

	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.EqualValues(t, 50, reservedCents)
	estimatedCents, leaseID, forceRefund := reservation.Snapshot()
	require.EqualValues(t, 50, estimatedCents)
	require.Equal(t, "lease-video", leaseID)
	require.False(t, forceRefund)
	leaseFields, err := client.HGetAll(ctx, "grok-video-reservation:lease:42:jp").Result()
	require.NoError(t, err)
	require.EqualValues(t, 50, gatewaylite.ParseInt64Field(leaseFields, "reserved_cents"))
}

func TestFinalizeGrokVideoTaskOnlyBillsSucceededTaskOnce(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(
		usageRepo,
		billingRepo,
		&openAIRecordUsageUserRepoStub{},
		&openAIRecordUsageSubRepoStub{},
		nil,
	)
	store := &grokVideoTaskMemoryStore{}
	svc.SetGrokVideoTaskStore(store)
	groupID := int64(7)
	apiKey := &APIKey{
		ID:      2,
		User:    &User{ID: 3},
		GroupID: &groupID,
		Group: &Group{
			ID:             groupID,
			Platform:       PlatformGrok,
			RateMultiplier: 1,
		},
	}
	account := &Account{ID: 4, Platform: PlatformGrok}
	task := &GrokVideoTask{
		RequestID:       "video-settle-1",
		APIKeyID:        apiKey.ID,
		UserID:          apiKey.User.ID,
		AccountID:       account.ID,
		GroupID:         &groupID,
		RequestedModel:  "grok-imagine-video",
		BillingModel:    "grok-imagine-video",
		UpstreamModel:   "grok-imagine-video",
		Resolution:      "720p",
		DurationSeconds: 5,
		VideoCount:      1,
		BillingSnapshot: &GrokVideoTaskBillingSnapshot{
			RateMultiplier:      1,
			VideoRateMultiplier: 1,
			Rates: map[string]GrokVideoTaskRateQuote{
				"720p": {TotalUnitCost: 0.07, ActualUnitCost: 0.07, ScaleByDuration: true},
			},
		},
		State:              GrokVideoTaskStatePending,
		CreatedAt:          time.Now().Add(-time.Second),
		RequestPayloadHash: "payload-1",
	}
	require.NoError(t, store.Save(context.Background(), task))

	// Pending and failure states never invoke the billing repository.
	require.NoError(t, svc.FinalizeGrokVideoTask(context.Background(), task, GrokVideoTaskStatus{State: "processing"}, apiKey, nil, account, &openAIRecordUsageAPIKeyQuotaStub{}))
	require.Zero(t, billingRepo.calls)

	failedTask := *task
	failedTask.RequestID = "video-failed"
	failedTask.State = GrokVideoTaskStatePending
	require.NoError(t, svc.FinalizeGrokVideoTask(context.Background(), &failedTask, GrokVideoTaskStatus{State: "failed", Message: "blocked"}, apiKey, nil, account, &openAIRecordUsageAPIKeyQuotaStub{}))
	require.Zero(t, billingRepo.calls)
	require.Equal(t, GrokVideoTaskStateFailed, failedTask.State)

	// A successful task is charged using a deterministic request ID and then
	// becomes terminal. Repeated polls do not invoke billing again.
	require.NoError(t, svc.FinalizeGrokVideoTask(context.Background(), task, GrokVideoTaskStatus{State: "completed", DurationSeconds: 5, Resolution: "720p"}, apiKey, nil, account, &openAIRecordUsageAPIKeyQuotaStub{}))
	require.Equal(t, 1, billingRepo.calls)
	require.Equal(t, "grok-video:video-settle-1", billingRepo.lastCmd.RequestID)
	require.Greater(t, billingRepo.lastCmd.BalanceCost, 0.0)
	require.Equal(t, GrokVideoTaskStateBilled, task.State)
	require.NotNil(t, task.BilledAt)

	require.NoError(t, svc.FinalizeGrokVideoTask(context.Background(), task, GrokVideoTaskStatus{State: "completed"}, apiKey, nil, account, &openAIRecordUsageAPIKeyQuotaStub{}))
	require.Equal(t, 1, billingRepo.calls)
}

func TestFinalizeGrokVideoTransformsUseOperationSpecificSeconds(t *testing.T) {
	tests := []struct {
		name              string
		operation         string
		requestedDuration int
		terminalDuration  int
		wantDuration      int
		wantCost          float64
	}{
		{name: "edit uses inherited terminal duration", operation: GrokVideoOperationEdit, terminalDuration: 90, wantDuration: 90, wantCost: 4.5},
		{name: "extension ignores total output duration", operation: GrokVideoOperationExtension, requestedDuration: 5, terminalDuration: 15, wantDuration: 5, wantCost: 0.25},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(
				usageRepo,
				billingRepo,
				&openAIRecordUsageUserRepoStub{},
				&openAIRecordUsageSubRepoStub{},
				nil,
			)
			store := &grokVideoTaskMemoryStore{}
			svc.SetGrokVideoTaskStore(store)
			groupID := int64(70 + index)
			apiKey := &APIKey{ID: int64(80 + index), User: &User{ID: int64(90 + index)}, GroupID: &groupID, Group: &Group{
				ID: groupID, Platform: PlatformGrok, RateMultiplier: 1,
			}}
			account := &Account{ID: int64(100 + index), Platform: PlatformGrok}
			task := &GrokVideoTask{
				RequestID: "video-transform-settle-" + tt.operation,
				Operation: tt.operation, APIKeyID: apiKey.ID, UserID: apiKey.User.ID, AccountID: account.ID, GroupID: &groupID,
				RequestedModel: "grok-imagine-video", BillingModel: "grok-imagine-video", UpstreamModel: "grok-imagine-video",
				Resolution: "720p", DurationSeconds: tt.requestedDuration, VideoCount: 1,
				BillingSnapshot: &GrokVideoTaskBillingSnapshot{RateMultiplier: 1, VideoRateMultiplier: 1, Rates: map[string]GrokVideoTaskRateQuote{
					"720p": {TotalUnitCost: 0.05, ActualUnitCost: 0.05, ScaleByDuration: true},
				}},
				State: GrokVideoTaskStatePending, CreatedAt: time.Now().Add(-time.Second),
			}
			require.NoError(t, store.Save(context.Background(), task))

			err := svc.FinalizeGrokVideoTask(context.Background(), task, GrokVideoTaskStatus{
				State: "done", DurationSeconds: tt.terminalDuration, Resolution: "720p", VideoCount: 1,
			}, apiKey, nil, account, &openAIRecordUsageAPIKeyQuotaStub{})
			require.NoError(t, err)
			require.Equal(t, GrokVideoTaskStateBilled, task.State)
			require.Equal(t, tt.wantDuration, task.DurationSeconds)
			require.Equal(t, 1, billingRepo.calls)
			require.InDelta(t, tt.wantCost, billingRepo.lastCmd.BalanceCost, 1e-9)
			require.Equal(t, tt.wantDuration, *usageRepo.lastLog.VideoDurationSeconds)
		})
	}
}

func TestFinalizeGrokVideoTaskFailureReleasesGatewayReservationOnce(t *testing.T) {
	reporter := &grokVideoUsageReporterStub{}
	gatewaylite.SetDefaultUsageReporter(reporter)
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(nil) })

	svc := &OpenAIGatewayService{}
	store := &grokVideoTaskMemoryStore{}
	svc.SetGrokVideoTaskStore(store)
	groupID := int64(9)
	task := &GrokVideoTask{
		RequestID:          "video-failed-reservation",
		QuotaRequestID:     "gateway-request-123",
		GatewayKeyID:       "key-123",
		LeaseID:            "lease-123",
		Region:             "jp",
		GatewayID:          "gateway-jp",
		ReservedCents:      40,
		UserID:             7,
		GroupID:            &groupID,
		GroupSnapshot:      &GrokVideoTaskGroupSnapshot{ID: groupID, Name: "Grok", Platform: PlatformGrok, RateMultiplier: 1.5},
		RequestedModel:     "grok-imagine-video",
		BillingModel:       "grok-imagine-video",
		UpstreamModel:      "grok-imagine-video",
		Resolution:         "720p",
		DurationSeconds:    5,
		VideoCount:         1,
		InboundEndpoint:    "/v1/videos/generations",
		State:              GrokVideoTaskStatePending,
		CreatedAt:          time.Now().Add(-time.Minute),
		RequestPayloadHash: "payload-failed-reservation",
	}
	require.NoError(t, store.Save(context.Background(), task))

	require.NoError(t, svc.FinalizeGrokVideoTask(
		context.Background(),
		task,
		GrokVideoTaskStatus{State: "failed", Message: "blocked"},
		nil,
		nil,
		nil,
		nil,
	))
	require.Equal(t, GrokVideoTaskStateFailed, task.State)
	require.NotNil(t, task.QuotaFinalizedAt)
	events := reporter.snapshot()
	require.Len(t, events, 1)
	require.Equal(t, "gateway-request-123", events[0].RequestID)
	require.Equal(t, "key-123", events[0].KeyID)
	require.Equal(t, "lease-123", events[0].LeaseID)
	require.EqualValues(t, 40, events[0].EstimatedCents)
	require.Zero(t, events[0].ActualCents)
	require.Equal(t, "video", events[0].BillingMode)

	// Repeated terminal observations cannot enqueue another release correction.
	require.NoError(t, svc.FinalizeGrokVideoTask(context.Background(), task, GrokVideoTaskStatus{State: "failed"}, nil, nil, nil, nil))
	require.Len(t, reporter.snapshot(), 1)
}

func TestFinalizeGrokVideoTaskCancellationReleasesReservationWithoutBilling(t *testing.T) {
	reporter := &grokVideoUsageReporterStub{}
	gatewaylite.SetDefaultUsageReporter(reporter)
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(nil) })

	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(
		&openAIRecordUsageLogRepoStub{inserted: true},
		billingRepo,
		&openAIRecordUsageUserRepoStub{},
		&openAIRecordUsageSubRepoStub{},
		nil,
	)
	store := &grokVideoTaskMemoryStore{}
	svc.SetGrokVideoTaskStore(store)
	task := &GrokVideoTask{
		RequestID:       "video-cancelled-reservation",
		QuotaRequestID:  "gateway-video-cancelled",
		GatewayKeyID:    "key-cancelled",
		LeaseID:         "lease-cancelled",
		Region:          "jp",
		GatewayID:       "gateway-jp",
		ReservedCents:   60,
		UserID:          31,
		RequestedModel:  "grok-imagine-video",
		BillingModel:    "grok-imagine-video",
		UpstreamModel:   "grok-imagine-video",
		Resolution:      "720p",
		DurationSeconds: 8,
		VideoCount:      1,
		State:           GrokVideoTaskStatePending,
		CreatedAt:       time.Now().Add(-time.Minute),
	}
	require.NoError(t, store.Save(context.Background(), task))

	require.NoError(t, svc.FinalizeGrokVideoTask(
		context.Background(),
		task,
		GrokVideoTaskStatus{State: GrokVideoTaskStateCancelled, Message: "cancelled by user"},
		nil,
		nil,
		nil,
		nil,
	))
	require.Equal(t, GrokVideoTaskStateCancelled, task.State)
	require.Zero(t, billingRepo.calls)
	events := reporter.snapshot()
	require.Len(t, events, 1)
	require.Equal(t, "gateway-video-cancelled", events[0].RequestID)
	require.EqualValues(t, 60, events[0].EstimatedCents)
	require.Zero(t, events[0].ActualCents)
}

func TestFinalizeGrokVideoTaskConcurrentSettlementBillsOnce(t *testing.T) {
	billingRepo := &grokVideoConcurrentBillingRepo{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(
		&openAIRecordUsageLogRepoStub{inserted: true},
		billingRepo,
		&openAIRecordUsageUserRepoStub{},
		&openAIRecordUsageSubRepoStub{},
		nil,
	)
	store := &grokVideoTaskMemoryStore{}
	svc.SetGrokVideoTaskStore(store)
	groupID := int64(21)
	apiKey := &APIKey{
		ID:      22,
		UserID:  23,
		User:    &User{ID: 23},
		GroupID: &groupID,
		Group:   &Group{ID: groupID, Platform: PlatformGrok, RateMultiplier: 1},
	}
	account := &Account{ID: 24, Platform: PlatformGrok}
	task := &GrokVideoTask{
		RequestID:       "video-concurrent-settlement",
		APIKeyID:        apiKey.ID,
		UserID:          apiKey.UserID,
		AccountID:       account.ID,
		GroupID:         &groupID,
		RequestedModel:  "grok-imagine-video",
		BillingModel:    "grok-imagine-video",
		UpstreamModel:   "grok-imagine-video",
		Resolution:      "720p",
		DurationSeconds: 5,
		VideoCount:      1,
		BillingSnapshot: &GrokVideoTaskBillingSnapshot{
			RateMultiplier:      1,
			VideoRateMultiplier: 1,
			Rates: map[string]GrokVideoTaskRateQuote{
				"720p": {TotalUnitCost: 0.07, ActualUnitCost: 0.07, ScaleByDuration: true},
			},
		},
		State:     GrokVideoTaskStatePending,
		CreatedAt: time.Now().Add(-time.Minute),
	}
	require.NoError(t, store.Save(context.Background(), task))
	first := *task
	second := *task
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, candidate := range []*GrokVideoTask{&first, &second} {
		wg.Add(1)
		go func(candidate *GrokVideoTask) {
			defer wg.Done()
			<-start
			errs <- svc.FinalizeGrokVideoTask(
				context.Background(),
				candidate,
				GrokVideoTaskStatus{State: "completed", DurationSeconds: 5, Resolution: "720p"},
				apiKey,
				nil,
				account,
				&openAIRecordUsageAPIKeyQuotaStub{},
			)
		}(candidate)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, billingRepo.Calls())
	require.NotNil(t, store.task)
	require.Equal(t, GrokVideoTaskStateBilled, store.task.State)
}

func TestGrokVideoTaskBillingSnapshotPreventsInFlightRepricing(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(
		usageRepo,
		billingRepo,
		&openAIRecordUsageUserRepoStub{},
		&openAIRecordUsageSubRepoStub{},
		nil,
	)
	store := &grokVideoTaskMemoryStore{}
	svc.SetGrokVideoTaskStore(store)
	groupID := int64(17)
	videoPrice := 0.10
	apiKey := &APIKey{
		ID:      12,
		UserID:  13,
		User:    &User{ID: 13},
		GroupID: &groupID,
		Group: &Group{
			ID:                   groupID,
			Platform:             PlatformGrok,
			Status:               StatusActive,
			Hydrated:             true,
			RateMultiplier:       2,
			VideoPrice720P:       &videoPrice,
			VideoRateIndependent: false,
		},
	}
	account := &Account{ID: 14, Platform: PlatformGrok}
	requestStartedAt := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	billingCtx, frozenPolicy, err := FreezeEffectiveRatePolicy(context.Background(), apiKey.Group, nil, requestStartedAt)
	require.NoError(t, err)
	task, err := svc.RegisterGrokVideoTask(billingCtx, GrokVideoTaskRegistration{
		APIKey:  apiKey,
		Account: account,
		Result: &OpenAIForwardResult{
			ResponseID:           "video-frozen-price",
			Model:                "grok-imagine-video",
			BillingModel:         "grok-imagine-video",
			UpstreamModel:        "grok-imagine-video",
			VideoCount:           1,
			VideoResolution:      "720p",
			VideoDurationSeconds: 5,
		},
		RequestedModel:     "grok-imagine-video",
		RequestPayloadHash: "payload-frozen-price",
	})
	require.NoError(t, err)
	require.NotNil(t, task.BillingSnapshot)
	require.InDelta(t, 1.0, task.BillingSnapshot.CostFor("720p", 5, 1).ActualCost, 1e-9)
	require.NotNil(t, task.BillingSnapshot.RatePolicyAudit)
	require.Equal(t, frozenPolicy.Version, task.BillingSnapshot.RatePolicyAudit.Version)

	// A persisted task restores the immutable request-time policy when a later
	// worker builds the delayed usage event. Current group settings are irrelevant.
	restoredTask := *task
	restoredTask.QuotaRequestID = "quota-video-frozen-price"
	restoredMetaCtx := restoredTask.usageContext(context.Background())
	restoredMeta, ok := gatewaylite.RequestMetaFromContext(restoredMetaCtx)
	require.True(t, ok)
	require.NotNil(t, restoredMeta.RatePolicyAudit)
	require.Equal(t, frozenPolicy.Version, restoredMeta.RatePolicyAudit.Version)
	require.Equal(t, requestStartedAt.UnixMilli(), restoredMeta.RatePolicyAudit.ResolvedAtMillis)

	// The administrator changes both price and multiplier while the task runs.
	changedPrice := 9.0
	apiKey.Group.VideoPrice720P = &changedPrice
	apiKey.Group.RateMultiplier = 9
	require.NoError(t, svc.FinalizeGrokVideoTask(
		context.Background(),
		task,
		GrokVideoTaskStatus{State: "completed", DurationSeconds: 5, Resolution: "720p"},
		apiKey,
		nil,
		account,
		&openAIRecordUsageAPIKeyQuotaStub{},
	))
	require.Equal(t, 1, billingRepo.calls)
	require.InDelta(t, 1.0, billingRepo.lastCmd.BalanceCost, 1e-9)
}
