package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	SchedulerModeSingle = "single"
	SchedulerModeMixed  = "mixed"
	SchedulerModeForced = "forced"
)

var (
	ErrSchedulerBucketRetired              = errors.New("scheduler bucket retired")
	ErrSchedulerBucketWriteFenced          = errors.New("scheduler bucket write fenced")
	ErrSchedulerGroupLifecycleLeaseInvalid = errors.New("scheduler group lifecycle lease invalid")
	ErrSchedulerGroupLifecycleLeaseLost    = errors.New("scheduler group lifecycle lease lost")
)

// SchedulerBucketWriteToken 绑定一次快照写入到某个 bucket（调度桶）的代际。
// 业务上必须在查询数据库前获取 token（写入令牌），这样分组被关闭/删除后，旧查询结果
// 即使晚返回，也不能重新发布到已退休的调度桶。
type SchedulerBucketWriteToken struct {
	Bucket SchedulerBucket
	Epoch  int64
}

func (t SchedulerBucketWriteToken) ValidFor(bucket SchedulerBucket) bool {
	return t.Epoch > 0 && t.Bucket == bucket
}

// SchedulerGroupLifecycleLease 表示一个分组生命周期变更的短租约。
// 同一分组的 retire（退休）和 reopen（重新打开）必须串行，避免多实例并发写出旧状态。
type SchedulerGroupLifecycleLease struct {
	GroupID    int64
	OwnerToken string
}

func (l SchedulerGroupLifecycleLease) ValidFor(groupID int64) bool {
	return groupID > 0 && l.GroupID == groupID && l.OwnerToken != ""
}

type SchedulerBucket struct {
	GroupID  int64
	Platform string
	Mode     string
}

func (b SchedulerBucket) String() string {
	return fmt.Sprintf("%d:%s:%s", b.GroupID, b.Platform, b.Mode)
}

func ParseSchedulerBucket(raw string) (SchedulerBucket, bool) {
	parts := strings.Split(raw, ":")
	if len(parts) != 3 {
		return SchedulerBucket{}, false
	}
	groupID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return SchedulerBucket{}, false
	}
	if parts[1] == "" || parts[2] == "" {
		return SchedulerBucket{}, false
	}
	return SchedulerBucket{
		GroupID:  groupID,
		Platform: parts[1],
		Mode:     parts[2],
	}, true
}

// SchedulerCache 负责调度快照与账号快照的缓存读写。
type SchedulerCache interface {
	// GetSnapshot 读取快照并返回命中与否（ready + active + 数据完整）。
	GetSnapshot(ctx context.Context, bucket SchedulerBucket) ([]*Account, bool, error)
	// SetSnapshot 写入快照并切换激活版本。
	SetSnapshot(ctx context.Context, bucket SchedulerBucket, accounts []Account) error
	// GetAccount 获取单账号快照。
	GetAccount(ctx context.Context, accountID int64) (*Account, error)
	// SetAccount 写入单账号快照（包含不可调度状态）。
	SetAccount(ctx context.Context, account *Account) error
	// DeleteAccount 删除单账号快照。
	DeleteAccount(ctx context.Context, accountID int64) error
	// UpdateLastUsed 批量更新账号的最后使用时间。
	UpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error
	// TryLockBucket 尝试获取分桶重建锁。
	TryLockBucket(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (bool, error)
	// UnlockBucket 释放分桶重建锁。
	UnlockBucket(ctx context.Context, bucket SchedulerBucket) error
	// ListBuckets 返回已注册的分桶集合。
	ListBuckets(ctx context.Context) ([]SchedulerBucket, error)
	// GetOutboxWatermark 读取 outbox 水位。
	GetOutboxWatermark(ctx context.Context) (int64, error)
	// SetOutboxWatermark 保存 outbox 水位。
	SetOutboxWatermark(ctx context.Context, id int64) error
}

// SchedulerAccountConcurrencyBatchCache 是 Redis（缓存服务）调度缓存的可选能力。
// 严格优先级整层满载时只批量读取轻量并发上限，避免为了识别配置刚变更而
// 逐个读取包含凭据的完整账号快照。
type SchedulerAccountConcurrencyBatchCache interface {
	GetAccountConcurrencyLimits(ctx context.Context, accountIDs []int64) (map[int64]int, error)
}

// SchedulerGroupCache is implemented by scheduler caches that can keep the
// lightweight group metadata needed by gateway-lite routing decisions.
type SchedulerGroupCache interface {
	GetGroup(ctx context.Context, groupID int64) (*Group, error)
	SetGroup(ctx context.Context, group *Group) error
	DeleteGroup(ctx context.Context, groupID int64) error
}

// SchedulerBucketLifecycleCache 是 Redis 调度缓存的增强能力。
// 主接口保持旧签名，避免测试替身和非 Redis 场景被迫实现复杂 fencing（写入防护）；
// 真实 gateway-lite Redis 缓存实现该接口后，调度快照服务会自动启用分组生命周期保护。
type SchedulerBucketLifecycleCache interface {
	CaptureBucketWriteToken(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error)
	SetSnapshotWithToken(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, accounts []Account) error
	RetireBucket(ctx context.Context, bucket SchedulerBucket) error
	ReopenBucket(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error)
	TryAcquireGroupLifecycleLease(ctx context.Context, groupID int64, ttl time.Duration) (SchedulerGroupLifecycleLease, bool, error)
	ReleaseGroupLifecycleLease(ctx context.Context, lease SchedulerGroupLifecycleLease) error
}
