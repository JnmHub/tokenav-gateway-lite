package admin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// CodexPATImportRequest keeps PAT creation separate from ordinary OAuth AT/RT
// import. The server decides the auth mode so an administrator cannot
// accidentally create a non-refreshable PAT account as ordinary OAuth.
type CodexPATImportRequest struct {
	AccessToken             string         `json:"access_token" binding:"required"`
	Name                    string         `json:"name"`
	Notes                   *string        `json:"notes"`
	GroupIDs                []int64        `json:"group_ids"`
	ProxyID                 *int64         `json:"proxy_id"`
	Concurrency             *int           `json:"concurrency"`
	Priority                *int           `json:"priority"`
	RateMultiplier          *float64       `json:"rate_multiplier"`
	LoadFactor              *int           `json:"load_factor"`
	CredentialExtras        map[string]any `json:"credential_extras"`
	Extra                   map[string]any `json:"extra"`
	SkipDefaultGroupBind    *bool          `json:"skip_default_group_bind"`
	ConfirmMixedChannelRisk *bool          `json:"confirm_mixed_channel_risk"`
}

type CodexPATImportResult struct {
	Action  string       `json:"action"`
	Account *dto.Account `json:"account"`
}

// ImportCodexPAT validates and imports a Codex Personal Access Token.
// POST /api/v1/admin/accounts/import/personal-access-token
func (h *AccountHandler) ImportCodexPAT(c *gin.Context) {
	var req CodexPATImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	req.AccessToken = strings.TrimSpace(req.AccessToken)
	if req.AccessToken == "" {
		response.BadRequest(c, "Codex personal access token is required")
		return
	}

	executeAdminIdempotentJSON(c, "admin.accounts.import_codex_pat", req, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.importCodexPAT(ctx, req)
	})
}

func (h *AccountHandler) importCodexPAT(ctx context.Context, req CodexPATImportRequest) (*CodexPATImportResult, error) {
	if err := service.ValidateOpenAILongContextBillingExtra(service.PlatformOpenAI, req.Extra); err != nil {
		return nil, err
	}
	if req.Concurrency != nil && *req.Concurrency < 0 {
		return nil, newCodexImportValidationError("concurrency must be >= 0")
	}
	if req.Priority != nil && *req.Priority < 0 {
		return nil, newCodexImportValidationError("priority must be >= 0")
	}
	if req.RateMultiplier != nil && *req.RateMultiplier < 0 {
		return nil, newCodexImportValidationError("rate_multiplier must be >= 0")
	}
	if req.LoadFactor != nil && (*req.LoadFactor < 0 || *req.LoadFactor > 10000) {
		return nil, newCodexImportValidationError("load_factor must be between 0 and 10000")
	}

	var proxyURL string
	if req.ProxyID != nil {
		proxy, err := h.adminService.GetProxy(ctx, *req.ProxyID)
		if err != nil {
			return nil, err
		}
		if proxy != nil {
			proxyURL = proxy.URL()
		}
	}

	tokenInfo, err := h.openaiOAuthService.ValidateCodexPersonalAccessToken(ctx, req.AccessToken, proxyURL)
	if err != nil {
		return nil, err
	}
	credentials := mergeCodexImportMap(
		h.openaiOAuthService.BuildAccountCredentials(tokenInfo),
		sanitizeCodexImportCredentialExtras(req.CredentialExtras),
	)
	credentials = service.NormalizeOpenAIPersonalAccessTokenCredentials(nil, tokenInfo, credentials)
	extra := mergeCodexImportMap(req.Extra, map[string]any{
		"import_source":       "codex_personal_access_token",
		"auth_provider":       "codex_personal_access_token",
		"imported_at":         time.Now().UTC().Format(time.RFC3339),
		"access_token_sha256": codexTokenFingerprint(req.AccessToken),
	})

	existingAccounts, err := h.listAccountsFiltered(ctx, service.PlatformOpenAI, service.AccountTypeOAuth, "", "", 0, "", "created_at", "desc")
	if err != nil {
		return nil, err
	}
	identityKeys := buildCodexIdentityKeys(tokenInfo.ChatGPTAccountID, tokenInfo.ChatGPTUserID, tokenInfo.Email, req.AccessToken)
	if existing := buildCodexAccountIndex(existingAccounts).Find(identityKeys); existing != nil {
		mergedCredentials := mergeCodexImportMap(existing.Credentials, credentials)
		// Existing OAuth fields must be removed after the merge. Otherwise an
		// account converted to PAT could still be picked by the refresh pool.
		mergedCredentials = service.NormalizeOpenAIPersonalAccessTokenCredentials(existing, tokenInfo, mergedCredentials)
		update := &service.UpdateAccountInput{
			Credentials:           mergedCredentials,
			Extra:                 mergeCodexImportMap(existing.Extra, extra),
			Concurrency:           req.Concurrency,
			Priority:              req.Priority,
			RateMultiplier:        req.RateMultiplier,
			LoadFactor:            req.LoadFactor,
			SkipMixedChannelCheck: req.ConfirmMixedChannelRisk != nil && *req.ConfirmMixedChannelRisk,
		}
		if name := strings.TrimSpace(req.Name); name != "" {
			update.Name = name
		}
		if req.Notes != nil {
			update.Notes = req.Notes
		}
		if req.ProxyID != nil {
			update.ProxyID = req.ProxyID
		}
		if len(req.GroupIDs) > 0 {
			groupIDs := append([]int64(nil), req.GroupIDs...)
			update.GroupIDs = &groupIDs
		}
		updated, err := h.adminService.UpdateAccount(ctx, existing.ID, update)
		if err != nil {
			return nil, err
		}
		if h.tokenCacheInvalidator != nil && updated != nil {
			_ = h.tokenCacheInvalidator.InvalidateToken(ctx, updated)
		}
		return &CodexPATImportResult{Action: "updated", Account: dto.AccountFromService(updated)}, nil
	}

	concurrency := 3
	if req.Concurrency != nil {
		concurrency = *req.Concurrency
	}
	priority := 50
	if req.Priority != nil {
		priority = *req.Priority
	}
	skipDefaultGroupBind := req.SkipDefaultGroupBind != nil && *req.SkipDefaultGroupBind
	account, err := h.adminService.CreateAccount(ctx, &service.CreateAccountInput{
		Name:                  buildCodexPATAccountName(req.Name, tokenInfo),
		Notes:                 req.Notes,
		Platform:              service.PlatformOpenAI,
		Type:                  service.AccountTypeOAuth,
		Credentials:           credentials,
		Extra:                 extra,
		ProxyID:               req.ProxyID,
		Concurrency:           concurrency,
		Priority:              priority,
		RateMultiplier:        req.RateMultiplier,
		LoadFactor:            req.LoadFactor,
		GroupIDs:              req.GroupIDs,
		SkipDefaultGroupBind:  skipDefaultGroupBind,
		SkipMixedChannelCheck: req.ConfirmMixedChannelRisk != nil && *req.ConfirmMixedChannelRisk,
	})
	if err != nil {
		return nil, err
	}
	return &CodexPATImportResult{Action: "created", Account: dto.AccountFromService(account)}, nil
}

func buildCodexPATAccountName(name string, tokenInfo *service.OpenAITokenInfo) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	if tokenInfo != nil {
		for _, candidate := range []string{tokenInfo.Email, tokenInfo.ChatGPTAccountID, tokenInfo.ChatGPTUserID} {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				return candidate
			}
		}
	}
	return "Codex PAT Account"
}

func newCodexImportValidationError(message string) error {
	return infraerrors.New(http.StatusBadRequest, "OPENAI_CODEX_PAT_IMPORT_INVALID", message)
}
