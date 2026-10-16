package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// CodexAgentIdentityImportRequest accepts the official auth.json as one
// opaque document. Individual private fields are deliberately not exposed as
// form controls or top-level API fields, reducing accidental mixed-mode data.
type CodexAgentIdentityImportRequest struct {
	Content                 string         `json:"content" binding:"required"`
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

type CodexAgentIdentityImportResult struct {
	Action  string       `json:"action"`
	Account *dto.Account `json:"account"`
}

type codexAgentIdentityDocument struct {
	RuntimeID  string
	PrivateKey string
	TaskID     string
	AccountID  string
	UserID     string
	Email      string
	PlanType   string
	FedRAMP    bool
}

// ImportCodexAgentIdentity imports the official Agent Identity auth.json.
// POST /api/v1/admin/accounts/import/agent-identity
func (h *AccountHandler) ImportCodexAgentIdentity(c *gin.Context) {
	var req CodexAgentIdentityImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if _, err := parseCodexAgentIdentityDocument(req.Content); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	executeAdminIdempotentJSON(c, "admin.accounts.import_codex_agent_identity", req, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.importCodexAgentIdentity(ctx, req)
	})
}

func (h *AccountHandler) importCodexAgentIdentity(ctx context.Context, req CodexAgentIdentityImportRequest) (*CodexAgentIdentityImportResult, error) {
	doc, err := parseCodexAgentIdentityDocument(req.Content)
	if err != nil {
		return nil, newCodexAgentIdentityValidationError(err.Error())
	}
	if err := validateCodexAgentIdentityImportOptions(req); err != nil {
		return nil, err
	}
	if err := service.ValidateOpenAILongContextBillingExtra(service.PlatformOpenAI, req.Extra); err != nil {
		return nil, err
	}

	credentials := mergeCodexImportMap(sanitizeCodexImportCredentialExtras(req.CredentialExtras), map[string]any{
		"auth_mode":                  service.OpenAIAuthModeAgentIdentity,
		"agent_runtime_id":           doc.RuntimeID,
		"agent_private_key":          doc.PrivateKey,
		"chatgpt_account_id":         doc.AccountID,
		"chatgpt_user_id":            doc.UserID,
		"chatgpt_account_is_fedramp": doc.FedRAMP,
	})
	setCodexCredentialIfNotEmpty(credentials, "task_id", doc.TaskID)
	setCodexCredentialIfNotEmpty(credentials, "email", doc.Email)
	setCodexCredentialIfNotEmpty(credentials, "plan_type", doc.PlanType)
	extra := mergeCodexImportMap(req.Extra, map[string]any{
		"import_source":           "codex_agent_identity_auth_json",
		"auth_provider":           "agent_identity",
		"imported_at":             time.Now().UTC().Format(time.RFC3339),
		"agent_runtime_id_sha256": codexAgentIdentityFingerprint(doc.RuntimeID),
	})

	existingAccounts, err := h.listAccountsFiltered(ctx, service.PlatformOpenAI, service.AccountTypeOAuth, "", "", 0, "", "created_at", "desc")
	if err != nil {
		return nil, err
	}
	if existing := buildCodexAccountIndex(existingAccounts).Find([]string{"account:" + doc.AccountID}); existing != nil {
		merged := mergeCodexImportMap(existing.Credentials, credentials)
		// Converting an existing account to Agent Identity must remove every
		// refreshable or bearer credential. The auth mode alone must be enough
		// to prove this account can never enter the OAuth refresh pool.
		for _, key := range []string{"access_token", "refresh_token", "id_token", "client_id", "client_secret", "session_key", "cookie", "api_key", "expires_at"} {
			delete(merged, key)
		}
		update := &service.UpdateAccountInput{
			Credentials:           merged,
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
		return &CodexAgentIdentityImportResult{Action: "updated", Account: dto.AccountFromService(updated)}, nil
	}

	concurrency := 3
	if req.Concurrency != nil {
		concurrency = *req.Concurrency
	}
	priority := 50
	if req.Priority != nil {
		priority = *req.Priority
	}
	account, err := h.adminService.CreateAccount(ctx, &service.CreateAccountInput{
		Name:                  buildCodexAgentIdentityAccountName(req.Name, doc),
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
		SkipDefaultGroupBind:  req.SkipDefaultGroupBind != nil && *req.SkipDefaultGroupBind,
		SkipMixedChannelCheck: req.ConfirmMixedChannelRisk != nil && *req.ConfirmMixedChannelRisk,
	})
	if err != nil {
		return nil, err
	}
	return &CodexAgentIdentityImportResult{Action: "created", Account: dto.AccountFromService(account)}, nil
}

func parseCodexAgentIdentityDocument(content string) (*codexAgentIdentityDocument, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, errors.New("Agent Identity auth.json 不能为空")
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return nil, errors.New("Agent Identity auth.json 格式无效")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("Agent Identity auth.json 只能包含一个 JSON 对象")
	}
	mode := firstCodexString(raw, []string{"auth_mode"}, []string{"authMode"})
	identity, ok := firstCodexAgentIdentityMap(raw, []string{"agent_identity"}, []string{"agentIdentity"})
	if !ok || !strings.EqualFold(mode, service.OpenAIAuthModeAgentIdentity) {
		return nil, errors.New("不是有效的 Agent Identity auth.json")
	}
	doc := &codexAgentIdentityDocument{
		RuntimeID:  firstCodexString(identity, []string{"agent_runtime_id"}, []string{"agentRuntimeId"}),
		PrivateKey: firstCodexString(identity, []string{"agent_private_key"}, []string{"agentPrivateKey"}),
		TaskID:     firstCodexString(identity, []string{"task_id"}, []string{"taskId"}),
		AccountID:  firstCodexString(identity, []string{"account_id"}, []string{"accountId"}),
		UserID:     firstCodexString(identity, []string{"chatgpt_user_id"}, []string{"chatgptUserId"}),
		Email:      firstCodexString(identity, []string{"email"}),
		PlanType:   firstCodexString(identity, []string{"plan_type"}, []string{"planType"}),
		FedRAMP:    firstCodexAgentIdentityBool(identity, []string{"chatgpt_account_is_fedramp"}, []string{"chatgptAccountIsFedramp"}),
	}
	if doc.RuntimeID == "" || doc.PrivateKey == "" || doc.AccountID == "" || doc.UserID == "" {
		return nil, errors.New("Agent Identity auth.json 缺少必要字段")
	}
	if err := service.ValidateOpenAIAgentIdentityPrivateKey(doc.PrivateKey); err != nil {
		return nil, errors.New("Agent Identity auth.json 私钥格式无效")
	}
	return doc, nil
}

func firstCodexAgentIdentityMap(obj map[string]any, paths ...[]string) (map[string]any, bool) {
	for _, path := range paths {
		value, ok := codexPathValue(obj, path)
		if !ok || value == nil {
			continue
		}
		if mapped, ok := value.(map[string]any); ok {
			return mapped, true
		}
	}
	return nil, false
}

func firstCodexAgentIdentityBool(obj map[string]any, paths ...[]string) bool {
	for _, path := range paths {
		value, ok := codexPathValue(obj, path)
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed
		case string:
			return strings.EqualFold(strings.TrimSpace(typed), "true")
		}
	}
	return false
}

func validateCodexAgentIdentityImportOptions(req CodexAgentIdentityImportRequest) error {
	if req.Concurrency != nil && *req.Concurrency < 0 {
		return newCodexAgentIdentityValidationError("concurrency must be >= 0")
	}
	if req.Priority != nil && *req.Priority < 0 {
		return newCodexAgentIdentityValidationError("priority must be >= 0")
	}
	if req.RateMultiplier != nil && *req.RateMultiplier < 0 {
		return newCodexAgentIdentityValidationError("rate_multiplier must be >= 0")
	}
	if req.LoadFactor != nil && (*req.LoadFactor < 0 || *req.LoadFactor > 10000) {
		return newCodexAgentIdentityValidationError("load_factor must be between 0 and 10000")
	}
	return nil
}

func buildCodexAgentIdentityAccountName(name string, doc *codexAgentIdentityDocument) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	if doc != nil {
		for _, candidate := range []string{doc.Email, doc.AccountID, doc.UserID} {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				return candidate
			}
		}
	}
	return "Codex Agent Identity Account"
}

func codexAgentIdentityFingerprint(runtimeID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(runtimeID)))
	return hex.EncodeToString(sum[:])
}

func newCodexAgentIdentityValidationError(message string) error {
	return infraerrors.New(http.StatusBadRequest, "OPENAI_CODEX_AGENT_IDENTITY_IMPORT_INVALID", message)
}
