package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func TestFetchChatGPTSubscriptionExpiresAt(t *testing.T) {
	const wantExpiresAt = "2026-06-10T02:52:15Z"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/backend-api/subscriptions", r.URL.Path)
		require.Equal(t, "acc_123", r.URL.Query().Get("account_id"))
		require.Equal(t, "Bearer access-token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type":    "plus",
			"active_until": wantExpiresAt,
			"will_renew":   true,
			"id":           "sub_123",
		})
	}))
	defer server.Close()

	oldURL := chatGPTSubscriptionsURL
	chatGPTSubscriptionsURL = server.URL + "/backend-api/subscriptions"
	t.Cleanup(func() { chatGPTSubscriptionsURL = oldURL })

	got := fetchChatGPTSubscriptionExpiresAt(context.Background(), func(proxyURL string) (*req.Client, error) {
		return req.C().SetTimeout(5 * time.Second), nil
	}, "access-token", "", "acc_123")

	require.Equal(t, wantExpiresAt, got)
}

func TestChatGPTAccountInfoBelongsToTokenAccount(t *testing.T) {
	require.False(t, chatGPTAccountInfoBelongsToTokenAccount(
		&OpenAITokenInfo{ChatGPTAccountID: "personal-a"}, &ChatGPTAccountInfo{AccountID: "workspace-b"}))
	require.True(t, chatGPTAccountInfoBelongsToTokenAccount(
		&OpenAITokenInfo{ChatGPTAccountID: "personal-a"}, &ChatGPTAccountInfo{AccountID: "PERSONAL-A"}))
	require.True(t, chatGPTAccountInfoBelongsToTokenAccount(&OpenAITokenInfo{}, &ChatGPTAccountInfo{AccountID: "workspace-b"}))
}

func TestEnrichTokenInfoDoesNotUseWorkspaceExpiryForPersonalPlan(t *testing.T) {
	const personalID = "personal-account-a"
	const workspaceID = "workspace-b"
	const personalExpiry = "2027-03-01T00:00:00Z"
	workspaceExpiry := time.Now().Add(720 * time.Hour).UTC().Format(time.RFC3339)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/accounts/check":
			_ = json.NewEncoder(writer).Encode(map[string]any{"accounts": map[string]any{
				workspaceID: map[string]any{
					"account":     map[string]any{"account_id": workspaceID, "plan_type": "pro", "is_default": true},
					"entitlement": map[string]any{"expires_at": workspaceExpiry},
				},
			}})
		case "/subscriptions":
			require.Equal(t, personalID, request.URL.Query().Get("account_id"))
			_ = json.NewEncoder(writer).Encode(map[string]any{"plan_type": "pro", "active_until": personalExpiry})
		case "/settings":
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	oldAccounts, oldSubscriptions, oldSettings := chatGPTAccountsCheckURL, chatGPTSubscriptionsURL, openAISettingsURL
	chatGPTAccountsCheckURL, chatGPTSubscriptionsURL, openAISettingsURL = server.URL+"/accounts/check", server.URL+"/subscriptions", server.URL+"/settings"
	t.Cleanup(func() {
		chatGPTAccountsCheckURL, chatGPTSubscriptionsURL, openAISettingsURL = oldAccounts, oldSubscriptions, oldSettings
	})

	tokenInfo := &OpenAITokenInfo{
		AccessToken: "access-token", ChatGPTAccountID: personalID, OrganizationID: workspaceID, PlanType: "pro",
	}
	service := &OpenAIOAuthService{privacyClientFactory: func(string) (*req.Client, error) { return req.C().SetTimeout(time.Second), nil }}
	service.enrichTokenInfo(context.Background(), tokenInfo, "")

	require.Equal(t, "pro", tokenInfo.PlanType)
	require.Equal(t, personalExpiry, tokenInfo.SubscriptionExpiresAt)
	require.NotEqual(t, workspaceExpiry, tokenInfo.SubscriptionExpiresAt)
}
