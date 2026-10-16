package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type grokVoiceMethodRecorder struct {
	lastReq *http.Request
	resp    *http.Response
}

func (r *grokVoiceMethodRecorder) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	r.lastReq = req
	return r.resp, nil
}

func (r *grokVoiceMethodRecorder) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return r.Do(req, proxyURL, accountID, concurrency)
}

type grokCustomVoiceMemoryStore struct {
	mu       sync.Mutex
	bindings map[string]*GrokCustomVoiceBinding
}

func (s *grokCustomVoiceMemoryStore) Ready(context.Context) error { return nil }

func (s *grokCustomVoiceMemoryStore) Save(_ context.Context, binding *GrokCustomVoiceBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindings == nil {
		s.bindings = make(map[string]*GrokCustomVoiceBinding)
	}
	copyBinding := *binding
	s.bindings[binding.VoiceID] = &copyBinding
	return nil
}

func (s *grokCustomVoiceMemoryStore) Get(_ context.Context, voiceID string) (*GrokCustomVoiceBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.bindings[voiceID]
	if !ok {
		return nil, ErrGrokCustomVoiceNotFound
	}
	copyBinding := *binding
	return &copyBinding, nil
}

func (s *grokCustomVoiceMemoryStore) ListForOwner(_ context.Context, userID, apiKeyID int64, gatewayKeyID string) ([]*GrokCustomVoiceBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*GrokCustomVoiceBinding, 0)
	for _, binding := range s.bindings {
		if binding.BelongsTo(userID, apiKeyID, gatewayKeyID) {
			copyBinding := *binding
			result = append(result, &copyBinding)
		}
	}
	return result, nil
}

func (s *grokCustomVoiceMemoryStore) Delete(_ context.Context, voiceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.bindings[voiceID]; !ok {
		return ErrGrokCustomVoiceNotFound
	}
	delete(s.bindings, voiceID)
	return nil
}

func TestGrokCustomVoiceBindingBelongsToExactAPIKey(t *testing.T) {
	local := &GrokCustomVoiceBinding{VoiceID: "voice123", UserID: 7, APIKeyID: 11}
	require.True(t, local.BelongsTo(7, 11, ""))
	require.False(t, local.BelongsTo(7, 12, ""))
	require.False(t, local.BelongsTo(8, 11, ""))

	remote := &GrokCustomVoiceBinding{VoiceID: "voice456", UserID: 7, GatewayKeyID: "key-public-id"}
	require.True(t, remote.BelongsTo(7, 0, "key-public-id"))
	require.False(t, remote.BelongsTo(7, 0, "other-key"))
	require.False(t, remote.BelongsTo(8, 0, "key-public-id"))
}

func TestOpenAIGatewayServiceCustomVoiceStoreLifecycle(t *testing.T) {
	store := &grokCustomVoiceMemoryStore{}
	svc := &OpenAIGatewayService{}
	svc.SetGrokCustomVoiceStore(store)
	binding := &GrokCustomVoiceBinding{
		VoiceID: "voice123", UserID: 7, APIKeyID: 11, AccountID: 23,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}

	require.NoError(t, svc.SaveGrokCustomVoiceBinding(context.Background(), binding))
	loaded, err := svc.GetGrokCustomVoiceBinding(context.Background(), binding.VoiceID)
	require.NoError(t, err)
	require.Equal(t, int64(23), loaded.AccountID)

	owned, err := svc.ListGrokCustomVoiceBindings(context.Background(), 7, 11, "")
	require.NoError(t, err)
	require.Len(t, owned, 1)
	foreign, err := svc.ListGrokCustomVoiceBindings(context.Background(), 7, 12, "")
	require.NoError(t, err)
	require.Empty(t, foreign)

	require.NoError(t, svc.DeleteGrokCustomVoiceBinding(context.Background(), binding.VoiceID))
	_, err = svc.GetGrokCustomVoiceBinding(context.Background(), binding.VoiceID)
	require.ErrorIs(t, err, ErrGrokCustomVoiceNotFound)
}

func TestFetchGrokCustomVoiceFromUpstreamUsesGET(t *testing.T) {
	upstream := &grokVoiceMethodRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"voice_id":"voice123"}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID: 23, Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "test-key", "base_url": "https://api.x.ai/v1"},
	}

	result, err := svc.FetchGrokCustomVoiceFromUpstream(context.Background(), account, "voice123", "application/json")

	require.NoError(t, err)
	require.Equal(t, http.MethodGet, upstream.lastReq.Method)
	require.Equal(t, "https://api.x.ai/v1/custom-voices/voice123", upstream.lastReq.URL.String())
	require.JSONEq(t, `{"voice_id":"voice123"}`, string(result.ResponseBody))
}

func TestDeleteGrokCustomVoiceFromUpstreamUsesDELETE(t *testing.T) {
	upstream := &grokVoiceMethodRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"deleted":true}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID: 23, Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "test-key", "base_url": "https://api.x.ai/v1"},
	}

	_, err := svc.DeleteGrokCustomVoiceFromUpstream(context.Background(), account, "voice123")

	require.NoError(t, err)
	require.Equal(t, http.MethodDelete, upstream.lastReq.Method)
	require.Equal(t, "https://api.x.ai/v1/custom-voices/voice123", upstream.lastReq.URL.String())
}
