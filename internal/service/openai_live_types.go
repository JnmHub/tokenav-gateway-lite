package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
)

const (
	LiveControllerPending  = "pending"
	LiveControllerObserver = "observer"
	LiveControllerProxy    = "proxy"
	LiveControllerClosed   = "closed"
)

var (
	ErrLiveUnavailable       = errors.New("live is unavailable")
	ErrLiveConcurrencyFull   = errors.New("live concurrency is full")
	ErrLiveCallNotFound      = errors.New("live call not found")
	ErrLiveIdentityMismatch  = errors.New("live call identity mismatch")
	ErrLiveControllerChanged = errors.New("live controller changed")
)

type LiveAttestationUnavailableError struct {
	Reason string
}

func (e *LiveAttestationUnavailableError) Error() string {
	if e == nil || e.Reason == "" {
		return "Live attestation is unavailable"
	}
	return "Live attestation is unavailable: " + e.Reason
}

// LiveCallRequest 是两个下游创建协议归一后的请求。Session 不做结构改写。
type LiveCallRequest struct {
	SDP     string          `json:"sdp"`
	Session json.RawMessage `json:"session"`
}

type LiveCallIdentity struct {
	APIKeyID                   int64
	UserID                     int64
	OrganizationID             int64
	OrganizationMaxConcurrency int
	APIKeyMaxConcurrency       int
	GroupID                    *int64
	// GroupMaxConcurrency 固化创建请求最终分组的每用户并发硬限制。
	// Live 会话会在 HTTP 返回后继续存活，不能只依赖创建阶段的普通请求槽位。
	GroupMaxConcurrency int
	SubscriptionID      *int64
	UserAgent           string
	IPAddress           string
	InboundEndpoint     string
}

type LiveCallRecord struct {
	CallID          string
	CallHash        string
	AccountID       int64
	APIKeyID        int64
	UserID          int64
	OrganizationID  int64
	GroupID         int64
	SubscriptionID  int64
	LeaseID         string
	Model           string
	CreatedAt       time.Time
	ExpiresAt       time.Time
	Controller      string
	ControllerOwner string
	UserAgent       string
	IPAddress       string
	InboundEndpoint string
	// AttestationCiphertext 仅用于让同一会话的 Sideband 复用创建时的证明。
	AttestationCiphertext string
	// GatewayMeta 是创建时已经鉴权的主站请求标量快照。长连接结束可能远晚于
	// HTTP 请求，不能再引用原 context；保存它才能按同一 Key/用户幂等上报。
	GatewayMeta gatewaylite.RequestMeta
	PolicyAudit gatewaylite.EffectivePolicyAuditStamp
}

type LiveCallCreated struct {
	SDP      []byte
	CallID   string
	Location string
	Account  *Account
}

// LiveCallStore 由 GatewayCache 的 Redis 实现可选提供，避免扩大旧缓存接口。
type LiveCallStore interface {
	SaveLiveCall(ctx context.Context, record *LiveCallRecord, ttl time.Duration) error
	GetLiveCall(ctx context.Context, callHash string) (*LiveCallRecord, error)
	ClaimLiveController(ctx context.Context, callHash, controller, owner string) (bool, error)
	ReleaseLiveController(ctx context.Context, callHash, owner string) (bool, error)
	GetLiveController(ctx context.Context, callHash string) (string, error)
	MarkLiveCallClosed(ctx context.Context, callHash string, ttl time.Duration) (bool, error)
}

type LiveConcurrencyCache interface {
	AcquireLiveLease(
		ctx context.Context,
		accountID int64,
		accountMax int,
		userID int64,
		userMax int,
		groupID int64,
		groupMax int,
		apiKeyID int64,
		leaseID string,
		replacingRegularSlots bool,
	) (bool, error)
	RefreshLiveLease(ctx context.Context, accountID, userID, groupID, apiKeyID int64, leaseID string) (bool, error)
	ReleaseLiveLease(ctx context.Context, accountID, userID, groupID, apiKeyID int64, leaseID string) error
}

type OrganizationLiveConcurrencyCache interface {
	AcquireOrganizationLiveLease(
		ctx context.Context,
		accountID int64,
		accountMax int,
		organizationID int64,
		organizationMax int,
		userID int64,
		memberMax int,
		groupID int64,
		groupMax int,
		apiKeyID int64,
		apiKeyMax int,
		leaseID string,
		replacingRegularSlots bool,
	) (bool, error)
	RefreshOrganizationLiveLease(ctx context.Context, accountID, organizationID, userID, groupID, apiKeyID int64, leaseID string) (bool, error)
	ReleaseOrganizationLiveLease(ctx context.Context, accountID, organizationID, userID, groupID, apiKeyID int64, leaseID string) error
}
