package handler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 只观测调用次数与生产生成的存储键；快照和认领仍交给真实 Redis 缓存。
type seedanceCompletionIdentityCache struct {
	service.GatewayCache
	service.SeedanceBillingStore
	taskKey      string
	claimCalls   int
	confirmCalls int
}

func (c *seedanceCompletionIdentityCache) SetGrokVideoPendingBilling(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	c.taskKey = key
	return c.GatewayCache.(interface {
		SetGrokVideoPendingBilling(context.Context, string, []byte, time.Duration) error
	}).SetGrokVideoPendingBilling(ctx, key, payload, ttl)
}

func (c *seedanceCompletionIdentityCache) ClaimSeedanceBilling(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	c.claimCalls++
	return c.SeedanceBillingStore.ClaimSeedanceBilling(ctx, key, token, ttl)
}

func (c *seedanceCompletionIdentityCache) ConfirmSeedanceBilling(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	c.confirmCalls++
	return c.SeedanceBillingStore.ConfirmSeedanceBilling(ctx, key, token, ttl)
}

type seedanceCompletionIdentityFixture struct {
	ctx           context.Context
	h             *OpenAIGatewayHandler
	client        *redis.Client
	quota         *gatewaylite.RedisQuota
	cache         *seedanceCompletionIdentityCache
	key           *service.APIKey
	account       *service.Account
	owner         service.MediaTaskIdentity
	meta          gatewaylite.RequestMeta
	auth          gatewaylite.FundingAuthorization
	taskID        string
	result        *service.OpenAIForwardResult
	usageRepo     *grokGatewayLiteE2EUsageLogRepo
	billingRepo   *grokGatewayLiteE2EBillingRepo
	recordCalls   int
	holdRequestID string
}

const seedanceCompletionIdentityPrefix = "seedance-completion-identity"

func newSeedanceCompletionIdentityFixture(t *testing.T, path string) *seedanceCompletionIdentityFixture {
	t.Helper()
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	quota := gatewaylite.NewRedisQuota(client, seedanceCompletionIdentityPrefix)
	previousQuota, previousReporter := gatewaylite.DefaultRedisQuota(), gatewaylite.DefaultUsageReporter()
	gatewaylite.SetDefaultRedisQuota(quota)
	gatewaylite.SetDefaultUsageReporter(gatewaylite.NewRedisUsageReporter(client, seedanceCompletionIdentityPrefix, &grokGatewayLiteE2EControl{}))
	t.Cleanup(func() {
		gatewaylite.SetDefaultRedisQuota(previousQuota)
		gatewaylite.SetDefaultUsageReporter(previousReporter)
	})
	baseCache := repository.NewGatewayCache(client)
	cache := &seedanceCompletionIdentityCache{GatewayCache: baseCache, SeedanceBillingStore: baseCache.(service.SeedanceBillingStore)}
	groupID := int64(7)
	account := &service.Account{ID: 4242, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive}
	key := &service.APIKey{UserID: 33, GroupID: &groupID, User: &service.User{ID: 33},
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, RateMultiplier: 2}}
	meta := gatewaylite.RequestMeta{RequestID: "original-create", KeyID: "grok-e2e-key", UserID: 33,
		GatewayID: "gateway-a", Region: "test", GroupID: groupID, BillingSubjectType: "user", BillingSubjectID: 33,
		LeaseID: "original-lease", BillingSettlementMode: gatewaylite.BillingSettlementModeLegacy}
	f := &seedanceCompletionIdentityFixture{client: client, quota: quota, cache: cache, key: key, account: account,
		meta: meta, taskID: service.SeedanceTaskKey("identity-task"), holdRequestID: meta.RequestID,
		usageRepo: &grokGatewayLiteE2EUsageLogRepo{}, billingRepo: &grokGatewayLiteE2EBillingRepo{}}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	cfg.Default.RateMultiplier = 1
	gateway := service.NewOpenAIGatewayService(
		&grokGatewayLiteE2EAccountRepo{account: *account}, f.usageRepo, f.billingRepo, nil, nil, nil, cache, cfg,
		nil, nil, service.NewBillingService(cfg, nil), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	f.h = &OpenAIGatewayHandler{gatewayService: gateway}
	createCtx := gatewaylite.ContextWithRequestMeta(context.Background(), meta)
	if path == "local" {
		key.ID = 11
		createCtx = service.ContextWithGatewayLiteLocalAPIKey(context.WithValue(context.Background(), ctxkey.RequestID, "local-create"))
		f.holdRequestID = "unrelated-hold"
	} else if path == "funded" {
		f.auth = gatewaylite.FundingAuthorization{AuthorizationID: "original-funding", RequestID: meta.RequestID,
			KeyID: meta.KeyID, UserID: meta.UserID, GatewayCode: meta.GatewayID, Region: meta.Region,
			EntryGatewayNodeID: 9, EntryGroupID: groupID, RequestedModel: "video", Endpoint: "/api/v3/contents/generations/tasks",
			BillingSubjectType: meta.BillingSubjectType, BillingSubjectID: meta.BillingSubjectID,
			Version: 1, Status: "active", Disposition: "authorized", UpperBoundMicroCents: 7000000000,
			ExecutionDeadlineMillis: time.Now().Add(time.Hour).UnixMilli(),
			Items:                   []gatewaylite.FundingAuthorizationItem{{ItemID: 1, Ordinal: 1, Source: "balance", ReservedMicroCents: 7000000000}}}
		require.NoError(t, quota.ReserveFunding(context.Background(), f.auth))
		f.meta.LeaseID = ""
		fundedMeta := f.meta
		fundedMeta.Funding = gatewaylite.NewRequestFunding(f.auth, nil, quota)
		createCtx = gatewaylite.ContextWithRequestMeta(context.Background(), fundedMeta)
		f.holdRequestID = "unrelated-hold"
	}
	// 本地/授权卡对照也保留同用户的独立余额占用，证明完成不会误扣该租约。
	require.NoError(t, quota.EnsureLease(context.Background(), gatewaylite.LeaseSnapshot{LeaseID: "original-lease",
		UserID: 33, BillingSubjectType: "user", BillingSubjectID: 33, Region: "test", AllocatedCents: 1000000,
		Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()}))
	reserved, err := quota.Reserve(context.Background(), gatewaylite.ReserveRequest{RequestID: f.holdRequestID,
		KeyID: meta.KeyID, UserID: 33, BillingSubjectType: "user", BillingSubjectID: 33,
		Region: "test", LeaseID: "original-lease", EstimatedCostCents: 2})
	require.NoError(t, err)
	require.True(t, reserved)
	f.owner, err = service.ResolveMediaTaskIdentity(createCtx, key, 33)
	require.NoError(t, err)
	// 使用既有价格辅助及真实任务保存，损坏用例只会在此后改一个原身份字段。
	require.NoError(t, gateway.StoreSeedancePendingBilling(createCtx, f.taskID, f.owner, service.GrokVideoPendingBilling{
		TaskID: f.taskID, Model: "video", BillingModel: "video", UpstreamModel: "video-upstream", AccountID: account.ID,
		GroupID: groupID, ExecutionPricing: seedanceTestExecutionPricing(t, "video", 2),
	}))
	pollMeta := meta
	pollMeta.RequestID, pollMeta.LeaseID = "free-poll", "poll-lease"
	f.ctx = gatewaylite.ContextWithRequestMeta(context.Background(), pollMeta)
	if path == "local" {
		f.ctx = service.ContextWithGatewayLiteLocalAPIKey(context.WithValue(context.Background(), ctxkey.RequestID, "free-poll"))
	}
	f.result = &service.OpenAIForwardResult{Usage: service.OpenAIUsage{OutputTokens: 1000000}, ResponseID: f.taskID}
	return f
}

func (f *seedanceCompletionIdentityFixture) record(ctx context.Context, result *service.OpenAIForwardResult) error {
	f.recordCalls++
	return f.h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
		Result: result, APIKey: f.key, User: f.key.User, Account: f.account,
		InboundEndpoint: "/api/v3/contents/generations/tasks", UpstreamEndpoint: "/api/v3/contents/generations/tasks",
	})
}

func (f *seedanceCompletionIdentityFixture) hold(t *testing.T) (gatewaylite.LeaseSnapshot, map[string]string) {
	t.Helper()
	lease, found, err := f.quota.LoadLeaseForSubject(f.ctx, gatewaylite.LeaseSubject{UserID: 33, BillingSubjectType: "user", BillingSubjectID: 33}, "test")
	require.NoError(t, err)
	require.True(t, found)
	request, err := f.client.HGetAll(f.ctx, seedanceCompletionIdentityPrefix+":req:"+f.holdRequestID).Result()
	require.NoError(t, err)
	return lease, request
}

func TestSeedanceCompletionIdentityRejectsDamagedOriginal(t *testing.T) {
	for _, field := range []string{"empty_request_id", "blank_request_id", "zero_user_id", "negative_user_id"} {
		t.Run(field, func(t *testing.T) {
			f := newSeedanceCompletionIdentityFixture(t, "remote")
			payload, err := f.cache.GetGrokVideoPendingBilling(f.ctx, f.cache.taskKey)
			require.NoError(t, err)
			var pending service.GrokVideoPendingBilling
			require.NoError(t, json.Unmarshal(payload, &pending))
			require.NotNil(t, pending.RequestMeta)
			originalOwner, originalPrice := *pending.Owner, pending.ExecutionPricing.Clone()
			switch field {
			case "empty_request_id":
				pending.RequestMeta.RequestID = ""
			case "blank_request_id":
				pending.RequestMeta.RequestID = " \t\n "
			case "zero_user_id":
				pending.RequestMeta.UserID = 0
			case "negative_user_id":
				pending.RequestMeta.UserID = -1
			}
			payload, err = json.Marshal(pending)
			require.NoError(t, err)
			require.NoError(t, f.cache.SetGrokVideoPendingBilling(f.ctx, f.cache.taskKey, payload, time.Hour))
			stored, err := f.h.gatewayService.LoadSeedancePendingBilling(f.ctx, f.taskID, f.owner)
			require.NoError(t, err)
			require.NotNil(t, stored)
			require.NotNil(t, stored.RequestMeta, "故障必须来自非nil残缺快照，不是任务丢失")
			require.Equal(t, originalOwner, *stored.Owner)
			require.Equal(t, originalPrice, stored.ExecutionPricing)
			require.NoError(t, service.ValidateSeedanceExecutionPricing(stored))
			beforeLease, beforeRequest := f.hold(t)
			require.EqualValues(t, 2, beforeLease.ReservedCents)
			require.Zero(t, beforeLease.SpentCents)
			// 早断言失败也保留零副作用观测；无需故意继续执行错误确认。
			t.Cleanup(func() {
				assert.Zero(t, f.cache.claimCalls, "残缺原身份必须在认领前拒绝")
				assert.Zero(t, f.recordCalls)
				assert.Zero(t, f.cache.confirmCalls)
				assert.Empty(t, f.billingRepo.snapshot())
				assert.Empty(t, f.usageRepo.snapshot())
				assert.ErrorIs(t, f.client.Get(f.ctx, "seedance_video_billed:"+f.cache.taskKey).Err(), redis.Nil)
				afterLease, afterRequest := f.hold(t)
				assert.Equal(t, beforeLease, afterLease)
				assert.Equal(t, beforeRequest, afterRequest)
				length, err := f.client.XLen(f.ctx, seedanceCompletionIdentityPrefix+":usage:stream").Result()
				assert.NoError(t, err)
				assert.Zero(t, length)
			})
			completion, err := prepareSeedanceCompletionBilling(f.ctx, f.h, f.key, middleware.AuthSubject{UserID: 33}, f.taskID, f.result)
			require.Error(t, err, "不能让失效的原RequestMeta绕过远程异步记账检查")
			require.Nil(t, completion)
			require.NoError(t, settleSeedanceCompletion(f.ctx, f.h, completion, f.record))
		})
	}
}

func TestSeedanceCompletionIdentityCompletePaths(t *testing.T) {
	for _, path := range []string{"remote", "funded", "local"} {
		t.Run(path, func(t *testing.T) {
			f := newSeedanceCompletionIdentityFixture(t, path)
			beforeLease, beforeRequest := f.hold(t)
			completion, err := prepareSeedanceCompletionBilling(f.ctx, f.h, f.key, middleware.AuthSubject{UserID: 33}, f.taskID, f.result)
			require.NoError(t, err)
			require.NotNil(t, completion)
			require.Equal(t, 1, f.cache.claimCalls)
			require.Equal(t, f.owner, completion.owner)
			require.NoError(t, settleSeedanceCompletion(f.ctx, f.h, completion, func(ctx context.Context, result *service.OpenAIForwardResult) error {
				restored, remote := gatewaylite.RequestMetaFromContext(ctx)
				if path == "local" {
					require.False(t, remote)
					require.True(t, service.IsGatewayLiteLocalAPIKeyContext(ctx))
					id, ok := gatewaylite.CanonicalRequestIDFromContext(ctx)
					require.True(t, ok)
					require.Equal(t, "local:"+service.StableGrokVideoBillingRequestID(f.taskID), id)
				} else {
					require.True(t, remote)
					require.Equal(t, f.meta.RequestID, restored.RequestID)
					require.Equal(t, f.meta.UserID, restored.UserID)
					require.Equal(t, f.meta.KeyID, restored.KeyID)
					require.Equal(t, f.meta.LeaseID, restored.LeaseID)
					require.Equal(t, path == "remote", restored.CommitAsyncReservation)
					if path == "funded" {
						auth, ok := gatewaylite.FundingFromContext(ctx)
						require.True(t, ok)
						require.Equal(t, f.auth, auth)
					}
				}
				return f.record(ctx, result)
			}))
			require.Equal(t, 1, f.recordCalls, "对照必须运行真实RecordUsage")
			require.Equal(t, 1, f.cache.confirmCalls)
			require.Equal(t, "done", f.client.Get(f.ctx, "seedance_video_billed:"+f.cache.taskKey).Val())
			require.Len(t, f.usageRepo.snapshot(), 1)
			afterLease, afterRequest := f.hold(t)
			if path == "remote" {
				require.EqualValues(t, 6000, afterLease.SpentCents)
				require.Zero(t, afterLease.ReservedCents)
				require.Equal(t, "committed", afterRequest["status"])
				require.Equal(t, "6000", afterRequest["actual_cents"])
			} else {
				require.Equal(t, beforeLease, afterLease)
				require.Equal(t, beforeRequest, afterRequest)
			}
			messages, err := f.client.XRange(f.ctx, seedanceCompletionIdentityPrefix+":usage:stream", "-", "+").Result()
			require.NoError(t, err)
			if path == "local" {
				require.Empty(t, f.billingRepo.snapshot())
				require.Empty(t, messages)
			} else {
				require.Len(t, f.billingRepo.snapshot(), 1)
				require.Len(t, messages, 1)
				var queued struct {
					Event gatewaylite.UsageEvent `json:"event"`
				}
				require.NoError(t, json.Unmarshal([]byte(messages[0].Values["event"].(string)), &queued))
				require.Equal(t, f.meta.RequestID, queued.Event.RequestID)
				if path == "funded" {
					require.Equal(t, f.auth.AuthorizationID, queued.Event.FundingAuthorizationID)
					require.True(t, queued.Event.Final)
					require.Empty(t, queued.Event.LeaseID)
				} else {
					require.False(t, queued.Event.Final)
					require.Equal(t, f.meta.LeaseID, queued.Event.LeaseID)
				}
			}
			completed, err := prepareSeedanceCompletionBilling(f.ctx, f.h, f.key, middleware.AuthSubject{UserID: 33}, f.taskID, f.result)
			require.NoError(t, err)
			require.Nil(t, completed, "真实确认之后不能再次记账")
			require.NoError(t, settleSeedanceCompletion(f.ctx, f.h, completed, f.record))
			require.Equal(t, 1, f.recordCalls)
			require.Equal(t, 1, f.cache.confirmCalls)
		})
	}
}
