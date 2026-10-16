//go:build unit

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizedSessionBindingModeDefaultsOff(t *testing.T) {
	require.Equal(t, SessionBindingModeOff, (&Config{}).NormalizedSessionBindingMode())
	require.Equal(t, SessionBindingModeObserve, (&Config{Security: SecurityConfig{SessionBindingMode: " OBSERVE "}}).NormalizedSessionBindingMode())
	require.Equal(t, SessionBindingModeEnforce, (&Config{Security: SecurityConfig{SessionBindingMode: "enforce"}}).NormalizedSessionBindingMode())
}

func TestValidateRejectsInvalidSessionSecurityConfig(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	cfg.Security.SessionBindingMode = "invalid"
	require.ErrorContains(t, cfg.Validate(), "session_binding_mode")
	cfg.Security.SessionBindingMode = SessionBindingModeOff
	cfg.Security.StepUpTTLSeconds = 901
	require.ErrorContains(t, cfg.Validate(), "step_up_ttl_seconds")
}
