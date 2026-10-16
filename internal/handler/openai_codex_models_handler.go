package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const codexModelsSourceHeader = "X-Sub2API-Codex-Models-Source"

type codexModelsStaleFallback struct {
	manifest  *service.CodexModelsManifest
	accountID int64
}

// CodexModels returns the dynamic Codex models manifest. For fixed_group（固定分组）
// keys, account selection and manifest entries are both scoped to the local group.
func (h *OpenAIGatewayHandler) CodexModels(c *gin.Context) {
	if c.Request.Context().Err() != nil {
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.Group == nil {
		h.errorResponse(c, http.StatusUnauthorized, "invalid_request_error", "API key group is required")
		return
	}
	if apiKey.Group.Platform != service.PlatformOpenAI && apiKey.Group.Platform != service.PlatformComposite {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Codex models manifest is only available for OpenAI and Composite groups")
		return
	}

	routingGroupID := apiKey.SchedulerGroupID()
	maxAccountSwitches := h.maxAccountSwitches
	if maxAccountSwitches <= 0 {
		maxAccountSwitches = 3
	}
	failedAccountIDs := make(map[int64]struct{})
	switchCount := 0
	var lastUpstreamErr error
	var staleFallback *codexModelsStaleFallback
	serveStaleFallback := func() bool {
		if staleFallback == nil || staleFallback.manifest == nil {
			return false
		}
		h.writeCodexModelsManifest(c, apiKey, routingGroupID, staleFallback.accountID, staleFallback.manifest)
		return true
	}

	for {
		account, err := h.gatewayService.SelectAccountForModelWithExclusions(
			c.Request.Context(), routingGroupID, "", "", failedAccountIDs,
		)
		if err != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			if serveStaleFallback() {
				return
			}
			if lastUpstreamErr != nil {
				h.errorResponseWithCode(c, infraerrors.Code(lastUpstreamErr), "upstream_error", infraerrors.Reason(lastUpstreamErr), infraerrors.Message(lastUpstreamErr))
				return
			}
			h.errorResponse(c, http.StatusServiceUnavailable, "upstream_error", "No available OpenAI accounts for this API key group")
			return
		}
		// 让 ops（运维错误日志）记录实际命中的账号，manifest 401 才能
		// 准确定位到需要隔离的 OAuth 账号。
		setOpsSelectedAccount(c, account.ID, account.Platform)

		manifest, err := h.gatewayService.FetchCodexModelsManifest(
			c.Request.Context(),
			account,
			routingGroupID,
			apiKey.Group,
			apiKey.AllowedModels,
			c.Query("client_version"),
			c.GetHeader("If-None-Match"),
		)
		if err != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			if service.IsRetryableCodexModelsManifestError(err) {
				failedAccountIDs[account.ID] = struct{}{}
				lastUpstreamErr = err
				if switchCount < maxAccountSwitches {
					switchCount++
					continue
				}
			}
			if serveStaleFallback() {
				return
			}
			h.errorResponseWithCode(c, infraerrors.Code(err), "upstream_error", infraerrors.Reason(err), infraerrors.Message(err))
			return
		}
		if c.Request.Context().Err() != nil {
			return
		}
		if manifest.CacheState == service.CodexModelsManifestCacheStateStale {
			// stale cache（过期兜底缓存）只作为 last-known-good 候选：继续尝试
			// 同一固定分组的其他账号，避免有新鲜能力清单时过早返回旧结果。
			if staleFallback == nil {
				staleFallback = &codexModelsStaleFallback{manifest: manifest, accountID: account.ID}
			}
			failedAccountIDs[account.ID] = struct{}{}
			if switchCount < maxAccountSwitches {
				switchCount++
				continue
			}
			h.writeCodexModelsManifest(c, apiKey, routingGroupID, staleFallback.accountID, staleFallback.manifest)
			return
		}

		h.writeCodexModelsManifest(c, apiKey, routingGroupID, account.ID, manifest)
		return
	}
}

func (h *OpenAIGatewayHandler) writeCodexModelsManifest(
	c *gin.Context,
	apiKey *service.APIKey,
	routingGroupID *int64,
	accountID int64,
	manifest *service.CodexModelsManifest,
) {
	if c == nil || manifest == nil {
		return
	}
	if manifest.ETag != "" {
		c.Header("ETag", manifest.ETag)
	}
	if manifest.CacheState != "" {
		c.Header(codexModelsSourceHeader, string(manifest.CacheState))
	}
	c.Header("Cache-Control", "private, no-cache")
	if manifest.CacheState == service.CodexModelsManifestCacheStateStale {
		apiKeyID := int64(0)
		if apiKey != nil {
			apiKeyID = apiKey.ID
		}
		groupID := int64(0)
		if routingGroupID != nil {
			groupID = *routingGroupID
		}
		requestLogger(c, "handler.openai_gateway.codex_models").Warn(
			"openai.codex_models_last_known_good_served",
			zap.Int64("api_key_id", apiKeyID),
			zap.Int64("group_id", groupID),
			zap.Int64("account_id", accountID),
			zap.String("manifest_source", string(manifest.CacheState)),
		)
	}
	if manifest.NotModified {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", manifest.Body)
}
