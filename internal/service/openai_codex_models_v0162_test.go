package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestConvertOpenAIModelListToCodexManifestV0162(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "standard list trims and deduplicates",
			body: `{"object":"list","data":[{"id":"gpt-5.6"},{"id":"  "},{"id":"gpt-5.6"},{"id":"gpt-5.6-codex"}]}`,
			want: `{"models":[{"slug":"gpt-5.6"},{"slug":"gpt-5.6-codex"}]}`,
		},
		{name: "existing manifest unchanged", body: `{"models":[{"slug":"gpt-5.6"}]}`, want: `{"models":[{"slug":"gpt-5.6"}]}`},
		{name: "empty list unchanged", body: `{"object":"list","data":[]}`, want: `{"object":"list","data":[]}`},
		{name: "invalid data unchanged", body: `{"object":"list","data":{"id":"gpt-5.6"}}`, want: `{"object":"list","data":{"id":"gpt-5.6"}}`},
		{name: "invalid json unchanged", body: `{"data":`, want: `{"data":`},
		{name: "non object unchanged", body: `[]`, want: `[]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, string(convertOpenAIModelListToCodexManifest([]byte(tt.body))))
		})
	}
}

type codexModelsV0162AccountStateRepo struct {
	AccountRepository
	tempUnschedulable int
	disabled          int
}

func (r *codexModelsV0162AccountStateRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.tempUnschedulable++
	return nil
}

func (r *codexModelsV0162AccountStateRepo) SetError(context.Context, int64, string) error {
	r.disabled++
	return nil
}

func TestFetchCodexModelsManifestV0162OAuth401EntersAccountState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":{"message":"invalid token"}}`))
	}))
	defer server.Close()
	originalURL := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	defer func() { chatgptCodexModelsURL = originalURL }()

	groupID := int64(11)
	account := codexModelsTestAccount(1, groupID, nil)
	account.Credentials["refresh_token"] = "test-refresh-token"
	repo := &codexModelsV0162AccountStateRepo{}
	svc := &OpenAIGatewayService{
		accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{account}}},
	}
	svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.rateLimitService.SetAccountRuntimeBlocker(svc)

	_, err := svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, codexModelsTestGroup(groupID), nil, "0.144.1", "")
	require.Error(t, err)
	require.True(t, IsRetryableCodexModelsManifestError(err))
	require.Equal(t, 1, repo.tempUnschedulable)
	require.Zero(t, repo.disabled)
}

func TestFetchCodexModelsManifestV0162APIKey401DoesNotFailoverOrDisable(t *testing.T) {
	groupID := int64(11)
	account := codexModelsAPIKeyTestAccount(2, groupID, "https://upstream.example/v1")
	upstream := &codexModelsHTTPUpstream{status: http.StatusUnauthorized, body: `{"error":"invalid api key"}`}
	repo := &codexModelsV0162AccountStateRepo{}
	svc := codexModelsAPIKeyTestService(account, upstream)
	svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	_, err := svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, codexModelsTestGroup(groupID), nil, "0.144.1", "")
	require.Error(t, err)
	require.False(t, IsRetryableCodexModelsManifestError(err))
	require.Zero(t, repo.tempUnschedulable)
	require.Zero(t, repo.disabled)
}
