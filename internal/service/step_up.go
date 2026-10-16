package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrStepUpRequired       = infraerrors.Forbidden("STEP_UP_REQUIRED", "recent two-factor verification is required")
	ErrStepUpNotEnabled     = infraerrors.Forbidden("STEP_UP_TOTP_NOT_ENABLED", "two-factor authentication must be enabled first")
	ErrStepUpInvalidScope   = infraerrors.BadRequest("STEP_UP_INVALID_SCOPE", "invalid step-up action scope")
	ErrStepUpInvalidGrant   = infraerrors.Forbidden("STEP_UP_INVALID_GRANT", "step-up authorization is invalid or expired")
	ErrStepUpSessionMissing = infraerrors.Unauthorized("STEP_UP_SESSION_REQUIRED", "a current browser session is required")
	ErrStepUpUnavailable    = infraerrors.ServiceUnavailable("STEP_UP_UNAVAILABLE", "step-up verification service unavailable")
)

const (
	StepUpScopeAccountsExport   = "accounts.export"
	StepUpScopeProxiesExport    = "proxies.export"
	StepUpScopeBackupCreate     = "backups.create"
	StepUpScopeBackupDownload   = "backups.download"
	StepUpScopeBackupRestore    = "backups.restore"
	StepUpScopeBackupDelete     = "backups.delete"
	StepUpScopeBackupS3Read     = "backups.s3.read"
	StepUpScopeBackupS3Update   = "backups.s3.update"
	StepUpScopeDataS3Read       = "data_management.s3.read"
	StepUpScopeDataS3Update     = "data_management.s3.update"
	StepUpScopeAdminRolePromote = "admin.role.promote"
	StepUpScopeAuditClear       = "audit_logs.clear"
)

var validStepUpScopes = map[string]struct{}{
	StepUpScopeAccountsExport:   {},
	StepUpScopeProxiesExport:    {},
	StepUpScopeBackupCreate:     {},
	StepUpScopeBackupDownload:   {},
	StepUpScopeBackupRestore:    {},
	StepUpScopeBackupDelete:     {},
	StepUpScopeBackupS3Read:     {},
	StepUpScopeBackupS3Update:   {},
	StepUpScopeDataS3Read:       {},
	StepUpScopeDataS3Update:     {},
	StepUpScopeAdminRolePromote: {},
	StepUpScopeAuditClear:       {},
}

func NormalizeStepUpScope(scope string) string {
	return strings.ToLower(strings.TrimSpace(scope))
}

func IsValidStepUpScope(scope string) bool {
	_, ok := validStepUpScopes[NormalizeStepUpScope(scope)]
	return ok
}

// StepUpSourceHash keeps grant issuance and consumption on one IP normalization/hash formula.
func StepUpSourceHash(clientIP string) string {
	return NewSessionBinding(clientIP, "").Hash()
}

type StepUpGrant struct {
	UserID       int64     `json:"user_id"`
	SessionID    string    `json:"session_id"`
	BindingHash  string    `json:"binding_hash"`
	ActionScope  string    `json:"action_scope"`
	Role         string    `json:"role"`
	TokenVersion int64     `json:"token_version"`
	VerifiedAt   time.Time `json:"verified_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type StepUpGrantStore interface {
	StoreStepUpGrant(ctx context.Context, tokenHash string, grant *StepUpGrant, ttl time.Duration) error
	ConsumeStepUpGrant(ctx context.Context, tokenHash string) (*StepUpGrant, error)
	IncrementStepUpAttempts(ctx context.Context, userID int64, sourceHash string, ttl time.Duration) (int, error)
	ClearStepUpAttempts(ctx context.Context, userID int64, sourceHash string) error
	MarkWebSessionVerified(ctx context.Context, sessionID string, verifiedAt time.Time) error
}

type StepUpChallenge struct {
	SessionID   string
	BindingHash string
	ActionScope string
	SourceHash  string
}

type StepUpVerificationResult struct {
	Token     string
	ExpiresIn int64
}

func (s *TotpService) stepUpTTL() time.Duration {
	seconds := 300
	if s != nil && s.cfg != nil && s.cfg.Security.StepUpTTLSeconds > 0 {
		seconds = s.cfg.Security.StepUpTTLSeconds
	}
	if seconds > 900 {
		seconds = 900
	}
	return time.Duration(seconds) * time.Second
}

// VerifyStepUp creates a one-time, action-scoped authorization token.
// The opaque token is returned once; Redis stores only its SHA-256 hash.
func (s *TotpService) VerifyStepUp(ctx context.Context, userID int64, challenge StepUpChallenge, code string) (*StepUpVerificationResult, error) {
	if s == nil || s.cache == nil {
		return nil, ErrStepUpUnavailable
	}
	scope := NormalizeStepUpScope(challenge.ActionScope)
	if !IsValidStepUpScope(scope) {
		return nil, ErrStepUpInvalidScope
	}
	if strings.TrimSpace(challenge.SessionID) == "" || strings.TrimSpace(challenge.BindingHash) == "" {
		return nil, ErrStepUpSessionMissing
	}
	store, ok := s.cache.(StepUpGrantStore)
	if !ok || store == nil {
		return nil, ErrStepUpUnavailable
	}

	sourceHash := strings.TrimSpace(challenge.SourceHash)
	if sourceHash == "" {
		sourceHash = "unknown"
	}
	attempts, err := store.IncrementStepUpAttempts(ctx, userID, sourceHash, 15*time.Minute)
	if err != nil {
		return nil, ErrStepUpUnavailable
	}
	if attempts > maxTotpAttempts {
		return nil, ErrTotpTooManyAttempts
	}

	if err := s.VerifyCode(ctx, userID, strings.TrimSpace(code)); err != nil {
		// 对挑战接口统一错误，避免泄露 TOTP 是否已启用或验证码时间窗口。
		if errors.Is(err, ErrTotpTooManyAttempts) {
			return nil, err
		}
		return nil, infraerrors.Forbidden("STEP_UP_VERIFICATION_FAILED", "step-up verification failed")
	}
	_ = store.ClearStepUpAttempts(ctx, userID, sourceHash)

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, ErrStepUpUnavailable
	}
	if !user.TotpEnabled {
		return nil, ErrStepUpNotEnabled
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate step-up token: %w", err)
	}
	token := "su_" + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])
	now := time.Now().UTC()
	ttl := s.stepUpTTL()
	grant := &StepUpGrant{
		UserID:       userID,
		SessionID:    strings.TrimSpace(challenge.SessionID),
		BindingHash:  strings.TrimSpace(challenge.BindingHash),
		ActionScope:  scope,
		Role:         user.Role,
		TokenVersion: resolvedTokenVersion(user),
		VerifiedAt:   now,
		ExpiresAt:    now.Add(ttl),
	}
	if err := store.MarkWebSessionVerified(ctx, grant.SessionID, now); err != nil {
		return nil, ErrStepUpUnavailable
	}
	if err := store.StoreStepUpGrant(ctx, tokenHash, grant, ttl); err != nil {
		return nil, ErrStepUpUnavailable
	}
	return &StepUpVerificationResult{Token: token, ExpiresIn: int64(ttl.Seconds())}, nil
}

// ConsumeStepUpGrant atomically consumes the token before checking its bindings.
// A mismatched or concurrent replay therefore cannot be tried again with altered context.
func (s *TotpService) ConsumeStepUpGrant(ctx context.Context, token string, expected StepUpChallenge, user *User) error {
	if s == nil || s.cache == nil || user == nil {
		return ErrStepUpUnavailable
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return ErrStepUpRequired
	}
	store, ok := s.cache.(StepUpGrantStore)
	if !ok || store == nil {
		return ErrStepUpUnavailable
	}
	sum := sha256.Sum256([]byte(token))
	grant, err := store.ConsumeStepUpGrant(ctx, hex.EncodeToString(sum[:]))
	if err != nil || grant == nil {
		return ErrStepUpInvalidGrant
	}
	now := time.Now().UTC()
	if now.After(grant.ExpiresAt) ||
		grant.UserID != user.ID ||
		grant.SessionID != strings.TrimSpace(expected.SessionID) ||
		grant.BindingHash != strings.TrimSpace(expected.BindingHash) ||
		grant.ActionScope != NormalizeStepUpScope(expected.ActionScope) ||
		grant.Role != user.Role ||
		grant.TokenVersion != resolvedTokenVersion(user) {
		return ErrStepUpInvalidGrant
	}
	return nil
}
