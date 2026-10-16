package service

import (
	"net/http"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// NormalizeGrokBaseURLCredentials validates and canonicalizes the persisted
// Grok base_url. Saving and reading must agree so reopening an account never
// silently changes a confirmed official, regional or third-party endpoint.
func NormalizeGrokBaseURLCredentials(platform, accountType string, credentials map[string]any) error {
	if platform != PlatformGrok || credentials == nil {
		return nil
	}
	raw, exists := credentials["base_url"]
	if !exists || raw == nil {
		return nil
	}
	baseURL, ok := raw.(string)
	if !ok {
		return infraerrors.New(http.StatusBadRequest, "INVALID_GROK_BASE_URL", "base_url must be a string")
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		delete(credentials, "base_url")
		return nil
	}
	if xai.IsSpoofedOfficialBaseURL(baseURL) {
		return infraerrors.New(http.StatusBadRequest, "INVALID_GROK_BASE_URL", "base_url is invalid or rejected by URL security policy")
	}

	var (
		normalized string
		err        error
	)
	if xai.IsOfficialBaseURL(baseURL) {
		normalized, err = xai.ValidateTrustedBaseURL(baseURL)
	} else {
		normalized, err = xai.ValidateBaseURL(baseURL)
	}
	if err != nil {
		return infraerrors.New(http.StatusBadRequest, "INVALID_GROK_BASE_URL", "base_url is invalid or rejected by URL security policy")
	}
	credentials["base_url"] = normalized
	_ = accountType // 保留参数，便于未来按 OAuth/API Key 增加不同保存约束。
	return nil
}
