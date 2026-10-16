package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/codexadmission"
	"github.com/Wei-Shaw/sub2api/internal/ingress"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayLiteCodexClientAdmissionRejectsBeforeDownstreamSideEffects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := codexadmission.DefaultSettings()
	settings.Mode = codexadmission.ModeEnforce
	settings.DeniedClients = []string{codexadmission.ClientCLI}
	runtime := codexadmission.NewRuntime(settings)
	aggregator := ingress.NewAggregator()

	downstreamCalls := 0 // stands for auth resolve, lease, dispatch, and usage writing
	router := gin.New()
	router.Use(gatewayLiteCodexClientAdmission(runtime, aggregator))
	router.POST("/v1/responses", func(c *gin.Context) {
		downstreamCalls++
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
	req.Header.Set("Originator", "codex_cli_rs")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Zero(t, downstreamCalls)
	require.EqualValues(t, 1, aggregator.Snapshot(60).RejectionCount)
}

func TestGatewayLiteCodexClientAdmissionAuditOnlyContinues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := codexadmission.DefaultSettings()
	settings.DeniedClients = []string{codexadmission.ClientCLI}
	runtime := codexadmission.NewRuntime(settings)

	router := gin.New()
	router.Use(GatewayLiteCodexClientAdmission(runtime))
	router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
	req.Header.Set("Originator", "codex_cli_rs")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.EqualValues(t, 1, runtime.Snapshot().AuditViolations)
	require.Zero(t, runtime.Snapshot().Denied)
}

func TestGatewayLiteGlobalAllowDoesNotBypassAccountCodexCLIOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := codexadmission.DefaultSettings()
	settings.Mode = codexadmission.ModeEnforce
	settings.AppServerEnabled = true
	runtime := codexadmission.NewRuntime(settings)
	detector := service.NewOpenAICodexClientRestrictionDetector(nil)
	account := &service.Account{
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Extra:    map[string]any{"codex_cli_only": true},
	}

	router := gin.New()
	router.Use(GatewayLiteCodexClientAdmission(runtime))
	router.POST("/backend-api/codex/responses", func(c *gin.Context) {
		// Global allow only reaches normal routing. The selected account's
		// codex_cli_only rule still evaluates independently and may deny.
		result := detector.Detect(c, account, nil)
		if !result.Matched {
			c.Status(http.StatusForbidden)
			return
		}
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/backend-api/codex/responses?client_version=0.144.1", nil)
	req.Header.Set("User-Agent", "custom-app-server/1.0")
	req.Header.Set("X-Codex-Engine", "responses")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestGatewayLiteCodexClientAdmissionUsesResponsesPathAsSecondSignal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := codexadmission.DefaultSettings()
	settings.Mode = codexadmission.ModeEnforce
	settings.MinVersion = "0.145.0"
	runtime := codexadmission.NewRuntime(settings)

	downstreamCalls := 0
	router := gin.New()
	router.Use(GatewayLiteCodexClientAdmission(runtime))
	router.POST("/v1/responses", func(c *gin.Context) {
		downstreamCalls++
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Zero(t, downstreamCalls)
	require.EqualValues(t, 1, runtime.Snapshot().Denied)
}

func TestGatewayLiteCodexClientAdmissionDoesNotRejectOrdinaryResponsesClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := codexadmission.DefaultSettings()
	settings.Mode = codexadmission.ModeEnforce
	settings.MinVersion = "0.145.0"
	settings.AllowedClients = []string{codexadmission.ClientDesktop}
	runtime := codexadmission.NewRuntime(settings)

	downstreamCalls := 0
	router := gin.New()
	router.Use(GatewayLiteCodexClientAdmission(runtime))
	router.POST("/v1/responses", func(c *gin.Context) {
		downstreamCalls++
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("User-Agent", "openai-python/2.3.0")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Equal(t, 1, downstreamCalls)
	require.Zero(t, runtime.Snapshot().Denied)
}
