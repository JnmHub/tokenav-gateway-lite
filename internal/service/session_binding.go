package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrSessionBindingMismatch = infraerrors.Unauthorized("SESSION_BINDING_MISMATCH", "session network fingerprint changed, please login again")
	ErrSessionRevoked         = infraerrors.Unauthorized("SESSION_REVOKED", "session has been revoked, please login again")
)

// SessionBinding contains the normalized browser fingerprint injected by the HTTP entrypoint.
// 只保留规范 IP 与 UA 哈希，避免把完整 User-Agent 长文本写入 Redis 或审计日志。
type SessionBinding struct {
	IP     string
	UAHash string

	mu       sync.Mutex
	mismatch *SessionBindingMismatch
}

type SessionBindingMismatch struct {
	Mode       string
	SessionID  string
	Expected   string
	Actual     string
	ObservedAt time.Time
}

func NewSessionBinding(ip, userAgent string) *SessionBinding {
	return &SessionBinding{
		IP:     strings.TrimSpace(ip),
		UAHash: HashUserAgent(userAgent),
	}
}

func HashUserAgent(userAgent string) string {
	normalized := strings.TrimSpace(userAgent)
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:16])
}

func (b *SessionBinding) Hash() string {
	if b == nil {
		return ""
	}
	ip := strings.TrimSpace(b.IP)
	uaHash := strings.TrimSpace(b.UAHash)
	if ip == "" && uaHash == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ip + "\n" + uaHash))
	return hex.EncodeToString(sum[:16])
}

func (b *SessionBinding) MarkMismatch(mode, sessionID, expected string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mismatch = &SessionBindingMismatch{
		Mode:       strings.TrimSpace(mode),
		SessionID:  strings.TrimSpace(sessionID),
		Expected:   strings.TrimSpace(expected),
		Actual:     b.Hash(),
		ObservedAt: time.Now().UTC(),
	}
}

func (b *SessionBinding) Mismatch() *SessionBindingMismatch {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mismatch == nil {
		return nil
	}
	copy := *b.mismatch
	return &copy
}

type sessionBindingCtxKey struct{}

func WithSessionBinding(ctx context.Context, binding *SessionBinding) context.Context {
	if binding == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionBindingCtxKey{}, binding)
}

func SessionBindingFromContext(ctx context.Context) *SessionBinding {
	if ctx == nil {
		return nil
	}
	binding, _ := ctx.Value(sessionBindingCtxKey{}).(*SessionBinding)
	return binding
}

func sessionBindingHashFromContext(ctx context.Context) string {
	return SessionBindingFromContext(ctx).Hash()
}

// WebSession is the server-side browser session record keyed by refresh-token family ID.
// It makes current-session revocation immediate for access tokens instead of waiting for JWT expiry.
type WebSession struct {
	SessionID      string     `json:"session_id"`
	UserID         int64      `json:"user_id"`
	ClientIP       string     `json:"client_ip"`
	UAHash         string     `json:"ua_hash"`
	BindingHash    string     `json:"binding_hash"`
	CreatedAt      time.Time  `json:"created_at"`
	LastSeenAt     time.Time  `json:"last_seen_at"`
	LastVerifiedAt *time.Time `json:"last_verified_at,omitempty"`
	ExpiresAt      time.Time  `json:"expires_at"`
}

// WebSessionStore is implemented by the Redis refresh-token repository.
// AuthService discovers it via an optional type assertion so existing test doubles remain compatible.
type WebSessionStore interface {
	UpsertWebSession(ctx context.Context, session *WebSession, ttl time.Duration) error
	GetWebSession(ctx context.Context, sessionID string) (*WebSession, error)
	TouchWebSession(ctx context.Context, sessionID string, seenAt time.Time, ttl time.Duration) error
	MarkWebSessionVerified(ctx context.Context, sessionID string, verifiedAt time.Time) error
	DeleteWebSession(ctx context.Context, sessionID string) error
	DeleteUserWebSessions(ctx context.Context, userID int64) error
}
