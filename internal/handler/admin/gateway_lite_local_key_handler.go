package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type GatewayLiteLocalKeyHandler struct {
	apiKeyService *service.APIKeyService
}

func NewGatewayLiteLocalKeyHandler(apiKeyService *service.APIKeyService) *GatewayLiteLocalKeyHandler {
	return &GatewayLiteLocalKeyHandler{apiKeyService: apiKeyService}
}

type gatewayLiteLocalKeyCreateRequest struct {
	Name        string   `json:"name" binding:"required,max=100"`
	GroupID     int64    `json:"group_id" binding:"required,gt=0"`
	IPWhitelist []string `json:"ip_whitelist"`
	IPBlacklist []string `json:"ip_blacklist"`
}

type gatewayLiteLocalKeyUpdateRequest struct {
	Name        *string   `json:"name" binding:"omitempty,max=100"`
	GroupID     *int64    `json:"group_id" binding:"omitempty,gt=0"`
	Status      *string   `json:"status"`
	IPWhitelist *[]string `json:"ip_whitelist"`
	IPBlacklist *[]string `json:"ip_blacklist"`
}

type gatewayLiteLocalKeyResponse struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	KeyMasked   string     `json:"key_masked"`
	Secret      string     `json:"secret,omitempty"`
	GroupID     int64      `json:"group_id"`
	GroupName   string     `json:"group_name"`
	Platform    string     `json:"platform"`
	Status      string     `json:"status"`
	IPWhitelist []string   `json:"ip_whitelist"`
	IPBlacklist []string   `json:"ip_blacklist"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func gatewayLiteLocalKeyDTO(key *service.APIKey, includeSecret bool) gatewayLiteLocalKeyResponse {
	if key == nil {
		return gatewayLiteLocalKeyResponse{
			IPWhitelist: []string{},
			IPBlacklist: []string{},
		}
	}
	groupID := int64(0)
	if key.GroupID != nil {
		groupID = *key.GroupID
	}
	groupName := ""
	platform := ""
	if key.Group != nil {
		groupName = key.Group.Name
		platform = key.Group.Platform
	}
	out := gatewayLiteLocalKeyResponse{
		ID:        key.ID,
		Name:      key.Name,
		KeyMasked: maskGatewayLiteLocalKey(key.Key),
		GroupID:   groupID,
		GroupName: groupName,
		Platform:  platform,
		Status:    key.Status,
		// JSON 中必须稳定输出 [] 而不是 null，旧数据的 nil slice 不能让前端读取 .length 时崩页。
		IPWhitelist: append([]string{}, key.IPWhitelist...),
		IPBlacklist: append([]string{}, key.IPBlacklist...),
		LastUsedAt:  key.LastUsedAt,
		CreatedAt:   key.CreatedAt,
		UpdatedAt:   key.UpdatedAt,
	}
	if includeSecret {
		out.Secret = key.Key
	}
	return out
}

func maskGatewayLiteLocalKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 14 {
		return "***"
	}
	return key[:10] + "..." + key[len(key)-4:]
}

func gatewayLiteLocalKeyAdminID(c *gin.Context) (int64, bool) {
	subject, ok := servermiddleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Administrator is not authenticated")
		return 0, false
	}
	return subject.UserID, true
}

// List returns gateway-local keys owned by the current administrator.
// GET /api/v1/admin/gateway-lite/local-api-keys
func (h *GatewayLiteLocalKeyHandler) List(c *gin.Context) {
	adminUserID, ok := gatewayLiteLocalKeyAdminID(c)
	if !ok {
		return
	}
	keys, err := h.apiKeyService.ListGatewayLiteLocalAPIKeys(c.Request.Context(), adminUserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]gatewayLiteLocalKeyResponse, 0, len(keys))
	for i := range keys {
		out = append(out, gatewayLiteLocalKeyDTO(&keys[i], false))
	}
	response.Success(c, out)
}

// Create creates a key that is authenticated and accounted for only by this
// gateway. It never enters control-plane quota or usage-reporting paths.
// POST /api/v1/admin/gateway-lite/local-api-keys
func (h *GatewayLiteLocalKeyHandler) Create(c *gin.Context) {
	adminUserID, ok := gatewayLiteLocalKeyAdminID(c)
	if !ok {
		return
	}
	var req gatewayLiteLocalKeyCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	key, err := h.apiKeyService.CreateGatewayLiteLocalAPIKey(c.Request.Context(), adminUserID, service.CreateGatewayLiteLocalAPIKeyRequest{
		Name:        req.Name,
		GroupID:     req.GroupID,
		IPWhitelist: req.IPWhitelist,
		IPBlacklist: req.IPBlacklist,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, gatewayLiteLocalKeyDTO(key, true))
}

// Reveal returns the plaintext value only after an explicit action from an authenticated
// administrator browser session. The gateway administrator machine key is intentionally not
// accepted: possession of that key must not grant the ability to read every local client key.
// POST /api/v1/admin/gateway-lite/local-api-keys/:id/reveal
func (h *GatewayLiteLocalKeyHandler) Reveal(c *gin.Context) {
	if c.GetString("auth_method") != service.AuditAuthMethodJWT {
		response.Forbidden(c, "An authenticated admin browser session is required")
		return
	}
	adminUserID, ok := gatewayLiteLocalKeyAdminID(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid local API key ID")
		return
	}
	secret, err := h.apiKeyService.RevealGatewayLiteLocalAPIKey(c.Request.Context(), id, adminUserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Secret-bearing responses must never be retained by browsers, reverse proxies or shared
	// caches. The operation audit records only the route and numeric resource ID.
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	response.Success(c, gin.H{"secret": secret})
}

// Update changes local routing or access state. Cache invalidation makes the
// next request observe the change immediately.
// PUT /api/v1/admin/gateway-lite/local-api-keys/:id
func (h *GatewayLiteLocalKeyHandler) Update(c *gin.Context) {
	adminUserID, ok := gatewayLiteLocalKeyAdminID(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid local API key ID")
		return
	}
	var req gatewayLiteLocalKeyUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	key, err := h.apiKeyService.UpdateGatewayLiteLocalAPIKey(c.Request.Context(), id, adminUserID, service.UpdateGatewayLiteLocalAPIKeyRequest{
		Name:        req.Name,
		GroupID:     req.GroupID,
		Status:      req.Status,
		IPWhitelist: req.IPWhitelist,
		IPBlacklist: req.IPBlacklist,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gatewayLiteLocalKeyDTO(key, false))
}

// Delete permanently revokes the local key through the existing soft-delete
// audit path, which also releases the unique plaintext key value.
// DELETE /api/v1/admin/gateway-lite/local-api-keys/:id
func (h *GatewayLiteLocalKeyHandler) Delete(c *gin.Context) {
	adminUserID, ok := gatewayLiteLocalKeyAdminID(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid local API key ID")
		return
	}
	if err := h.apiKeyService.DeleteGatewayLiteLocalAPIKey(c.Request.Context(), id, adminUserID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Gateway-lite local API key deleted"})
}
