package handler

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// SeedanceTasks exposes Ark's native asynchronous video task protocol.
func (h *OpenAIGatewayHandler) SeedanceTasks(c *gin.Context) {
	if c.Request.Method == http.MethodPost && c.GetHeader("Content-Type") != "" {
		mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || mediaType != "application/json" {
			h.errorResponse(c, http.StatusUnsupportedMediaType, "invalid_request_error", "Seedance requires application/json")
			return
		}
	}
	key, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || key.Group == nil || (key.Group.Platform != service.PlatformOpenAI && key.Group.Platform != service.PlatformComposite) {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "Seedance requires an OpenAI or composite group")
		return
	}
	endpoint := service.SeedanceEndpointCreate
	setActualUpstreamEndpoint(c, EndpointSeedanceTasks)
	taskID := ""
	if c.Request.Method != http.MethodPost {
		taskID = service.SeedanceTaskKey(c.Param("task_id"))
		endpoint = service.SeedanceEndpointStatus
		if c.Request.Method == http.MethodDelete {
			endpoint = service.SeedanceEndpointDelete
		}
	}
	h.handleGrokMedia(c, endpoint, taskID)
}

var errSeedanceBillingPersistenceUncertain = errors.New("seedance billing persistence is uncertain")

type seedanceCompletionBilling struct {
	result  *service.OpenAIForwardResult
	pending *service.GrokVideoPendingBilling
	owner   service.MediaTaskIdentity
	token   string
}

// Ark 只按上游 completion_tokens 结算。准备阶段的认领不是已记账，
// 必须等待原创建身份的 RecordUsage 可靠入队成功后再确认。
func prepareSeedanceCompletionBilling(ctx context.Context, h *OpenAIGatewayHandler, key *service.APIKey, subject middleware.AuthSubject, taskID string, result *service.OpenAIForwardResult) (*seedanceCompletionBilling, error) {
	if result == nil || result.Usage.OutputTokens <= 0 {
		return nil, nil
	}
	owner, err := service.ResolveMediaTaskIdentity(ctx, key, subject.UserID)
	if err != nil {
		return nil, err
	}
	pending, err := h.gatewayService.LoadSeedancePendingBilling(ctx, taskID, owner)
	if err != nil {
		return nil, err
	}
	if pending == nil {
		return nil, ErrSeedanceTaskNotFound
	}
	if owner.Kind == "remote" {
		meta := pending.RequestMeta
		// 残缺原身份会被上下文访问器忽略，必须在认领前拒绝，避免绕过异步结算检查。
		if meta == nil || strings.TrimSpace(meta.RequestID) == "" || meta.UserID <= 0 {
			return nil, errors.New("seedance original billing identity is unavailable")
		}
	}
	if err := service.ValidateSeedanceExecutionPricing(pending); err != nil {
		return nil, err
	}
	token, err := h.gatewayService.ClaimSeedanceBilling(ctx, taskID, owner)
	if err != nil || token == "" {
		return nil, err
	}
	merged := *result
	merged.Model = pending.Model
	merged.BillingModel = firstNonEmptyString(pending.BillingModel, pending.Model)
	merged.UpstreamModel = firstNonEmptyString(pending.UpstreamModel, result.UpstreamModel)
	merged.RequestID = service.StableGrokVideoBillingRequestID(taskID)
	merged.ResponseID = taskID
	merged.Duration = service.GrokVideoE2EDuration(pending.CreatedAt, time.Now())
	pending.TaskID = taskID
	return &seedanceCompletionBilling{result: &merged, pending: pending, owner: owner, token: token}, nil
}

func settleSeedanceCompletion(ctx context.Context, h *OpenAIGatewayHandler, completion *seedanceCompletionBilling, record func(context.Context, *service.OpenAIForwardResult) error) error {
	if completion == nil {
		return nil
	}
	billingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	billingCtx = service.SeedanceCompletionBillingContext(billingCtx, completion.pending)
	if err := record(billingCtx, completion.result); err != nil {
		// 模糊提交不能立即重新结算；保留认领至过期，再用同一原请求编号恢复。
		if errors.Is(err, errSeedanceBillingPersistenceUncertain) {
			return err
		}
		// 明确失败保留任务快照并释放自身认领，允许下次轮询重试。
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer releaseCancel()
		return errors.Join(err, h.gatewayService.ReleaseSeedanceBilling(releaseCtx, completion.result.ResponseID, completion.owner, completion.token))
	}
	// 确认失败时保留认领至自然过期，不删除可能已接管的新 worker 所有权。
	return h.gatewayService.ConfirmSeedanceBilling(billingCtx, completion.result.ResponseID, completion.owner, completion.token)
}

func recordSeedanceCompletionUsage(c *gin.Context, h *OpenAIGatewayHandler, key *service.APIKey, subject middleware.AuthSubject, subscription *service.UserSubscription, account *service.Account, taskID string, result *service.OpenAIForwardResult) error {
	completion, err := prepareSeedanceCompletionBilling(c.Request.Context(), h, key, subject, taskID, result)
	if err != nil || completion == nil {
		return err
	}
	return settleSeedanceCompletion(c.Request.Context(), h, completion, func(ctx context.Context, billed *service.OpenAIForwardResult) error {
		err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result: billed, APIKey: key, User: key.User, Account: account, Subscription: subscription,
			InboundEndpoint: GetInboundEndpoint(c), UpstreamEndpoint: GetUpstreamEndpoint(c, account.Platform),
			UserAgent: c.GetHeader("User-Agent"), IPAddress: ip.GetClientIP(c), APIKeyService: h.apiKeyService,
			ChannelUsageFields: service.ChannelUsageFields{OriginalModel: completion.pending.OriginalModel, ChannelMappedModel: billed.UpstreamModel},
		})
		if err != nil {
			// RecordUsage 跨数据库/Redis，无法证明返回错误前未提交；不立即释放模糊认领。
			return errors.Join(errSeedanceBillingPersistenceUncertain, err)
		}
		return nil
	})
}

// firstNonEmptyString 返回第一个非空（trim 后）字符串，全空返回空串。
func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
