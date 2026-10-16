package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const (
	seedanceRegisteredGroupID       = int64(7)
	seedanceRegisteredGlobalGroupID = int64(3000000007)
	seedanceRegisteredUserID        = int64(33)
	seedanceRegisteredAccountID     = int64(4242)
	seedanceRegisteredNodeID        = int64(9)
	seedanceRegisteredGateway       = "seedance-registered-gw"
	seedanceRegisteredPrefix        = "seedance-registered"
	seedanceRegisteredOwnerKeyID    = "seedance-owner"
	seedanceRegisteredOtherKeyID    = "seedance-other"
	seedanceRegisteredSecret        = "synthetic-secret"
	seedanceRegisteredModel         = "gpt-5.5"
	seedanceRegisteredAlias         = "customer-seedance-alias-no-catalog"
	seedanceRegisteredUpstreamModel = "ep-seedance-synthetic"
	seedanceRegisteredTaskID        = "seedance-registered-task"
	seedanceRegisteredTaskPath      = "/contents/generations/tasks"
	seedanceRegisteredContent       = `[{"type":"text","text":"waves over a quiet shore"}]`
)

// 仅合成主站响应并记录调用；鉴权、租约、任务归属、冻结价及结算均执行生产实现。
// 普通余额必须经正式 selection 准入返回 legacy_balance，不制造 fixed_media 授权。
type seedanceRegisteredControl struct {
	mu   sync.Mutex
	keys map[string]gatewaylite.KeySnapshot
	seen seedanceRegisteredControlCalls
}

type seedanceRegisteredControlCalls struct {
	resolves   []gatewaylite.ResolveKeyRequest
	rates      []float64
	funding    []gatewaylite.AuthorizeFundingRequest
	leases     []gatewaylite.AcquireLeaseRequest
	events     []gatewaylite.UsageEvent
	unexpected []string
}

func (s *seedanceRegisteredControl) ResolveKey(_ context.Context, req gatewaylite.ResolveKeyRequest) (*gatewaylite.ResolveKeyResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen.resolves = append(s.seen.resolves, req)
	key, ok := s.keys[req.KeyID]
	if !ok {
		return &gatewaylite.ResolveKeyResponse{OK: false, Error: "not_found"}, nil
	}
	group := *key.Group
	key.Group = &group
	s.seen.rates = append(s.seen.rates, key.Group.RateMultiplier)
	return &gatewaylite.ResolveKeyResponse{OK: true, Key: key}, nil
}

func (s *seedanceRegisteredControl) AcquireLease(_ context.Context, req gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen.leases = append(s.seen.leases, req)
	return &gatewaylite.AcquireLeaseResponse{OK: true, Lease: gatewaylite.LeaseSnapshot{
		LeaseID: "seedance-original-lease", UserID: req.UserID, Region: req.Region,
		BillingSubjectType: req.BillingSubjectType, BillingSubjectID: req.BillingSubjectID,
		AllocatedCents: 1000000, Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}}, nil
}

func (s *seedanceRegisteredControl) unexpectedCall(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen.unexpected = append(s.seen.unexpected, name)
	return errors.New("unexpected Seedance fixture call: " + name)
}

func (s *seedanceRegisteredControl) RebalanceLease(context.Context, gatewaylite.RebalanceLeaseRequest) (*gatewaylite.RebalanceLeaseResponse, error) {
	return nil, s.unexpectedCall("RebalanceLease")
}

func (s *seedanceRegisteredControl) AuthorizeFunding(_ context.Context, req gatewaylite.AuthorizeFundingRequest) (*gatewaylite.FundingAuthorizationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen.funding = append(s.seen.funding, req)
	return &gatewaylite.FundingAuthorizationResponse{OK: true, Disposition: "legacy_balance", Authorization: &gatewaylite.FundingAuthorization{
		RequestID: req.RequestID, KeyID: req.KeyID, UserID: req.UserID,
		GatewayCode: req.GatewayCode, Region: req.Region, EntryGatewayNodeID: req.EntryGatewayNodeID,
		EntryGroupID: req.EntryGroupID, RequestedModel: req.RequestedModel, Endpoint: req.Endpoint,
		Disposition: "legacy_balance",
	}}, nil
}

func (s *seedanceRegisteredControl) ResizeFundingAuthorization(context.Context, gatewaylite.ResizeFundingAuthorizationRequest) (*gatewaylite.FundingAuthorizationResponse, error) {
	return nil, s.unexpectedCall("ResizeFundingAuthorization")
}

func (s *seedanceRegisteredControl) ReleaseFundingAuthorization(context.Context, gatewaylite.ReleaseFundingAuthorizationRequest) (*gatewaylite.FundingAuthorizationResponse, error) {
	return nil, s.unexpectedCall("ReleaseFundingAuthorization")
}

func (s *seedanceRegisteredControl) LookupFundingAuthorization(context.Context, string) (*gatewaylite.FundingAuthorizationResponse, error) {
	return nil, s.unexpectedCall("LookupFundingAuthorization")
}

func (s *seedanceRegisteredControl) ReportUsage(_ context.Context, event gatewaylite.UsageEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen.events = append(s.seen.events, event)
	return nil
}

func (s *seedanceRegisteredControl) updateKey(keyID string, update func(*gatewaylite.KeySnapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.keys[keyID]
	group := *key.Group
	key.Group = &group
	update(&key)
	s.keys[keyID] = key
}

func (s *seedanceRegisteredControl) snapshot() seedanceRegisteredControlCalls {
	s.mu.Lock()
	defer s.mu.Unlock()
	return seedanceRegisteredControlCalls{
		resolves:   append([]gatewaylite.ResolveKeyRequest(nil), s.seen.resolves...),
		rates:      append([]float64(nil), s.seen.rates...),
		funding:    append([]gatewaylite.AuthorizeFundingRequest(nil), s.seen.funding...),
		leases:     append([]gatewaylite.AcquireLeaseRequest(nil), s.seen.leases...),
		events:     append([]gatewaylite.UsageEvent(nil), s.seen.events...),
		unexpected: append([]string(nil), s.seen.unexpected...),
	}
}

type seedanceRegisteredSelection struct {
	groupID  int64
	platform string
}

type seedanceRegisteredAccountRepo struct {
	service.AccountRepository
	mu         sync.Mutex
	account    service.Account
	selections []seedanceRegisteredSelection
	lookups    []int64
}

func (s *seedanceRegisteredAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]service.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selections = append(s.selections, seedanceRegisteredSelection{groupID, platform})
	if groupID != seedanceRegisteredGroupID || platform != s.account.Platform || !s.account.Schedulable {
		return nil, nil
	}
	return []service.Account{s.account}, nil
}

func (s *seedanceRegisteredAccountRepo) ListSchedulableByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	return s.ListSchedulableByGroupIDAndPlatform(ctx, seedanceRegisteredGroupID, platform)
}

func (s *seedanceRegisteredAccountRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	return s.ListSchedulableByPlatform(ctx, platform)
}

func (s *seedanceRegisteredAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups = append(s.lookups, id)
	if id != s.account.ID {
		return nil, service.ErrNoAvailableAccounts
	}
	account := s.account
	return &account, nil
}

func (s *seedanceRegisteredAccountRepo) disableScheduling() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.account.Schedulable = false
}

func (s *seedanceRegisteredAccountRepo) snapshot() ([]seedanceRegisteredSelection, []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]seedanceRegisteredSelection(nil), s.selections...), append([]int64(nil), s.lookups...)
}

type seedanceRegisteredUsageRepo struct {
	service.UsageLogRepository
	mu   sync.Mutex
	logs []service.UsageLog
}

func (s *seedanceRegisteredUsageRepo) Create(_ context.Context, entry *service.UsageLog) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, *entry)
	return true, nil
}

func (*seedanceRegisteredUsageRepo) ResolveGatewayLiteUsageIdentity(_ context.Context, userID int64, keyID string) (int64, int64, error) {
	if keyID != seedanceRegisteredOwnerKeyID {
		return 0, 0, errors.New("unexpected billing key")
	}
	return userID, 7001, nil
}

func (s *seedanceRegisteredUsageRepo) snapshot() []service.UsageLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]service.UsageLog(nil), s.logs...)
}

type seedanceRegisteredBillingRepo struct {
	service.UsageBillingRepository
	mu       sync.Mutex
	commands []service.UsageBillingCommand
}

func (s *seedanceRegisteredBillingRepo) Apply(_ context.Context, command *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, *command)
	// 只证明提交一次本地计费命令，不模拟、更不声称证明主站真实钱包扣款。
	return &service.UsageBillingApplyResult{Applied: false}, nil
}

func (s *seedanceRegisteredBillingRepo) snapshot() []service.UsageBillingCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]service.UsageBillingCommand(nil), s.commands...)
}

type seedanceRegisteredUpstreamCall struct {
	method, path, body, authorization string
	accountID                         int64
}

type seedanceRegisteredUpstream struct {
	client    *http.Client
	mu        sync.Mutex
	calls     []seedanceRegisteredUpstreamCall
	arkCalls  atomic.Int64
	succeeded atomic.Bool
}

func (s *seedanceRegisteredUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	s.mu.Lock()
	s.calls = append(s.calls, seedanceRegisteredUpstreamCall{req.Method, req.URL.Path, string(body), req.Header.Get("Authorization"), accountID})
	s.mu.Unlock()
	return s.client.Do(req)
}

func (s *seedanceRegisteredUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxy, accountID, concurrency)
}

func (s *seedanceRegisteredUpstream) snapshot() []seedanceRegisteredUpstreamCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]seedanceRegisteredUpstreamCall(nil), s.calls...)
}

func (s *seedanceRegisteredUpstream) serveArk(w http.ResponseWriter, r *http.Request) {
	s.arkCalls.Add(1)
	if r.Header.Get("Authorization") != "Bearer synthetic-ark-account" {
		http.Error(w, "unexpected upstream credential", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	path := "/api/v3" + seedanceRegisteredTaskPath
	switch {
	case r.Method == http.MethodPost && r.URL.Path == path:
		_ = json.NewEncoder(w).Encode(map[string]any{"id": seedanceRegisteredTaskID, "status": "queued"})
	case r.Method == http.MethodGet && r.URL.Path == path+"/"+seedanceRegisteredTaskID:
		status, tokens := "running", 0
		if s.succeeded.Load() {
			status, tokens = "succeeded", 1000000
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": seedanceRegisteredTaskID, "status": status, "model": seedanceRegisteredUpstreamModel, "usage": map[string]any{"completion_tokens": tokens}})
	case r.Method == http.MethodDelete && r.URL.Path == path+"/"+seedanceRegisteredTaskID:
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

type seedanceRegisteredRuntime struct {
	router  *gin.Engine
	gateway *service.OpenAIGatewayService
	handler *handler.OpenAIGatewayHandler
}

type seedanceRegisteredFixture struct {
	cfg            *config.Config
	client         *redis.Client
	quota          *gatewaylite.RedisQuota
	reporter       *gatewaylite.RedisUsageReporter
	runtimeRef     *gatewaylite.RuntimeConfigRef
	control        *seedanceRegisteredControl
	accounts       *seedanceRegisteredAccountRepo
	usage          *seedanceRegisteredUsageRepo
	billing        *seedanceRegisteredBillingRepo
	upstream       *seedanceRegisteredUpstream
	pricing        *service.PricingService
	requestedModel string
	aiStore        *aiRouteStore
}

func newSeedanceRegisteredFixture(t *testing.T, platform string) *seedanceRegisteredFixture {
	t.Helper()
	// 用例改变进程级默认队列/租约；禁止 t.Parallel，并在清理时恢复原值。
	gin.SetMode(gin.TestMode)
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	f := &seedanceRegisteredFixture{
		cfg: &config.Config{RunMode: config.RunModeGatewayLite}, client: client,
		control: &seedanceRegisteredControl{keys: make(map[string]gatewaylite.KeySnapshot)},
		usage:   &seedanceRegisteredUsageRepo{}, billing: &seedanceRegisteredBillingRepo{},
		upstream: &seedanceRegisteredUpstream{}, requestedModel: seedanceRegisteredModel,
	}
	if platform == service.PlatformComposite {
		f.requestedModel = seedanceRegisteredAlias
	}
	secretHash := sha256.Sum256([]byte(seedanceRegisteredSecret))
	for _, keyID := range []string{seedanceRegisteredOwnerKeyID, seedanceRegisteredOtherKeyID} {
		f.control.keys[keyID] = gatewaylite.KeySnapshot{
			KeyID: keyID, UserID: seedanceRegisteredUserID, SecretHash: hex.EncodeToString(secretHash[:]), Status: service.StatusActive,
			BillingSubjectType: "user", BillingSubjectID: seedanceRegisteredUserID,
			Platform: platform, GroupID: seedanceRegisteredGlobalGroupID, Concurrency: 5,
			AllowedModels: []string{f.requestedModel}, GatewayAccessEnforced: true,
			GatewayStrategy: "fixed_group", KeyDefaultGatewayNodeID: seedanceRegisteredNodeID,
			AvailableGateways: []gatewaylite.GatewayRouteSummary{{ID: seedanceRegisteredNodeID, Code: seedanceRegisteredGateway, Region: "test-region"}},
			Group: &gatewaylite.GatewayGroupSnapshot{ID: seedanceRegisteredGlobalGroupID, LocalGroupID: seedanceRegisteredGroupID,
				Name: "Seedance 正式路由测试", Platform: platform, Status: service.StatusActive, RateMultiplier: 2, AllowImageGeneration: true},
		}
	}
	ark := httptest.NewServer(http.HandlerFunc(f.upstream.serveArk))
	t.Cleanup(ark.Close)
	f.upstream.client = ark.Client()
	f.accounts = &seedanceRegisteredAccountRepo{account: service.Account{
		ID: seedanceRegisteredAccountID, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 5, GroupIDs: []int64{seedanceRegisteredGroupID},
		Credentials: map[string]any{"api_key": "synthetic-ark-account", "base_url": ark.URL + "/api/v3",
			"openai_capabilities": []string{"seedance"}, "model_mapping": map[string]any{seedanceRegisteredModel: seedanceRegisteredUpstreamModel}},
	}}
	f.cfg.Default.RateMultiplier = 1
	f.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	f.cfg.Gateway.MaxBodySize, f.cfg.Gateway.TextMaxBodySize = 1<<20, 1<<20
	f.cfg.GatewayLite.CompositeGroupsEnabled = true
	f.cfg.Pricing.DataDir = t.TempDir()
	f.pricing = service.NewPricingService(f.cfg, nil)
	t.Cleanup(f.pricing.Stop)
	_, err := f.pricing.UpsertModelPricingOverride(seedanceRegisteredModel, service.LiteLLMModelPricing{InputCostPerToken: 5e-6, OutputCostPerToken: 3e-5})
	require.NoError(t, err)
	f.quota = gatewaylite.NewRedisQuota(client, seedanceRegisteredPrefix)
	f.reporter = gatewaylite.NewRedisUsageReporter(client, seedanceRegisteredPrefix, f.control)
	f.runtimeRef = gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "test-region", GatewayCode: seedanceRegisteredGateway, RedisPrefix: seedanceRegisteredPrefix})
	previousReporter, previousQuota, previousRuntime := gatewaylite.DefaultUsageReporter(), gatewaylite.DefaultRedisQuota(), gatewaylite.DefaultRuntimeConfigRef()
	gatewaylite.SetDefaultUsageReporter(f.reporter)
	gatewaylite.SetDefaultRedisQuota(f.quota)
	gatewaylite.SetDefaultRuntimeConfigRef(f.runtimeRef)
	t.Cleanup(func() {
		gatewaylite.SetDefaultUsageReporter(previousReporter)
		gatewaylite.SetDefaultRedisQuota(previousQuota)
		gatewaylite.SetDefaultRuntimeConfigRef(previousRuntime)
	})
	workerCtx, stop := context.WithCancel(context.Background())
	f.reporter.StartWorkers(workerCtx, 1)
	t.Cleanup(stop)
	return f
}

func (f *seedanceRegisteredFixture) newRuntime(t *testing.T, aiEnabled bool) seedanceRegisteredRuntime {
	t.Helper()
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, f.cfg, nil)
	t.Cleanup(billingCache.Stop)
	concurrency := service.NewConcurrencyService(repository.NewConcurrencyCache(f.client, 5, 30))
	gateway := service.NewOpenAIGatewayService(f.accounts, f.usage, f.billing, nil, nil, nil, repository.NewGatewayCache(f.client), f.cfg,
		nil, concurrency, service.NewBillingService(f.cfg, f.pricing), nil, billingCache, f.upstream, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(gateway.CloseOpenAIWSPool)
	apiKeys := service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, f.cfg)
	var moderation *service.ContentModerationService
	if aiEnabled {
		settings := service.ContentModerationConfig{Enabled: true, Mode: "pre_block", KeywordBlockingMode: "ai_only", PolicyVersion: 2, AllGroups: true,
			AI: service.ContentModerationAIConfig{APIKey: "synthetic-review-key", BaseURL: f.accounts.account.Credentials["base_url"].(string), Model: "deepseek-flash", ExternalDataConsent: true, PenaltiesEnabled: true}}
		raw, err := json.Marshal(settings)
		require.NoError(t, err)
		moderation = service.NewContentModerationService(&aiRouteSettings{values: map[string]string{
			service.SettingKeyRiskControlEnabled: "true", service.SettingKeyContentModerationConfig: string(raw),
		}}, nil, nil, nil, nil, nil, nil)
		f.aiStore = &aiRouteStore{}
		moderation.SetAIStore(f.aiStore)
	}
	openai := handler.NewOpenAIGatewayHandler(gateway, concurrency, billingCache, apiKeys, nil, nil, moderation, f.pricing, nil, f.cfg)
	resolver := service.NewCompositeRouteResolver(routeCompositeRepoStub{routes: []service.CompositeModelRoute{{
		ID: 1, GroupID: seedanceRegisteredGroupID, PublicModel: seedanceRegisteredAlias, MatchType: service.CompositeRouteMatchExact,
		TargetPlatform: service.PlatformOpenAI, UpstreamModel: seedanceRegisteredModel, Endpoint: service.CompositeRouteEndpointAny, Enabled: true,
	}}})
	// 不预设 Gin 鉴权/平台/资金上下文；所有请求仅通过 Bearer 和正式中间件进入。
	// 不启用 Key 正缓存，以使测试中的主站快照改动在下一次真实鉴权立即可见。
	auth := middleware.NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfig(f.control, f.runtimeRef, f.quota, nil, f.reporter)
	router := gin.New()
	RegisterGatewayRoutes(router, &handler.Handlers{OpenAIGateway: openai, Gateway: &handler.GatewayHandler{}}, auth, apiKeys, nil, nil, nil, resolver, f.cfg)
	return seedanceRegisteredRuntime{router: router, gateway: gateway, handler: openai}
}

func seedanceRegisteredBearer(keyID string) string {
	return "aiceo_sk_" + keyID + "_" + seedanceRegisteredSecret
}

func (f *seedanceRegisteredFixture) perform(runtime seedanceRegisteredRuntime, method, path, bearer string) *httptest.ResponseRecorder {
	body := ""
	if method == http.MethodPost {
		body = `{"model":"` + f.requestedModel + `","content":` + seedanceRegisteredContent + `}`
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	runtime.router.ServeHTTP(w, req)
	return w
}

func (f *seedanceRegisteredFixture) pending(t *testing.T, runtime seedanceRegisteredRuntime) *service.GrokVideoPendingBilling {
	t.Helper()
	// 仅在成功创建后读取生产写入的快照；测试不调用 Prepare/Store 来构造任务。
	pending, err := runtime.gateway.LoadSeedancePendingBilling(context.Background(), service.SeedanceTaskKey(seedanceRegisteredTaskID), service.MediaTaskIdentity{
		Kind: "remote", UserID: seedanceRegisteredUserID, GatewayKeyID: seedanceRegisteredOwnerKeyID,
		GatewayID: seedanceRegisteredGateway, BillingSubjectType: "user", BillingSubjectID: seedanceRegisteredUserID,
	})
	require.NoError(t, err)
	require.NotNil(t, pending)
	return pending
}

func (f *seedanceRegisteredFixture) settledQuota(t *testing.T, requestID, leaseID string) (map[string]string, gatewaylite.LeaseSnapshot) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// 主站事件不传 QuotaRequestID；直接读取原创建请求的生产 Redis 记录，不能预写结算状态。
	request, err := f.client.HGetAll(ctx, seedanceRegisteredPrefix+":req:"+requestID).Result()
	require.NoError(t, err)
	require.NotEmpty(t, request, "原创建请求的额度记录必须存在")
	require.Equal(t, requestID, request["request_id"])
	require.Equal(t, seedanceRegisteredOwnerKeyID, request["key_id"])
	require.Equal(t, strconv.FormatInt(seedanceRegisteredUserID, 10), request["user_id"])
	require.Equal(t, "user", request["billing_subject_type"])
	require.Equal(t, strconv.FormatInt(seedanceRegisteredUserID, 10), request["billing_subject_id"])
	require.Equal(t, seedanceRegisteredGateway, request["region"])
	require.Equal(t, leaseID, request["lease_id"])
	require.Equal(t, seedanceRegisteredPrefix+":lease:"+strconv.FormatInt(seedanceRegisteredUserID, 10)+":"+seedanceRegisteredGateway, request["lease_key"])
	require.Equal(t, "1", request["usage_report_enqueued"], "原请求必须留下真实队列交接标记")
	lease, found, err := f.quota.LoadLeaseForSubject(ctx, gatewaylite.LeaseSubject{
		UserID: seedanceRegisteredUserID, BillingSubjectType: "user", BillingSubjectID: seedanceRegisteredUserID,
	}, seedanceRegisteredGateway)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, leaseID, lease.LeaseID)
	require.Equal(t, seedanceRegisteredUserID, lease.UserID)
	require.Equal(t, "user", lease.BillingSubjectType)
	require.Equal(t, seedanceRegisteredUserID, lease.BillingSubjectID)
	// 已上报不等于本地额度已提交；请求必须进入 committed（已提交），6000 美分须从预留转为已用。
	require.Equal(t, "committed", request["status"], "完成结算必须提交原额度请求；request=%v lease=%+v", request, lease)
	require.Equal(t, "6000", request["actual_cents"])
	require.EqualValues(t, 6000, lease.SpentCents)
	require.Zero(t, lease.ReservedCents)
	return request, lease
}

func (f *seedanceRegisteredFixture) waitForEvents(t *testing.T, count int) []gatewaylite.UsageEvent {
	t.Helper()
	require.Eventually(t, func() bool {
		stats, err := f.reporter.UsageQueueStats(context.Background())
		return err == nil && stats.StreamLength == 0 && stats.PendingCount == 0 && stats.DeadCount == 0 && len(f.control.snapshot().events) == count
	}, 3*time.Second, 10*time.Millisecond, "可靠队列须排空后再断言事件次数")
	return f.control.snapshot().events
}

var (
	_ middleware.GatewayLiteControlPlane = (*seedanceRegisteredControl)(nil)
	_ gatewaylite.FundingControlPlane    = (*seedanceRegisteredControl)(nil)
	_ gatewaylite.UsageReportClient      = (*seedanceRegisteredControl)(nil)
	_ service.AccountRepository          = (*seedanceRegisteredAccountRepo)(nil)
	_ service.UsageLogRepository         = (*seedanceRegisteredUsageRepo)(nil)
	_ service.UsageBillingRepository     = (*seedanceRegisteredBillingRepo)(nil)
	_ service.HTTPUpstream               = (*seedanceRegisteredUpstream)(nil)
)
