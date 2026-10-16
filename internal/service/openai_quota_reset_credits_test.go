//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

type resetCreditsAccountRepo struct {
	AccountRepository
	accounts       map[int64]*Account
	extraUpdates   map[int64]map[string]any
	extraUpdateErr error
}

func (r *resetCreditsAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	account, ok := r.accounts[id]
	if !ok {
		return nil, errors.New("account not found")
	}
	return account, nil
}

func (r *resetCreditsAccountRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	if r.extraUpdateErr != nil {
		return r.extraUpdateErr
	}
	if r.extraUpdates == nil {
		r.extraUpdates = make(map[int64]map[string]any)
	}
	r.extraUpdates[id] = updates
	return nil
}

type resetCreditsTokenCache struct {
	tokens map[string]string
}

func (c *resetCreditsTokenCache) GetAccessToken(_ context.Context, key string) (string, error) {
	token, ok := c.tokens[key]
	if !ok {
		return "", errors.New("token not found")
	}
	return token, nil
}

func (c *resetCreditsTokenCache) SetAccessToken(context.Context, string, string, time.Duration) error {
	return nil
}

func (c *resetCreditsTokenCache) DeleteAccessToken(context.Context, string) error {
	return nil
}

func (c *resetCreditsTokenCache) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}

func (c *resetCreditsTokenCache) ReleaseRefreshLock(context.Context, string) error {
	return nil
}

func newResetCreditsRedirectingFactory(server *httptest.Server) PrivacyClientFactory {
	targetURL, _ := url.Parse(server.URL)
	return func(string) (*req.Client, error) {
		client := req.C().WrapRoundTripFunc(func(roundTripper req.RoundTripper) req.RoundTripFunc {
			return func(request *req.Request) (*req.Response, error) {
				request.URL.Scheme = targetURL.Scheme
				request.URL.Host = targetURL.Host
				return roundTripper.RoundTrip(request)
			}
		})
		return client, nil
	}
}

func TestParseOpenAIRateLimitResetCreditDetailsCompatibleShapes(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantCount *int
		wantDates []string
	}{
		{name: "snake case", body: `{"available_count":2,"credits":[{"expires_at":"2026-07-03T04:05:06Z"}]}`, wantCount: intPointer(2), wantDates: []string{"2026-07-03T04:05:06Z"}},
		{name: "camel case", body: `{"availableCount":"1","rate_limit_reset_credits":[{"expiresAt":"2026-07-04T04:05:06Z"}]}`, wantCount: intPointer(1), wantDates: []string{"2026-07-04T04:05:06Z"}},
		{name: "items", body: `{"items":[{"expires_at":"2026-07-05T04:05:06Z"}]}`, wantDates: []string{"2026-07-05T04:05:06Z"}},
		{name: "data", body: `{"data":[{"expires_at":"2026-07-06T04:05:06Z"}]}`, wantDates: []string{"2026-07-06T04:05:06Z"}},
		{name: "top level array", body: `[{"expires_at":"2026-07-07T04:05:06Z"}]`, wantDates: []string{"2026-07-07T04:05:06Z"}},
		{name: "null", body: `null`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			details, err := parseOpenAIRateLimitResetCreditDetails([]byte(test.body))
			require.NoError(t, err)
			if test.wantCount == nil {
				require.Nil(t, details.AvailableCount)
			} else {
				require.NotNil(t, details.AvailableCount)
				require.Equal(t, *test.wantCount, *details.AvailableCount)
			}
			require.Len(t, details.Credits, len(test.wantDates))
			for index, date := range test.wantDates {
				require.Equal(t, date, details.Credits[index].ExpiresAt)
			}
		})
	}
}

func TestParseOpenAIRateLimitResetCreditDetailsFiltersUnavailableRecords(t *testing.T) {
	details, err := parseOpenAIRateLimitResetCreditDetails([]byte(`{
		"availableCount":"2",
		"credits":[
			{"id":"secret-id","reset_type":"codex_rate_limits","status":"redeemed","expires_at":"2026-07-01T04:05:06Z"},
			{"reset_type":"codex_rate_limits","status":"available","expires_at":"2026-07-04T04:05:06Z"},
			{"resetType":"codex_rate_limits","status":"available","expiresAt":"2026-07-03T04:05:06Z"},
			{"reset_type":"other","status":"available","expires_at":"2026-07-02T04:05:06Z"}
		]
	}`))
	require.NoError(t, err)
	require.NotNil(t, details.AvailableCount)
	require.Equal(t, 2, *details.AvailableCount)
	require.Equal(t, 2, details.AvailableCreditCount)
	require.Equal(t, []OpenAIRateLimitResetCreditDetail{
		{ExpiresAt: "2026-07-04T04:05:06Z"},
		{ExpiresAt: "2026-07-03T04:05:06Z"},
	}, details.Credits)

	encoded, err := json.Marshal(details.Credits)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret-id")
}

func TestQueryUsageResetCreditDetailsPrecedence(t *testing.T) {
	tests := []struct {
		name        string
		usageBody   string
		detailBody  string
		detailCode  int
		wantCount   int
		wantCredits int
		wantNil     bool
	}{
		{name: "detail count creates missing usage value", usageBody: `{}`, detailBody: `{"available_count":3,"credits":[{"expires_at":"2026-07-03T04:05:06Z"}]}`, wantCount: 3, wantCredits: 1},
		{name: "explicit zero overrides usage", usageBody: `{"rate_limit_reset_credits":{"available_count":4}}`, detailBody: `{"available_count":0,"credits":[{"expires_at":"2026-07-03T04:05:06Z"}]}`, wantCredits: 1},
		{name: "list count overrides usage", usageBody: `{"rate_limit_reset_credits":{"available_count":7}}`, detailBody: `{"credits":[{"expires_at":"2026-07-03T04:05:06Z"},{"expiresAt":"2026-07-04T04:05:06Z"}]}`, wantCount: 2, wantCredits: 2},
		{name: "empty list overrides usage", usageBody: `{"rate_limit_reset_credits":{"available_count":7}}`, detailBody: `{"credits":[]}`},
		{name: "record without expiry still counts", usageBody: `{"rate_limit_reset_credits":{"available_count":7}}`, detailBody: `{"credits":[{"status":"available"},{"status":"available","expires_at":"2026-07-04T04:05:06Z"}]}`, wantCount: 2, wantCredits: 1},
		{name: "empty details preserve usage", usageBody: `{"rate_limit_reset_credits":{"available_count":5,"credits":[{"expires_at":"usage-expiry"}]}}`, detailBody: `{}`, wantCount: 5, wantCredits: 1},
		{name: "negative count preserves usage", usageBody: `{"rate_limit_reset_credits":{"available_count":4}}`, detailBody: `{"available_count":-1}`, wantCount: 4},
		{name: "detail failure is non fatal", usageBody: `{"rate_limit_reset_credits":{"available_count":4}}`, detailBody: `{"error":"unauthorized"}`, detailCode: http.StatusUnauthorized, wantCount: 4},
		{name: "missing usage and empty details stay nil", usageBody: `{}`, detailBody: `{}`, wantNil: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			account := &Account{
				ID:       100,
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Status:   StatusActive,
				Credentials: map[string]any{
					"chatgpt_account_id": "org-parent123",
				},
			}
			repo := &resetCreditsAccountRepo{accounts: map[int64]*Account{100: account}}
			cache := &resetCreditsTokenCache{tokens: map[string]string{OpenAITokenCacheKey(account): "fake-token"}}
			provider := NewOpenAITokenProvider(repo, cache, nil)

			var detailCalls int
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("content-type", "application/json")
				switch request.URL.Path {
				case "/backend-api/wham/usage":
					_, _ = writer.Write([]byte(test.usageBody))
				case "/backend-api/wham/rate-limit-reset-credits":
					detailCalls++
					if test.detailCode != 0 {
						writer.WriteHeader(test.detailCode)
					}
					_, _ = writer.Write([]byte(test.detailBody))
				default:
					http.NotFound(writer, request)
				}
			}))
			defer server.Close()

			service := NewOpenAIQuotaService(repo, nil, provider, newResetCreditsRedirectingFactory(server))
			usage, err := service.QueryUsage(context.Background(), 100)
			require.NoError(t, err)
			require.NotNil(t, usage)
			require.Equal(t, 1, detailCalls)
			if test.wantNil {
				require.Nil(t, usage.RateLimitResetCredits)
				return
			}
			require.NotNil(t, usage.RateLimitResetCredits)
			require.Equal(t, test.wantCount, usage.RateLimitResetCredits.AvailableCount)
			require.Len(t, usage.RateLimitResetCredits.Credits, test.wantCredits)
		})
	}
}

func TestCacheResetCreditsSnapshotFiltersExpiredEntries(t *testing.T) {
	now := time.Now().UTC()
	futureOne := now.Add(time.Hour).Format(time.RFC3339)
	futureTwo := now.Add(2 * time.Hour).Format(time.RFC3339)
	repo := &resetCreditsAccountRepo{}
	quotaService := &OpenAIQuotaService{accountRepo: repo}

	err := quotaService.CacheResetCreditsSnapshot(context.Background(), 100, &OpenAIRateLimitResetCredits{
		AvailableCount: 4,
		Credits: []OpenAIRateLimitResetCreditDetail{
			{ExpiresAt: now.Add(-time.Minute).Format(time.RFC3339)},
			{ExpiresAt: "invalid"},
			{ExpiresAt: futureOne},
			{ExpiresAt: futureTwo},
		},
	})
	require.NoError(t, err)

	snapshot, ok := repo.extraUpdates[100][openaiQuotaResetCreditsKey].(*OpenAIRateLimitResetCredits)
	require.True(t, ok)
	require.Equal(t, 2, snapshot.AvailableCount)
	require.Equal(t, []OpenAIRateLimitResetCreditDetail{
		{ExpiresAt: futureOne},
		{ExpiresAt: futureTwo},
	}, snapshot.Credits)
}

func TestCacheResetCreditsSnapshotPreservesOldCacheWithoutFutureExpiry(t *testing.T) {
	repo := &resetCreditsAccountRepo{}
	quotaService := &OpenAIQuotaService{accountRepo: repo}

	err := quotaService.CacheResetCreditsSnapshot(context.Background(), 100, &OpenAIRateLimitResetCredits{
		AvailableCount: 1,
		Credits: []OpenAIRateLimitResetCreditDetail{
			{ExpiresAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)},
		},
	})
	require.Error(t, err)
	require.Empty(t, repo.extraUpdates)
}

func TestCacheResetCreditsSnapshotAllowsZeroCount(t *testing.T) {
	repo := &resetCreditsAccountRepo{}
	quotaService := &OpenAIQuotaService{accountRepo: repo}

	err := quotaService.CacheResetCreditsSnapshot(context.Background(), 100, &OpenAIRateLimitResetCredits{AvailableCount: 0})
	require.NoError(t, err)

	snapshot, ok := repo.extraUpdates[100][openaiQuotaResetCreditsKey].(*OpenAIRateLimitResetCredits)
	require.True(t, ok)
	require.Zero(t, snapshot.AvailableCount)
	require.Empty(t, snapshot.Credits)
}

func intPointer(value int) *int {
	return &value
}
