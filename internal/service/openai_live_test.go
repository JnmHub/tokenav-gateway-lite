package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type liveFinalizeStore struct {
	GatewayCache
	mu               sync.Mutex
	record           *LiveCallRecord
	closed           bool
	claimErr         error
	getCallErr       error
	getControllerErr error
}

func (s *liveFinalizeStore) SaveLiveCall(_ context.Context, record *LiveCallRecord, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record = record
	return nil
}

func (s *liveFinalizeStore) GetLiveCall(_ context.Context, _ string) (*LiveCallRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getCallErr != nil {
		return nil, s.getCallErr
	}
	return s.record, nil
}

func (s *liveFinalizeStore) ClaimLiveController(context.Context, string, string, string) (bool, error) {
	if s.claimErr != nil {
		return false, s.claimErr
	}
	return true, nil
}

func (s *liveFinalizeStore) ReleaseLiveController(context.Context, string, string) (bool, error) {
	return true, nil
}

func (s *liveFinalizeStore) GetLiveController(context.Context, string) (string, error) {
	if s.getControllerErr != nil {
		return "", s.getControllerErr
	}
	return LiveControllerPending, nil
}

func (s *liveFinalizeStore) MarkLiveCallClosed(context.Context, string, time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, nil
	}
	s.closed = true
	return true, nil
}

type liveFinalizeConcurrencyCache struct {
	ConcurrencyCache
	releaseCalls atomic.Int32
}

type liveUnitDialer struct {
	conn    *liveSoakFrameConn
	url     string
	headers http.Header
}

func (d *liveUnitDialer) Dial(_ context.Context, url string, headers http.Header, _ string) (openAIWSClientConn, int, http.Header, error) {
	d.url = url
	d.headers = headers.Clone()
	return d.conn, http.StatusSwitchingProtocols, nil, nil
}

func (c *liveFinalizeConcurrencyCache) AcquireLiveLease(context.Context, int64, int, int64, int, int64, int, int64, string, bool) (bool, error) {
	return true, nil
}

func (c *liveFinalizeConcurrencyCache) RefreshLiveLease(context.Context, int64, int64, int64, int64, string) (bool, error) {
	return true, nil
}

func (c *liveFinalizeConcurrencyCache) ReleaseLiveLease(context.Context, int64, int64, int64, int64, string) error {
	c.releaseCalls.Add(1)
	return nil
}

func TestValidateLiveCallRequestRequiresSDPAndJSONObject(t *testing.T) {
	require.NoError(t, ValidateLiveCallRequest(&LiveCallRequest{SDP: "offer", Session: []byte(`{"model":"gpt-live"}`)}))
	require.Error(t, ValidateLiveCallRequest(&LiveCallRequest{Session: []byte(`{}`)}))
	require.Error(t, ValidateLiveCallRequest(&LiveCallRequest{SDP: "offer", Session: []byte(`[]`)}))
	require.Error(t, ValidateLiveCallRequest(&LiveCallRequest{SDP: "offer", Session: []byte(`{`)}))
}

func TestLiveSessionEndedTreatsLeaseLossAsTerminal(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "lease lost", err: ErrLiveUnavailable, want: true},
		{name: "wrapped lease lost", err: fmt.Errorf("refresh live lease: %w", ErrLiveUnavailable), want: true},
		{name: "upstream closed", err: ErrLiveCallNotFound, want: true},
		{name: "maximum duration", err: context.DeadlineExceeded, want: true},
		{name: "controller changed", err: ErrLiveControllerChanged, want: false},
		{name: "temporary disconnect", err: errors.New("temporary websocket disconnect"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, liveSessionEnded(test.err))
		})
	}
}

func TestRunLiveControllerClosesExpiredSession(t *testing.T) {
	upstream := newLiveSoakFrameConn()
	record := &LiveCallRecord{ExpiresAt: time.Now().Add(20 * time.Millisecond)}
	service := &OpenAIGatewayService{}

	err := service.runLiveController(context.Background(), record, upstream, make(chan error))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case frame := <-upstream.frames:
		require.Equal(t, coderws.MessageText, frame.messageType)
		require.JSONEq(t, `{"type":"session.close"}`, string(frame.payload))
	case <-time.After(time.Second):
		t.Fatal("Live session expiry did not send session.close upstream")
	}
}

func TestGetLiveCallForIdentityRejectsMismatchedCaller(t *testing.T) {
	groupID := int64(44)
	record := &LiveCallRecord{
		CallID: "call-identity", CallHash: hashLiveCallID("call-identity"),
		APIKeyID: -22, UserID: 33, GroupID: groupID, Controller: LiveControllerPending,
	}
	store := &liveFinalizeStore{record: record}
	service := &OpenAIGatewayService{cache: store}

	_, err := service.GetLiveCallForIdentity(context.Background(), record.CallID, LiveCallIdentity{
		APIKeyID: -99, UserID: record.UserID, GroupID: &groupID,
	})
	require.ErrorIs(t, err, ErrLiveIdentityMismatch)
	loaded, err := service.GetLiveCallForIdentity(context.Background(), record.CallID, LiveCallIdentity{
		APIKeyID: record.APIKeyID, UserID: record.UserID,
	})
	require.NoError(t, err)
	require.Equal(t, record.CallID, loaded.CallID)

	otherGroupID := int64(99)
	loaded, err = service.GetLiveCallForIdentity(context.Background(), record.CallID, LiveCallIdentity{
		APIKeyID: record.APIKeyID, UserID: record.UserID, GroupID: &otherGroupID,
	})
	require.NoError(t, err, "Sideband 应按 API Key 和用户归属校验，并沿用记录中的最终分组")
	require.Equal(t, groupID, loaded.GroupID)
}

func TestProxyLiveSidebandForwardsTextBinaryAndAuthentication(t *testing.T) {
	account := Account{
		ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 2,
		Credentials: map[string]any{
			"access_token":       "live-unit-access-token",
			"chatgpt_account_id": "acct_live_unit",
		},
	}
	cipher := newLiveAttestationCipher(&config.Config{JWT: config.JWTConfig{Secret: "live-unit-secret"}})
	attestation, err := cipher.Encrypt(`{"v":1,"t":"unit-attestation"}`)
	require.NoError(t, err)
	record := &LiveCallRecord{
		CallID: "call-proxy", CallHash: hashLiveCallID("call-proxy"), AccountID: account.ID,
		APIKeyID: -22, UserID: 33, GroupID: 44, LeaseID: "lease-proxy",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
		Controller: LiveControllerPending, AttestationCiphertext: attestation,
	}
	upstream := newLiveSoakFrameConn()
	dialer := &liveUnitDialer{conn: upstream}
	store := newLiveSoakStore()
	service := &OpenAIGatewayService{
		accountRepo:               stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:                     store,
		openaiWSPassthroughDialer: dialer,
		liveAttestationCipher:     cipher,
	}

	proxyResult := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		downstream, acceptErr := coderws.Accept(writer, request, nil)
		if acceptErr != nil {
			proxyResult <- acceptErr
			return
		}
		defer func() { _ = downstream.CloseNow() }()
		proxyResult <- service.ProxyLiveSidebandWithFrameHook(
			request.Context(), record, downstream,
			func(_ context.Context, messageType coderws.MessageType, payload []byte) ([]byte, bool, error) {
				if messageType == coderws.MessageText && gjson.GetBytes(payload, "type").String() == "blocked.client.frame" {
					return payload, false, nil
				}
				return payload, true, nil
			},
		)
	}))
	defer server.Close()
	client, _, err := coderws.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// hook（钩子）拒绝当前帧后连接仍可继续；若它被误写上游，下面首次
	// 读取会收到 blocked.client.frame 而不是安全帧，测试会直接失败。
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"blocked.client.frame"}`)))
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"client.text"}`)))
	messageType, payload, err := client.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, messageType)
	require.JSONEq(t, `{"type":"client.text"}`, string(payload))

	require.NoError(t, client.Write(ctx, coderws.MessageBinary, []byte{1, 2, 3}))
	messageType, payload, err = client.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageBinary, messageType)
	require.Equal(t, []byte{1, 2, 3}, payload)

	require.Equal(t, "wss://chatgpt.com/backend-api/codex/call-proxy", dialer.url)
	require.Equal(t, "Bearer live-unit-access-token", dialer.headers.Get("Authorization"))
	require.Equal(t, "acct_live_unit", dialer.headers.Get("Chatgpt-Account-Id"))
	require.Equal(t, `{"v":1,"t":"unit-attestation"}`, dialer.headers.Get(liveAttestationHeader))
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"session.ended"}`)))
	_, _, err = client.Read(ctx)
	require.NoError(t, err)
	require.ErrorIs(t, <-proxyResult, ErrLiveCallNotFound)
}

func TestWaitForLiveObserverRetryLeavesExpiryForFinalizer(t *testing.T) {
	record := &LiveCallRecord{
		CallID: "call-expired", CallHash: hashLiveCallID("call-expired"),
		Controller: LiveControllerObserver, ExpiresAt: time.Now().Add(-time.Minute),
	}
	store := newLiveSoakStore()
	store.controllers[record.CallHash] = LiveControllerObserver
	service := &OpenAIGatewayService{cache: store}
	require.True(t, service.waitForLiveObserverRetry(record))

	store.mu.Lock()
	store.controllers[record.CallHash] = LiveControllerProxy
	store.mu.Unlock()
	require.False(t, service.waitForLiveObserverRetry(record))
}

func TestWaitForLiveObserverRetryTreatsStoreFailureAsRetryable(t *testing.T) {
	record := &LiveCallRecord{
		CallHash: hashLiveCallID("call-store-error"), Controller: LiveControllerObserver,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	service := &OpenAIGatewayService{cache: &liveFinalizeStore{
		record: record, getControllerErr: errors.New("redis: connection reset"),
	}}

	require.True(t, service.waitForLiveObserverRetry(record))
}

func TestObserveLiveCallStoreFailureFallsBackToExpiryFinalization(t *testing.T) {
	previousInterval := liveObserverStoreRetryInterval
	liveObserverStoreRetryInterval = time.Millisecond
	t.Cleanup(func() { liveObserverStoreRetryInterval = previousInterval })

	for _, test := range []struct {
		name       string
		claimErr   error
		getCallErr error
	}{
		{name: "claim failure", claimErr: errors.New("redis: claim timeout")},
		{name: "read failure", getCallErr: errors.New("redis: read timeout")},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := &LiveCallRecord{
				CallID: "call-store-fallback", CallHash: hashLiveCallID("call-store-fallback"),
				AccountID: 11, APIKeyID: 22, UserID: 33, LeaseID: "lease-store-fallback",
				Model: "gpt-live", CreatedAt: time.Now().Add(-time.Minute),
				ExpiresAt: time.Now().Add(-time.Second), Controller: LiveControllerObserver,
			}
			store := &liveFinalizeStore{
				record: record, claimErr: test.claimErr, getCallErr: test.getCallErr,
			}
			concurrency := &liveFinalizeConcurrencyCache{}
			repo := &gatewayLiteUsageLogRepoStub{}
			service := &OpenAIGatewayService{
				cache: store, concurrencyService: NewConcurrencyService(concurrency), usageLogRepo: repo,
			}

			service.observeLiveCall(record)

			require.Equal(t, int32(1), concurrency.releaseCalls.Load())
			require.Equal(t, 1, repo.bestEffortCalls)
			require.NotNil(t, repo.lastLog)
			require.Equal(t, record.CallHash, repo.lastLog.RequestID)
		})
	}
}

func TestLiveUsageCorrectionIsZeroCostAndKeepsCreationPolicy(t *testing.T) {
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "call-hash", KeyID: "remote-key", UserID: 42,
		Region: "us", GatewayID: "us-1", GroupID: 7, GroupName: "Live",
		PolicyRevision: 31, PolicyChecksum: "policy-31", PriceVersion: 30,
	})
	duration := 1250
	groupID := int64(7)
	event, ok := gatewayLiteUsageCorrectionEvent(ctx, &UsageLog{
		RequestID: "call-hash", UserID: 42, APIKeyID: -9, AccountID: 11,
		Model: "gpt-live", RequestedModel: "gpt-live", GroupID: &groupID,
		RequestType: RequestTypeLive, DurationMs: &duration,
	})
	require.True(t, ok)
	require.Equal(t, "openai_live", event.Protocol)
	require.Equal(t, int64(0), event.ActualCents)
	require.Equal(t, int64(0), event.EstimatedCents)
	require.Equal(t, int64(1250), event.LatencyMillis)
	require.Equal(t, int64(31), event.PolicyRevision)
	require.Equal(t, "policy-31", event.PolicyChecksum)
	require.Equal(t, int64(30), event.PriceVersion)
}

func TestLiveAttestationCipherRoundTrip(t *testing.T) {
	cipher := newLiveAttestationCipher(&config.Config{JWT: config.JWTConfig{Secret: "test-secret"}})
	require.NotNil(t, cipher)
	encrypted, err := cipher.Encrypt(`{"v":1,"t":"device-token"}`)
	require.NoError(t, err)
	require.NotContains(t, encrypted, "device-token")
	decrypted, err := cipher.Decrypt(encrypted)
	require.NoError(t, err)
	require.JSONEq(t, `{"v":1,"t":"device-token"}`, decrypted)
}

func TestFinalizeLiveCallConcurrentPathsSettleExactlyOnce(t *testing.T) {
	store := &liveFinalizeStore{}
	concurrency := &liveFinalizeConcurrencyCache{}
	repo := &gatewayLiteUsageLogRepoStub{resolvedUserID: 420, resolvedKeyID: 700}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	svc := &OpenAIGatewayService{
		cache:              store,
		concurrencyService: NewConcurrencyService(concurrency),
		usageLogRepo:       repo,
		cfg:                cfg,
	}
	record := &LiveCallRecord{
		CallID: "call-1", CallHash: "call-hash", AccountID: 11, APIKeyID: -22,
		UserID: 42, GroupID: 7, LeaseID: "live-lease", Model: "gpt-live",
		CreatedAt: time.Now().Add(-time.Second), ExpiresAt: time.Now().Add(time.Hour),
		InboundEndpoint: "/v1/live",
		GatewayMeta: gatewaylite.RequestMeta{
			RequestID: "create-request", KeyID: "remote-key", UserID: 42,
			Region: "us", GatewayID: "us-1", GroupID: 7, GroupName: "Live",
			PolicyRevision: 31, PolicyChecksum: "policy-31", PriceVersion: 30,
		},
	}
	store.record = record

	previousReporter := gatewaylite.DefaultUsageReporter()
	var reported atomic.Int32
	var reportedMu sync.Mutex
	var reportedEvent gatewaylite.UsageEvent
	gatewaylite.SetDefaultUsageReporter(gatewayLiteUsageReporterFunc(func(_ context.Context, event gatewaylite.UsageEvent) error {
		reported.Add(1)
		reportedMu.Lock()
		reportedEvent = event
		reportedMu.Unlock()
		return nil
	}))
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(previousReporter) })

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.finalizeLiveCall(record)
		}()
	}
	wg.Wait()

	require.Equal(t, int32(1), concurrency.releaseCalls.Load())
	require.Equal(t, int32(1), reported.Load())
	reportedMu.Lock()
	require.Equal(t, "call-hash", reportedEvent.RequestID)
	require.Equal(t, "openai_live", reportedEvent.Protocol)
	require.Zero(t, reportedEvent.ActualCents)
	reportedMu.Unlock()
	require.Equal(t, 1, repo.resolveCalls)
	require.Equal(t, 1, repo.queuedCalls)
	require.NotNil(t, repo.lastLog)
	require.Equal(t, RequestTypeLive, repo.lastLog.RequestType)
}
