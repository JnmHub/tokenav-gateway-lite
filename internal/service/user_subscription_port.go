package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

type UserSubscriptionRepository interface {
	Create(ctx context.Context, sub *UserSubscription) error
	GetByID(ctx context.Context, id int64) (*UserSubscription, error)
	// GetByIDIncludeDeleted 绕过软删除过滤读取订阅（含已撤销）。恢复订阅必须看到已删行。
	GetByIDIncludeDeleted(ctx context.Context, id int64) (*UserSubscription, error)
	GetByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*UserSubscription, error)
	GetActiveByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*UserSubscription, error)
	Update(ctx context.Context, sub *UserSubscription) error
	Delete(ctx context.Context, id int64) error
	// Restore 清除软删除标记并写入恢复后的状态，返回恢复后的订阅。
	// 恢复前必须已确认该 (用户, 分组) 没有活跃订阅，否则会出现重复活跃行。
	Restore(ctx context.Context, subscriptionID int64, restoredStatus string) (*UserSubscription, error)

	ListByUserID(ctx context.Context, userID int64) ([]UserSubscription, error)
	ListActiveByUserID(ctx context.Context, userID int64) ([]UserSubscription, error)
	ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]UserSubscription, *pagination.PaginationResult, error)
	List(ctx context.Context, params pagination.PaginationParams, userID, groupID *int64, status, platform, sortBy, sortOrder string) ([]UserSubscription, *pagination.PaginationResult, error)

	ExistsByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (bool, error)
	// ExistsActiveByUserIDAndGroupID 判断该 (用户, 分组) 是否存在活跃订阅，供恢复订阅时做唯一性前置校验。
	ExistsActiveByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (bool, error)
	ExtendExpiry(ctx context.Context, subscriptionID int64, newExpiresAt time.Time) error
	UpdateStatus(ctx context.Context, subscriptionID int64, status string) error
	UpdateNotes(ctx context.Context, subscriptionID int64, notes string) error

	// [tokenav 定制] 保留本地实现：窗口方法保持单锚点签名。
	// 官方版本把 ActivateWindows 拆成 dailyStart/periodicStart 两个锚点，并给
	// Reset*Usage 增加 expectedWindowStart 做 CAS。我们的本地仓储实现（以及
	// user_subscription_repo_integration_test.go）目前是单锚点签名，日历日对齐
	// 由 user_subscription.go 读取侧的 automaticDailyWindowStartAt 统一修正。
	// 签名升级需要 repository 层配套改造，已列入跨层待办清单。
	ActivateWindows(ctx context.Context, id int64, start time.Time) error
	ResetDailyUsage(ctx context.Context, id int64, newWindowStart time.Time) error
	ResetWeeklyUsage(ctx context.Context, id int64, newWindowStart time.Time) error
	ResetMonthlyUsage(ctx context.Context, id int64, newWindowStart time.Time) error
	IncrementUsage(ctx context.Context, id int64, costUSD float64) error

	BatchUpdateExpiredStatus(ctx context.Context) (int64, error)
}
