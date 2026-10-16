package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexModelsFailoverAccountRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (r codexModelsFailoverAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			account := r.accounts[i]
			return &account, nil
		}
	}
	return nil, service.ErrAccountNotFound
}

func (r codexModelsFailoverAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]service.Account, error) {
	accounts := make([]service.Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if account.Platform != platform || !account.Schedulable || account.Status != service.StatusActive {
			continue
		}
		for _, candidateGroupID := range account.GroupIDs {
			if candidateGroupID == groupID {
				accounts = append(accounts, account)
				break
			}
		}
	}
	return accounts, nil
}

type codexModelsFailoverHTTPUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
	statuses   map[int64]int
	errors     map[int64]error
	bodies     map[int64]string
}

func (u *codexModelsFailoverHTTPUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	u.mu.Unlock()

	if err := u.errors[accountID]; err != nil {
		return nil, err
	}
	status := u.statuses[accountID]
	if status == 0 {
		status = http.StatusOK
	}
	body, hasCustomBody := u.bodies[accountID]
	if !hasCustomBody {
		body = `{"models":[{"slug":"gpt-5.6-sol"}]}`
	}
	if status < 200 || status >= 300 {
		body = `{"error":{"message":"manifest failed"}}`
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (u *codexModelsFailoverHTTPUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

func TestCodexModelsCanceledRequestDoesNotWriteResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil).WithContext(ctx)

	(&OpenAIGatewayHandler{}).CodexModels(c)
	require.False(t, c.Writer.Written())
}

func TestCodexModelsFailsOverInsideFixedGroupForRetryableErrors(t *testing.T) {
	for _, status := range []int{
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			handler, upstream, groupID := newCodexModelsFailoverTestHandler(3)
			upstream.statuses[1] = status
			recorder := performCodexModelsRequest(t, handler, groupID)

			require.Equal(t, []int64{1, 2}, upstream.calls())
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.Equal(t, string(service.CodexModelsManifestCacheStateUpstream), recorder.Header().Get(codexModelsSourceHeader))
			require.JSONEq(t, `{"models":[{"slug":"gpt-5.6-sol"}]}`, recorder.Body.String())
		})
	}
}

func TestCompositeCodexModelsSelectsOnlyOpenAIAccountsInsideFixedGroup(t *testing.T) {
	groupID := int64(42)
	otherGroupID := int64(84)
	nonOpenAIAccount := newCodexModelsFailoverAccount(2, groupID, service.AccountTypeAPIKey)
	nonOpenAIAccount.Platform = service.PlatformAnthropic
	upstream := &codexModelsFailoverHTTPUpstream{
		statuses: map[int64]int{1: http.StatusServiceUnavailable},
		errors:   make(map[int64]error),
		bodies:   make(map[int64]string),
	}
	handler := &OpenAIGatewayHandler{
		gatewayService: newCodexModelsGatewayService([]service.Account{
			newCodexModelsFailoverAccount(1, groupID, service.AccountTypeAPIKey),
			nonOpenAIAccount,
			newCodexModelsFailoverAccount(3, otherGroupID, service.AccountTypeAPIKey),
			newCodexModelsFailoverAccount(4, groupID, service.AccountTypeAPIKey),
		}, upstream),
		maxAccountSwitches: 3,
	}

	recorder := performCodexModelsRequestForPlatform(t, handler, groupID, service.PlatformComposite)

	require.Equal(t, []int64{1, 4}, upstream.calls())
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

func TestWriteCodexModelsManifestMarksLastKnownGoodSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.144.1", nil)
	groupID := int64(42)

	h := &OpenAIGatewayHandler{}
	h.writeCodexModelsManifest(
		c,
		&service.APIKey{ID: 101},
		&groupID,
		7,
		&service.CodexModelsManifest{
			Body:       []byte(`{"models":[{"slug":"last-known-good"}]}`),
			ETag:       `W/"stale"`,
			CacheState: service.CodexModelsManifestCacheStateStale,
		},
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, `W/"stale"`, recorder.Header().Get("ETag"))
	require.Equal(t, string(service.CodexModelsManifestCacheStateStale), recorder.Header().Get(codexModelsSourceHeader))
	require.Equal(t, "private, no-cache", recorder.Header().Get("Cache-Control"))
	require.JSONEq(t, `{"models":[{"slug":"last-known-good"}]}`, recorder.Body.String())
}

func TestCodexModelsFailsOverFromRetryableTransportError(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "connection reset", err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler, upstream, groupID := newCodexModelsFailoverTestHandler(3)
			upstream.errors[1] = tt.err
			recorder := performCodexModelsRequest(t, handler, groupID)

			require.Equal(t, []int64{1, 2}, upstream.calls())
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		})
	}
}

func TestCodexModelsFailsOverFromInvalidManifestWithoutCachingIt(t *testing.T) {
	handler, upstream, groupID := newCodexModelsFailoverTestHandler(3)
	upstream.bodies[1] = `{"object":"list","data":[]}`
	recorder := performCodexModelsRequest(t, handler, groupID)

	require.Equal(t, []int64{1, 2}, upstream.calls())
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{"models":[{"slug":"gpt-5.6-sol"}]}`, recorder.Body.String())
}

func TestCodexModelsFailsOverAcross5xxAndInvalid2xx(t *testing.T) {
	handler, upstream, groupID := newCodexModelsFailoverTestHandler(3)
	upstream.statuses[1] = http.StatusServiceUnavailable
	upstream.bodies[2] = `{"models":[{"name":"missing-id"}]}`
	recorder := performCodexModelsRequest(t, handler, groupID)

	require.Equal(t, []int64{1, 2, 3}, upstream.calls())
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{"models":[{"slug":"gpt-5.6-sol"}]}`, recorder.Body.String())
}

func TestCodexModelsDoesNotFailOverForPermanent4xx(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			handler, upstream, groupID := newCodexModelsFailoverTestHandler(3)
			upstream.statuses[1] = status
			recorder := performCodexModelsRequest(t, handler, groupID)

			require.Equal(t, []int64{1}, upstream.calls())
			require.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
		})
	}
}

func TestCodexModelsHonorsSwitchLimitAndReturnsLastError(t *testing.T) {
	handler, upstream, groupID := newCodexModelsFailoverTestHandler(1)
	upstream.statuses[1] = http.StatusServiceUnavailable
	upstream.statuses[2] = http.StatusGatewayTimeout
	upstream.statuses[3] = http.StatusBadGateway
	recorder := performCodexModelsRequest(t, handler, groupID)

	require.Equal(t, []int64{1, 2}, upstream.calls())
	require.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "upstream error 504")
}

func TestCodexModelsCanSwitchFromOAuthToAPIKeyAccount(t *testing.T) {
	handler, upstream, groupID := newCodexModelsFailoverTestHandler(3)
	handler.gatewayService = newCodexModelsGatewayService([]service.Account{
		newCodexModelsFailoverAccount(1, groupID, service.AccountTypeOAuth),
		newCodexModelsFailoverAccount(2, groupID, service.AccountTypeAPIKey),
	}, upstream)
	upstream.statuses[1] = http.StatusServiceUnavailable

	recorder := performCodexModelsRequest(t, handler, groupID)
	require.Equal(t, []int64{1, 2}, upstream.calls())
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

func TestCodexModelsDoesNotSelectAccountFromAnotherGroup(t *testing.T) {
	groupID := int64(42)
	otherGroupID := int64(84)
	upstream := &codexModelsFailoverHTTPUpstream{
		statuses: map[int64]int{1: http.StatusServiceUnavailable},
		errors:   make(map[int64]error),
	}
	handler := &OpenAIGatewayHandler{
		gatewayService: newCodexModelsGatewayService([]service.Account{
			newCodexModelsFailoverAccount(1, groupID, service.AccountTypeAPIKey),
			newCodexModelsFailoverAccount(2, otherGroupID, service.AccountTypeAPIKey),
		}, upstream),
		maxAccountSwitches: 3,
	}
	recorder := performCodexModelsRequest(t, handler, groupID)

	require.Equal(t, []int64{1}, upstream.calls())
	require.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
}

func newCodexModelsFailoverTestHandler(maxSwitches int) (*OpenAIGatewayHandler, *codexModelsFailoverHTTPUpstream, int64) {
	gin.SetMode(gin.TestMode)
	groupID := int64(42)
	accounts := []service.Account{
		newCodexModelsFailoverAccount(1, groupID, service.AccountTypeAPIKey),
		newCodexModelsFailoverAccount(2, groupID, service.AccountTypeAPIKey),
		newCodexModelsFailoverAccount(3, groupID, service.AccountTypeAPIKey),
	}
	upstream := &codexModelsFailoverHTTPUpstream{
		statuses: make(map[int64]int),
		errors:   make(map[int64]error),
		bodies:   make(map[int64]string),
	}
	return &OpenAIGatewayHandler{
		gatewayService:     newCodexModelsGatewayService(accounts, upstream),
		maxAccountSwitches: maxSwitches,
	}, upstream, groupID
}

func newCodexModelsFailoverAccount(id, groupID int64, accountType string) service.Account {
	credentials := map[string]any{
		"api_key":  fmt.Sprintf("sk-%d", id),
		"base_url": fmt.Sprintf("https://upstream-%d.example/v1", id),
	}
	if accountType == service.AccountTypeOAuth {
		credentials = map[string]any{
			"access_token":       fmt.Sprintf("oauth-%d", id),
			"chatgpt_account_id": fmt.Sprintf("account-%d", id),
		}
	}
	return service.Account{
		ID:          id,
		Name:        fmt.Sprintf("upstream-%d", id),
		Platform:    service.PlatformOpenAI,
		Type:        accountType,
		Status:      service.StatusActive,
		Schedulable: true,
		Priority:    int(id - 1),
		Concurrency: 1,
		GroupIDs:    []int64{groupID},
		Credentials: credentials,
	}
}

func newCodexModelsGatewayService(accounts []service.Account, upstream service.HTTPUpstream) *service.OpenAIGatewayService {
	cfg := &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}
	return service.NewOpenAIGatewayService(
		codexModelsFailoverAccountRepo{accounts: accounts},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		nil,
		nil,
		nil,
		upstream,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
}

func performCodexModelsRequest(t *testing.T, handler *OpenAIGatewayHandler, groupID int64) *httptest.ResponseRecorder {
	return performCodexModelsRequestForPlatform(t, handler, groupID, service.PlatformOpenAI)
}

func performCodexModelsRequestForPlatform(t *testing.T, handler *OpenAIGatewayHandler, groupID int64, platform string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.144.1", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		GroupID:                 &groupID,
		Group:                   &service.Group{ID: groupID, Platform: platform, AllowImageGeneration: true},
		GatewayStrategy:         "fixed_group",
		KeyDefaultGatewayNodeID: 1,
	})

	handler.CodexModels(c)
	return recorder
}
