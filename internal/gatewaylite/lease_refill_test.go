package gatewaylite

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type leaseRefillControlStub struct {
	acquireCount   atomic.Int32
	rebalanceCount atomic.Int32
	acquireFn      func(AcquireLeaseRequest) (*AcquireLeaseResponse, error)
	rebalanceFn    func(RebalanceLeaseRequest) (*RebalanceLeaseResponse, error)
}

func (s *leaseRefillControlStub) AcquireLease(_ context.Context, req AcquireLeaseRequest) (*AcquireLeaseResponse, error) {
	s.acquireCount.Add(1)
	if s.acquireFn != nil {
		return s.acquireFn(req)
	}
	return nil, nil
}

func (s *leaseRefillControlStub) RebalanceLease(_ context.Context, req RebalanceLeaseRequest) (*RebalanceLeaseResponse, error) {
	s.rebalanceCount.Add(1)
	if s.rebalanceFn != nil {
		return s.rebalanceFn(req)
	}
	return nil, nil
}

func TestSchedulePostUsageLeaseRefillCoalescesConcurrentCorrections(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "post-usage-coalesce")
	lowLease := LeaseSnapshot{
		LeaseID: "lease-coalesce", UserID: 42, Region: "sg",
		AllocatedCents: 100, SpentCents: 90, Version: 1,
		ExpiresAt: time.Now().Add(time.Hour).Unix(), TargetCents: 100, RefillAtCents: 20,
	}
	require.NoError(t, quota.EnsureLease(ctx, lowLease))

	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	control := &leaseRefillControlStub{
		acquireFn: func(req AcquireLeaseRequest) (*AcquireLeaseResponse, error) {
			enteredOnce.Do(func() { close(entered) })
			<-release
			require.True(t, req.LowWatermark)
			require.Equal(t, "gateway_post_usage_refill", req.Reason)
			return &AcquireLeaseResponse{OK: true, Lease: LeaseSnapshot{
				LeaseID: "lease-coalesce", UserID: 42, Region: "sg",
				AllocatedCents: 200, SpentCents: 90, Version: 2,
				ExpiresAt: lowLease.ExpiresAt, TargetCents: 100, RefillAtCents: 20,
			}}, nil
		},
	}

	const triggers = 100
	var wg sync.WaitGroup
	wg.Add(triggers)
	for range triggers {
		go func() {
			defer wg.Done()
			schedulePostUsageLeaseRefillWithControl(control, quota, leaseRefillSignal{Needed: true, Lease: lowLease}, "sg")
		}()
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for refill")
	}
	wg.Wait()
	close(release)

	require.Eventually(t, func() bool {
		lease, found, err := quota.LoadLease(ctx, 42, "sg")
		return err == nil && found && lease.AllocatedCents == 200
	}, time.Second, 10*time.Millisecond)
	require.EqualValues(t, 1, control.acquireCount.Load())
	require.Zero(t, control.rebalanceCount.Load())
}

func TestRefillLeaseAtLowWatermarkRebalancesAfterStaleAcquire(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "post-usage-rebalance")
	lowLease := LeaseSnapshot{
		LeaseID: "lease-rebalance", UserID: 42, Region: "sg",
		AllocatedCents: 100, SpentCents: 90, Version: 1,
		ExpiresAt: time.Now().Add(time.Hour).Unix(), TargetCents: 100, RefillAtCents: 20,
	}
	require.NoError(t, quota.EnsureLease(ctx, lowLease))

	control := &leaseRefillControlStub{
		acquireFn: func(req AcquireLeaseRequest) (*AcquireLeaseResponse, error) {
			require.True(t, req.LowWatermark)
			return &AcquireLeaseResponse{OK: true, Lease: lowLease}, nil
		},
		rebalanceFn: func(req RebalanceLeaseRequest) (*RebalanceLeaseResponse, error) {
			require.True(t, req.LowWatermark)
			return &RebalanceLeaseResponse{OK: true, Lease: LeaseSnapshot{
				LeaseID: "lease-rebalance", UserID: 42, Region: "sg",
				AllocatedCents: 200, SpentCents: 90, Version: 2,
				ExpiresAt: lowLease.ExpiresAt, TargetCents: 100, RefillAtCents: 20,
			}}, nil
		},
	}

	refilled, err := RefillLeaseAtLowWatermark(ctx, control, quota, 42, "sg", "sg", "gateway_post_usage_refill")
	require.NoError(t, err)
	require.True(t, refilled)
	require.EqualValues(t, 1, control.acquireCount.Load())
	require.EqualValues(t, 1, control.rebalanceCount.Load())
	lease, found, err := quota.LoadLease(ctx, 42, "sg")
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 200, lease.AllocatedCents)
}

func TestRefillLeaseAtLowWatermarkReloadsAfterAcquireSnapshotConflict(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "post-usage-conflict")
	current := LeaseSnapshot{
		LeaseID: "lease-current", UserID: 42, Region: "sg",
		AllocatedCents: 100, SpentCents: 90, Version: 2,
		ExpiresAt: time.Now().Add(time.Hour).Unix(), TargetCents: 100, RefillAtCents: 20,
	}
	require.NoError(t, quota.EnsureLease(ctx, current))

	control := &leaseRefillControlStub{
		acquireFn: func(req AcquireLeaseRequest) (*AcquireLeaseResponse, error) {
			require.Equal(t, "lease-current", req.ObservedLeaseID)
			// Simulate a delayed AcquireLease response racing with a newer control
			// push already installed in Redis. Applying it must conflict safely.
			return &AcquireLeaseResponse{OK: true, Lease: LeaseSnapshot{
				LeaseID: "lease-stale-response", UserID: 42, Region: "sg",
				AllocatedCents: 200, Version: 1, ExpiresAt: current.ExpiresAt,
				TargetCents: 100, RefillAtCents: 20,
			}}, nil
		},
		rebalanceFn: func(req RebalanceLeaseRequest) (*RebalanceLeaseResponse, error) {
			require.True(t, req.LowWatermark)
			require.Equal(t, "lease-current", req.ObservedLeaseID)
			return &RebalanceLeaseResponse{OK: true, Lease: LeaseSnapshot{
				LeaseID: "lease-current", UserID: 42, Region: "sg",
				AllocatedCents: 200, SpentCents: 90, Version: 3,
				ExpiresAt: current.ExpiresAt, TargetCents: 100, RefillAtCents: 20,
			}}, nil
		},
	}

	refilled, err := RefillLeaseAtLowWatermark(ctx, control, quota, 42, "sg", "sg", "gateway_post_usage_refill")
	require.NoError(t, err)
	require.True(t, refilled)
	require.EqualValues(t, 1, control.acquireCount.Load())
	require.EqualValues(t, 1, control.rebalanceCount.Load())
	lease, found, err := quota.LoadLease(ctx, 42, "sg")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "lease-current", lease.LeaseID)
	require.EqualValues(t, 200, lease.AllocatedCents)
	require.EqualValues(t, 90, lease.SpentCents)
}
