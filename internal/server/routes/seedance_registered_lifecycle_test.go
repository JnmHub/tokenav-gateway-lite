package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func seedanceRegisteredPrefixes() []struct{ name, path string } {
	return []struct{ name, path string }{{"api_v3", "/api/v3"}, {"v3", "/v3"}, {"v1", "/v1"}, {"bare", ""}}
}

// 正向期望固定为成功：未修复的 composite→OpenAI 必须在正式 POST 产生红灯，
// 禁止为测试手工指定 Grok 目标，或将已知 400 写成“预期成功的拒绝”。
func TestSeedanceRegisteredLifecycle(t *testing.T) {
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformComposite} {
		t.Run(platform, func(t *testing.T) {
			for _, prefix := range seedanceRegisteredPrefixes() {
				t.Run(prefix.name, func(t *testing.T) {
					f := newSeedanceRegisteredFixture(t, platform)
					original := f.newRuntime(t, false)
					base := prefix.path + seedanceRegisteredTaskPath
					owner := seedanceRegisteredBearer(seedanceRegisteredOwnerKeyID)
					assertSeedanceRegisteredBadCredentials(t, f, original, base)

					created := f.perform(original, http.MethodPost, base, owner)
					require.Equal(t, http.StatusOK, created.Code, "正式创建必须到达 Ark；未修复的聚合平台判断应在此返回 400: %s", created.Body.String())
					require.JSONEq(t, `{"id":"`+seedanceRegisteredTaskID+`","status":"queued"}`, created.Body.String())
					require.NotEmpty(t, created.Header().Get("X-Client-Request-ID"))
					createRequestID := "client:" + created.Header().Get("X-Client-Request-ID")
					calls := f.upstream.snapshot()
					require.Len(t, calls, 1)
					require.Equal(t, http.MethodPost, calls[0].method)
					require.Equal(t, "/api/v3"+seedanceRegisteredTaskPath, calls[0].path)
					require.JSONEq(t, `{"model":"`+seedanceRegisteredUpstreamModel+`","content":`+seedanceRegisteredContent+`}`, calls[0].body,
						"真实聚合映射与账号映射只改变 model，content 必须完整发送")
					pending := f.pending(t, original)
					require.Equal(t, service.SeedanceTaskKey(seedanceRegisteredTaskID), pending.TaskID)
					require.Equal(t, seedanceRegisteredAccountID, pending.AccountID)
					require.Equal(t, seedanceRegisteredGroupID, pending.GroupID)
					require.Equal(t, seedanceRegisteredModel, pending.Model)
					require.Equal(t, seedanceRegisteredModel, pending.BillingModel)
					require.Equal(t, seedanceRegisteredUpstreamModel, pending.UpstreamModel)
					require.Equal(t, f.requestedModel, pending.OriginalModel)
					require.Nil(t, pending.FundingSnapshot, "普通余额测试不得预造固定媒体授权")
					require.NotNil(t, pending.RequestMeta)
					require.Equal(t, createRequestID, pending.RequestMeta.RequestID)
					require.Equal(t, seedanceRegisteredOwnerKeyID, pending.RequestMeta.KeyID)
					require.Equal(t, "seedance-original-lease", pending.RequestMeta.LeaseID)
					require.Nil(t, pending.RequestMeta.Reservation, "持久快照不持有当前请求锁")
					require.NoError(t, service.ValidateSeedanceExecutionPricing(pending))
					require.InDelta(t, 2, pending.ExecutionPricing.RateMultiplier, 1e-12)
					require.NotNil(t, pending.ExecutionPricing.Resolved)
					require.NotNil(t, pending.ExecutionPricing.Resolved.BasePricing)
					require.InDelta(t, 3e-5, pending.ExecutionPricing.Resolved.BasePricing.OutputPricePerToken, 1e-12)
					f.waitForEvents(t, 0)
					require.Empty(t, f.billing.snapshot())
					require.Empty(t, f.usage.snapshot())

					selectionsAtCreate, _ := f.accounts.snapshot()
					// 原账号不再参与新请求调度，旧任务查询/删除仍必须直接使用已保存账号。
					f.accounts.disableScheduling()
					restarted := f.newRuntime(t, false)
					require.NotSame(t, original.router, restarted.router)
					require.NotSame(t, original.gateway, restarted.gateway)
					require.NotSame(t, original.handler, restarted.handler)
					for _, model := range []string{seedanceRegisteredModel, seedanceRegisteredUpstreamModel, f.requestedModel} {
						_, err := f.pricing.UpsertModelPricingOverride(model, service.LiteLLMModelPricing{InputCostPerToken: 1e-3, OutputCostPerToken: 2e-3})
						require.NoError(t, err)
					}
					f.control.updateKey(seedanceRegisteredOwnerKeyID, func(key *gatewaylite.KeySnapshot) { key.Group.RateMultiplier = 9 })
					currentPrice, err := service.NewBillingService(f.cfg, f.pricing).GetModelPricing(seedanceRegisteredModel)
					require.NoError(t, err)
					require.InDelta(t, 2e-3, currentPrice.OutputPricePerToken, 1e-12)
					restored := f.pending(t, restarted)
					require.Equal(t, pending, restored, "重建实例和热改当前价格不得覆盖 Redis 原任务快照")

					assertSeedanceRegisteredOwnership(t, f, restarted, base+"/"+seedanceRegisteredTaskID)
					running := f.perform(restarted, http.MethodGet, base+"/"+seedanceRegisteredTaskID, owner)
					require.Equal(t, http.StatusOK, running.Code, running.Body.String())
					require.Contains(t, running.Body.String(), `"status":"running"`)
					f.waitForEvents(t, 0)
					require.Empty(t, f.billing.snapshot())
					require.Empty(t, f.usage.snapshot())

					f.upstream.succeeded.Store(true)
					for i := 0; i < 3; i++ {
						completed := f.perform(restarted, http.MethodGet, base+"/"+seedanceRegisteredTaskID, owner)
						require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
						require.Contains(t, completed.Body.String(), `"status":"succeeded"`)
					}
					events := f.waitForEvents(t, 1)
					require.Equal(t, createRequestID, events[0].RequestID)
					// QuotaRequestID 只供入队前额度脚本使用（json:"-"），主站事件不得携带该内部字段。
					require.Empty(t, events[0].QuotaRequestID)
					require.Equal(t, seedanceRegisteredOwnerKeyID, events[0].KeyID)
					require.Equal(t, seedanceRegisteredUserID, events[0].UserID)
					require.Equal(t, "user", events[0].BillingSubjectType)
					require.Equal(t, seedanceRegisteredUserID, events[0].BillingSubjectID)
					require.Equal(t, pending.RequestMeta.LeaseID, events[0].LeaseID)
					require.Empty(t, events[0].FundingAuthorizationID)
					require.EqualValues(t, 1000000, events[0].TokensOut)
					require.InDelta(t, 2, events[0].RateMultiplier, 1e-12)
					require.EqualValues(t, 6000, events[0].ActualCents, "按原价 30 美元/百万输出词元 × 原倍率 2 结算")
					require.Equal(t, f.requestedModel, events[0].RequestedModel)
					require.Equal(t, seedanceRegisteredUpstreamModel, events[0].UpstreamModel)
					require.Equal(t, service.PlatformOpenAI, events[0].TargetPlatform)
					commands := f.billing.snapshot()
					require.Len(t, commands, 1, "只证明一次结算命令，不声称主站钱包扣款次数")
					require.Equal(t, createRequestID, commands[0].RequestID)
					require.Equal(t, seedanceRegisteredAccountID, commands[0].AccountID)
					require.Equal(t, 1000000, commands[0].OutputTokens)
					logs := f.usage.snapshot()
					require.Len(t, logs, 1)
					require.Equal(t, createRequestID, logs[0].RequestID)
					require.EqualValues(t, 7001, logs[0].APIKeyID)
					require.Equal(t, seedanceRegisteredAccountID, logs[0].AccountID)
					require.Equal(t, 1000000, logs[0].OutputTokens)
					require.InDelta(t, 2, logs[0].RateMultiplier, 1e-12)
					require.InDelta(t, 30, logs[0].TotalCost, 1e-9)
					require.InDelta(t, 60, logs[0].ActualCost, 1e-9)
					settledRequest, settledLease := f.settledQuota(t, createRequestID, pending.RequestMeta.LeaseID)

					// 再重建一次，确认“已结算”标记来自持久缓存，删除也不生成计费。
					finalRuntime := f.newRuntime(t, false)
					lastPoll := f.perform(finalRuntime, http.MethodGet, base+"/"+seedanceRegisteredTaskID, owner)
					require.Equal(t, http.StatusOK, lastPoll.Code, lastPoll.Body.String())
					polledRequest, polledLease := f.settledQuota(t, createRequestID, pending.RequestMeta.LeaseID)
					require.Equal(t, settledRequest, polledRequest, "重建后的完成查询不得改写原请求结算记录")
					require.Equal(t, settledLease, polledLease, "重复查询不得增加已用额度或重建预留")
					deleted := f.perform(finalRuntime, http.MethodDelete, base+"/"+seedanceRegisteredTaskID, owner)
					require.Equal(t, http.StatusNoContent, deleted.Code, deleted.Body.String())
					require.Empty(t, deleted.Body.String())
					deletedRequest, deletedLease := f.settledQuota(t, createRequestID, pending.RequestMeta.LeaseID)
					require.Equal(t, settledRequest, deletedRequest, "删除任务不得改写原请求结算记录")
					require.Equal(t, settledLease, deletedLease, "删除任务不得增加费用或退回已消费额度")
					require.Equal(t, events, f.waitForEvents(t, 1))
					require.Equal(t, commands, f.billing.snapshot())
					require.Equal(t, logs, f.usage.snapshot())
					calls = f.upstream.snapshot()
					require.Len(t, calls, 7, "一次创建、一次运行态、四次完成查询、一次删除")
					require.EqualValues(t, len(calls), f.upstream.arkCalls.Load())
					for _, call := range calls {
						require.Equal(t, seedanceRegisteredAccountID, call.accountID)
						require.Equal(t, "Bearer synthetic-ark-account", call.authorization)
						if call.method != http.MethodPost {
							require.Equal(t, "/api/v3"+seedanceRegisteredTaskPath+"/"+seedanceRegisteredTaskID, call.path)
						}
					}
					selected, lookups := f.accounts.snapshot()
					require.Equal(t, selectionsAtCreate, selected, "所有查询/删除必须复用原账号，不能重新调度")
					require.NotEmpty(t, selected)
					for _, selection := range selected {
						require.Equal(t, seedanceRegisteredSelection{seedanceRegisteredGroupID, service.PlatformOpenAI}, selection)
					}
					require.NotEmpty(t, lookups)
					for _, id := range lookups {
						require.Equal(t, seedanceRegisteredAccountID, id)
					}
					control := f.control.snapshot()
					require.Contains(t, control.rates, float64(9), "重建后的真实鉴权必须确实取得新倍率")
					require.Len(t, control.funding, 1, "查询/删除不进入新资金选择")
					require.Equal(t, createRequestID, control.funding[0].RequestID)
					require.Equal(t, f.requestedModel, control.funding[0].RequestedModel)
					require.Equal(t, base, control.funding[0].Endpoint)
					require.Equal(t, "selection", control.funding[0].BoundKind)
					require.Len(t, control.leases, 1, "仅创建准入一次租约")
					require.Empty(t, control.unexpected)
					var exemptResolves int
					for _, resolve := range control.resolves {
						if resolve.BillingExempt {
							exemptResolves++
						}
					}
					require.GreaterOrEqual(t, exemptResolves, 6, "免费轮询和删除仍走生产鉴权")
				})
			}
		})
	}
}

func assertSeedanceRegisteredBadCredentials(t *testing.T, f *seedanceRegisteredFixture, runtime seedanceRegisteredRuntime, base string) {
	t.Helper()
	for _, credential := range []struct{ name, bearer string }{
		{"missing", ""}, {"wrong_secret", "aiceo_sk_" + seedanceRegisteredOwnerKeyID + "_incorrect-secret"},
	} {
		for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
			path := base
			if method != http.MethodPost {
				path += "/" + seedanceRegisteredTaskID
			}
			denied := f.perform(runtime, method, path, credential.bearer)
			require.Equal(t, http.StatusUnauthorized, denied.Code, "%s %s: %s", credential.name, method, denied.Body.String())
			require.Empty(t, f.upstream.snapshot())
			require.Zero(t, f.upstream.arkCalls.Load())
		}
	}
	control := f.control.snapshot()
	require.Empty(t, control.funding)
	require.Empty(t, control.leases)
	require.Empty(t, control.events)
}

func assertSeedanceRegisteredOwnership(t *testing.T, f *seedanceRegisteredFixture, runtime seedanceRegisteredRuntime, path string) {
	t.Helper()
	beforeCalls, beforeArk := len(f.upstream.snapshot()), f.upstream.arkCalls.Load()
	for _, tc := range []struct {
		name   string
		keyID  string
		mutate func(*gatewaylite.KeySnapshot)
	}{
		{"same_user_other_key", seedanceRegisteredOtherKeyID, func(*gatewaylite.KeySnapshot) {}},
		{"same_key_other_user", seedanceRegisteredOwnerKeyID, func(key *gatewaylite.KeySnapshot) { key.UserID = 34 }},
		{"same_key_other_billing_subject", seedanceRegisteredOwnerKeyID, func(key *gatewaylite.KeySnapshot) { key.BillingSubjectID = 34 }},
		{"same_key_organization_subject", seedanceRegisteredOwnerKeyID, func(key *gatewaylite.KeySnapshot) {
			key.OrganizationID, key.BillingSubjectType, key.BillingSubjectID = 91, "organization", 91
		}},
	} {
		f.control.updateKey(seedanceRegisteredOwnerKeyID, tc.mutate)
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			denied := f.perform(runtime, method, path, seedanceRegisteredBearer(tc.keyID))
			require.Equal(t, http.StatusNotFound, denied.Code, "%s %s: %s", tc.name, method, denied.Body.String())
			require.Contains(t, denied.Body.String(), "Seedance task not found")
			require.Len(t, f.upstream.snapshot(), beforeCalls, "归属不匹配不能触发上游适配器")
			require.Equal(t, beforeArk, f.upstream.arkCalls.Load(), "归属不匹配不能触发 Ark")
		}
		f.control.updateKey(seedanceRegisteredOwnerKeyID, func(key *gatewaylite.KeySnapshot) {
			key.UserID, key.OrganizationID = seedanceRegisteredUserID, 0
			key.BillingSubjectType, key.BillingSubjectID = "user", seedanceRegisteredUserID
		})
	}
}

func TestSeedanceRegisteredAIUnsupported(t *testing.T) {
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformComposite} {
		t.Run(platform, func(t *testing.T) {
			for _, prefix := range seedanceRegisteredPrefixes() {
				t.Run(prefix.name, func(t *testing.T) {
					f := newSeedanceRegisteredFixture(t, platform)
					runtime := f.newRuntime(t, true)
					denied := f.perform(runtime, http.MethodPost, prefix.path+seedanceRegisteredTaskPath, seedanceRegisteredBearer(seedanceRegisteredOwnerKeyID))
					require.Equal(t, http.StatusServiceUnavailable, denied.Code, denied.Body.String())
					var response struct {
						Error   struct{ Code string } `json:"error"`
						Allowed bool                  `json:"allowed"`
						Blocked bool                  `json:"blocked"`
					}
					require.NoError(t, json.Unmarshal(denied.Body.Bytes(), &response))
					require.Equal(t, "ai_unsupported_protocol", response.Error.Code)
					require.False(t, response.Allowed)
					require.True(t, response.Blocked)
					require.Empty(t, f.upstream.snapshot())
					require.Zero(t, f.upstream.arkCalls.Load(), "审查与业务上游共用合成 HTTP 服务，必须均为零")
					require.Zero(t, f.aiStore.penalties.Load())
					require.Empty(t, f.billing.snapshot())
					require.Empty(t, f.usage.snapshot())
					// 真实鉴权已预占普通余额，拒绝后允许零费用审计，但不得留下占用或收费。
					events := f.waitForEvents(t, 1)
					require.Zero(t, events[0].ActualCents)
					require.Zero(t, events[0].TokensOut)
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					lease, found, err := f.quota.LoadLeaseForSubject(ctx, gatewaylite.LeaseSubject{
						UserID: seedanceRegisteredUserID, BillingSubjectType: "user", BillingSubjectID: seedanceRegisteredUserID,
					}, seedanceRegisteredGateway)
					require.NoError(t, err)
					require.True(t, found)
					require.Zero(t, lease.ReservedCents)
					require.Zero(t, lease.SpentCents)
					keys, err := f.client.Keys(ctx, "*").Result()
					require.NoError(t, err)
					for _, key := range keys {
						require.False(t, strings.Contains(key, "seedance-admission:"), "AI 拒绝不得创建任务 admission（预登记）")
					}
					require.Empty(t, f.control.snapshot().unexpected)
				})
			}
		})
	}
}
