package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

type grokVideoPollHTTPUpstream struct {
	mu        sync.Mutex
	responses []*http.Response
	err       error
	requests  []*http.Request
}

func (u *grokVideoPollHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.requests = append(u.requests, req)
	if u.err != nil {
		return nil, u.err
	}
	if len(u.responses) == 0 {
		return nil, fmt.Errorf("no mocked grok response")
	}
	response := u.responses[0]
	u.responses = u.responses[1:]
	return response, nil
}

func (u *grokVideoPollHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func grokVideoPollResponse(status int, body string, headers ...http.Header) *http.Response {
	header := make(http.Header)
	if len(headers) > 0 && headers[0] != nil {
		header = headers[0]
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type grokVideoTaskAPIKeyReaderStub struct {
	key *APIKey
	err error
}

func (s *grokVideoTaskAPIKeyReaderStub) GetByID(_ context.Context, _ int64) (*APIKey, error) {
	return s.key, s.err
}

func TestPollGrokVideoTaskStatusUsesOriginalAccountTransport(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	upstream := &grokVideoPollHTTPUpstream{responses: []*http.Response{
		grokVideoPollResponse(http.StatusOK, `{"status":"completed","video":{"url":"https://cdn.example/video.mp4","duration_seconds":7,"resolution":"720p"}}`),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:          42,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 3,
		Credentials: map[string]any{
			"api_key":  "xai-secret",
			"base_url": "https://xai.test/v1",
		},
	}

	status, err := svc.PollGrokVideoTaskStatus(context.Background(), account, "video-123")
	require.NoError(t, err)
	require.Equal(t, GrokVideoTaskStateSucceeded, status.State)
	require.Equal(t, 7, status.DurationSeconds)
	require.Equal(t, "720p", status.Resolution)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://xai.test/v1/videos/video-123", upstream.requests[0].URL.String())
	require.Equal(t, "Bearer xai-secret", upstream.requests[0].Header.Get("Authorization"))
}

func TestPollGrokVideoTaskStatusRequiresOfficialDoneWithVideoURL(t *testing.T) {
	upstream := &grokVideoPollHTTPUpstream{responses: []*http.Response{
		grokVideoPollResponse(http.StatusOK, `{"status":"completed","video":{"url":"https://vidgen.x.ai/video.mp4"}}`),
		grokVideoPollResponse(http.StatusOK, `{"status":"done"}`),
		grokVideoPollResponse(http.StatusOK, `{"status":"done","video":{"url":"https://vidgen.x.ai/video.mp4"}}`),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:       44,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "xai-secret",
		},
	}

	status, err := svc.PollGrokVideoTaskStatus(context.Background(), account, "video-official")
	require.NoError(t, err)
	require.Empty(t, status.State)
	status, err = svc.PollGrokVideoTaskStatus(context.Background(), account, "video-official")
	require.NoError(t, err)
	require.Empty(t, status.State)
	status, err = svc.PollGrokVideoTaskStatus(context.Background(), account, "video-official")
	require.NoError(t, err)
	require.Equal(t, GrokVideoTaskStateSucceeded, status.State)
}

func TestPollGrokVideoTaskStatusReturnsRetryAfterWithoutCharging(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	header := http.Header{"Retry-After": []string{"45"}}
	upstream := &grokVideoPollHTTPUpstream{responses: []*http.Response{
		grokVideoPollResponse(http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`, header),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:       43,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "xai-secret",
			"base_url": "https://xai.test/v1",
		},
	}

	_, err := svc.PollGrokVideoTaskStatus(context.Background(), account, "video-rate-limited")
	var pollErr *GrokVideoTaskPollError
	require.ErrorAs(t, err, &pollErr)
	require.Equal(t, http.StatusTooManyRequests, pollErr.StatusCode)
	require.Equal(t, 45*time.Second, pollErr.RetryAfter)
}

func TestGrokVideoTaskRunnerSettlesCompletedTaskWithoutClientPolling(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(
		usageRepo,
		billingRepo,
		&openAIRecordUsageUserRepoStub{},
		&openAIRecordUsageSubRepoStub{},
		nil,
	)
	groupID := int64(7)
	account := Account{
		ID:          4,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "xai-secret",
			"base_url": "https://xai.test/v1",
		},
	}
	svc.accountRepo = stubOpenAIAccountRepo{accounts: []Account{account}}
	upstream := &grokVideoPollHTTPUpstream{responses: []*http.Response{
		grokVideoPollResponse(http.StatusOK, `{"status":"completed","video":{"url":"https://cdn.example/video.mp4","duration_seconds":5,"resolution":"720p"}}`),
	}}
	svc.httpUpstream = upstream
	store := &grokVideoTaskMemoryStore{}
	svc.SetGrokVideoTaskStore(store)

	apiKey := &APIKey{
		ID:      2,
		UserID:  3,
		User:    &User{ID: 3},
		GroupID: &groupID,
		Group: &Group{
			ID:             groupID,
			Name:           "Grok",
			Platform:       PlatformGrok,
			Status:         StatusActive,
			Hydrated:       true,
			RateMultiplier: 1,
		},
	}
	nextPoll := time.Now().Add(-time.Second)
	task := &GrokVideoTask{
		RequestID:       "video-background-1",
		APIKeyID:        apiKey.ID,
		UserID:          apiKey.UserID,
		AccountID:       account.ID,
		GroupID:         &groupID,
		GroupSnapshot:   newGrokVideoTaskGroupSnapshot(apiKey.Group),
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
		CreatedAt:          time.Now().Add(-time.Minute),
		NextPollAt:         &nextPoll,
		RequestPayloadHash: "payload-background-1",
	}
	require.NoError(t, store.Save(context.Background(), task))
	runner := NewGrokVideoTaskRunner(
		store,
		svc,
		&grokVideoTaskAPIKeyReaderStub{key: apiKey},
		nil,
		&openAIRecordUsageAPIKeyQuotaStub{},
		time.Hour,
	)

	runner.processTask(task)
	require.Equal(t, 1, billingRepo.calls)
	require.Equal(t, "grok-video:video-background-1", billingRepo.lastCmd.RequestID)
	require.Equal(t, GrokVideoTaskStateBilled, store.task.State)
	require.NotNil(t, store.task.BilledAt)

	// A later runner pass sees the billed state and cannot charge again.
	runner.processTask(store.task)
	require.Equal(t, 1, billingRepo.calls)
}

func TestGrokVideoTaskRunnerTimeoutReleasesReservationWithoutBilling(t *testing.T) {
	reporter := &grokVideoUsageReporterStub{}
	gatewaylite.SetDefaultUsageReporter(reporter)
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(nil) })

	store := &grokVideoTaskMemoryStore{}
	svc := &OpenAIGatewayService{grokVideoTaskStore: store}
	nextPoll := time.Now().Add(-time.Minute)
	task := &GrokVideoTask{
		RequestID:       "video-timeout-release",
		QuotaRequestID:  "gateway-video-timeout",
		GatewayKeyID:    "key-timeout",
		LeaseID:         "lease-timeout",
		Region:          "jp",
		GatewayID:       "gateway-jp",
		ReservedCents:   80,
		UserID:          99,
		RequestedModel:  "grok-imagine-video",
		BillingModel:    "grok-imagine-video",
		UpstreamModel:   "grok-imagine-video",
		Resolution:      "720p",
		DurationSeconds: 10,
		VideoCount:      1,
		State:           GrokVideoTaskStatePending,
		CreatedAt:       time.Now().Add(-GrokVideoTaskMaxPendingAge - time.Minute),
		NextPollAt:      &nextPoll,
	}
	require.NoError(t, store.Save(context.Background(), task))
	runner := NewGrokVideoTaskRunner(store, svc, nil, nil, nil, time.Hour)

	runner.processTask(task)

	require.NotNil(t, store.task)
	require.Equal(t, GrokVideoTaskStateFailed, store.task.State)
	require.Contains(t, store.task.LastStatusMessage, "timed out")
	require.NotNil(t, store.task.QuotaFinalizedAt)
	events := reporter.snapshot()
	require.Len(t, events, 1)
	require.Equal(t, "gateway-video-timeout", events[0].RequestID)
	require.EqualValues(t, 80, events[0].EstimatedCents)
	require.Zero(t, events[0].ActualCents)
}

func TestGrokVideoTaskRunnerBillingContextAllowsGatewaySnapshotKeyIDZero(t *testing.T) {
	groupID := int64(12)
	runner := NewGrokVideoTaskRunner(nil, nil, nil, nil, nil, time.Second)
	task := &GrokVideoTask{
		RequestID:      "video-key-zero",
		APIKeyID:       0,
		UserID:         88,
		GatewayKeyID:   "public-key-id",
		QuotaRequestID: "gateway-request-key-zero",
		GroupID:        &groupID,
		GroupSnapshot: &GrokVideoTaskGroupSnapshot{
			ID:             groupID,
			Name:           "Grok 4.5",
			Platform:       PlatformGrok,
			RateMultiplier: 1.25,
		},
	}

	apiKey, subscription, updater, err := runner.billingContext(context.Background(), task)
	require.NoError(t, err)
	require.NotNil(t, apiKey)
	require.EqualValues(t, 0, apiKey.ID)
	require.EqualValues(t, 88, apiKey.UserID)
	require.NotNil(t, apiKey.User)
	require.EqualValues(t, 88, apiKey.User.ID)
	require.NotNil(t, apiKey.Group)
	require.EqualValues(t, groupID, apiKey.Group.ID)
	require.Nil(t, subscription)
	require.Nil(t, updater)
}
