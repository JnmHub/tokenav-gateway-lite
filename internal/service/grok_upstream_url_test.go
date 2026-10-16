//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func TestGrokOAuthOfficialAndRegionalEndpointsIgnoreOperatorAllowlist(t *testing.T) {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = true
	cfg.Security.URLAllowlist.UpstreamHosts = []string{"other.example.test"}

	for _, baseURL := range []string{xai.DefaultCLIBaseURL, xai.DefaultBaseURL, xai.DefaultUSEast1BaseURL, xai.DefaultUSWest2BaseURL, xai.DefaultEUWest1BaseURL} {
		account := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"base_url": baseURL}}
		target, err := buildGrokResponsesURL(account, cfg)
		require.NoError(t, err)
		require.Equal(t, baseURL+"/responses", target)
	}
}

func TestGrokCustomRelayFollowsOperatorURLPolicyAcrossPaths(t *testing.T) {
	account := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"base_url": "https://relay.example.test/tenant/xai/v1"}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = true
	cfg.Security.URLAllowlist.UpstreamHosts = []string{"relay.example.test"}

	responsesURL, err := buildGrokResponsesURL(account, cfg)
	require.NoError(t, err)
	require.Equal(t, "https://relay.example.test/tenant/xai/v1/responses", responsesURL)
	modelsURL, err := buildGrokModelsURL(account, cfg)
	require.NoError(t, err)
	require.Equal(t, "https://relay.example.test/tenant/xai/v1/models", modelsURL)
	mediaURL, err := buildGrokMediaURL(account, cfg, GrokMediaEndpointImagesGenerations, "")
	require.NoError(t, err)
	require.Equal(t, "https://relay.example.test/tenant/xai/v1/images/generations", mediaURL)

	cfg.Security.URLAllowlist.UpstreamHosts = []string{"other.example.test"}
	_, err = buildGrokResponsesURL(account, cfg)
	require.EqualError(t, err, "invalid base url: base URL rejected by URL security policy")
}

func TestGrokCLIMediaUsesOfficialAPIWhileExplicitEndpointsStaySelected(t *testing.T) {
	cli := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"base_url": xai.DefaultCLIBaseURL}}
	target, err := buildGrokMediaURL(cli, nil, GrokMediaEndpointImagesGenerations, "")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/images/generations", target)

	regional := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"base_url": xai.DefaultUSWest2BaseURL}}
	target, err = buildGrokMediaURL(regional, nil, GrokMediaEndpointImagesGenerations, "")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultUSWest2BaseURL+"/images/generations", target)
}
