package middleware

import (
	"context"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

const StepUpTokenHeader = "X-Step-Up-Token"

// StepUpAuthMiddleware consumes one-time, action-scoped TOTP grants for high-risk routes.
type StepUpAuthMiddleware struct {
	totpService stepUpGrantConsumer
	userService stepUpUserReader
}

type stepUpGrantConsumer interface {
	ConsumeStepUpGrant(ctx context.Context, token string, expected service.StepUpChallenge, user *service.User) error
}

type stepUpEnabledReader interface {
	StepUpEnabled() bool
}

type stepUpUserReader interface {
	GetByID(ctx context.Context, id int64) (*service.User, error)
}

func NewStepUpAuthMiddleware(totpService *service.TotpService, userService *service.UserService) *StepUpAuthMiddleware {
	return &StepUpAuthMiddleware{totpService: totpService, userService: userService}
}

func (m *StepUpAuthMiddleware) ForScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !enforceStepUp(c, m.totpService, m.userService, scope) {
			return
		}
		c.Next()
	}
}

// EnforceStepUp supports conditional high-risk operations such as role promotion.
func EnforceStepUp(c *gin.Context, totpService *service.TotpService, userService *service.UserService, scope string) bool {
	return enforceStepUp(c, totpService, userService, scope)
}

func enforceStepUp(c *gin.Context, totpService stepUpGrantConsumer, userService stepUpUserReader, scope string) bool {
	// Optional interface keeps existing narrow test doubles compatible. The real
	// TotpService always implements it and defaults to disabled after upgrade.
	if enabledReader, ok := totpService.(stepUpEnabledReader); ok && !enabledReader.StepUpEnabled() {
		return true
	}
	if c.GetString("auth_method") != "jwt" {
		AbortWithError(c, 403, "STEP_UP_MACHINE_CREDENTIAL_FORBIDDEN", "A two-factor verified browser session is required")
		return false
	}
	subject, ok := GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		AbortWithError(c, 401, "UNAUTHORIZED", "Authorization required")
		return false
	}
	user, err := userService.GetByID(c.Request.Context(), subject.UserID)
	if err != nil {
		AbortWithError(c, 503, "STEP_UP_UNAVAILABLE", "Step-up verification service unavailable")
		return false
	}
	if !user.TotpEnabled {
		AbortWithError(c, 403, "STEP_UP_TOTP_NOT_ENABLED", "This operation requires TOTP; enable two-factor authentication first")
		return false
	}
	sessionID := strings.TrimSpace(c.GetString(ContextKeySessionID))
	bindingHash := currentSessionBindingHash(c)
	challenge := service.StepUpChallenge{
		SessionID:   sessionID,
		BindingHash: bindingHash,
		ActionScope: scope,
		SourceHash:  service.StepUpSourceHash(SecurityClientIP(c)),
	}
	if err := totpService.ConsumeStepUpGrant(c.Request.Context(), c.GetHeader(StepUpTokenHeader), challenge, user); err != nil {
		appErr := infraerrors.FromError(err)
		AbortWithError(c, int(appErr.Code), appErr.Reason, appErr.Message)
		return false
	}
	return true
}
