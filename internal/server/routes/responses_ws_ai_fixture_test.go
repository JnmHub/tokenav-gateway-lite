package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	responsesWSAIKeyID = "responseswsai"
	responsesWSAIKey   = "tokenav_sk_responseswsai_synthetic"
	responsesWSAITrace = "client-repeated-trace"
	responsesWSAINode  = "responses-ws-ai-node"
	responsesWSAIModel = "gpt-5.4"
	responsesWSAIWait  = 5 * time.Second
)

// 通知只唤醒观察者，记录始终留存；失败或乱序不能把前例消息当成本例证据。
type responsesWSAIRecords[T any] struct {
	mu      sync.Mutex
	entries []T
	changed chan struct{}
}

func (r *responsesWSAIRecords[T]) add(entry T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry)
	if r.changed != nil {
		close(r.changed)
	}
	r.changed = make(chan struct{})
}

func (r *responsesWSAIRecords[T]) snapshot() ([]T, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.changed == nil {
		r.changed = make(chan struct{})
	}
	return append([]T(nil), r.entries...), r.changed
}

type responsesWSAIPenalty struct {
	ID, Key, Action, Category string
	UserID, Amount            int64
}

// 审查存储只观察处罚投递；业务额度仍由真实 Redis 租约和结算链验证。
type responsesWSAIStore struct {
	mu        sync.Mutex
	penalties []responsesWSAIPenalty
}

func (s *responsesWSAIStore) EnqueuePenalty(_ context.Context, in gatewaylite.ContentModerationPenaltyRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.penalties = append(s.penalties, responsesWSAIPenalty{in.RequestID, in.KeyID, in.Action, in.Category, in.UserID, in.DeductBalanceCents})
	return nil
}

func (*responsesWSAIStore) ClaimPenalty(context.Context, time.Time) (*service.ContentModerationAIPenaltyEvent, error) {
	return nil, nil
}

func (*responsesWSAIStore) FinishPenalty(context.Context, service.ContentModerationAIPenaltyEvent, error, bool) error {
	return nil
}

func (s *responsesWSAIStore) Stats(context.Context, string, time.Time) (service.ContentModerationAIStoreStats, error) {
	return service.ContentModerationAIStoreStats{PenaltyPending: int64(len(s.snapshot()))}, nil
}

func (s *responsesWSAIStore) snapshot() []responsesWSAIPenalty {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]responsesWSAIPenalty(nil), s.penalties...)
}

type responsesWSAIAudit struct {
	ID, Action, Code, Key, Endpoint, Excerpt string
	Text, ReviewedText, Phase, Reason        string
	UserID, BillingSubjectID                 int64
	BillingSubjectType                       string
	Flagged, ProviderCalled                  bool
	Categories                               []string
	Coverage                                 service.ContentModerationAICoverage
}

type responsesWSAIRepo struct {
	service.ContentModerationRepository
	created         responsesWSAIRecords[responsesWSAIAudit]
	legacyPenalties atomic.Int64
}

func (r *responsesWSAIRepo) CreateLog(_ context.Context, in *service.ContentModerationLog) error {
	audit := responsesWSAIAudit{
		ID: in.RequestID, Action: in.Action, Code: in.Error, Key: in.ControlPlaneKeyID,
		Endpoint: in.Endpoint, Excerpt: in.InputExcerpt, Text: in.FullInputText, ReviewedText: in.ReviewTextExcerpt,
		Flagged: in.Flagged, BillingSubjectType: in.BillingSubjectType, BillingSubjectID: in.BillingSubjectID,
	}
	if in.UserID != nil {
		audit.UserID = *in.UserID
	}
	audit.Phase, _ = in.AIReviewDetail["phase"].(string)
	audit.Reason, _ = in.AIReviewDetail["reason"].(string)
	audit.ProviderCalled, _ = in.AIReviewDetail["provider_called"].(bool)
	audit.Coverage, _ = in.AIReviewDetail["coverage"].(service.ContentModerationAICoverage)
	categories, _ := in.AIReviewDetail["categories"].([]string)
	audit.Categories = append([]string(nil), categories...)
	r.created.add(audit)
	return nil
}

func (*responsesWSAIRepo) CleanupExpiredLogs(context.Context, time.Time, time.Time) (*service.ContentModerationCleanupResult, error) {
	return &service.ContentModerationCleanupResult{FinishedAt: time.Now()}, nil
}

func (r *responsesWSAIRepo) RecordUserPenalty(context.Context, service.ContentModerationRecordUserPenaltyInput) (*service.ContentModerationUserPenalty, error) {
	r.legacyPenalties.Add(1)
	return nil, errors.New("AI review must not call legacy local punishment")
}

func (*responsesWSAIRepo) IsUserPermanentlyBanned(context.Context, int64) (bool, error) {
	return false, nil
}

func (r *responsesWSAIRepo) CountFlaggedByUserSince(context.Context, int64, time.Time, bool) (int, error) {
	r.legacyPenalties.Add(1)
	return 0, errors.New("AI error must not enter legacy violation counting")
}

type responsesWSAIUsage struct {
	ID            string
	Cost          float64
	Input, Output int
}

type responsesWSAILogs struct {
	service.UsageLogRepository
	created responsesWSAIRecords[responsesWSAIUsage]
}

func (r *responsesWSAILogs) Create(_ context.Context, in *service.UsageLog) (bool, error) {
	r.created.add(responsesWSAIUsage{in.RequestID, in.ActualCost, in.InputTokens, in.OutputTokens})
	return true, nil
}

func (*responsesWSAILogs) ResolveGatewayLiteUsageIdentity(context.Context, int64, string) (int64, int64, error) {
	return 33, 9, nil
}

type responsesWSAIBilling struct {
	service.UsageBillingRepository
	mu    sync.Mutex
	calls []responsesWSAIUsage
}

func (r *responsesWSAIBilling) Apply(_ context.Context, in *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, responsesWSAIUsage{in.RequestID, in.BalanceCost, in.InputTokens, in.OutputTokens})
	// 只观察真实计价器交来的命令；远程主站 Key 的实际区域结算仍由生产 Redis 队列处理。
	return &service.UsageBillingApplyResult{Applied: false}, nil
}

func (r *responsesWSAIBilling) snapshot() []responsesWSAIUsage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]responsesWSAIUsage(nil), r.calls...)
}

type responsesWSAIAccounts struct {
	service.AccountRepository
	account service.Account
}

func (r *responsesWSAIAccounts) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	if platform != r.account.Platform {
		return nil, nil
	}
	return []service.Account{r.account}, nil
}

func (r *responsesWSAIAccounts) ListSchedulableByGroupIDAndPlatform(ctx context.Context, group int64, platform string) ([]service.Account, error) {
	if group != 7 {
		return nil, nil
	}
	return r.ListSchedulableByPlatform(ctx, platform)
}

func (r *responsesWSAIAccounts) GetByID(_ context.Context, id int64) (*service.Account, error) {
	if id != r.account.ID {
		return nil, nil
	}
	account := r.account
	return &account, nil
}

// 故意不实现 FundingControlPlane/OAuth 接口：只测普通主站 Key 的真实租约链。
type responsesWSAIControl struct{ resolves, leases atomic.Int64 }

func (s *responsesWSAIControl) ResolveKey(_ context.Context, in gatewaylite.ResolveKeyRequest) (*gatewaylite.ResolveKeyResponse, error) {
	s.resolves.Add(1)
	if in.KeyID != responsesWSAIKeyID || in.GatewayCode != responsesWSAINode {
		return nil, errors.New("unexpected ordinary key scope")
	}
	digest := sha256.Sum256([]byte("synthetic"))
	return &gatewaylite.ResolveKeyResponse{OK: true, Key: gatewaylite.KeySnapshot{
		KeyID: responsesWSAIKeyID, UserID: 33, SecretHash: hex.EncodeToString(digest[:]), Status: "active",
		Platform: service.PlatformOpenAI, BillingSubjectType: "user", BillingSubjectID: 33,
		GroupID: 50, KeyDefaultGatewayNodeID: 5, Concurrency: 4, AllowedModels: []string{responsesWSAIModel},
		GatewayStrategy: "fixed_group", GatewayAccessEnforced: true,
		AvailableGateways: []gatewaylite.GatewayRouteSummary{{ID: 5, Code: responsesWSAINode}},
		Group: &gatewaylite.GatewayGroupSnapshot{ID: 50, LocalGroupID: 7, GatewayNodeID: 5, GatewayCode: responsesWSAINode,
			Platform: service.PlatformOpenAI, Status: "active", RateMultiplier: 1},
	}}, nil
}

func (s *responsesWSAIControl) AcquireLease(_ context.Context, in gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error) {
	s.leases.Add(1)
	if in.UserID != 33 || in.KeyID != responsesWSAIKeyID || in.Region != responsesWSAINode {
		return nil, errors.New("unexpected lease subject")
	}
	return &gatewaylite.AcquireLeaseResponse{OK: true, Lease: gatewaylite.LeaseSnapshot{
		LeaseID: "responses-ws-ai-lease", UserID: 33, BillingSubjectType: "user", BillingSubjectID: 33,
		Region: in.Region, AllocatedCents: 100000, Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}}, nil
}

func (*responsesWSAIControl) RebalanceLease(context.Context, gatewaylite.RebalanceLeaseRequest) (*gatewaylite.RebalanceLeaseResponse, error) {
	return nil, errors.New("synthetic lease has ample capacity; no rebalance expected")
}

func (*responsesWSAIControl) ReportUsage(context.Context, gatewaylite.UsageEvent) error {
	return errors.New("queue workers must not run in this recording fixture")
}

type responsesWSAIReview struct {
	Text  string
	Input service.ContentModerationAICanonicalInput
	Reply chan string
}

type responsesWSAIDispatch struct {
	Payload []byte
	Number  int64
}

type responsesWSAIEnd struct {
	Canonical, Admission string
	Turn                 uint64
	Authenticated        bool
}

type responsesWSAIConnection struct {
	ID             string
	path           string
	sent, released responsesWSAIRecords[string]
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	end            responsesWSAIEnd // 只在 done 关闭后读取，不并发读取 Gin map。
	resources      responsesWSAIRecords[<-chan struct{}]
	reviews        responsesWSAIRecords[responsesWSAIReview]
	dispatched     responsesWSAIRecords[responsesWSAIDispatch]
}

func (c *responsesWSAIConnection) resource() func() {
	done := make(chan struct{})
	c.resources.add(done)
	return func() { close(done) }
}

type responsesWSAIFixture struct {
	store                            *responsesWSAIStore
	repo                             *responsesWSAIRepo
	logs                             *responsesWSAILogs
	billing                          *responsesWSAIBilling
	control                          *responsesWSAIControl
	redis                            *redis.Client
	quota                            *gatewaylite.RedisQuota
	prefix, url                      string
	creates, providerCalls, sequence atomic.Int64
	connections, inputs              sync.Map
	errors                           responsesWSAIRecords[error]
}

func (f *responsesWSAIFixture) connection() *responsesWSAIConnection {
	ctx, cancel := context.WithCancel(context.Background())
	c := &responsesWSAIConnection{ID: fmt.Sprintf("%s-%d", responsesWSAITrace, f.sequence.Add(1)), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	f.connections.Store(c.ID, c)
	return c
}

func (f *responsesWSAIFixture) fail(err error) { f.errors.add(err) }

func (f *responsesWSAIFixture) observe(c *gin.Context) {
	value, ok := f.connections.Load(c.GetHeader("X-Request-ID"))
	if !ok {
		f.fail(errors.New("unowned gateway connection"))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	owner := value.(*responsesWSAIConnection)
	defer func() {
		// handler 及转发回调已经返回，才读取真实服务端准入字段。
		owner.end.Canonical, _ = gatewaylite.CanonicalRequestIDFromContext(c.Request.Context())
		_, owner.end.Authenticated = gatewaylite.RequestMetaFromContext(c.Request.Context())
		admission, _ := c.Get("content_moderation_admission_id")
		turn, _ := c.Get("content_moderation_admission_turn")
		owner.end.Admission, _ = admission.(string)
		owner.end.Turn, _ = turn.(uint64)
		close(owner.done)
	}()
	c.Next()
}

func (f *responsesWSAIFixture) serveReview(w http.ResponseWriter, r *http.Request) {
	f.providerCalls.Add(1)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		f.fail(err)
		w.WriteHeader(500)
		return
	}
	var input service.ContentModerationAICanonicalInput
	// 多模态报文：user content 是 parts 数组，data JSON 在首项 text 里；
	// 兼容回退旧纯字符串形态。
	userContent := gjson.GetBytes(raw, "messages.1.content")
	if userContent.IsArray() {
		userContent = gjson.GetBytes(raw, "messages.1.content.0.text")
	}
	err = json.Unmarshal([]byte(userContent.String()), &input)
	if err != nil || len(input.Fragments) == 0 || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer review-synthetic" {
		f.fail(errors.New("invalid synthetic review request"))
		w.WriteHeader(500)
		return
	}
	var owner *responsesWSAIConnection
	var text string
	for _, fragment := range input.Fragments {
		if value, ok := f.inputs.Load(fragment.Text); ok {
			owner, text = value.(*responsesWSAIConnection), fragment.Text
			break
		}
	}
	if owner == nil {
		f.fail(errors.New("unowned AI provider request"))
		w.WriteHeader(500)
		return
	}
	defer owner.resource()()
	request := responsesWSAIReview{Text: text, Input: input, Reply: make(chan string, 1)}
	owner.reviews.add(request)
	var outcome string
	select {
	case outcome = <-request.Reply:
	case <-owner.ctx.Done():
		return
	case <-r.Context().Done():
		return
	}
	if outcome == "429" || outcome == "503" {
		status := http.StatusServiceUnavailable
		if outcome == "429" {
			status = http.StatusTooManyRequests
		}
		w.WriteHeader(status)
		return
	}
	result := service.ContentModerationAIResult{
		PolicyVersion: "responses-ws-ai-v1", Verdict: "allow", Categories: []string{},
		Reason: "Synthetic whole-request review completed",
	}
	switch outcome {
	case "block":
		result.Verdict, result.Categories = "block", []string{"violence"}
	case "missing_categories":
		result.Categories = nil
	case "missing_reason":
		result.Reason = ""
	}
	content, _ := json.Marshal(result)
	if outcome == "invalid_schema" {
		content = []byte(`{"verdict":"allow"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	// 模型别名和 usage 缺省不影响整体结论，审查不再依赖运营费用记录。
	err = json.NewEncoder(w).Encode(map[string]any{"model": "synthetic-review-alias", "choices": []any{map[string]any{
		"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)},
	}}})
	if err != nil {
		f.fail(err)
	}
}

func (f *responsesWSAIFixture) serveBusiness(w http.ResponseWriter, r *http.Request) {
	// APIKey 账号会原样转发 User-Agent；只用于关联自有测试 socket，不注入鉴权或会话状态。
	value, ok := f.connections.Load(r.UserAgent())
	if !ok {
		f.fail(errors.New("unowned business websocket"))
		w.WriteHeader(500)
		return
	}
	owner := value.(*responsesWSAIConnection)
	defer owner.resource()()
	conn, err := coderws.Accept(w, r, nil)
	if err != nil {
		f.fail(err)
		return
	}
	defer conn.CloseNow()
	// 与本地网关请求上限一致，避免测试上游的默认帧限制掩盖完整段落送审。
	conn.SetReadLimit(1 << 20)
	for {
		ctx, cancel := context.WithTimeout(owner.ctx, responsesWSAIWait)
		_, payload, readErr := conn.Read(ctx)
		cancel()
		if readErr != nil {
			return
		}
		if gjson.GetBytes(payload, "type").String() != "response.create" {
			f.fail(errors.New("unexpected business websocket frame"))
			return
		}
		n := f.creates.Add(1)
		owner.dispatched.add(responsesWSAIDispatch{append([]byte(nil), payload...), n})
		// 只合成上游协议/用量，计价与入队完全交给真实 gateway handler。
		body := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_ws_ai_%d","model":"gpt-5.4","usage":{"input_tokens":10000,"output_tokens":5}}}`, n)
		ctx, cancel = context.WithTimeout(owner.ctx, responsesWSAIWait)
		err = conn.Write(ctx, coderws.MessageText, []byte(body))
		cancel()
		if err != nil {
			f.fail(err)
			return
		}
	}
}

func newResponsesWSAIFixture(t *testing.T) *responsesWSAIFixture {
	t.Helper()
	// 整张矩阵串行共用服务，避免每个子例启动一组没有 Stop API 的审查后台 worker。
	f := &responsesWSAIFixture{
		store: &responsesWSAIStore{}, repo: &responsesWSAIRepo{},
		logs: &responsesWSAILogs{}, billing: &responsesWSAIBilling{}, control: &responsesWSAIControl{},
		prefix: "responses-ws-ai",
	}
	mini := miniredis.RunT(t)
	f.redis = redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = f.redis.Close() })
	f.quota = gatewaylite.NewRedisQuota(f.redis, f.prefix)
	reporter := gatewaylite.NewRedisUsageReporter(f.redis, f.prefix, f.control)
	runtime := gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "synthetic", GatewayCode: responsesWSAINode, RedisPrefix: f.prefix})
	oldControl, oldQuota, oldRuntime, oldReporter := gatewaylite.DefaultControlPlaneClientRef(), gatewaylite.DefaultRedisQuota(), gatewaylite.DefaultRuntimeConfigRef(), gatewaylite.DefaultUsageReporter()
	gatewaylite.SetDefaultControlPlaneClientRef(nil)
	gatewaylite.SetDefaultRedisQuota(f.quota)
	gatewaylite.SetDefaultRuntimeConfigRef(runtime)
	gatewaylite.SetDefaultUsageReporter(reporter)
	t.Cleanup(func() {
		gatewaylite.SetDefaultControlPlaneClientRef(oldControl)
		gatewaylite.SetDefaultRedisQuota(oldQuota)
		gatewaylite.SetDefaultRuntimeConfigRef(oldRuntime)
		gatewaylite.SetDefaultUsageReporter(oldReporter)
	})
	provider := httptest.NewServer(http.HandlerFunc(f.serveReview))
	t.Cleanup(provider.Close)
	business := httptest.NewServer(http.HandlerFunc(f.serveBusiness))
	t.Cleanup(business.Close)
	cfg := &config.Config{RunMode: config.RunModeGatewayLite, Gateway: config.GatewayConfig{MaxBodySize: 1 << 20, TextMaxBodySize: 1 << 20}}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled, cfg.Gateway.OpenAIWS.APIKeyEnabled = true, true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2, cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true, true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds, cfg.Gateway.OpenAIWS.ReadTimeoutSeconds, cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3, 8, 3
	moderationConfig := service.ContentModerationConfig{
		Enabled: true, Mode: "pre_block", KeywordBlockingMode: "ai_only", PolicyVersion: 2, AllGroups: true,
		WorkerCount: 1, DeductBalanceOnBlockEnabled: true, DeductBalanceOnBlockAmount: 0.25,
		AI: service.ContentModerationAIConfig{Adapter: service.ContentModerationAIAdapterChat, BaseURL: provider.URL, APIKey: "review-synthetic",
			Model: "deepseek-flash", PolicyVersion: "responses-ws-ai-v1", ExternalDataConsent: true, PenaltiesEnabled: true, ImageReviewEnabled: true},
	}
	raw, err := json.Marshal(moderationConfig)
	require.NoError(t, err)
	moderation := service.NewContentModerationService(&aiRouteSettings{values: map[string]string{
		service.SettingKeyRiskControlEnabled: "true", service.SettingKeyContentModerationConfig: string(raw),
	}}, f.repo, nil, nil, nil, nil, nil)
	moderation.SetAIStore(f.store)
	account := service.Account{ID: 4242, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive,
		Schedulable: true, Concurrency: 4, GroupIDs: []int64{7},
		Credentials: map[string]any{"api_key": "business-synthetic", "base_url": business.URL, "model_mapping": map[string]any{responsesWSAIModel: responsesWSAIModel}},
		Extra:       map[string]any{"openai_apikey_responses_websockets_v2_enabled": true, "openai_apikey_responses_websockets_v2_mode": service.OpenAIWSIngressModePassthrough},
	}
	concurrency := service.NewConcurrencyService(repository.NewConcurrencyCache(f.redis, 1, 5))
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	gateway := service.NewOpenAIGatewayService(&responsesWSAIAccounts{account: account}, f.logs, f.billing, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil)
	t.Cleanup(gateway.CloseOpenAIWSPool)
	apiKeys := &service.APIKeyService{}
	openai := handler.NewOpenAIGatewayHandler(gateway, concurrency, billingCache, apiKeys, nil, nil, moderation, nil, nil, cfg)
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	router := gin.New()
	router.Use(servermiddleware.RequestLogger())
	router.Use(f.observe)
	auth := servermiddleware.NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfig(f.control, runtime, f.quota, gatewaylite.NewRedisKeyCache(f.redis, f.prefix), reporter)
	RegisterGatewayRoutes(router, &handler.Handlers{OpenAIGateway: openai, Gateway: &handler.GatewayHandler{}}, auth, apiKeys, nil, nil, nil, nil, cfg)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	f.url = "ws" + strings.TrimPrefix(server.URL, "http")
	return f
}
