package handler

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type concurrencyCacheMock struct {
	acquireUserSlotFn      func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error)
	acquireUserGroupSlotFn func(ctx context.Context, userID int64, userMaxConcurrency int, groupID int64, groupMaxConcurrency int, apiKeyID int64, requestID string) (bool, error)
	acquireAccountSlotFn   func(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error)
	acquireIngressLeaseFn  func(ctx context.Context, apiKeyID int64, maxConnections int, leaseID string) (bool, error)
	refreshIngressLeaseFn  func(ctx context.Context, apiKeyID int64, leaseID string) (bool, error)
	releaseIngressLeaseFn  func(ctx context.Context, apiKeyID int64, leaseID string) error
	releaseUserCalled      int32
	releaseUserGroupCalled int32
	releaseAccountCalled   int32
	releaseIngressCalled   int32
}

func (m *concurrencyCacheMock) AcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
	if m.acquireAccountSlotFn != nil {
		return m.acquireAccountSlotFn(ctx, accountID, maxConcurrency, requestID)
	}
	return false, nil
}

func (m *concurrencyCacheMock) ReleaseAccountSlot(ctx context.Context, accountID int64, requestID string) error {
	atomic.AddInt32(&m.releaseAccountCalled, 1)
	return nil
}

func (m *concurrencyCacheMock) GetAccountConcurrency(ctx context.Context, accountID int64) (int, error) {
	return 0, nil
}

func (m *concurrencyCacheMock) GetAccountConcurrencyBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error) {
	result := make(map[int64]int, len(accountIDs))
	for _, accountID := range accountIDs {
		result[accountID] = 0
	}
	return result, nil
}

func (m *concurrencyCacheMock) IncrementAccountWaitCount(ctx context.Context, accountID int64, maxWait int) (bool, error) {
	return true, nil
}

func (m *concurrencyCacheMock) DecrementAccountWaitCount(ctx context.Context, accountID int64) error {
	return nil
}

func (m *concurrencyCacheMock) GetAccountWaitingCount(ctx context.Context, accountID int64) (int, error) {
	return 0, nil
}

func (m *concurrencyCacheMock) AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
	if m.acquireUserSlotFn != nil {
		return m.acquireUserSlotFn(ctx, userID, maxConcurrency, requestID)
	}
	return false, nil
}

func (m *concurrencyCacheMock) ReleaseUserSlot(ctx context.Context, userID int64, requestID string) error {
	atomic.AddInt32(&m.releaseUserCalled, 1)
	return nil
}

func (m *concurrencyCacheMock) AcquireUserGroupAndAPIKeySlot(
	ctx context.Context,
	userID int64,
	userMaxConcurrency int,
	groupID int64,
	groupMaxConcurrency int,
	apiKeyID int64,
	requestID string,
) (bool, error) {
	if m.acquireUserGroupSlotFn != nil {
		return m.acquireUserGroupSlotFn(ctx, userID, userMaxConcurrency, groupID, groupMaxConcurrency, apiKeyID, requestID)
	}
	return false, nil
}

func (m *concurrencyCacheMock) ReleaseUserGroupAndAPIKeySlot(ctx context.Context, userID int64, groupID int64, apiKeyID int64, requestID string) error {
	atomic.AddInt32(&m.releaseUserGroupCalled, 1)
	return nil
}

func (m *concurrencyCacheMock) GetUserConcurrency(ctx context.Context, userID int64) (int, error) {
	return 0, nil
}

func (m *concurrencyCacheMock) IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error) {
	return true, nil
}

func (m *concurrencyCacheMock) DecrementWaitCount(ctx context.Context, userID int64) error {
	return nil
}

func (m *concurrencyCacheMock) GetAccountsLoadBatch(ctx context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	return map[int64]*service.AccountLoadInfo{}, nil
}

func (m *concurrencyCacheMock) GetUsersLoadBatch(ctx context.Context, users []service.UserWithConcurrency) (map[int64]*service.UserLoadInfo, error) {
	return map[int64]*service.UserLoadInfo{}, nil
}

func (m *concurrencyCacheMock) CleanupExpiredAccountSlots(ctx context.Context, accountID int64) error {
	return nil
}

func (m *concurrencyCacheMock) CleanupStaleProcessSlots(ctx context.Context, activeRequestPrefix string) error {
	return nil
}

func (m *concurrencyCacheMock) AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int, leaseID string) (bool, error) {
	if m.acquireIngressLeaseFn != nil {
		return m.acquireIngressLeaseFn(ctx, apiKeyID, maxConnections, leaseID)
	}
	return true, nil
}

func (m *concurrencyCacheMock) RefreshOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) (bool, error) {
	if m.refreshIngressLeaseFn != nil {
		return m.refreshIngressLeaseFn(ctx, apiKeyID, leaseID)
	}
	return true, nil
}

func (m *concurrencyCacheMock) ReleaseOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) error {
	atomic.AddInt32(&m.releaseIngressCalled, 1)
	if m.releaseIngressLeaseFn != nil {
		return m.releaseIngressLeaseFn(ctx, apiKeyID, leaseID)
	}
	return nil
}

func TestConcurrencyHelper_TryAcquireUserSlot(t *testing.T) {
	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
	}
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)

	release, acquired, err := helper.TryAcquireUserSlot(context.Background(), 101, 2)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, release)

	release()
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.releaseUserCalled))
}

func TestConcurrencyHelper_TryAcquireUserGroupSlotForAPIKeyPassesAllDimensions(t *testing.T) {
	var gotUserID, gotGroupID, gotAPIKeyID int64
	var gotUserMax, gotGroupMax int
	cache := &concurrencyCacheMock{
		acquireUserGroupSlotFn: func(_ context.Context, userID int64, userMax int, groupID int64, groupMax int, apiKeyID int64, _ string) (bool, error) {
			gotUserID, gotUserMax = userID, userMax
			gotGroupID, gotGroupMax = groupID, groupMax
			gotAPIKeyID = apiKeyID
			return true, nil
		},
	}
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)

	release, acquired, err := helper.TryAcquireUserGroupSlotForAPIKey(context.Background(), 101, 8, 202, 3, -303)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, release)
	require.Equal(t, int64(101), gotUserID)
	require.Equal(t, 8, gotUserMax)
	require.Equal(t, int64(202), gotGroupID)
	require.Equal(t, 3, gotGroupMax)
	require.Equal(t, int64(-303), gotAPIKeyID)

	release()
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.releaseUserGroupCalled))
}

func TestFinalAPIKeyConcurrencyIDSupportsControlPlaneSnapshotKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{KeyID: "control-plane-key-1", UserID: 101})
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{Name: "control-plane-key-1"})

	first := finalAPIKeyConcurrencyID(c)
	second := finalAPIKeyConcurrencyID(c)
	require.Negative(t, first)
	require.Equal(t, first, second)
}

func TestConcurrencyHelper_TryAcquireAccountSlot_NotAcquired(t *testing.T) {
	cache := &concurrencyCacheMock{
		acquireAccountSlotFn: func(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
			return false, nil
		},
	}
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)

	release, acquired, err := helper.TryAcquireAccountSlot(context.Background(), 201, 1)
	require.NoError(t, err)
	require.False(t, acquired)
	require.Nil(t, release)
	require.Equal(t, int32(0), atomic.LoadInt32(&cache.releaseAccountCalled))
}

func TestConcurrencyHelper_AcquireOpenAIWSIngressLease(t *testing.T) {
	t.Run("capacity rejected", func(t *testing.T) {
		cache := &concurrencyCacheMock{
			acquireIngressLeaseFn: func(context.Context, int64, int, string) (bool, error) {
				return false, nil
			},
		}
		helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)

		lease, acquired, err := helper.AcquireOpenAIWSIngressLease(context.Background(), 301, 1)
		require.NoError(t, err)
		require.False(t, acquired)
		require.Nil(t, lease)
	})

	t.Run("release returns capacity", func(t *testing.T) {
		cache := &concurrencyCacheMock{}
		helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)

		lease, acquired, err := helper.AcquireOpenAIWSIngressLease(context.Background(), 302, 1)
		require.NoError(t, err)
		require.True(t, acquired)
		require.NotNil(t, lease)
		lease.Release()
		require.Equal(t, int32(1), atomic.LoadInt32(&cache.releaseIngressCalled))
	})
}
