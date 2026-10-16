package middleware

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayCustomErrorSettingRepo struct{ values map[string]string }

func (r *gatewayCustomErrorSettingRepo) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}
func (r *gatewayCustomErrorSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}
func (r *gatewayCustomErrorSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
func (r *gatewayCustomErrorSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}
func (r *gatewayCustomErrorSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}
func (r *gatewayCustomErrorSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return r.values, nil
}
func (r *gatewayCustomErrorSettingRepo) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func TestGatewayCustomErrorMessageMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &gatewayCustomErrorSettingRepo{values: map[string]string{
		service.SettingKeyGatewayCustomErrorMessage: "推广活动：https://example.com",
	}}
	settings := service.NewSettingService(repo, &config.Config{})
	require.NoError(t, settings.WarmGatewayCustomErrorMessage(context.Background()))

	router := gin.New()
	router.Use(GatewayCustomErrorMessage(settings))
	router.GET("/v1/error", func(c *gin.Context) {
		c.Header("Retry-After", "17")
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "real reason", "type": "authentication_error", "code": "invalid_api_key"}})
	})
	router.GET("/responses", func(c *gin.Context) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "alias route detail"})
	})
	router.GET("/backend-api/codex/responses", func(c *gin.Context) {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "codex detail", "code": "upstream_error"}})
	})
	router.GET("/v1/success", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/json", []byte(`{"error":null,"message":"success","data":{"message":"model output"}}`))
	})
	router.GET("/v1/sse", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(c.Writer, "event: error\ndata: {\"error\":{\"message\":\"real reason\",\"type\":\"upstream_error\"}}\n\n")
	})
	router.GET("/api/v1/admin/error", func(c *gin.Context) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "admin detail", "code": "bad_admin_input"}})
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/error", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code)
	require.Equal(t, "17", response.Header().Get("Retry-After"))
	require.JSONEq(t, `{"error":{"message":"推广活动：https://example.com","type":"authentication_error","code":"invalid_api_key"}}`, response.Body.String())

	for _, path := range []string{"/responses", "/backend-api/codex/responses"} {
		aliasRequest := httptest.NewRequest(http.MethodGet, path, nil)
		aliasResponse := httptest.NewRecorder()
		router.ServeHTTP(aliasResponse, aliasRequest)
		require.Contains(t, aliasResponse.Body.String(), "推广活动：https://example.com")
		require.NotContains(t, aliasResponse.Body.String(), "detail")
	}

	successRequest := httptest.NewRequest(http.MethodGet, "/v1/success", nil)
	successResponse := httptest.NewRecorder()
	router.ServeHTTP(successResponse, successRequest)
	require.Equal(t, `{"error":null,"message":"success","data":{"message":"model output"}}`, successResponse.Body.String())

	sseRequest := httptest.NewRequest(http.MethodGet, "/v1/sse", nil)
	sseResponse := httptest.NewRecorder()
	router.ServeHTTP(sseResponse, sseRequest)
	require.Contains(t, sseResponse.Body.String(), `"message":"推广活动：https://example.com"`)
	require.Contains(t, sseResponse.Body.String(), `"type":"upstream_error"`)

	adminRequest := httptest.NewRequest(http.MethodGet, "/api/v1/admin/error", nil)
	adminResponse := httptest.NewRecorder()
	router.ServeHTTP(adminResponse, adminRequest)
	require.Contains(t, adminResponse.Body.String(), "admin detail")
}

type exactLengthResponseWriter struct {
	gin.ResponseWriter
}

func (w *exactLengthResponseWriter) Write(payload []byte) (int, error) {
	return len(payload), nil
}

func (w *exactLengthResponseWriter) WriteString(payload string) (int, error) {
	return len(payload), nil
}

func TestGatewayCustomErrorWriterReportsOriginalInputLength(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	testContext.Writer.WriteHeader(http.StatusBadRequest)
	base := &exactLengthResponseWriter{ResponseWriter: testContext.Writer}
	w := &gatewayCustomErrorWriter{ResponseWriter: base, message: "a much longer advertising message"}
	w.Header().Set("Content-Type", "application/json")
	original := []byte(`{"error":{"message":"x","code":"bad"}}`)
	n, err := w.Write(original)
	require.NoError(t, err)
	require.Equal(t, len(original), n)
	n, err = io.WriteString(w, string(original))
	require.NoError(t, err)
	require.Equal(t, len(original), n)
}

func TestGatewayCustomErrorWriterRemovesContentLengthBeforeCommit(t *testing.T) {
	recorder := httptest.NewRecorder()
	testContext, _ := gin.CreateTestContext(recorder)
	w := &gatewayCustomErrorWriter{ResponseWriter: testContext.Writer, message: "custom"}
	w.WriteHeader(http.StatusBadRequest)
	w.Header().Set("Content-Length", "12")
	w.WriteHeaderNow()
	require.Empty(t, recorder.Header().Get("Content-Length"))
}

func TestGatewayCustomErrorMessageUsesRequestStartSnapshot(t *testing.T) {
	repo := &gatewayCustomErrorSettingRepo{values: map[string]string{
		service.SettingKeyGatewayCustomErrorMessage: "first snapshot",
	}}
	settings := service.NewSettingService(repo, &config.Config{})
	require.NoError(t, settings.WarmGatewayCustomErrorMessage(context.Background()))
	router := gin.New()
	router.Use(GatewayCustomErrorMessage(settings))
	router.GET("/v1/snapshot", func(c *gin.Context) {
		repo.values[service.SettingKeyGatewayCustomErrorMessage] = "later snapshot"
		require.NoError(t, settings.WarmGatewayCustomErrorMessage(c.Request.Context()))
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "real", "code": "bad"}})
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/snapshot", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Contains(t, response.Body.String(), "first snapshot")
	require.NotContains(t, response.Body.String(), "later snapshot")
}

func TestGatewayCustomErrorClientPathCoverage(t *testing.T) {
	for _, path := range []string{
		"/v1/messages", "/v1beta/models/x", "/models", "/responses", "/backend-api/codex/responses",
		"/chat/completions", "/messages/count_tokens", "/embeddings", "/images/tasks/1", "/videos/1",
		"/alpha/search", "/antigravity/v1/messages",
	} {
		require.Truef(t, isGatewayClientRequestPath(path), "customer path must be covered: %s", path)
	}
	for _, path := range []string{"/", "/health", "/ready", "/api/v1/admin/settings", "/api/v1/auth/login"} {
		require.Falsef(t, isGatewayClientRequestPath(path), "non-customer path must stay original: %s", path)
	}
}
