package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// OpenAIOAuthHandler handles OpenAI OAuth-related operations
type OpenAIOAuthHandler struct {
	openaiOAuthService *service.OpenAIOAuthService
	adminService       service.AdminService
	quotaService       openAIQuotaService
	rateLimitService   openAIAccountStateRecoverer
}

type openAIQuotaService interface {
	QueryUsage(ctx context.Context, accountID int64) (*service.OpenAIQuotaUsage, error)
	CacheResetCreditsSnapshot(ctx context.Context, accountID int64, credits *service.OpenAIRateLimitResetCredits) error
	ResetCredit(ctx context.Context, accountID int64) (*service.OpenAIQuotaResetResult, error)
}

type openAIAccountStateRecoverer interface {
	RecoverAccountState(ctx context.Context, accountID int64, options service.AccountRecoveryOptions) (*service.SuccessfulTestRecoveryResult, error)
}

const (
	openAIQuotaResetWarningCacheRefreshFailed    = "reset_credit_cache_refresh_failed"
	openAIQuotaResetWarningAccountRecoveryFailed = "account_state_recovery_failed"
	openAIQuotaResetWarningAccountRefreshFailed  = "account_state_refresh_failed"
	openAIQuotaResetPostProcessTimeout           = 8 * time.Second
)

type openAIQuotaResetResponse struct {
	service.OpenAIQuotaResetResult
	Quota                 *service.OpenAIQuotaUsage `json:"quota,omitempty"`
	Account               *dto.Account              `json:"account,omitempty"`
	CacheRefreshed        bool                      `json:"cache_refreshed"`
	AccountStateRecovered bool                      `json:"account_state_recovered"`
	WarningCode           string                    `json:"warning_code,omitempty"`
}

type openAIQuotaRefreshResponse struct {
	service.OpenAIQuotaUsage
	CachePersisted bool `json:"cache_persisted"`
}

// The reset credit is already consumed before post-processing begins. Detach
// recovery from the browser connection so closing the page cannot strand the
// account in a blocked state, while the timeout prevents leaked background work.
func openAIQuotaResetPostProcessContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, openAIQuotaResetPostProcessTimeout)
}

func oauthPlatformFromPath(c *gin.Context) string {
	return service.PlatformOpenAI
}

// NewOpenAIOAuthHandler creates a new OpenAI OAuth handler
func NewOpenAIOAuthHandler(
	openaiOAuthService *service.OpenAIOAuthService,
	adminService service.AdminService,
	quotaService *service.OpenAIQuotaService,
	rateLimitService *service.RateLimitService,
) *OpenAIOAuthHandler {
	handler := &OpenAIOAuthHandler{
		openaiOAuthService: openaiOAuthService,
		adminService:       adminService,
	}
	if quotaService != nil {
		handler.quotaService = quotaService
	}
	if rateLimitService != nil {
		handler.rateLimitService = rateLimitService
	}
	return handler
}

// OpenAIGenerateAuthURLRequest represents the request for generating OpenAI auth URL
type OpenAIGenerateAuthURLRequest struct {
	ProxyID     *int64 `json:"proxy_id"`
	RedirectURI string `json:"redirect_uri"`
}

// GenerateAuthURL generates OpenAI OAuth authorization URL
// POST /api/v1/admin/openai/generate-auth-url
func (h *OpenAIOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req OpenAIGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Allow empty body
		req = OpenAIGenerateAuthURLRequest{}
	}

	result, err := h.openaiOAuthService.GenerateAuthURL(
		c.Request.Context(),
		req.ProxyID,
		req.RedirectURI,
		oauthPlatformFromPath(c),
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

// OpenAIExchangeCodeRequest represents the request for exchanging OpenAI auth code
type OpenAIExchangeCodeRequest struct {
	SessionID   string `json:"session_id" binding:"required"`
	Code        string `json:"code" binding:"required"`
	State       string `json:"state" binding:"required"`
	RedirectURI string `json:"redirect_uri"`
	ProxyID     *int64 `json:"proxy_id"`
}

// ExchangeCode exchanges OpenAI authorization code for tokens
// POST /api/v1/admin/openai/exchange-code
func (h *OpenAIOAuthHandler) ExchangeCode(c *gin.Context) {
	var req OpenAIExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	tokenInfo, err := h.openaiOAuthService.ExchangeCode(c.Request.Context(), &service.OpenAIExchangeCodeInput{
		SessionID:   req.SessionID,
		Code:        req.Code,
		State:       req.State,
		RedirectURI: req.RedirectURI,
		ProxyID:     req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, tokenInfo)
}

// OpenAIRefreshTokenRequest represents the request for refreshing OpenAI token
type OpenAIRefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token"`
	RT           string `json:"rt"`
	ClientID     string `json:"client_id"`
	ProxyID      *int64 `json:"proxy_id"`
}

// RefreshToken refreshes an OpenAI OAuth token
// POST /api/v1/admin/openai/refresh-token
func (h *OpenAIOAuthHandler) RefreshToken(c *gin.Context) {
	var req OpenAIRefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	refreshToken := strings.TrimSpace(req.RefreshToken)
	if refreshToken == "" {
		refreshToken = strings.TrimSpace(req.RT)
	}
	if refreshToken == "" {
		response.BadRequest(c, "refresh_token is required")
		return
	}

	var proxyURL string
	if req.ProxyID != nil {
		proxy, err := h.adminService.GetProxy(c.Request.Context(), *req.ProxyID)
		if err == nil && proxy != nil {
			proxyURL = proxy.URL()
		}
	}

	// 未指定 client_id 时，根据请求路径平台自动设置默认值，避免 repository 层盲猜
	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" {
		platform := oauthPlatformFromPath(c)
		clientID, _ = openai.OAuthClientConfigByPlatform(platform)
	}

	tokenInfo, err := h.openaiOAuthService.RefreshTokenWithClientID(c.Request.Context(), refreshToken, proxyURL, clientID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, tokenInfo)
}

// RefreshAccountToken refreshes token for a specific OpenAI account
// POST /api/v1/admin/openai/accounts/:id/refresh
func (h *OpenAIOAuthHandler) RefreshAccountToken(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}

	// Get account
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	platform := oauthPlatformFromPath(c)
	if account.Platform != platform {
		response.BadRequest(c, "Account platform does not match OAuth endpoint")
		return
	}

	// Only refresh OAuth-based accounts
	if !account.IsOAuth() {
		response.BadRequest(c, "Cannot refresh non-OAuth account credentials")
		return
	}

	// Use OpenAI OAuth service to refresh token
	tokenInfo, err := h.openaiOAuthService.RefreshAccountToken(c.Request.Context(), account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Build new credentials from token info
	newCredentials := h.openaiOAuthService.BuildAccountCredentials(tokenInfo)

	// Preserve non-token settings from existing credentials
	for k, v := range account.Credentials {
		if _, exists := newCredentials[k]; !exists {
			newCredentials[k] = v
		}
	}
	newCredentials = service.NormalizeOpenAIPersonalAccessTokenCredentials(account, tokenInfo, newCredentials)

	updatedAccount, err := h.adminService.UpdateAccount(c.Request.Context(), accountID, &service.UpdateAccountInput{
		Credentials: newCredentials,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.AccountFromService(updatedAccount))
}

// CreateAccountFromOAuth creates a new OpenAI OAuth account from token info
// POST /api/v1/admin/openai/create-from-oauth
func (h *OpenAIOAuthHandler) CreateAccountFromOAuth(c *gin.Context) {
	var req struct {
		SessionID   string  `json:"session_id" binding:"required"`
		Code        string  `json:"code" binding:"required"`
		State       string  `json:"state" binding:"required"`
		RedirectURI string  `json:"redirect_uri"`
		ProxyID     *int64  `json:"proxy_id"`
		Name        string  `json:"name"`
		Concurrency int     `json:"concurrency"`
		Priority    int     `json:"priority"`
		GroupIDs    []int64 `json:"group_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// Exchange code for tokens
	tokenInfo, err := h.openaiOAuthService.ExchangeCode(c.Request.Context(), &service.OpenAIExchangeCodeInput{
		SessionID:   req.SessionID,
		Code:        req.Code,
		State:       req.State,
		RedirectURI: req.RedirectURI,
		ProxyID:     req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Build credentials from token info
	credentials := h.openaiOAuthService.BuildAccountCredentials(tokenInfo)

	platform := oauthPlatformFromPath(c)

	// Use email as default name if not provided
	name := req.Name
	if name == "" && tokenInfo.Email != "" {
		name = tokenInfo.Email
	}
	if name == "" {
		name = "OpenAI OAuth Account"
	}

	// Create account
	account, err := h.adminService.CreateAccount(c.Request.Context(), &service.CreateAccountInput{
		Name:        name,
		Platform:    platform,
		Type:        "oauth",
		Credentials: credentials,
		Extra:       nil,
		ProxyID:     req.ProxyID,
		Concurrency: req.Concurrency,
		Priority:    req.Priority,
		GroupIDs:    req.GroupIDs,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.AccountFromService(account))
}

// QueryQuota queries the rate-limit / quota usage for an OpenAI account.
// GET /api/v1/admin/openai/accounts/:id/quota
func (h *OpenAIOAuthHandler) QueryQuota(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if h.quotaService == nil {
		response.BadRequest(c, "openai quota service is not enabled")
		return
	}
	usage, err := h.quotaService.QueryUsage(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, usage)
}

// RefreshQuota refreshes upstream quota and persists its reset-credit snapshot.
// POST /api/v1/admin/openai/accounts/:id/quota/refresh
func (h *OpenAIOAuthHandler) RefreshQuota(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if h.quotaService == nil {
		response.BadRequest(c, "openai quota service is not enabled")
		return
	}

	usage, err := h.quotaService.QueryUsage(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if usage == nil {
		response.Error(c, http.StatusInternalServerError, "openai quota query returned an empty result")
		return
	}

	result := openAIQuotaRefreshResponse{OpenAIQuotaUsage: *usage}
	if err := h.quotaService.CacheResetCreditsSnapshot(c.Request.Context(), accountID, usage.RateLimitResetCredits); err != nil {
		slog.Warn("openai_quota_reset_credit_cache_persist_failed", "account_id", accountID, "error", err)
		response.Success(c, result)
		return
	}
	result.CachePersisted = true
	response.Success(c, result)
}

// ResetQuota consumes one rate-limit reset credit for an OpenAI account.
// POST /api/v1/admin/openai/accounts/:id/reset-quota
func (h *OpenAIOAuthHandler) ResetQuota(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if h.quotaService == nil {
		response.BadRequest(c, "openai quota service is not enabled")
		return
	}
	result, err := h.quotaService.ResetCredit(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if result == nil {
		response.Error(c, http.StatusInternalServerError, "openai quota reset returned an empty result")
		return
	}

	resetResponse := openAIQuotaResetResponse{OpenAIQuotaResetResult: *result}
	postCtx, cancelPost := openAIQuotaResetPostProcessContext(c.Request.Context())
	defer cancelPost()

	if h.rateLimitService == nil {
		resetResponse.WarningCode = openAIQuotaResetWarningAccountRecoveryFailed
		response.Success(c, resetResponse)
		return
	}
	if _, err := h.rateLimitService.RecoverAccountState(postCtx, accountID, service.AccountRecoveryOptions{InvalidateToken: true}); err != nil {
		slog.Warn("openai_quota_reset_account_recovery_failed", "account_id", accountID, "error", err)
		resetResponse.WarningCode = openAIQuotaResetWarningAccountRecoveryFailed
		response.Success(c, resetResponse)
		return
	}
	resetResponse.AccountStateRecovered = true

	usage, usageErr := h.quotaService.QueryUsage(postCtx, accountID)
	if usageErr != nil || usage == nil {
		slog.Warn("openai_quota_reset_cache_refresh_failed", "account_id", accountID, "error", usageErr)
		resetResponse.WarningCode = openAIQuotaResetWarningCacheRefreshFailed
	} else if err := h.quotaService.CacheResetCreditsSnapshot(postCtx, accountID, usage.RateLimitResetCredits); err != nil {
		slog.Warn("openai_quota_reset_cache_refresh_failed", "account_id", accountID, "error", err)
		resetResponse.WarningCode = openAIQuotaResetWarningCacheRefreshFailed
	} else {
		resetResponse.Quota = usage
		resetResponse.CacheRefreshed = true
	}

	account, err := h.adminService.GetAccount(postCtx, accountID)
	if err != nil {
		slog.Warn("openai_quota_reset_account_refresh_failed", "account_id", accountID, "error", err)
		if resetResponse.WarningCode == "" {
			resetResponse.WarningCode = openAIQuotaResetWarningAccountRefreshFailed
		}
		response.Success(c, resetResponse)
		return
	}
	resetResponse.Account = dto.AccountFromService(account)
	response.Success(c, resetResponse)
}

type CreateSparkShadowRequest struct {
	Name        string  `json:"name"`
	Priority    int     `json:"priority"`
	Concurrency int     `json:"concurrency"`
	GroupIDs    []int64 `json:"group_ids"`
}

func (h *OpenAIOAuthHandler) sparkShadowService(c *gin.Context) service.SparkShadowAdminService {
	sparkService, ok := h.adminService.(service.SparkShadowAdminService)
	if !ok {
		response.BadRequest(c, "Spark shadow account service is not enabled")
		return nil
	}
	return sparkService
}

// CreateSparkShadow 从路径中选定母账号，管理员无需手输 parent_account_id。
// POST /api/v1/admin/accounts/:id/shadow
func (h *OpenAIOAuthHandler) CreateSparkShadow(c *gin.Context) {
	parentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || parentID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req CreateSparkShadowRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			response.BadRequest(c, "Invalid request: "+err.Error())
			return
		}
	}
	sparkService := h.sparkShadowService(c)
	if sparkService == nil {
		return
	}
	shadow, err := sparkService.CreateShadow(c.Request.Context(), parentID, service.ShadowOptions{
		Name: req.Name, Priority: req.Priority, Concurrency: req.Concurrency, GroupIDs: req.GroupIDs,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.AccountFromService(shadow))
}

// ListSparkShadows 查看母账号关联的影子账号。
// GET /api/v1/admin/accounts/:id/shadows
func (h *OpenAIOAuthHandler) ListSparkShadows(c *gin.Context) {
	parentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || parentID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	sparkService := h.sparkShadowService(c)
	if sparkService == nil {
		return
	}
	shadows, err := sparkService.ListShadows(c.Request.Context(), parentID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	items := make([]*dto.Account, 0, len(shadows))
	for _, shadow := range shadows {
		items = append(items, dto.AccountFromService(shadow))
	}
	response.Success(c, items)
}

// SyncSparkShadow 只同步母账号分组和代理，不复制 token，不覆盖影子的并发/优先级。
// POST /api/v1/admin/accounts/:id/shadow/sync
func (h *OpenAIOAuthHandler) SyncSparkShadow(c *gin.Context) {
	shadowID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || shadowID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	sparkService := h.sparkShadowService(c)
	if sparkService == nil {
		return
	}
	shadow, err := sparkService.SyncShadow(c.Request.Context(), shadowID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.AccountFromService(shadow))
}
