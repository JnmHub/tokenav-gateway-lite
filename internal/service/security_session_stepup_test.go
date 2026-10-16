//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type sessionAwareRefreshCacheStub struct {
	mu       sync.Mutex
	tokens   map[string]*RefreshTokenData
	sessions map[string]*WebSession
}

func newSessionAwareRefreshCacheStub() *sessionAwareRefreshCacheStub {
	return &sessionAwareRefreshCacheStub{tokens: map[string]*RefreshTokenData{}, sessions: map[string]*WebSession{}}
}

func (s *sessionAwareRefreshCacheStub) StoreRefreshToken(_ context.Context, hash string, data *RefreshTokenData, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *data
	s.tokens[hash] = &copy
	return nil
}
func (s *sessionAwareRefreshCacheStub) GetRefreshToken(_ context.Context, hash string) (*RefreshTokenData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := s.tokens[hash]
	if data == nil {
		return nil, ErrRefreshTokenNotFound
	}
	copy := *data
	return &copy, nil
}
func (s *sessionAwareRefreshCacheStub) DeleteRefreshToken(_ context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, hash)
	return nil
}
func (s *sessionAwareRefreshCacheStub) DeleteUserRefreshTokens(_ context.Context, userID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, data := range s.tokens {
		if data.UserID == userID {
			delete(s.tokens, key)
		}
	}
	for key, session := range s.sessions {
		if session.UserID == userID {
			delete(s.sessions, key)
		}
	}
	return nil
}
func (s *sessionAwareRefreshCacheStub) DeleteTokenFamily(_ context.Context, familyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, data := range s.tokens {
		if data.FamilyID == familyID {
			delete(s.tokens, key)
		}
	}
	delete(s.sessions, familyID)
	return nil
}
func (s *sessionAwareRefreshCacheStub) AddToUserTokenSet(context.Context, int64, string, time.Duration) error {
	return nil
}
func (s *sessionAwareRefreshCacheStub) AddToFamilyTokenSet(context.Context, string, string, time.Duration) error {
	return nil
}
func (s *sessionAwareRefreshCacheStub) GetUserTokenHashes(context.Context, int64) ([]string, error) {
	return nil, nil
}
func (s *sessionAwareRefreshCacheStub) GetFamilyTokenHashes(context.Context, string) ([]string, error) {
	return nil, nil
}
func (s *sessionAwareRefreshCacheStub) IsTokenInFamily(context.Context, string, string) (bool, error) {
	return true, nil
}
func (s *sessionAwareRefreshCacheStub) UpsertWebSession(_ context.Context, session *WebSession, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *session
	s.sessions[session.SessionID] = &copy
	return nil
}
func (s *sessionAwareRefreshCacheStub) GetWebSession(_ context.Context, sessionID string) (*WebSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.sessions[sessionID]
	if value == nil {
		return nil, ErrSessionRevoked
	}
	copy := *value
	return &copy, nil
}
func (s *sessionAwareRefreshCacheStub) TouchWebSession(context.Context, string, time.Time, time.Duration) error {
	return nil
}
func (s *sessionAwareRefreshCacheStub) MarkWebSessionVerified(_ context.Context, sessionID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.sessions[sessionID]
	if value == nil {
		return ErrSessionRevoked
	}
	t := at
	value.LastVerifiedAt = &t
	return nil
}
func (s *sessionAwareRefreshCacheStub) DeleteWebSession(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
	return nil
}
func (s *sessionAwareRefreshCacheStub) DeleteUserWebSessions(_ context.Context, userID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, value := range s.sessions {
		if value.UserID == userID {
			delete(s.sessions, key)
		}
	}
	return nil
}

func newSessionBindingAuthService(mode string, cache RefreshTokenCache, user *User) *AuthService {
	cfg := &config.Config{
		JWT:      config.JWTConfig{Secret: "test-jwt-secret-that-is-long-enough-123", ExpireHour: 1, RefreshTokenExpireDays: 30},
		Security: config.SecurityConfig{SessionBindingMode: mode},
	}
	return NewAuthService(nil, &userRepoStub{user: user}, nil, cache, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
}

func TestRefreshTokenPairEnforceRevokesMismatchedSession(t *testing.T) {
	cache := newSessionAwareRefreshCacheStub()
	user := &User{ID: 7, Email: "admin@example.com", Role: RoleAdmin, Status: StatusActive, PasswordHash: "hash"}
	svc := newSessionBindingAuthService(config.SessionBindingModeEnforce, cache, user)
	loginCtx := WithSessionBinding(context.Background(), NewSessionBinding("1.2.3.4", "Browser/1"))
	pair, err := svc.GenerateTokenPair(loginCtx, user, "")
	require.NoError(t, err)
	claims, err := svc.ValidateToken(pair.AccessToken)
	require.NoError(t, err)
	require.NotEmpty(t, claims.SessionID)

	refreshBinding := NewSessionBinding("5.6.7.8", "Browser/1")
	_, err = svc.RefreshTokenPair(WithSessionBinding(context.Background(), refreshBinding), pair.RefreshToken)
	require.ErrorIs(t, err, ErrSessionBindingMismatch)
	require.NotNil(t, refreshBinding.Mismatch())
	_, exists := cache.sessions[claims.SessionID]
	require.False(t, exists, "enforce mismatch must revoke the server-side session")
}

func TestRefreshTokenPairObserveAllowsButPreservesOriginalBinding(t *testing.T) {
	cache := newSessionAwareRefreshCacheStub()
	user := &User{ID: 8, Email: "admin@example.com", Role: RoleAdmin, Status: StatusActive, PasswordHash: "hash"}
	svc := newSessionBindingAuthService(config.SessionBindingModeObserve, cache, user)
	original := NewSessionBinding("1.2.3.4", "Browser/1")
	pair, err := svc.GenerateTokenPair(WithSessionBinding(context.Background(), original), user, "")
	require.NoError(t, err)

	changed := NewSessionBinding("5.6.7.8", "Browser/2")
	rotated, err := svc.RefreshTokenPair(WithSessionBinding(context.Background(), changed), pair.RefreshToken)
	require.NoError(t, err)
	require.Equal(t, config.SessionBindingModeObserve, changed.Mismatch().Mode)
	claims, err := svc.ValidateToken(rotated.AccessToken)
	require.NoError(t, err)
	require.Equal(t, original.Hash(), claims.BindingHash)
}

type stepUpCacheStub struct {
	mu     sync.Mutex
	grants map[string]*StepUpGrant
}

func (s *stepUpCacheStub) GetSetupSession(context.Context, int64) (*TotpSetupSession, error) {
	return nil, nil
}
func (s *stepUpCacheStub) SetSetupSession(context.Context, int64, *TotpSetupSession, time.Duration) error {
	return nil
}
func (s *stepUpCacheStub) DeleteSetupSession(context.Context, int64) error { return nil }
func (s *stepUpCacheStub) GetLoginSession(context.Context, string) (*TotpLoginSession, error) {
	return nil, nil
}
func (s *stepUpCacheStub) SetLoginSession(context.Context, string, *TotpLoginSession, time.Duration) error {
	return nil
}
func (s *stepUpCacheStub) DeleteLoginSession(context.Context, string) error            { return nil }
func (s *stepUpCacheStub) IncrementVerifyAttempts(context.Context, int64) (int, error) { return 0, nil }
func (s *stepUpCacheStub) GetVerifyAttempts(context.Context, int64) (int, error)       { return 0, nil }
func (s *stepUpCacheStub) ClearVerifyAttempts(context.Context, int64) error            { return nil }
func (s *stepUpCacheStub) StoreStepUpGrant(_ context.Context, hash string, grant *StepUpGrant, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.grants == nil {
		s.grants = map[string]*StepUpGrant{}
	}
	copy := *grant
	s.grants[hash] = &copy
	return nil
}
func (s *stepUpCacheStub) ConsumeStepUpGrant(_ context.Context, hash string) (*StepUpGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	grant := s.grants[hash]
	delete(s.grants, hash)
	if grant == nil {
		return nil, nil
	}
	copy := *grant
	return &copy, nil
}
func (s *stepUpCacheStub) IncrementStepUpAttempts(context.Context, int64, string, time.Duration) (int, error) {
	return 1, nil
}
func (s *stepUpCacheStub) ClearStepUpAttempts(context.Context, int64, string) error { return nil }
func (s *stepUpCacheStub) MarkWebSessionVerified(context.Context, string, time.Time) error {
	return nil
}

func TestConsumeStepUpGrantIsOneTimeAndActionScoped(t *testing.T) {
	cache := &stepUpCacheStub{grants: map[string]*StepUpGrant{}}
	svc := &TotpService{cache: cache}
	user := &User{ID: 11, Role: RoleAdmin, PasswordHash: "hash"}
	token := "su_test-token"
	hash := sha256Hex(token)
	cache.grants[hash] = &StepUpGrant{
		UserID: user.ID, SessionID: "session-1", BindingHash: "binding-1",
		ActionScope: StepUpScopeAccountsExport, Role: RoleAdmin,
		TokenVersion: resolvedTokenVersion(user), ExpiresAt: time.Now().Add(time.Minute),
	}
	expected := StepUpChallenge{SessionID: "session-1", BindingHash: "binding-1", ActionScope: StepUpScopeAccountsExport}
	require.NoError(t, svc.ConsumeStepUpGrant(context.Background(), token, expected, user))
	require.ErrorIs(t, svc.ConsumeStepUpGrant(context.Background(), token, expected, user), ErrStepUpInvalidGrant)
}

func TestConsumeStepUpGrantMismatchStillConsumesToken(t *testing.T) {
	cache := &stepUpCacheStub{grants: map[string]*StepUpGrant{}}
	svc := &TotpService{cache: cache}
	user := &User{ID: 12, Role: RoleAdmin, PasswordHash: "hash"}
	token := "su_mismatch-token"
	cache.grants[sha256Hex(token)] = &StepUpGrant{
		UserID: user.ID, SessionID: "session-1", BindingHash: "binding-1",
		ActionScope: StepUpScopeBackupDelete, Role: RoleAdmin,
		TokenVersion: resolvedTokenVersion(user), ExpiresAt: time.Now().Add(time.Minute),
	}
	wrong := StepUpChallenge{SessionID: "session-1", BindingHash: "binding-2", ActionScope: StepUpScopeBackupDelete}
	require.ErrorIs(t, svc.ConsumeStepUpGrant(context.Background(), token, wrong, user), ErrStepUpInvalidGrant)
	correct := StepUpChallenge{SessionID: "session-1", BindingHash: "binding-1", ActionScope: StepUpScopeBackupDelete}
	require.ErrorIs(t, svc.ConsumeStepUpGrant(context.Background(), token, correct, user), ErrStepUpInvalidGrant)
}

func TestConsumeStepUpGrantRejectsExpiredAndCrossBoundaryUse(t *testing.T) {
	tests := []struct {
		name         string
		mutateGrant  func(*StepUpGrant)
		mutateExpect func(*StepUpChallenge)
		mutateUser   func(*User)
	}{
		{name: "expired", mutateGrant: func(g *StepUpGrant) { g.ExpiresAt = time.Now().Add(-time.Second) }},
		{name: "cross session", mutateExpect: func(c *StepUpChallenge) { c.SessionID = "session-2" }},
		{name: "cross binding", mutateExpect: func(c *StepUpChallenge) { c.BindingHash = "binding-2" }},
		{name: "cross scope", mutateExpect: func(c *StepUpChallenge) { c.ActionScope = StepUpScopeBackupS3Read }},
		{name: "cross user", mutateUser: func(u *User) { u.ID++ }},
		{name: "role changed", mutateUser: func(u *User) { u.Role = RoleUser }},
		{name: "token version changed", mutateUser: func(u *User) { u.PasswordHash = "new-hash" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := &stepUpCacheStub{grants: map[string]*StepUpGrant{}}
			svc := &TotpService{cache: cache}
			user := &User{ID: 21, Role: RoleAdmin, PasswordHash: "hash"}
			token := "su_boundary-" + tt.name
			grant := &StepUpGrant{
				UserID: user.ID, SessionID: "session-1", BindingHash: "binding-1",
				ActionScope: StepUpScopeBackupS3Update, Role: RoleAdmin,
				TokenVersion: resolvedTokenVersion(user), ExpiresAt: time.Now().Add(time.Minute),
			}
			expected := StepUpChallenge{SessionID: "session-1", BindingHash: "binding-1", ActionScope: StepUpScopeBackupS3Update}
			if tt.mutateGrant != nil {
				tt.mutateGrant(grant)
			}
			if tt.mutateExpect != nil {
				tt.mutateExpect(&expected)
			}
			if tt.mutateUser != nil {
				tt.mutateUser(user)
			}
			cache.grants[sha256Hex(token)] = grant
			require.ErrorIs(t, svc.ConsumeStepUpGrant(context.Background(), token, expected, user), ErrStepUpInvalidGrant)
			// 绑定不匹配同样先原子消费，修正上下文后也不能重放。
			require.ErrorIs(t, svc.ConsumeStepUpGrant(context.Background(), token, expected, user), ErrStepUpInvalidGrant)
		})
	}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestSummarizeAuditJSONNeverStoresValues(t *testing.T) {
	summary := SummarizeAuditJSON([]byte(`{"password":"raw-secret","name":"alice","api_key":"sk-secret","nested":{"token":"hidden"}}`))
	require.Equal(t, []string{"api_key", "name", "nested", "password"}, summary["fields"])
	raw := fmt.Sprint(summary)
	require.NotContains(t, raw, "raw-secret")
	require.NotContains(t, raw, "sk-secret")
	require.NotContains(t, raw, "alice")
	require.NotContains(t, raw, "hidden")
}

func TestSanitizeOperationAuditExtraOnlyKeepsShortWhitelistedScalars(t *testing.T) {
	extra := SanitizeOperationAuditExtra(map[string]any{
		"mode":          " mismatch ",
		"count":         2,
		"configured":    true,
		"request_body":  map[string]any{"secret": "raw-secret"},
		"reason":        strings.Repeat("x", maxOperationAuditExtraStringBytes+1),
		"source":        []string{"external-response"},
		"unknown_field": "must-drop",
	})
	require.Equal(t, map[string]any{"mode": "mismatch", "count": 2, "configured": true}, extra)
	raw := fmt.Sprint(extra)
	require.NotContains(t, raw, "raw-secret")
	require.NotContains(t, raw, "external-response")
	require.NotContains(t, raw, "must-drop")
}
