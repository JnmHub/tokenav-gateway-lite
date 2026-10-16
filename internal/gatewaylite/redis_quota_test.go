package gatewaylite

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRedisQuotaReserveCommitRefund(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "test")
	lease := LeaseSnapshot{
		LeaseID:        "lease1",
		UserID:         42,
		Region:         "sg",
		AllocatedCents: 100,
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
	}

	require.NoError(t, quota.EnsureLease(ctx, lease))
	loaded, ok, err := quota.LoadLease(ctx, 42, "sg")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "lease1", loaded.LeaseID)

	ok, err = quota.Reserve(ctx, ReserveRequest{
		RequestID:          "req1",
		KeyID:              "key1",
		UserID:             42,
		Region:             "sg",
		LeaseID:            "lease1",
		EstimatedCostCents: 10,
	})
	require.NoError(t, err)
	require.True(t, ok)

	fields, err := client.HGetAll(ctx, "test:lease:42:sg").Result()
	require.NoError(t, err)
	require.EqualValues(t, 10, ParseInt64Field(fields, "reserved_cents"))

	require.NoError(t, quota.Commit(ctx, UsageCommit{RequestID: "req1", ActualCents: 7}))
	fields, err = client.HGetAll(ctx, "test:lease:42:sg").Result()
	require.NoError(t, err)
	require.EqualValues(t, 0, ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 7, ParseInt64Field(fields, "spent_cents"))

	require.NoError(t, quota.AdjustCommitted(ctx, UsageCommit{RequestID: "req1", ActualCents: 9}))
	fields, err = client.HGetAll(ctx, "test:lease:42:sg").Result()
	require.NoError(t, err)
	require.EqualValues(t, 9, ParseInt64Field(fields, "spent_cents"))

	ok, err = quota.Reserve(ctx, ReserveRequest{
		RequestID:          "req2",
		KeyID:              "key1",
		UserID:             42,
		Region:             "sg",
		LeaseID:            "lease1",
		EstimatedCostCents: 10,
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, quota.Refund(ctx, "req2"))
	fields, err = client.HGetAll(ctx, "test:lease:42:sg").Result()
	require.NoError(t, err)
	require.EqualValues(t, 0, ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 9, ParseInt64Field(fields, "spent_cents"))
}

func TestRedisQuotaStaleRequestDoesNotMutateReplacementLease(t *testing.T) {
	setup := func(t *testing.T, prefix string) (context.Context, *redis.Client, *RedisQuota, string) {
		t.Helper()
		ctx := context.Background()
		mr := miniredis.RunT(t)
		client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		quota := NewRedisQuota(client, prefix)
		leaseKey := prefix + ":lease:42:sg"
		require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
			LeaseID: "lease-old", UserID: 42, Region: "sg", AllocatedCents: 100,
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		}))
		return ctx, client, quota, leaseKey
	}
	replaceLease := func(t *testing.T, ctx context.Context, client *redis.Client, quota *RedisQuota, leaseKey string) {
		t.Helper()
		require.NoError(t, client.Del(ctx, leaseKey).Err())
		require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
			LeaseID: "lease-new", UserID: 42, Region: "sg", AllocatedCents: 200,
			ReservedCents: 3, SpentCents: 5, Version: 2,
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		}))
	}
	assertReplacementUntouched := func(t *testing.T, ctx context.Context, client *redis.Client, leaseKey string) {
		t.Helper()
		fields, err := client.HGetAll(ctx, leaseKey).Result()
		require.NoError(t, err)
		require.Equal(t, "lease-new", fields["lease_id"])
		require.EqualValues(t, 3, ParseInt64Field(fields, "reserved_cents"))
		require.EqualValues(t, 5, ParseInt64Field(fields, "spent_cents"))
	}

	t.Run("commit", func(t *testing.T) {
		ctx, client, quota, leaseKey := setup(t, "stale-commit")
		ok, err := quota.Reserve(ctx, ReserveRequest{
			RequestID: "req", KeyID: "key", UserID: 42, Region: "sg",
			LeaseID: "lease-old", EstimatedCostCents: 10,
		})
		require.NoError(t, err)
		require.True(t, ok)
		replaceLease(t, ctx, client, quota, leaseKey)

		actual, err := quota.CommitResolved(ctx, UsageCommit{RequestID: "req", ActualCents: 7})
		require.NoError(t, err)
		require.EqualValues(t, 7, actual)
		assertReplacementUntouched(t, ctx, client, leaseKey)
		request, err := client.HGetAll(ctx, "stale-commit:req:req").Result()
		require.NoError(t, err)
		require.Equal(t, "committed", request["status"])
		require.Equal(t, "7", request["actual_cents"])
	})

	t.Run("refund", func(t *testing.T) {
		ctx, client, quota, leaseKey := setup(t, "stale-refund")
		ok, err := quota.Reserve(ctx, ReserveRequest{
			RequestID: "req", KeyID: "key", UserID: 42, Region: "sg",
			LeaseID: "lease-old", EstimatedCostCents: 10,
		})
		require.NoError(t, err)
		require.True(t, ok)
		replaceLease(t, ctx, client, quota, leaseKey)

		require.NoError(t, quota.Refund(ctx, "req"))
		assertReplacementUntouched(t, ctx, client, leaseKey)
		status, err := client.HGet(ctx, "stale-refund:req:req", "status").Result()
		require.NoError(t, err)
		require.Equal(t, "refunded", status)
	})

	t.Run("adjust", func(t *testing.T) {
		ctx, client, quota, leaseKey := setup(t, "stale-adjust")
		ok, err := quota.Reserve(ctx, ReserveRequest{
			RequestID: "req", KeyID: "key", UserID: 42, Region: "sg",
			LeaseID: "lease-old", EstimatedCostCents: 10,
		})
		require.NoError(t, err)
		require.True(t, ok)
		replaceLease(t, ctx, client, quota, leaseKey)

		require.NoError(t, quota.AdjustCommitted(ctx, UsageCommit{RequestID: "req", ActualCents: 7}))
		assertReplacementUntouched(t, ctx, client, leaseKey)
		request, err := client.HGetAll(ctx, "stale-adjust:req:req").Result()
		require.NoError(t, err)
		require.Equal(t, "7", request["corrected_actual_cents"])
		require.NoError(t, quota.Commit(ctx, UsageCommit{RequestID: "req", ActualCents: 1}))
		assertReplacementUntouched(t, ctx, client, leaseKey)
	})

	t.Run("resize-and-ensure", func(t *testing.T) {
		ctx, client, quota, leaseKey := setup(t, "stale-resize")
		ok, err := quota.Reserve(ctx, ReserveRequest{
			RequestID: "req", KeyID: "key", UserID: 42, Region: "sg",
			LeaseID: "lease-old", EstimatedCostCents: 10,
		})
		require.NoError(t, err)
		require.True(t, ok)
		replaceLease(t, ctx, client, quota, leaseKey)

		resized, err := quota.ResizeReservation(ctx, "req", 20)
		require.NoError(t, err)
		require.False(t, resized)
		result, err := quota.EnsureCommittedCost(ctx, "req", 20)
		require.NoError(t, err)
		require.Equal(t, RequestCostMissing, result)
		reserved, err := quota.Reserve(ctx, ReserveRequest{
			RequestID: "req", KeyID: "key", UserID: 42, Region: "sg",
			LeaseID: "lease-new", EstimatedCostCents: 10,
		})
		require.NoError(t, err)
		require.False(t, reserved)
		_, found, reserved, err := quota.ReserveActiveLease(ctx, ReserveRequest{
			RequestID: "req", KeyID: "key", UserID: 42, Region: "sg", EstimatedCostCents: 10,
		})
		require.NoError(t, err)
		require.False(t, found)
		require.False(t, reserved)
		assertReplacementUntouched(t, ctx, client, leaseKey)
		requestLeaseID, err := client.HGet(ctx, "stale-resize:req:req", "lease_id").Result()
		require.NoError(t, err)
		require.Equal(t, "lease-old", requestLeaseID)
	})
}

func TestRedisQuotaConcurrentSettlementIsIdempotentAndNonNegative(t *testing.T) {
	run := func(t *testing.T, prefix string, settle func(context.Context, *RedisQuota) error, wantSpent int64) {
		t.Helper()
		ctx := context.Background()
		mr := miniredis.RunT(t)
		client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer client.Close()
		quota := NewRedisQuota(client, prefix)
		require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
			LeaseID: "lease", UserID: 42, Region: "sg", AllocatedCents: 100,
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		}))
		ok, err := quota.Reserve(ctx, ReserveRequest{
			RequestID: "req", KeyID: "key", UserID: 42, Region: "sg",
			LeaseID: "lease", EstimatedCostCents: 10,
		})
		require.NoError(t, err)
		require.True(t, ok)

		const workers = 64
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		wg.Add(workers)
		for range workers {
			go func() {
				defer wg.Done()
				errs <- settle(ctx, quota)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
		fields, err := client.HGetAll(ctx, prefix+":lease:42:sg").Result()
		require.NoError(t, err)
		require.Zero(t, ParseInt64Field(fields, "reserved_cents"))
		require.EqualValues(t, wantSpent, ParseInt64Field(fields, "spent_cents"))
	}

	t.Run("commit", func(t *testing.T) {
		run(t, "concurrent-commit", func(ctx context.Context, quota *RedisQuota) error {
			return quota.Commit(ctx, UsageCommit{RequestID: "req", ActualCents: 7})
		}, 7)
	})
	t.Run("refund", func(t *testing.T) {
		run(t, "concurrent-refund", func(ctx context.Context, quota *RedisQuota) error {
			return quota.Refund(ctx, "req")
		}, 0)
	})
	t.Run("adjust", func(t *testing.T) {
		run(t, "concurrent-adjust", func(ctx context.Context, quota *RedisQuota) error {
			if err := quota.Commit(ctx, UsageCommit{RequestID: "req", ActualCents: 1}); err != nil {
				return err
			}
			return quota.AdjustCommitted(ctx, UsageCommit{RequestID: "req", ActualCents: 9})
		}, 9)
	})
}

func TestRedisQuotaSettlementClampsCorruptCountersAtZero(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "clamp")
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease", UserID: 42, Region: "sg", AllocatedCents: 100,
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))
	ok, err := quota.Reserve(ctx, ReserveRequest{
		RequestID: "req", KeyID: "key", UserID: 42, Region: "sg",
		LeaseID: "lease", EstimatedCostCents: 10,
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, client.HSet(ctx, "clamp:lease:42:sg", "reserved_cents", 1).Err())
	require.NoError(t, quota.Commit(ctx, UsageCommit{RequestID: "req", ActualCents: 7}))
	fields, err := client.HGetAll(ctx, "clamp:lease:42:sg").Result()
	require.NoError(t, err)
	require.Zero(t, ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 7, ParseInt64Field(fields, "spent_cents"))

	require.NoError(t, client.HSet(ctx, "clamp:lease:42:sg", "spent_cents", 1).Err())
	require.NoError(t, quota.AdjustCommitted(ctx, UsageCommit{RequestID: "req", ActualCents: 0}))
	fields, err = client.HGetAll(ctx, "clamp:lease:42:sg").Result()
	require.NoError(t, err)
	require.Zero(t, ParseInt64Field(fields, "spent_cents"))
}

func TestRedisQuotaReserveActiveLeaseReturnsPostReservationSnapshot(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "active")

	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID:        "lease-active",
		UserID:         42,
		Region:         "sg",
		AllocatedCents: 20,
		ReservedCents:  2,
		SpentCents:     3,
		Version:        7,
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
		TargetCents:    20,
		RefillAtCents:  5,
	}))

	request := ReserveRequest{RequestID: "req-active", KeyID: "key-1", UserID: 42, Region: "sg", EstimatedCostCents: 4}
	lease, found, reserved, err := quota.ReserveActiveLease(ctx, request)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, reserved)
	require.Equal(t, "lease-active", lease.LeaseID)
	require.EqualValues(t, 6, lease.ReservedCents)
	require.EqualValues(t, 3, lease.SpentCents)
	require.EqualValues(t, 11, lease.AvailableCents())

	// 同一个 request_id（请求编号）重试必须幂等，不能再次增加预留。
	duplicate, found, reserved, err := quota.ReserveActiveLease(ctx, request)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, reserved)
	require.EqualValues(t, 6, duplicate.ReservedCents)

	_, found, reserved, err = quota.ReserveActiveLease(ctx, ReserveRequest{
		RequestID: "req-too-large", KeyID: "key-1", UserID: 42, Region: "sg", EstimatedCostCents: 12,
	})
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, reserved)
}

func TestRedisQuotaAdjustBeforeCommit(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "test")

	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID:        "lease1",
		UserID:         42,
		Region:         "sg",
		AllocatedCents: 100,
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
	}))
	ok, err := quota.Reserve(ctx, ReserveRequest{
		RequestID:          "req1",
		KeyID:              "key1",
		UserID:             42,
		Region:             "sg",
		LeaseID:            "lease1",
		EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, quota.AdjustCommitted(ctx, UsageCommit{RequestID: "req1", ActualCents: 7}))
	actualCents, err := quota.CommitResolved(ctx, UsageCommit{RequestID: "req1", ActualCents: 1})
	require.NoError(t, err)
	require.EqualValues(t, 7, actualCents)

	fields, err := client.HGetAll(ctx, "test:lease:42:sg").Result()
	require.NoError(t, err)
	require.EqualValues(t, 0, ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 7, ParseInt64Field(fields, "spent_cents"))
}

func TestRedisQuotaResizeReservationBeforeCommit(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "resize")

	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID:        "lease-resize",
		UserID:         42,
		Region:         "jp",
		AllocatedCents: 100,
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
	}))
	ok, err := quota.Reserve(ctx, ReserveRequest{
		RequestID:          "req-video",
		KeyID:              "key-video",
		UserID:             42,
		Region:             "jp",
		LeaseID:            "lease-resize",
		EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = quota.ResizeReservation(ctx, "req-video", 40)
	require.NoError(t, err)
	require.True(t, ok)
	fields, err := client.HGetAll(ctx, "resize:lease:42:jp").Result()
	require.NoError(t, err)
	require.EqualValues(t, 40, ParseInt64Field(fields, "reserved_cents"))
	requestFields, err := client.HGetAll(ctx, "resize:req:req-video").Result()
	require.NoError(t, err)
	require.EqualValues(t, 40, ParseInt64Field(requestFields, "estimated_cents"))

	// A resize beyond the available lease is rejected atomically and preserves
	// the previous reservation.
	ok, err = quota.ResizeReservation(ctx, "req-video", 101)
	require.NoError(t, err)
	require.False(t, ok)
	fields, err = client.HGetAll(ctx, "resize:lease:42:jp").Result()
	require.NoError(t, err)
	require.EqualValues(t, 40, ParseInt64Field(fields, "reserved_cents"))

	require.NoError(t, quota.Commit(ctx, UsageCommit{RequestID: "req-video", ActualCents: 35}))
	fields, err = client.HGetAll(ctx, "resize:lease:42:jp").Result()
	require.NoError(t, err)
	require.EqualValues(t, 0, ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 35, ParseInt64Field(fields, "spent_cents"))

	result, err := quota.EnsureCommittedCost(ctx, "req-video", 50)
	require.NoError(t, err)
	require.Equal(t, RequestCostAdjusted, result)
	fields, err = client.HGetAll(ctx, "resize:lease:42:jp").Result()
	require.NoError(t, err)
	require.EqualValues(t, 50, ParseInt64Field(fields, "spent_cents"))

	result, err = quota.EnsureCommittedCost(ctx, "req-video", 101)
	require.NoError(t, err)
	require.Equal(t, RequestCostInsufficient, result)
	fields, err = client.HGetAll(ctx, "resize:lease:42:jp").Result()
	require.NoError(t, err)
	require.EqualValues(t, 50, ParseInt64Field(fields, "spent_cents"))

	result, err = quota.EnsureCommittedCost(ctx, "req-video", 20)
	require.NoError(t, err)
	require.Equal(t, RequestCostAdjusted, result)
	fields, err = client.HGetAll(ctx, "resize:lease:42:jp").Result()
	require.NoError(t, err)
	require.EqualValues(t, 20, ParseInt64Field(fields, "spent_cents"))
}

func TestRedisQuotaEnsureLeasePreservesLocalCounters(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "test")

	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID:        "lease1",
		UserID:         42,
		Region:         "sg",
		AllocatedCents: 100,
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
	}))
	ok, err := quota.Reserve(ctx, ReserveRequest{
		RequestID:          "req1",
		KeyID:              "key1",
		UserID:             42,
		Region:             "sg",
		LeaseID:            "lease1",
		EstimatedCostCents: 10,
	})
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID:        "lease1",
		UserID:         42,
		Region:         "sg",
		AllocatedCents: 200,
		ReservedCents:  0,
		SpentCents:     0,
		Version:        2,
		ExpiresAt:      time.Now().Add(2 * time.Hour).Unix(),
	}))

	fields, err := client.HGetAll(ctx, "test:lease:42:sg").Result()
	require.NoError(t, err)
	require.EqualValues(t, 200, ParseInt64Field(fields, "allocated_cents"))
	require.EqualValues(t, 10, ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 0, ParseInt64Field(fields, "spent_cents"))
	require.EqualValues(t, 2, ParseInt64Field(fields, "version"))
}

func TestRedisQuotaEnsureLeaseRejectsStaleAndLowerSnapshots(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "cas")

	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease1", UserID: 42, Region: "sg", AllocatedCents: 200,
		ReservedCents: 10, SpentCents: 30, Version: 5, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease1", UserID: 42, Region: "sg", AllocatedCents: 100,
		Version: 4, ExpiresAt: time.Now().Add(2 * time.Hour).Unix(),
	}))
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease1", UserID: 42, Region: "sg", AllocatedCents: 150,
		Version: 6, ExpiresAt: time.Now().Add(2 * time.Hour).Unix(),
	}))

	lease, ok, err := quota.LoadLease(ctx, 42, "sg")
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 200, lease.AllocatedCents)
	require.EqualValues(t, 10, lease.ReservedCents)
	require.EqualValues(t, 30, lease.SpentCents)
	require.EqualValues(t, 6, lease.Version)
}

func TestRedisQuotaEnsureLeaseProtectsActiveLeaseID(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "lease-id")

	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-old", UserID: 42, Region: "jp", AllocatedCents: 100,
		Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))
	err := quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-new", UserID: 42, Region: "jp", AllocatedCents: 200,
		Version: 2, ExpiresAt: time.Now().Add(2 * time.Hour).Unix(),
	})
	require.True(t, errors.Is(err, ErrLeaseSnapshotConflict))

	lease, ok, loadErr := quota.LoadLease(ctx, 42, "jp")
	require.NoError(t, loadErr)
	require.True(t, ok)
	require.Equal(t, "lease-old", lease.LeaseID)
}

func TestRedisQuotaEnsureLeaseReplacesExpiredLeaseWithoutReservations(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "replace")

	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-old", UserID: 42, Region: "us", AllocatedCents: 100,
		SpentCents: 100, Version: 1, ExpiresAt: time.Now().Add(-time.Minute).Unix(),
	}))
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-new", UserID: 42, Region: "us", AllocatedCents: 50,
		Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))

	lease, ok, err := quota.LoadLease(ctx, 42, "us")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "lease-new", lease.LeaseID)
	require.EqualValues(t, 50, lease.AllocatedCents)
	require.Zero(t, lease.SpentCents)
}

func TestRedisQuotaEnsureLeaseConcurrentResponsesKeepNewestAllocation(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "concurrent")
	expiresAt := time.Now().Add(time.Hour).Unix()

	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease1", UserID: 42, Region: "sg", AllocatedCents: 10,
		Version: 1, ExpiresAt: expiresAt,
	}))
	var wg sync.WaitGroup
	for version := int64(2); version <= 20; version++ {
		version := version
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
				LeaseID: "lease1", UserID: 42, Region: "sg", AllocatedCents: version * 10,
				Version: version, ExpiresAt: expiresAt,
			}))
		}()
	}
	wg.Wait()

	lease, ok, err := quota.LoadLease(ctx, 42, "sg")
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 200, lease.AllocatedCents)
	require.EqualValues(t, 20, lease.Version)
}

func TestRedisQuotaLeaseRefillLockAllowsOneConcurrentOwner(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "prefetch-lock")

	var wg sync.WaitGroup
	var owners atomic.Int32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := quota.TryAcquireLeaseRefillLock(ctx, 42, "sg", 5*time.Second)
			if err == nil && ok {
				owners.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, owners.Load())
}

func TestRedisQuotaActualCostIdempotentAccumulatorReusesFirstResult(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "actual-cost-v2")

	for i := 0; i < 100; i++ {
		cents, err := quota.AccumulateActualCostMicroCentsIdempotent(ctx, "sg:key:42", "request-1", 1_500_000, 1_000_000)
		require.NoError(t, err)
		require.EqualValues(t, 1, cents)
	}
	cents, err := quota.AccumulateActualCostMicroCentsIdempotent(ctx, "sg:key:42", "request-1", 0, 1_000_000)
	require.NoError(t, err)
	require.EqualValues(t, 1, cents)
	cents, err = quota.AccumulateActualCostMicroCentsIdempotent(ctx, "sg:key:42", "request-2", 500_000, 1_000_000)
	require.NoError(t, err)
	require.EqualValues(t, 1, cents)
}

func TestRedisQuotaActualCostIdempotentAccumulatorIsAtomicUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "actual-cost-v2-concurrent")

	const workers = 1000
	results := make(chan int64, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			cents, err := quota.AccumulateActualCostMicroCentsIdempotent(ctx, "sg:key:42", "same-request", 1_500_000, 1_000_000)
			results <- cents
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	for cents := range results {
		require.EqualValues(t, 1, cents)
	}

	cents, err := quota.AccumulateActualCostMicroCentsIdempotent(ctx, "sg:key:42", "next-request", 500_000, 1_000_000)
	require.NoError(t, err)
	require.EqualValues(t, 1, cents)
}

func TestRedisQuotaActualCostIdempotentAccumulatorCarriesDifferentRequests(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "actual-cost-v2-different")

	var emitted int64
	for i := 0; i < 100; i++ {
		cents, err := quota.AccumulateActualCostMicroCentsIdempotent(ctx, "sg:key:42", fmt.Sprintf("request-%d", i), 600_000, 1_000_000)
		require.NoError(t, err)
		emitted += cents
	}
	require.EqualValues(t, 60, emitted)
}

func TestRedisQuotaInvalidateLeaseSnapshotDeletesMatchingLease(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "invalidate-match")

	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-1", UserID: 42, Region: "sg", AllocatedCents: 100,
		Version: 5, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))
	invalidated, err := quota.InvalidateLeaseSnapshot(ctx, 42, "sg", "lease-1", 5)
	require.NoError(t, err)
	require.True(t, invalidated)
	_, ok, err := quota.LoadLease(ctx, 42, "sg")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestRedisQuotaInvalidateLeaseSnapshotRejectsOlderOrDifferentLease(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "invalidate-guard")

	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-current", UserID: 42, Region: "jp", AllocatedCents: 100,
		Version: 8, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))
	invalidated, err := quota.InvalidateLeaseSnapshot(ctx, 42, "jp", "lease-current", 7)
	require.NoError(t, err)
	require.False(t, invalidated)
	invalidated, err = quota.InvalidateLeaseSnapshot(ctx, 42, "jp", "lease-old", 99)
	require.NoError(t, err)
	require.False(t, invalidated)
	lease, ok, err := quota.LoadLease(ctx, 42, "jp")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "lease-current", lease.LeaseID)
	require.EqualValues(t, 8, lease.Version)
}
