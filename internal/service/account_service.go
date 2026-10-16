package service

import (
	"context"
	"fmt"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

var (
	ErrAccountNotFound      = infraerrors.NotFound("ACCOUNT_NOT_FOUND", "account not found")
	ErrAccountNilInput      = infraerrors.BadRequest("ACCOUNT_NIL_INPUT", "account input cannot be nil")
	ErrAccountNotInFallback = infraerrors.BadRequest("ACCOUNT_NOT_IN_FALLBACK", "account is not in proxy fallback state")
)

const AccountListGroupUngrouped int64 = -1
const AccountPrivacyModeUnsetFilter = "__unset__"

// OAuthRefreshPageOptions 描述一次有界、基于主键游标的 OAuth 刷新候选扫描。
// Platforms 由 TokenRefreshService 的刷新器注册表传入，避免仓储层平台列表与实际刷新能力漂移。
type OAuthRefreshPageOptions struct {
	Platforms            []string
	AfterID              int64
	Limit                int
	ActiveOnly           bool
	IncludeSetupToken    bool
	RequireRefreshToken  bool
	ExcludeRetryCooldown bool
}

// OAuthRefreshCandidatePage 保留原始 ID 页的游标元数据。
// 即使账号在 ID 查询与完整对象加载之间被并发删除，调用方仍能安全推进游标。
type OAuthRefreshCandidatePage struct {
	Accounts    []Account
	NextAfterID int64
	HasMore     bool
}

// OAuthRefreshCandidatePager 与 AccountRepository 分离，避免让大量业务测试桩为后台分页扫描提供无意义实现。
type OAuthRefreshCandidatePager interface {
	ListOAuthRefreshCandidatePage(ctx context.Context, options OAuthRefreshPageOptions) (*OAuthRefreshCandidatePage, error)
}

type AccountRepository interface {
	// [v0.2.5 新增] 模型可用性探测候选账号查询
	ListModelAvailabilityCandidates(ctx context.Context, groupID *int64, platforms []string, includeGrouped bool) ([]Account, error)
	Create(ctx context.Context, account *Account) error
	GetByID(ctx context.Context, id int64) (*Account, error)
	// GetByIDs fetches accounts by IDs in a single query.
	// It should return all accounts found (missing IDs are ignored).
	GetByIDs(ctx context.Context, ids []int64) ([]*Account, error)
	// ExistsByID 检查账号是否存在，仅返回布尔值，用于删除前的轻量级存在性检查
	ExistsByID(ctx context.Context, id int64) (bool, error)
	// GetByCRSAccountID finds an account previously synced from CRS.
	// Returns (nil, nil) if not found.
	GetByCRSAccountID(ctx context.Context, crsAccountID string) (*Account, error)
	// FindByExtraField 根据 extra 字段中的键值对查找账号
	FindByExtraField(ctx context.Context, key string, value any) ([]Account, error)
	// ListCRSAccountIDs returns a map of crs_account_id -> local account ID
	// for all accounts that have been synced from CRS.
	ListCRSAccountIDs(ctx context.Context) (map[string]int64, error)
	Update(ctx context.Context, account *Account) error
	Delete(ctx context.Context, id int64) error

	List(ctx context.Context, params pagination.PaginationParams) ([]Account, *pagination.PaginationResult, error)
	ListWithFilters(ctx context.Context, params pagination.PaginationParams, platform, accountType, status, search string, groupID int64, privacyMode, wsProbe string) ([]Account, *pagination.PaginationResult, error)
	ListByGroup(ctx context.Context, groupID int64) ([]Account, error)
	ListActive(ctx context.Context) ([]Account, error)
	ListOAuthRefreshCandidates(ctx context.Context) ([]Account, error)
	ListByPlatform(ctx context.Context, platform string) ([]Account, error)

	UpdateLastUsed(ctx context.Context, id int64) error
	BatchUpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error
	SetError(ctx context.Context, id int64, errorMsg string) error
	ClearError(ctx context.Context, id int64) error
	SetSchedulable(ctx context.Context, id int64, schedulable bool) error
	AutoPauseExpiredAccounts(ctx context.Context, now time.Time) (int64, error)
	BindGroups(ctx context.Context, accountID int64, groupIDs []int64) error

	ListSchedulable(ctx context.Context) ([]Account, error)
	ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]Account, error)
	ListSchedulableByPlatform(ctx context.Context, platform string) ([]Account, error)
	ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error)
	ListSchedulableByPlatforms(ctx context.Context, platforms []string) ([]Account, error)
	ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]Account, error)
	ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]Account, error)
	ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]Account, error)

	SetRateLimited(ctx context.Context, id int64, resetAt time.Time) error
	SetModelRateLimit(ctx context.Context, id int64, scope string, resetAt time.Time, reason ...string) error
	SetOverloaded(ctx context.Context, id int64, until time.Time) error
	SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error
	ClearTempUnschedulable(ctx context.Context, id int64) error
	ClearRateLimit(ctx context.Context, id int64) error
	ClearAntigravityQuotaScopes(ctx context.Context, id int64) error
	ClearModelRateLimits(ctx context.Context, id int64) error
	UpdateSessionWindow(ctx context.Context, id int64, start, end *time.Time, status string) error
	// UpdateSessionWindowEnd 仅更新 5h 窗口的结束时间，不动 start / status。
	// 用于 active poll 拿到新 ResetsAt 后回写，避免覆盖请求路径上记录的 status。
	UpdateSessionWindowEnd(ctx context.Context, id int64, end time.Time) error
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error
	BulkUpdate(ctx context.Context, ids []int64, updates AccountBulkUpdate) (int64, error)
	// IncrementQuotaUsed 原子递增 API Key 账号的配额用量（总/日/周）
	IncrementQuotaUsed(ctx context.Context, id int64, amount float64) error
	// ResetQuotaUsed 重置 API Key 账号所有维度的配额用量为 0
	ResetQuotaUsed(ctx context.Context, id int64) error
	// RevertProxyFallback 将账号的 proxy_id 切回 proxy_fallback_origin_id，并清空 origin 字段。
	// 仅当 proxy_fallback_origin_id IS NOT NULL 时更新，否则视为账号不存在（返回 ErrAccountNotFound）。
	RevertProxyFallback(ctx context.Context, accountID int64) error
}

// AccountDuplicateRepository 提供账号与精确分组优先级的原子写入能力。
// 复制账号不能先建账号再绑分组，否则中途失败会留下可被幂等恢复误认的半成品。
type AccountDuplicateRepository interface {
	CreateWithAccountGroups(ctx context.Context, account *Account, groups []AccountGroup) error
}

// AccountGroupModelMappingRepository is intentionally optional so existing
// account repository test doubles and integrations remain source-compatible.
// Callers that expose the per-group mapping editor can type-assert this
// capability and atomically replace all mappings for the account's bindings.
type AccountGroupModelMappingRepository interface {
	ReplaceAccountGroupModelMappings(ctx context.Context, accountID int64, mappings map[int64]map[string]string) error
}

// CodexTailBurstSessionMarker identifies one active tail-burst session.
//
// The marker is deliberately separate from AccountRepository so older
// repository test doubles and integrations remain source-compatible.  The
// concrete repository uses it as a compare-and-set guard: a stale collector
// must never clear a newer session's active flag or overwrite its summary.
type CodexTailBurstSessionMarker struct {
	// SessionID is a per-entry random identity. It prevents a delayed
	// collector from matching a newer session that happens to reuse the same
	// second-level timestamp and other accounting fields.
	SessionID    string
	Since        string
	Window       string
	Reason       string
	EntryPercent *float64
	EntryResetAt *string
}

// AccountCodexTailBurstCASRepository is an optional capability implemented by
// the SQL account repository.  Entry and settlement are both atomic at the row
// level; callers fall back to UpdateExtra when an older repository does not
// expose this capability.
type AccountCodexTailBurstCASRepository interface {
	MarkCodexTailBurstStartedIfInactive(ctx context.Context, accountID int64, updates map[string]any) (applied bool, err error)
	UpdateCodexTailBurstDeltaIfMarkerMatches(ctx context.Context, accountID int64, marker CodexTailBurstSessionMarker, updates map[string]any) (applied bool, err error)
	SettleCodexTailBurstIfMarkerMatches(ctx context.Context, accountID int64, marker CodexTailBurstSessionMarker, updates map[string]any) (applied bool, err error)
}

// AccountCodexTailBurstSessionRepository is an optional read capability for
// the background tail-burst collector. Unlike the normal platform listing,
// this query includes inactive accounts so a session that was opened before an
// account was disabled can still be settled and removed from the runtime
// snapshot. Keeping it optional preserves compatibility with lightweight test
// repositories and older integrations.
type AccountCodexTailBurstSessionRepository interface {
	ListByPlatformForCodexTailBurst(ctx context.Context, platform string) ([]Account, error)
}

// AccountShadowLifecycleRepository 提供母账号与 Spark 影子的原子删除能力。
// 母子账号不能由 service 层逐条删除：中途失败会留下半删除的账号树，
// 让共享凭据解析、调度缓存和后台展示互相矛盾。
type AccountShadowLifecycleRepository interface {
	DeleteWithShadows(ctx context.Context, accountID int64) error
}

// AccountBulkUpdate describes the fields that can be updated in a bulk operation.
// Nil pointers mean "do not change".
type AccountBulkUpdate struct {
	Name           *string
	ProxyID        *int64
	Concurrency    *int
	Priority       *int
	RateMultiplier *float64
	LoadFactor     *int
	Status         *string
	Schedulable    *bool
	Credentials    map[string]any
	Extra          map[string]any
}

// CreateAccountRequest 创建账号请求
type CreateAccountRequest struct {
	Name               string         `json:"name"`
	Notes              *string        `json:"notes"`
	Platform           string         `json:"platform"`
	Type               string         `json:"type"`
	Credentials        map[string]any `json:"credentials"`
	Extra              map[string]any `json:"extra"`
	ProxyID            *int64         `json:"proxy_id"`
	Concurrency        int            `json:"concurrency"`
	Priority           int            `json:"priority"`
	GroupIDs           []int64        `json:"group_ids"`
	ExpiresAt          *time.Time     `json:"expires_at"`
	AutoPauseOnExpired *bool          `json:"auto_pause_on_expired"`
	Status             *string        `json:"status"`
	Schedulable        *bool          `json:"schedulable"`
}

// UpdateAccountRequest 更新账号请求
type UpdateAccountRequest struct {
	Name               *string         `json:"name"`
	Notes              *string         `json:"notes"`
	Credentials        *map[string]any `json:"credentials"`
	Extra              *map[string]any `json:"extra"`
	ProxyID            *int64          `json:"proxy_id"`
	Concurrency        *int            `json:"concurrency"`
	Priority           *int            `json:"priority"`
	Status             *string         `json:"status"`
	GroupIDs           *[]int64        `json:"group_ids"`
	ExpiresAt          *time.Time      `json:"expires_at"`
	AutoPauseOnExpired *bool           `json:"auto_pause_on_expired"`
}

// AccountService 账号管理服务
type AccountService struct {
	accountRepo AccountRepository
	groupRepo   GroupRepository
}

type groupExistenceBatchChecker interface {
	ExistsByIDs(ctx context.Context, ids []int64) (map[int64]bool, error)
}

// NewAccountService 创建账号服务实例
func NewAccountService(accountRepo AccountRepository, groupRepo GroupRepository) *AccountService {
	return &AccountService{
		accountRepo: accountRepo,
		groupRepo:   groupRepo,
	}
}

func normalizeAccountSchedulableForStatus(status string, schedulable bool) bool {
	if status != StatusActive {
		return false
	}
	return schedulable
}

// Create 创建账号
func (s *AccountService) Create(ctx context.Context, req CreateAccountRequest) (*Account, error) {
	accountExtra, err := normalizeOpenAILongContextBillingExtra(req.Platform, req.Extra)
	if err != nil {
		return nil, err
	}
	accountExtra, err = normalizeAccountResponseModelPolicyExtra(accountExtra)
	if err != nil {
		return nil, err
	}
	accountExtra, err = normalizeCodexFingerprintModeExtra(req.Platform, req.Type, accountExtra, true)
	if err != nil {
		return nil, err
	}
	accountExtra = prepareCodexFingerprintExtraForCreate(req.Platform, req.Type, accountExtra)

	// 验证分组是否存在（如果指定了分组）
	if len(req.GroupIDs) > 0 {
		if err := s.validateGroupIDsExist(ctx, req.GroupIDs); err != nil {
			return nil, err
		}
	}

	// 创建账号
	status := StatusActive
	if req.Status != nil && *req.Status != "" {
		status = *req.Status
	}
	schedulable := true
	if req.Schedulable != nil {
		schedulable = *req.Schedulable
	}
	schedulable = normalizeAccountSchedulableForStatus(status, schedulable)

	account := &Account{
		Name:        req.Name,
		Notes:       normalizeAccountNotes(req.Notes),
		Platform:    req.Platform,
		Type:        req.Type,
		Credentials: req.Credentials,
		Extra:       accountExtra,
		ProxyID:     req.ProxyID,
		Concurrency: req.Concurrency,
		Priority:    req.Priority,
		Status:      status,
		Schedulable: schedulable,
		ExpiresAt:   req.ExpiresAt,
	}
	if req.AutoPauseOnExpired != nil {
		account.AutoPauseOnExpired = *req.AutoPauseOnExpired
	} else {
		account.AutoPauseOnExpired = true
	}

	if err := s.accountRepo.Create(ctx, account); err != nil {
		return nil, fmt.Errorf("create account: %w", err)
	}

	// require_oauth_only 检查：apikey 类型账号不可加入限制分组
	if account.Type == AccountTypeAPIKey && len(req.GroupIDs) > 0 {
		for _, gid := range req.GroupIDs {
			g, err := s.groupRepo.GetByID(ctx, gid)
			if err != nil {
				return nil, err
			}
			if g.RequireOAuthOnly && (g.Platform == PlatformOpenAI || g.Platform == PlatformAntigravity || g.Platform == PlatformAnthropic || g.Platform == PlatformGemini || g.Platform == PlatformGrok) {
				return nil, fmt.Errorf("分组 [%s] 仅允许 OAuth 账号，apikey 类型账号无法加入", g.Name)
			}
		}
	}

	// 绑定分组
	if len(req.GroupIDs) > 0 {
		if err := s.accountRepo.BindGroups(ctx, account.ID, req.GroupIDs); err != nil {
			return nil, fmt.Errorf("bind groups: %w", err)
		}
	}

	return account, nil
}

// GetByID 根据ID获取账号
func (s *AccountService) GetByID(ctx context.Context, id int64) (*Account, error) {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get account: %w", err)
	}
	return account, nil
}

// List 获取账号列表
func (s *AccountService) List(ctx context.Context, params pagination.PaginationParams) ([]Account, *pagination.PaginationResult, error) {
	accounts, pagination, err := s.accountRepo.List(ctx, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list accounts: %w", err)
	}
	return accounts, pagination, nil
}

// ListByPlatform 根据平台获取账号列表
func (s *AccountService) ListByPlatform(ctx context.Context, platform string) ([]Account, error) {
	accounts, err := s.accountRepo.ListByPlatform(ctx, platform)
	if err != nil {
		return nil, fmt.Errorf("list accounts by platform: %w", err)
	}
	return accounts, nil
}

// ListByGroup 根据分组获取账号列表
func (s *AccountService) ListByGroup(ctx context.Context, groupID int64) ([]Account, error) {
	accounts, err := s.accountRepo.ListByGroup(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("list accounts by group: %w", err)
	}
	return accounts, nil
}

// Update 更新账号
func (s *AccountService) Update(ctx context.Context, id int64, req UpdateAccountRequest) (*Account, error) {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get account: %w", err)
	}

	// 更新字段
	if req.Name != nil {
		account.Name = *req.Name
	}
	if req.Notes != nil {
		account.Notes = normalizeAccountNotes(req.Notes)
	}

	if req.Credentials != nil {
		account.Credentials = *req.Credentials
	}

	if req.Extra != nil {
		normalizedExtra, normalizeErr := normalizeAccountResponseModelPolicyExtra(*req.Extra)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		normalizedExtra, normalizeErr = normalizeCodexFingerprintModeExtra(account.Platform, account.Type, normalizedExtra, false)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		normalizedExtra = prepareCodexFingerprintExtraForUpdate(account, normalizedExtra)
		account.Extra = normalizedExtra
	}

	if req.ProxyID != nil {
		account.ProxyID = req.ProxyID
	}

	if req.Concurrency != nil {
		account.Concurrency = *req.Concurrency
	}

	if req.Priority != nil {
		account.Priority = *req.Priority
	}

	if req.Status != nil {
		account.Status = *req.Status
	}
	account.Schedulable = normalizeAccountSchedulableForStatus(account.Status, account.Schedulable)
	if req.ExpiresAt != nil {
		account.ExpiresAt = req.ExpiresAt
	}
	if req.AutoPauseOnExpired != nil {
		account.AutoPauseOnExpired = *req.AutoPauseOnExpired
	}

	// 先验证分组是否存在（在任何写操作之前）
	if req.GroupIDs != nil {
		if err := s.validateGroupIDsExist(ctx, *req.GroupIDs); err != nil {
			return nil, err
		}
	}

	// 执行更新
	if err := s.accountRepo.Update(ctx, account); err != nil {
		return nil, fmt.Errorf("update account: %w", err)
	}

	// require_oauth_only 检查
	if account.Type == AccountTypeAPIKey && req.GroupIDs != nil {
		for _, gid := range *req.GroupIDs {
			g, err := s.groupRepo.GetByID(ctx, gid)
			if err != nil {
				return nil, err
			}
			if g.RequireOAuthOnly && (g.Platform == PlatformOpenAI || g.Platform == PlatformAntigravity || g.Platform == PlatformAnthropic || g.Platform == PlatformGemini || g.Platform == PlatformGrok) {
				return nil, fmt.Errorf("分组 [%s] 仅允许 OAuth 账号，apikey 类型账号无法加入", g.Name)
			}
		}
	}

	// 绑定分组
	if req.GroupIDs != nil {
		if err := s.accountRepo.BindGroups(ctx, account.ID, *req.GroupIDs); err != nil {
			return nil, fmt.Errorf("bind groups: %w", err)
		}
	}

	return account, nil
}

// Delete 删除账号
// 优化：使用 ExistsByID 替代 GetByID 进行存在性检查，
// 避免加载完整账号对象及其关联数据，提升删除操作的性能
func (s *AccountService) Delete(ctx context.Context, id int64) error {
	// 使用轻量级的存在性检查，而非加载完整账号对象
	exists, err := s.accountRepo.ExistsByID(ctx, id)
	if err != nil {
		return fmt.Errorf("check account: %w", err)
	}
	// 明确返回账号不存在错误，便于调用方区分错误类型
	if !exists {
		return ErrAccountNotFound
	}

	lifecycleRepo, ok := s.accountRepo.(AccountShadowLifecycleRepository)
	if !ok {
		return fmt.Errorf("delete account: atomic account and Spark shadow deletion is not configured")
	}
	if err := lifecycleRepo.DeleteWithShadows(ctx, id); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}

	return nil
}

func (s *AccountService) validateGroupIDsExist(ctx context.Context, groupIDs []int64) error {
	if len(groupIDs) == 0 {
		return nil
	}
	if s.groupRepo == nil {
		return fmt.Errorf("group repository not configured")
	}

	if batchChecker, ok := s.groupRepo.(groupExistenceBatchChecker); ok {
		existsByID, err := batchChecker.ExistsByIDs(ctx, groupIDs)
		if err != nil {
			return fmt.Errorf("check groups exists: %w", err)
		}
		for _, groupID := range groupIDs {
			if groupID <= 0 {
				return fmt.Errorf("get group: %w", ErrGroupNotFound)
			}
			if !existsByID[groupID] {
				return fmt.Errorf("get group: %w", ErrGroupNotFound)
			}
		}
		return nil
	}

	for _, groupID := range groupIDs {
		_, err := s.groupRepo.GetByID(ctx, groupID)
		if err != nil {
			return fmt.Errorf("get group: %w", err)
		}
	}
	return nil
}

// UpdateStatus 更新账号状态
func (s *AccountService) UpdateStatus(ctx context.Context, id int64, status string, errorMessage string) error {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get account: %w", err)
	}

	account.Status = status
	account.ErrorMessage = errorMessage
	account.Schedulable = normalizeAccountSchedulableForStatus(account.Status, account.Schedulable)

	if err := s.accountRepo.Update(ctx, account); err != nil {
		return fmt.Errorf("update account: %w", err)
	}

	return nil
}

// UpdateLastUsed 更新最后使用时间
func (s *AccountService) UpdateLastUsed(ctx context.Context, id int64) error {
	if err := s.accountRepo.UpdateLastUsed(ctx, id); err != nil {
		return fmt.Errorf("update last used: %w", err)
	}
	return nil
}

// GetCredential 获取账号凭证（安全访问）
func (s *AccountService) GetCredential(ctx context.Context, id int64, key string) (string, error) {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return "", fmt.Errorf("get account: %w", err)
	}

	return account.GetCredential(key), nil
}

// TestCredentials 测试账号凭证是否有效（需要实现具体平台的测试逻辑）
func (s *AccountService) TestCredentials(ctx context.Context, id int64) error {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get account: %w", err)
	}

	// 根据平台执行不同的测试逻辑
	switch account.Platform {
	case PlatformAnthropic:
		// TODO: 测试Anthropic API凭证
		return nil
	case PlatformOpenAI:
		// TODO: 测试OpenAI API凭证
		return nil
	case PlatformGemini:
		// TODO: 测试Gemini API凭证
		return nil
	default:
		return fmt.Errorf("unsupported platform: %s", account.Platform)
	}
}

// [v0.2.5 新增]
type AdminAccountRepository interface {
	AccountRepository
	AccountDuplicateRepository
	AccountBillingSettingsRepository
}

// [v0.2.5 新增]
type AccountBillingSettingsRepository interface {
	UpdateWithAccountBillingSettings(
		ctx context.Context,
		account *Account,
		probeEnabled *bool,
		rateSyncEnabled *bool,
		rateMultiplier *float64,
	) error
}
