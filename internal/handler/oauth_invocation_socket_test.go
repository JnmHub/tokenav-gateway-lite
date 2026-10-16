package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const oauthInvocationSocketToken = "tokenav_oa_socket_test_not_a_production_credential"

// This fixture exercises the real gateway handler, auth middleware, HTTP client,
// billing resolver and Redis queue. Main-site mutations remain an HTTP fixture;
// PostgreSQL authority is covered by the separate native integration suite.
type oauthInvocationSocketControl struct {
	mu           sync.Mutex
	resolved     gatewaylite.OAuthInvocationResolveResponse
	reservations map[string]gatewaylite.OAuthInvocationReservation
	funding      map[string]gatewaylite.FundingAuthorization
	fundingInput map[string]gatewaylite.AuthorizeFundingRequest
	operations   map[string]string
	calls        []string
	revoked      bool
	deny         string
	loseReply    string
	spent        int64
}

func newOAuthInvocationSocketControl(t *testing.T) (*oauthInvocationSocketControl, *gatewaylite.ControlPlaneClientRef) {
	t.Helper()
	s := &oauthInvocationSocketControl{reservations: map[string]gatewaylite.OAuthInvocationReservation{}, funding: map[string]gatewaylite.FundingAuthorization{},
		fundingInput: map[string]gatewaylite.AuthorizeFundingRequest{}, operations: map[string]string{}}
	s.resolved = gatewaylite.OAuthInvocationResolveResponse{OK: true, DelegationID: "socket-delegation", ExpiresAtMillis: time.Now().Add(time.Hour).UnixMilli(),
		Key: &gatewaylite.KeySnapshot{KeyID: "od_socket", UserID: 33, Status: "active", Platform: service.PlatformOpenAI, BillingSubjectType: "user", BillingSubjectID: 33,
			KeyDefaultGatewayNodeID: 5, GroupID: 50, Concurrency: 5, AllowedModels: []string{"gpt-5.4"}, GatewayStrategy: "fixed_group", GatewayAccessEnforced: true,
			AvailableGateways: []gatewaylite.GatewayRouteSummary{{ID: 5, Code: "socket-node"}},
			Group:             &gatewaylite.GatewayGroupSnapshot{ID: 50, GatewayNodeID: 5, LocalGroupID: 7, GatewayCode: "socket-node", Platform: service.PlatformOpenAI, Status: "active", RateMultiplier: 1}},
		Invocation: &gatewaylite.OAuthInvocation{GatewayNodeID: 5, GroupID: 50, Models: []string{"gpt-5.4"}, MaxCostCents: 10000, ExpiresIn: 3600, RateLimitRPM: 60, Concurrency: 2, CostConsent: true}}
	server := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(server.Close)
	client, err := gatewaylite.NewControlPlaneClient(server.URL, "socket-internal-test", 2*time.Second)
	require.NoError(t, err)
	return s, gatewaylite.NewControlPlaneClientRef(client)
}

func (s *oauthInvocationSocketControl) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	s.calls = append(s.calls, path)
	deny := func(code string) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": code})
	}
	if r.Header.Get("Authorization") != "Bearer socket-internal-test" {
		deny("invalid_internal_identity")
		return
	}
	if s.deny == path {
		deny("denied")
		return
	}
	var out any
	switch path {
	case "/internal/oauth/invocations/resolve":
		var in gatewaylite.OAuthInvocationResolveRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.InvocationToken != oauthInvocationSocketToken || in.GatewayCode != "socket-node" || s.revoked || s.resolved.ExpiresAtMillis <= time.Now().UnixMilli() {
			deny("invalid_token")
			return
		}
		out = s.resolved
	case "/internal/oauth/invocations/reserve":
		var in gatewaylite.OAuthInvocationReserveRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		if s.revoked || s.resolved.ExpiresAtMillis <= time.Now().UnixMilli() || len(s.reservations) >= s.resolved.Invocation.RateLimitRPM {
			deny("slow_down")
			return
		}
		if in.RequestID == "" || in.DelegationID != s.resolved.DelegationID || in.KeyID != "od_socket" || in.GatewayCode != "socket-node" || in.EntryGroupID != 50 || in.EntryGatewayNodeID != 5 || in.RequestedModel != "gpt-5.4" || in.UpperBoundMicroCents <= 0 {
			deny("scope_conflict")
			return
		}
		a, exists := s.reservations[in.RequestID]
		if !exists {
			a = gatewaylite.OAuthInvocationReservation{ReservationID: "r-" + in.RequestID, RequestID: in.RequestID, DelegationID: in.DelegationID,
				GatewayCode: in.GatewayCode, KeyID: in.KeyID, EntryGatewayNodeID: in.EntryGatewayNodeID, EntryGroupID: in.EntryGroupID, RequestedModel: in.RequestedModel,
				Endpoint: in.Endpoint, UpperBoundMicroCents: in.UpperBoundMicroCents, ExecutionDeadlineMillis: in.ExecutionDeadlineMillis, Status: "reserved", Version: 1}
			s.reservations[in.RequestID] = a
		}
		out = gatewaylite.OAuthInvocationReservationResponse{OK: true, Reservation: &a}
	case "/internal/billing/authorize":
		var in gatewaylite.AuthorizeFundingRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		a := s.reservations[in.RequestID]
		if !in.ForceAuthorization || in.BoundKind != "bounded" || in.BoundEvidence["kind"] != "text_max_output" || in.BoundEvidence["price_source"] != "gateway_runtime" ||
			in.UpperBoundMicroCents != a.UpperBoundMicroCents || in.EntryGroupID != a.EntryGroupID || in.EntryGatewayNodeID != a.EntryGatewayNodeID || in.RequestedModel != a.RequestedModel || in.ExecutionDeadlineMillis != a.ExecutionDeadlineMillis {
			deny("force_bounded_funding_required")
			return
		}
		f := gatewaylite.FundingAuthorization{AuthorizationID: "f-" + in.RequestID, RequestID: in.RequestID, KeyID: in.KeyID, UserID: in.UserID, BillingSubjectType: "user", BillingSubjectID: in.UserID,
			GatewayCode: in.GatewayCode, Region: in.Region, EntryGatewayNodeID: in.EntryGatewayNodeID, EntryGroupID: in.EntryGroupID, RequestedModel: in.RequestedModel, Endpoint: in.Endpoint,
			Status: "active", Disposition: "authorized", Version: 2, Revision: 2, UpperBoundMicroCents: in.UpperBoundMicroCents, BoundKind: in.BoundKind, BoundEvidence: in.BoundEvidence,
			ExecutionDeadlineMillis: in.ExecutionDeadlineMillis, ForceAuthorization: true,
			Items: []gatewaylite.FundingAuthorizationItem{{ItemID: 1, Ordinal: 1, Source: "balance", ReservedMicroCents: in.UpperBoundMicroCents}}}
		s.funding[in.RequestID], s.fundingInput[in.RequestID] = f, in
		out = gatewaylite.FundingAuthorizationResponse{OK: true, Disposition: "authorized", Authorization: &f}
	case "/internal/oauth/invocations/bind":
		var in gatewaylite.OAuthInvocationBindRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		a, f := s.reservations[in.RequestID], s.funding[in.RequestID]
		if in.ExpectedVersion != a.Version || in.ReservationID != a.ReservationID || f.Status != "active" || f.AuthorizationID != in.FundingAuthorizationID || f.UpperBoundMicroCents != a.UpperBoundMicroCents {
			deny("bind_conflict")
			return
		}
		a.FundingAuthorizationID, a.Status, a.Version = f.AuthorizationID, "active", a.Version+1
		s.reservations[in.RequestID] = a
		out = gatewaylite.OAuthInvocationReservationResponse{OK: true, Reservation: &a}
	case "/internal/oauth/invocations/status":
		var in gatewaylite.OAuthInvocationStatusRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		a, exists := s.reservations[in.RequestID]
		if !exists || in.GatewayCode != a.GatewayCode || in.ReservationID != a.ReservationID || in.DelegationID != a.DelegationID {
			deny("status_conflict")
			return
		}
		out = gatewaylite.OAuthInvocationReservationResponse{OK: true, Reservation: &a}
	case "/internal/usage/report", "/internal/usage/report-batch":
		var events []gatewaylite.UsageEvent
		if strings.HasSuffix(path, "-batch") {
			var in gatewaylite.UsageBatchReportRequest
			_ = json.NewDecoder(r.Body).Decode(&in)
			events = in.Events
		} else {
			var in gatewaylite.UsageEvent
			_ = json.NewDecoder(r.Body).Decode(&in)
			events = []gatewaylite.UsageEvent{in}
		}
		for _, in := range events {
			f, exists := s.funding[in.RequestID]
			if !exists || in.FundingAuthorizationID != f.AuthorizationID || in.OAuthInvocationReservation == nil || in.ActualMicroCents == nil || !in.Final || *in.ActualMicroCents > f.UpperBoundMicroCents {
				deny("usage_identity_conflict")
				return
			}
			f.Status, f.ActualMicroCents, f.SettlementRevision = "settled", *in.ActualMicroCents, in.SettlementRevision
			s.funding[in.RequestID] = f
		}
		out = map[string]any{"ok": true}
	case "/internal/oauth/invocations/settle", "/internal/oauth/invocations/release":
		var in gatewaylite.OAuthInvocationFinalizeRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		a, f := s.reservations[in.RequestID], s.funding[in.RequestID]
		raw, _ := json.Marshal(in)
		digest := sha256.Sum256(raw)
		fingerprint := hex.EncodeToString(digest[:])
		operationKey := path + ":" + in.OperationID
		prior, duplicate := s.operations[operationKey]
		if duplicate && prior != fingerprint {
			deny("operation_conflict")
			return
		}
		if !duplicate {
			if !in.TerminalConfirmed || in.ReservationID != a.ReservationID || in.FundingAuthorizationID != a.FundingAuthorizationID || in.ExpectedVersion != a.Version {
				deny("final_identity_conflict")
				return
			}
			if strings.HasSuffix(path, "/release") {
				if in.Revision != 0 || in.ActualMicroCents != 0 || (a.FundingAuthorizationID != "" && f.Status != "released") {
					deny("release_conflict")
					return
				}
				a.Status = "released"
			} else {
				if f.Status != "settled" || f.SettlementRevision != in.Revision || f.ActualMicroCents != in.ActualMicroCents || in.ActualMicroCents > a.UpperBoundMicroCents {
					deny("funding_must_settle_first")
					return
				}
				s.spent += in.ActualMicroCents - a.ActualMicroCents
				a.Status, a.Revision, a.ActualMicroCents = "settled", in.Revision, in.ActualMicroCents
			}
			a.Version++
			s.operations[operationKey], s.reservations[in.RequestID] = fingerprint, a
		}
		out = gatewaylite.OAuthInvocationReservationResponse{OK: true, Reservation: &a}
	case "/internal/billing/release-authorization":
		var in gatewaylite.ReleaseFundingAuthorizationRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		f := s.funding[in.RequestID]
		if !in.TerminalConfirmed || in.AuthorizationID != f.AuthorizationID || in.ExpectedVersion != f.Version {
			deny("funding_release_conflict")
			return
		}
		f.Status, f.Version = "released", f.Version+1
		s.funding[in.RequestID] = f
		out = gatewaylite.FundingAuthorizationResponse{OK: true, Disposition: "authorized", Authorization: &f}
	default:
		deny("ordinary_key_or_lease_path_forbidden")
		return
	}
	if s.loseReply == path {
		s.loseReply = ""
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

type oauthInvocationSocketUsageRepo struct{ service.UsageBillingRepository }

func (*oauthInvocationSocketUsageRepo) Apply(context.Context, *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	// Remote gateway Keys cannot deduct a local wallet; the HTTP funding authority
	// and the real usage queue must perform both financial mutations below.
	return &service.UsageBillingApplyResult{Applied: false}, nil
}

type oauthInvocationSocketLogs struct {
	service.UsageLogRepository
	created chan *service.UsageLog
}

func (s *oauthInvocationSocketLogs) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	s.created <- log
	return true, nil
}
func (*oauthInvocationSocketLogs) ResolveGatewayLiteUsageIdentity(context.Context, int64, string) (int64, int64, error) {
	return 33, 9, nil
}

func oauthInvocationSocketRedis(t *testing.T) (*redis.Client, string) {
	t.Helper()
	url := strings.TrimSpace(os.Getenv("TOKENAV_TEST_REDIS_URL"))
	if url == "" {
		t.Skip("TOKENAV_TEST_REDIS_URL is required for the real-socket/real-Redis test")
	}
	options, err := redis.ParseURL(url)
	require.NoError(t, err)
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(context.Background()).Err())
	prefix := fmt.Sprintf("oauth-socket-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		keys, err := client.Keys(context.Background(), prefix+":*").Result()
		require.NoError(t, err)
		if len(keys) > 0 {
			require.NoError(t, client.Del(context.Background(), keys...).Err())
		}
	})
	return client, prefix
}

func TestOAuthInvocationResponsesWebSocketRealSocket(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/responses"} {
		for _, state := range []string{"two_allowed_turns", "expiry", "revocation", "rpm", "funding_denied", "bind_denied", "reserve_reply_loss", "authorize_reply_loss", "bind_reply_loss", "missing_output_limit", "upstream_unknown"} {
			t.Run(path+"/"+state, func(t *testing.T) { runOAuthInvocationSocket(t, path, state) })
		}
	}
}

func runOAuthInvocationSocket(t *testing.T, path, state string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	client, prefix := oauthInvocationSocketRedis(t)
	control, ref := newOAuthInvocationSocketControl(t)
	quota := gatewaylite.NewRedisQuota(client, prefix)
	reporter := gatewaylite.NewRedisUsageReporter(client, prefix, gatewaylite.NewOAuthInvocationUsageSink(ref, ref))
	runtime := gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "socket-region", GatewayCode: "socket-node", RedisPrefix: prefix})
	oldControl, oldQuota, oldRuntime, oldReporter := gatewaylite.DefaultControlPlaneClientRef(), gatewaylite.DefaultRedisQuota(), gatewaylite.DefaultRuntimeConfigRef(), gatewaylite.DefaultUsageReporter()
	gatewaylite.SetDefaultControlPlaneClientRef(ref)
	gatewaylite.SetDefaultRedisQuota(quota)
	gatewaylite.SetDefaultRuntimeConfigRef(runtime)
	gatewaylite.SetDefaultUsageReporter(reporter)
	t.Cleanup(func() {
		gatewaylite.SetDefaultControlPlaneClientRef(oldControl)
		gatewaylite.SetDefaultRedisQuota(oldQuota)
		gatewaylite.SetDefaultRuntimeConfigRef(oldRuntime)
		gatewaylite.SetDefaultUsageReporter(oldReporter)
	})
	var creates atomic.Int32
	type dispatchedTurn struct {
		payload   []byte
		requestID string
		bound     bool
	}
	dispatched := make(chan dispatchedTurn, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			_, payload, err := conn.Read(ctx)
			cancel()
			if err != nil {
				return
			}
			if gjson.GetBytes(payload, "type").String() != "response.create" {
				continue
			}
			if gjson.GetBytes(payload, "max_output_tokens").Int() <= 0 {
				t.Error("actual upstream create lost its enforced output cap")
				return
			}
			n := creates.Add(1)
			control.mu.Lock()
			observed := dispatchedTurn{payload: append([]byte(nil), payload...)}
			for requestID, a := range control.reservations {
				if strings.HasSuffix(requestID, fmt.Sprintf(":ws-turn:%d", n)) {
					f := control.funding[requestID]
					observed.requestID = requestID
					observed.bound = a.Status == "active" && f.Status == "active" && a.FundingAuthorizationID == f.AuthorizationID
				}
			}
			control.mu.Unlock()
			dispatched <- observed
			if state == "upstream_unknown" {
				continue
			}
			ctx, cancel = context.WithTimeout(r.Context(), 3*time.Second)
			err = conn.Write(ctx, coderws.MessageText, []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_socket_%d","model":"gpt-5.4","usage":{"input_tokens":2,"output_tokens":1}}}`, n)))
			cancel()
			if err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled, cfg.Gateway.OpenAIWS.APIKeyEnabled, cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2, cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true, true, true, true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds, cfg.Gateway.OpenAIWS.ReadTimeoutSeconds, cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3, 8, 3
	if state == "upstream_unknown" {
		// A watchdog timeout is an uncertain consumption outcome. Its configured
		// deadline must finish before the fixture's handler-termination assertion.
		cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 2
	}
	account := service.Account{ID: 4242, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 5, GroupIDs: []int64{7},
		Credentials: map[string]any{"api_key": "socket-test-upstream", "base_url": upstream.URL, "model_mapping": map[string]any{"gpt-5.4": "gpt-5.4"}},
		Extra:       map[string]any{"openai_apikey_responses_websockets_v2_enabled": true, "openai_apikey_responses_websockets_v2_mode": service.OpenAIWSIngressModePassthrough}}
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	defer billingCache.Stop()
	logs := &oauthInvocationSocketLogs{created: make(chan *service.UsageLog, 4)}
	gateway := service.NewOpenAIGatewayService(&openAIWSUsageHandlerAccountRepoStub{account: account}, logs, &oauthInvocationSocketUsageRepo{}, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil)
	defer gateway.CloseOpenAIWSPool()
	slots := &concurrencyCacheMock{acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }, acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }}
	h := &OpenAIGatewayHandler{gatewayService: gateway, billingCacheService: billingCache, apiKeyService: &service.APIKeyService{}, cfg: cfg,
		concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(slots), SSEPingFormatNone, time.Second)}
	router := gin.New()
	router.Use(gin.HandlerFunc(middleware.NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfig(ref, runtime, quota, nil, reporter)))
	handlerDone := make(chan struct{})
	router.GET(path, func(c *gin.Context) {
		defer close(handlerDone)
		h.ResponsesWebSocket(c)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path, &coderws.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + oauthInvocationSocketToken}}})
	cancel()
	require.NoError(t, err)
	defer func() {
		_ = conn.CloseNow()
		select {
		case <-handlerDone:
		case <-time.After(5 * time.Second):
			t.Error("production WebSocket handler did not exit after socket close")
		}
	}()
	control.mu.Lock()
	require.Empty(t, control.reservations, "handshake must not reserve a paid turn")
	require.Empty(t, control.funding)
	switch state {
	case "funding_denied":
		control.deny = "/internal/billing/authorize"
	case "bind_denied":
		control.deny = "/internal/oauth/invocations/bind"
	case "reserve_reply_loss":
		control.loseReply = "/internal/oauth/invocations/reserve"
	case "authorize_reply_loss":
		control.loseReply = "/internal/billing/authorize"
	case "bind_reply_loss":
		control.loseReply = "/internal/oauth/invocations/bind"
	}
	control.mu.Unlock()
	write := func(maxOutput bool) {
		payload := `{"type":"response.create","model":"gpt-5.4","input":"hello"}`
		if maxOutput {
			payload = `{"type":"response.create","model":"gpt-5.4","max_output_tokens":32,"input":"hello"}`
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(payload)))
	}
	read := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, body, err := conn.Read(ctx)
		return body, err
	}
	write(state != "missing_output_limit")
	if state == "upstream_unknown" {
		require.Eventually(t, func() bool { return creates.Load() == 1 }, 3*time.Second, 10*time.Millisecond)
		_ = conn.CloseNow()
		select {
		case <-handlerDone:
		case <-time.After(5 * time.Second):
			t.Fatal("unknown upstream turn did not reach handler termination")
		}
		control.mu.Lock()
		defer control.mu.Unlock()
		require.Len(t, control.reservations, 1)
		require.Len(t, control.funding, 1)
		for _, a := range control.reservations {
			require.Equal(t, "active", a.Status)
		}
		for _, f := range control.funding {
			require.Equal(t, "active", f.Status)
		}
		require.Zero(t, client.XLen(context.Background(), prefix+":usage:stream").Val())
		return
	}
	if strings.HasSuffix(state, "denied") || strings.HasSuffix(state, "reply_loss") || state == "missing_output_limit" {
		_, err := read()
		require.Error(t, err)
		require.NotEqual(t, coderws.StatusCode(-1), coderws.CloseStatus(err), "failure must close by admission, not a test read timeout")
		require.Zero(t, creates.Load(), "failed admission cannot send a create upstream")
		control.mu.Lock()
		defer control.mu.Unlock()
		if state == "missing_output_limit" {
			require.Empty(t, control.reservations)
		} else {
			require.Len(t, control.reservations, 1)
		}
		if state == "bind_denied" || state == "authorize_reply_loss" || state == "bind_reply_loss" {
			require.Len(t, control.funding, 1)
		} else {
			require.Empty(t, control.funding)
		}
		for _, a := range control.reservations {
			if strings.HasSuffix(state, "reply_loss") {
				require.Contains(t, []string{"active", "reserved"}, a.Status)
			} else {
				require.Equal(t, "released", a.Status)
			}
		}
		for _, f := range control.funding {
			if strings.HasSuffix(state, "reply_loss") {
				require.Equal(t, "active", f.Status)
			} else {
				require.Equal(t, "released", f.Status)
			}
		}
		return
	}
	body, err := read()
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(body, "type").String())
	collectUsage := func(expected int) gatewaylite.UsageEvent {
		select {
		case log := <-logs.created:
			require.Greater(t, log.ActualCost, 0.0)
		case <-time.After(3 * time.Second):
			t.Fatal("actual per-turn usage was not recorded")
		}
		var messages []redis.XMessage
		require.Eventually(t, func() bool {
			var err error
			messages, err = client.XRange(context.Background(), prefix+":usage:stream", "-", "+").Result()
			return err == nil && len(messages) == expected
		}, 3*time.Second, 10*time.Millisecond)
		var queued struct {
			Event gatewaylite.UsageEvent `json:"event"`
		}
		require.NoError(t, json.Unmarshal([]byte(messages[expected-1].Values["event"].(string)), &queued))
		event := queued.Event
		require.NotNil(t, event.OAuthInvocationReservation, "detached RecordUsage must retain original delegation")
		require.NotNil(t, event.FundingSnapshot)
		require.NotNil(t, event.ActualMicroCents)
		require.Greater(t, *event.ActualMicroCents, int64(0))
		select {
		case observed := <-dispatched:
			require.True(t, observed.bound, "both original holds must be bound before the real upstream sees a create")
			require.Equal(t, event.RequestID, observed.requestID)
			digest := sha256.Sum256(observed.payload)
			require.Equal(t, hex.EncodeToString(digest[:]), event.FundingSnapshot.BoundEvidence["forwarded_body_sha256"])
			require.EqualValues(t, 32, gjson.GetBytes(observed.payload, "max_output_tokens").Int())
		default:
			t.Fatal("actual socket dispatch evidence missing")
		}
		require.Equal(t, event.FundingAuthorizationID, event.OAuthInvocationReservation.FundingAuthorizationID)
		require.Equal(t, event.RequestID, event.OAuthInvocationReservation.RequestID)
		require.EqualValues(t, 50, event.OAuthInvocationReservation.EntryGroupID)
		return event
	}
	drain := func(events ...gatewaylite.UsageEvent) {
		// One worker owns the entire drain. Cancelling between turns would leave a
		// blocked Redis read able to claim the next event with a cancelled context.
		workerCtx, stop := context.WithCancel(context.Background())
		defer stop()
		reporter.StartWorkersWithBatchSize(workerCtx, 1, 1)
		require.Eventually(t, func() bool {
			control.mu.Lock()
			defer control.mu.Unlock()
			for _, event := range events {
				a, f := control.reservations[event.RequestID], control.funding[event.RequestID]
				if a.Status != "settled" || f.Status != "settled" || a.ActualMicroCents != *event.ActualMicroCents {
					return false
				}
			}
			stats, err := reporter.UsageQueueStats(context.Background())
			return err == nil && stats.StreamLength == 0 && stats.PendingCount == 0 && stats.DeadCount == 0
		}, 3*time.Second, 10*time.Millisecond, "both financial sides must settle before queue ACK")
	}
	first := collectUsage(1)
	control.mu.Lock()
	switch state {
	case "expiry":
		control.resolved.ExpiresAtMillis = time.Now().Add(-time.Second).UnixMilli()
	case "revocation":
		control.revoked = true
	case "rpm":
		control.resolved.Invocation.RateLimitRPM = 1
	}
	control.mu.Unlock()
	write(true)
	body, err = read()
	if state != "two_allowed_turns" {
		require.Error(t, err)
		require.Equal(t, coderws.StatusTryAgainLater, coderws.CloseStatus(err))
		require.EqualValues(t, 1, creates.Load())
		control.mu.Lock()
		require.Len(t, control.reservations, 1)
		require.Len(t, control.funding, 1)
		control.mu.Unlock()
		drain(first)
		return
	}
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(body, "type").String())
	second := collectUsage(2)
	drain(first, second)
	require.NotEqual(t, first.RequestID, second.RequestID)
	require.NotEqual(t, first.FundingAuthorizationID, second.FundingAuthorizationID)
	require.NotEqual(t, first.OAuthInvocationReservation.ReservationID, second.OAuthInvocationReservation.ReservationID)
	require.EqualValues(t, 2, creates.Load())
	control.mu.Lock()
	defer control.mu.Unlock()
	require.Len(t, control.fundingInput, 2)
	for id, in := range control.fundingInput {
		require.True(t, in.ForceAuthorization)
		require.Equal(t, "bounded", in.BoundKind)
		require.EqualValues(t, in.UpperBoundMicroCents, control.reservations[id].UpperBoundMicroCents)
		require.Equal(t, control.funding[id].AuthorizationID, control.reservations[id].FundingAuthorizationID)
	}
	require.EqualValues(t, *first.ActualMicroCents+*second.ActualMicroCents, control.spent)
	require.NotContains(t, control.calls, "/internal/keys/resolve")
	require.NotContains(t, control.calls, "/internal/leases/acquire")
}
