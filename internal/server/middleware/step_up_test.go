package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubStepUpGrantChecker struct {
	granted bool
	err     error
}

func (s stubStepUpGrantChecker) HasStepUpGrant(ctx context.Context, userID int64, sessionKey string) (bool, error) {
	return s.granted, s.err
}

// ConsumeStepUpGrant 补齐 stepUpGrantConsumer 接口。新实现为一次性 grant 消费模型：
// 未授予（granted=false）且未指定错误时返回 service.ErrStepUpRequired（与真实
// TotpService.ConsumeStepUpGrant 收到空 token 时的行为一致），由实现统一映射为
// 403 STEP_UP_REQUIRED；错误态沿用桩上注入的 err。
func (s stubStepUpGrantChecker) ConsumeStepUpGrant(context.Context, string, service.StepUpChallenge, *service.User) error {
	if !s.granted {
		if s.err != nil {
			return s.err
		}
		return service.ErrStepUpRequired
	}
	return nil
}

// stepUpDisabledGrantChecker 显式实现 stepUpEnabledReader 并返回 false，对应"功能开关关闭"。
// 不带该方法的 stubStepUpGrantChecker 在本地 enforceStepUp 中保持门控开启（fail-closed），
// 等价于官方版本传入非空且开启的设置。
type stepUpDisabledGrantChecker struct{ stubStepUpGrantChecker }

func (stepUpDisabledGrantChecker) StepUpEnabled() bool { return false }

type stubStepUpUserReader struct {
	user *service.User
	err  error
}

func (s stubStepUpUserReader) GetByID(ctx context.Context, id int64) (*service.User, error) {
	return s.user, s.err
}

func newStepUpTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/sensitive", nil)
	return c, rec
}

func TestEnforceStepUpRejectsAdminAPIKey(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set("auth_method", service.AuditAuthMethodAdminAPIKey)

	ok := enforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &service.User{TotpEnabled: true}}, "test.scope")

	// 新实现把 admin API key 等所有机器凭证统一按「非 JWT 凭证」拒绝，
	// 且先于认证主体 / TOTP / grant 校验（不泄露门控后续链路信息）。
	require.False(t, ok)
	require.True(t, c.IsAborted())
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_MACHINE_CREDENTIAL_FORBIDDEN")
}

func TestEnforceStepUpRequiresAuthSubject(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	// 机器凭证检查已前移：需显式声明 JWT 凭证才会进入认证主体校验。
	c.Set("auth_method", "jwt")

	ok := enforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &service.User{TotpEnabled: true}}, "test.scope")

	require.False(t, ok)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestEnforceStepUpRequiresTotpEnabled(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set("auth_method", "jwt")
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})

	ok := enforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &service.User{ID: 1, TotpEnabled: false}}, "test.scope")

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_TOTP_NOT_ENABLED")
}

func TestEnforceStepUpFailsClosedOnGrantError(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set("auth_method", "jwt")
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})

	// 真实 TotpService 在 grant 存储层故障（如 Redis 不可用）时返回
	// service.ErrStepUpUnavailable，桩沿用同一契约；实现 fail-closed 返回 503。
	ok := enforceStepUp(c, stubStepUpGrantChecker{err: service.ErrStepUpUnavailable}, stubStepUpUserReader{user: &service.User{ID: 1, TotpEnabled: true}}, "test.scope")

	require.False(t, ok)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_UNAVAILABLE")
}

func TestEnforceStepUpRequiresGrant(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set("auth_method", "jwt")
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})

	ok := enforceStepUp(c, stubStepUpGrantChecker{granted: false}, stubStepUpUserReader{user: &service.User{ID: 1, TotpEnabled: true}}, "test.scope")

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_REQUIRED")
}

func TestEnforceStepUpPassesWithGrant(t *testing.T) {
	c, _ := newStepUpTestContext(t)
	c.Set("auth_method", "jwt")
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})

	ok := enforceStepUp(c, stubStepUpGrantChecker{granted: true}, stubStepUpUserReader{user: &service.User{ID: 1, TotpEnabled: true}}, "test.scope")

	require.True(t, ok)
	require.False(t, c.IsAborted())
}

// 功能开关关闭时：不论 TOTP/grant/凭证类型，一律放行（恢复门控引入前行为）。
func TestEnforceStepUpDisabledSkipsAllChecks(t *testing.T) {
	disabled := stepUpDisabledGrantChecker{stubStepUpGrantChecker{granted: false}}

	t.Run("no totp, no grant", func(t *testing.T) {
		c, _ := newStepUpTestContext(t)
		c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})

		ok := enforceStepUp(c, disabled, stubStepUpUserReader{user: &service.User{ID: 1, TotpEnabled: false}}, "test.scope")

		require.True(t, ok)
		require.False(t, c.IsAborted())
	})

	t.Run("admin api key", func(t *testing.T) {
		c, _ := newStepUpTestContext(t)
		c.Set("auth_method", service.AuditAuthMethodAdminAPIKey)

		ok := enforceStepUp(c, disabled, stubStepUpUserReader{user: nil, err: errors.New("should not be called")}, "test.scope")

		require.True(t, ok)
		require.False(t, c.IsAborted())
	})
}

// checker 未实现开关接口时保持门控（fail-closed），避免装配缺陷静默关闭安全控制。
// 对应官方版本"settings 为 nil 时 fail-closed"的语义：本地无 settings 参数，
// 由 totpService 上的可选 stepUpEnabledReader 接口表达同一开关。
func TestEnforceStepUpNilSettingsFailsClosed(t *testing.T) {
	c, rec := newStepUpTestContext(t)
	c.Set("auth_method", "jwt")
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})

	ok := enforceStepUp(c, stubStepUpGrantChecker{granted: false}, stubStepUpUserReader{user: &service.User{ID: 1, TotpEnabled: true}}, "test.scope")

	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_REQUIRED")
}

// EnforceStepUp 的 totpService 为 typed-nil 时不得 panic：新签名下功能开关收敛到
// TotpService.StepUpEnabled()（nil 安全读取，返回 false），门控按"功能开关关闭"
// 处理直接放行——与真实的"升级后默认关闭"装配语义一致。
// fail-closed 语义由 TestEnforceStepUpNilSettingsFailsClosed（未实现开关接口）继续覆盖。
func TestEnforceStepUpTypedNilTotpServiceBehavesAsDisabled(t *testing.T) {
	c, rec := newStepUpTestContext(t)

	require.NotPanics(t, func() {
		ok := EnforceStepUp(c, nil, nil, "")

		require.True(t, ok, "typed-nil TotpService 的开关读取为 false，与功能开关关闭同等放行")
	})
	require.False(t, c.IsAborted())
	require.Equal(t, http.StatusOK, rec.Code)
}
