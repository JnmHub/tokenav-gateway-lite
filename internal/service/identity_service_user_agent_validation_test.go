package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fingerprintValidationCache struct {
	fingerprint *Fingerprint
	setCalls    int
}

func (cache *fingerprintValidationCache) GetFingerprint(context.Context, int64) (*Fingerprint, error) {
	if cache.fingerprint == nil {
		return nil, nil
	}
	clone := *cache.fingerprint
	return &clone, nil
}
func (cache *fingerprintValidationCache) SetFingerprint(_ context.Context, _ int64, fingerprint *Fingerprint) error {
	cache.setCalls++
	clone := *fingerprint
	cache.fingerprint = &clone
	return nil
}
func (*fingerprintValidationCache) GetMaskedSessionID(context.Context, int64) (string, error) {
	return "", nil
}
func (*fingerprintValidationCache) SetMaskedSessionID(context.Context, int64, string) error {
	return nil
}

func fingerprintHeaders(userAgent string) http.Header {
	headers := http.Header{}
	if userAgent != "" {
		headers.Set("User-Agent", userAgent)
	}
	return headers
}

func TestIsAcceptableFingerprintUserAgent(t *testing.T) {
	for _, test := range []struct {
		name, userAgent string
		want            bool
	}{
		{"official", "claude-cli/2.1.161 (external, cli)", true},
		{"other product", "some-sdk/1.2.3 (node)", true},
		{"local suffix", "claude-cli/999.0.0-local (undefined, cli)", false},
		{"sentinel", "claude-cli/999.0.0 (external, cli)", false},
		{"browser", "Mozilla/5.0 (Windows NT 10.0)", false},
		{"too long", "claude-cli/2.1.161 (" + strings.Repeat("a", 300) + ")", false},
	} {
		t.Run(test.name, func(t *testing.T) { require.Equal(t, test.want, isAcceptableFingerprintUserAgent(test.userAgent)) })
	}
}

func TestFingerprintRejectsMalformedUserAgentOnCreate(t *testing.T) {
	cache := &fingerprintValidationCache{}
	fingerprint, err := NewIdentityService(cache).GetOrCreateFingerprint(context.Background(), 1, fingerprintHeaders("claude-cli/999.0.0-local"))
	require.NoError(t, err)
	require.Equal(t, defaultFingerprint.UserAgent, fingerprint.UserAgent)
	require.Equal(t, 1, cache.setCalls)
}

func TestFingerprintHealsPoisonedCache(t *testing.T) {
	cache := &fingerprintValidationCache{fingerprint: &Fingerprint{
		UserAgent: "claude-cli/999.0.0-local", ClientID: "existing-client", UpdatedAt: time.Now().Unix(),
	}}
	fingerprint, err := NewIdentityService(cache).GetOrCreateFingerprint(context.Background(), 1, fingerprintHeaders("claude-cli/2.1.160 (external, cli)"))
	require.NoError(t, err)
	require.Equal(t, "claude-cli/2.1.160 (external, cli)", fingerprint.UserAgent)
	require.Equal(t, "existing-client", fingerprint.ClientID)
	require.Equal(t, 1, cache.setCalls)
}

func TestFingerprintRejectsSentinelUpgrade(t *testing.T) {
	cache := &fingerprintValidationCache{fingerprint: &Fingerprint{
		UserAgent: "claude-cli/2.1.160 (external, cli)", ClientID: "existing-client", UpdatedAt: time.Now().Unix(),
	}}
	fingerprint, err := NewIdentityService(cache).GetOrCreateFingerprint(context.Background(), 1, fingerprintHeaders("claude-cli/999.0.0 (external, cli)"))
	require.NoError(t, err)
	require.Equal(t, "claude-cli/2.1.160 (external, cli)", fingerprint.UserAgent)
	require.Zero(t, cache.setCalls)
}
