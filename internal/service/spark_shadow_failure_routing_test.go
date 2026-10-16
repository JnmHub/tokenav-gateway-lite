//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type sparkFailureRoutingRepo struct {
	rateLimitAccountRepoStub
	rateLimitedAccountIDs []int64
}

func (r *sparkFailureRoutingRepo) SetRateLimited(_ context.Context, id int64, _ time.Time) error {
	r.rateLimitedAccountIDs = append(r.rateLimitedAccountIDs, id)
	return nil
}

func newSparkFailureRoutingFixture() (*sparkFailureRoutingRepo, *RateLimitService, *Account, *Account) {
	parentID := int64(7101)
	parent := &Account{
		ID:       parentID,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":  "parent-at",
			"refresh_token": "parent-rt",
		},
	}
	shadow := &Account{
		ID:              7102,
		Platform:        PlatformOpenAI,
		Type:            AccountTypeOAuth,
		Status:          StatusActive,
		ParentAccountID: &parentID,
		QuotaDimension:  QuotaDimensionSpark,
		Credentials:     map[string]any{"model_mapping": defaultSparkShadowModelMapping()},
	}
	repo := &sparkFailureRoutingRepo{}
	repo.accountsByID = map[int64]*Account{parent.ID: parent, shadow.ID: shadow}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	return repo, svc, parent, shadow
}

func TestSparkShadowSharedCredentialFailureClassification(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "401 always belongs to shared credentials", status: http.StatusUnauthorized, body: `{"error":{"message":"expired"}}`, want: true},
		{name: "403 always belongs to shared credentials", status: http.StatusForbidden, body: `{"error":{"message":"workspace forbidden"}}`, want: true},
		{name: "400 invalid api key code", status: http.StatusBadRequest, body: `{"error":{"code":"invalid_api_key","message":"bad credential"}}`, want: true},
		{name: "400 disabled organization message", status: http.StatusBadRequest, body: `{"error":{"message":"Organization has been disabled"}}`, want: true},
		{name: "400 identity verification message", status: http.StatusBadRequest, body: `{"error":{"message":"Identity verification is required"}}`, want: true},
		{name: "400 request parameter remains request scoped", status: http.StatusBadRequest, body: `{"error":{"type":"invalid_request_error","message":"unknown parameter tools[2]"}}`, want: false},
		{name: "429 remains Spark quota scoped", status: http.StatusTooManyRequests, body: `{"error":{"type":"rate_limit_error"}}`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isSharedCredentialFailure(tt.status, []byte(tt.body)))
		})
	}
}

func TestSparkShadowFailureStateTargetsCorrectAccount(t *testing.T) {
	t.Run("401 cools down parent", func(t *testing.T) {
		repo, svc, parent, shadow := newSparkFailureRoutingFixture()
		invalidator := &tokenCacheInvalidatorRecorder{}
		svc.SetTokenCacheInvalidator(invalidator)

		disabled := svc.HandleUpstreamError(context.Background(), shadow, http.StatusUnauthorized, http.Header{}, []byte(`{"error":{"message":"expired access token"}}`))

		require.True(t, disabled)
		require.Equal(t, parent.ID, repo.lastTempAccountID)
		require.Zero(t, repo.lastErrorAccountID)
		require.Len(t, invalidator.accounts, 1)
		require.Equal(t, parent.ID, invalidator.accounts[0].ID)
	})

	t.Run("403 counter cooldown targets parent", func(t *testing.T) {
		repo, svc, parent, shadow := newSparkFailureRoutingFixture()
		svc.SetOpenAI403CounterCache(&openAI403CounterCacheStub{counts: []int64{1}})

		disabled := svc.HandleUpstreamError(context.Background(), shadow, http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"workspace forbidden"}}`))

		require.True(t, disabled)
		require.Equal(t, parent.ID, repo.lastTempAccountID)
		require.Zero(t, repo.lastErrorAccountID)
	})

	t.Run("credential level 400 disables parent", func(t *testing.T) {
		repo, svc, parent, shadow := newSparkFailureRoutingFixture()

		disabled := svc.HandleUpstreamError(context.Background(), shadow, http.StatusBadRequest, http.Header{}, []byte(`{"error":{"code":"invalid_api_key","message":"Incorrect API key provided"}}`))

		require.True(t, disabled)
		require.Equal(t, parent.ID, repo.lastErrorAccountID)
		require.Zero(t, repo.lastTempAccountID)
	})

	t.Run("429 only cools down Spark shadow", func(t *testing.T) {
		repo, svc, _, shadow := newSparkFailureRoutingFixture()

		disabled := svc.HandleUpstreamError(context.Background(), shadow, http.StatusTooManyRequests, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"Spark capacity reached"}}`))

		require.False(t, disabled)
		require.Equal(t, []int64{shadow.ID}, repo.rateLimitedAccountIDs)
		require.Zero(t, repo.lastTempAccountID)
		require.Zero(t, repo.lastErrorAccountID)
	})
}
