package middleware

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayLiteExhaustedLeaseCoalescesConcurrentRefreshAndKeepsReservationsIndependent(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), PoolSize: 16})
	// miniredis 用 Go 内 Lua 执行器；限制同时建立的连接，避免模拟器开销挤占真实 3s 续租边界。
	client.AddHook(leaseRefreshRedisContextHook{})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "lease-coalesce")
	const (
		userID = int64(910001)
		region = "coalesce-1"
		count  = 200
	)
	expiresAt := time.Now().Add(time.Hour).Unix()
	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID: "lease-coalesce", UserID: userID, Region: region,
		AllocatedCents: 1, SpentCents: 1, Version: 1, ExpiresAt: expiresAt,
	}))

	var acquireCount atomic.Int32
	acquireStarted := make(chan struct{})
	releaseAcquire := make(chan struct{})
	var startedOnce sync.Once
	stub := gatewayLiteControlStub{
		acquireCount: &acquireCount,
		acquireRespFn: func(_ gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error) {
			startedOnce.Do(func() { close(acquireStarted) })
			<-releaseAcquire
			return &gatewaylite.AcquireLeaseResponse{OK: true, Lease: gatewaylite.LeaseSnapshot{
				LeaseID: "lease-coalesce", UserID: userID, Region: region,
				AllocatedCents: 1001, SpentCents: 1, Version: 2, ExpiresAt: expiresAt,
			}}, nil
		},
	}
	key := gatewaylite.KeySnapshot{KeyID: "key-coalesce", UserID: userID}
	joined := make(chan struct{}, count)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseAcquire) }) }
	defer release()
	start := make(chan struct{})
	errorsCh := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			waiterCtx := &leaseRefreshJoinedContext{Context: ctx, joined: joined}
			lease, err := gatewayLiteReserveQuota(waiterCtx, stub, quota, key, region, fmt.Sprintf("coalesced-%d", index), 1)
			if err != nil {
				errorsCh <- err
				return
			}
			if lease == nil || lease.LeaseID != "lease-coalesce" {
				errorsCh <- fmt.Errorf("unexpected lease: %+v", lease)
			}
		}(i)
	}
	close(start)
	select {
	case <-acquireStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the shared lease refresh")
	}
	// 只在所有真实预留入口均加入同一次共享续租后放行，不让机器调度速度决定波次。
	waitForLeaseRefreshWaiters(t, joined, count)
	release()
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}

	require.EqualValues(t, 1, acquireCount.Load(), "同一用户和网关只能请求一次主站续租")
	lease, ok, err := quota.LoadLease(ctx, userID, region)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, count, lease.ReservedCents)
	requestKeys, err := client.Keys(ctx, "lease-coalesce:req:coalesced-*").Result()
	require.NoError(t, err)
	require.Len(t, requestKeys, count, "每个请求必须保留独立的金额预留记录")
}

func TestGatewayLiteLeaseRefreshWaiterCancellationDoesNotCancelSharedRefresh(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), PoolSize: 16})
	// miniredis 用 Go 内 Lua 执行器；限制同时建立的连接，避免模拟器开销挤占真实 3s 续租边界。
	client.AddHook(leaseRefreshRedisContextHook{})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "lease-cancel")
	const (
		userID = int64(910002)
		region = "cancel-1"
	)
	expiresAt := time.Now().Add(time.Hour).Unix()
	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID: "lease-cancel", UserID: userID, Region: region,
		AllocatedCents: 1, SpentCents: 1, Version: 1, ExpiresAt: expiresAt,
	}))

	var acquireCount atomic.Int32
	acquireStarted := make(chan struct{})
	releaseAcquire := make(chan struct{})
	var startedOnce sync.Once
	stub := gatewayLiteControlStub{
		acquireCount: &acquireCount,
		acquireRespFn: func(_ gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error) {
			startedOnce.Do(func() { close(acquireStarted) })
			<-releaseAcquire
			return &gatewaylite.AcquireLeaseResponse{OK: true, Lease: gatewaylite.LeaseSnapshot{
				LeaseID: "lease-cancel", UserID: userID, Region: region,
				AllocatedCents: 11, SpentCents: 1, Version: 2, ExpiresAt: expiresAt,
			}}, nil
		},
	}
	key := gatewaylite.KeySnapshot{KeyID: "key-cancel", UserID: userID}
	joined := make(chan struct{}, 2)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseAcquire) }) }
	defer release()
	canceledCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	firstResult := make(chan error, 1)
	go func() {
		waiterCtx := &leaseRefreshJoinedContext{Context: canceledCtx, joined: joined}
		_, err := gatewayLiteReserveQuota(waiterCtx, stub, quota, key, region, "cancel-first", 1)
		firstResult <- err
	}()
	select {
	case <-acquireStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for lease refresh")
	}
	secondResult := make(chan error, 1)
	go func() {
		waiterCtx := &leaseRefreshJoinedContext{Context: ctx, joined: joined}
		_, err := gatewayLiteReserveQuota(waiterCtx, stub, quota, key, region, "cancel-second", 1)
		secondResult <- err
	}()
	waitForLeaseRefreshWaiters(t, joined, 2)
	cancel()
	require.ErrorIs(t, <-firstResult, context.Canceled)
	release()
	require.NoError(t, <-secondResult)
	require.EqualValues(t, 1, acquireCount.Load())

	lease, ok, err := quota.LoadLease(ctx, userID, region)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 1, lease.ReservedCents, "取消的等待请求不能产生金额预留")
}

func TestGatewayLiteLeaseRefreshFailureIsSharedWithoutReservations(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), PoolSize: 16})
	// miniredis 用 Go 内 Lua 执行器；限制同时建立的连接，避免模拟器开销挤占真实 3s 续租边界。
	client.AddHook(leaseRefreshRedisContextHook{})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "lease-failure")
	const (
		userID = int64(910003)
		region = "failure-1"
		count  = 64
	)
	expiresAt := time.Now().Add(time.Hour).Unix()
	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID: "lease-failure", UserID: userID, Region: region,
		AllocatedCents: 1, SpentCents: 1, Version: 1, ExpiresAt: expiresAt,
	}))

	expectedErr := errors.New("control plane unavailable")
	var acquireCount atomic.Int32
	acquireStarted := make(chan struct{})
	releaseAcquire := make(chan struct{})
	var startedOnce sync.Once
	stub := gatewayLiteControlStub{
		acquireCount: &acquireCount,
		acquireRespFn: func(_ gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error) {
			startedOnce.Do(func() { close(acquireStarted) })
			<-releaseAcquire
			return nil, expectedErr
		},
	}
	key := gatewaylite.KeySnapshot{KeyID: "key-failure", UserID: userID}
	results := make(chan error, count)
	joined := make(chan struct{}, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			// Hook 只剥离 Redis 内部的观察包装，外层 Done 在 DoChan 注册后给出入队信号。
			waiterCtx := &leaseRefreshJoinedContext{Context: ctx, joined: joined}
			_, err := gatewayLiteReserveQuota(waiterCtx, stub, quota, key, region, fmt.Sprintf("failure-%d", index), 1)
			results <- err
		}(i)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseAcquire) }) }
	defer release()
	waitForLeaseRefreshWaiters(t, joined, count)
	select {
	case <-acquireStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for lease refresh")
	}
	release()
	wg.Wait()
	close(results)
	for err := range results {
		require.ErrorIs(t, err, expectedErr)
	}
	require.EqualValues(t, 1, acquireCount.Load())
	requestKeys, err := client.Keys(ctx, "lease-failure:req:failure-*").Result()
	require.NoError(t, err)
	require.Empty(t, requestKeys)
}

type leaseRefreshJoinedContext struct {
	context.Context
	joined chan<- struct{}
	once   sync.Once
}

func (c *leaseRefreshJoinedContext) Done() <-chan struct{} {
	c.once.Do(func() { c.joined <- struct{}{} })
	return c.Context.Done()
}

// Redis 也会读取 Context.Done。仅在测试客户端的内部操作使用原 Context，
// 保留取消与截止时间语义，同时让外层观察精确指向 singleflight 的 DoChan 注册之后。
// Hook 不修改命令、响应、金额、连接池超时或生产共享续租逻辑。
type leaseRefreshRedisContextHook struct{}

func (leaseRefreshRedisContextHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(leaseRefreshRedisContext(ctx), network, addr)
	}
}

func (leaseRefreshRedisContextHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		return next(leaseRefreshRedisContext(ctx), cmd)
	}
}

func (leaseRefreshRedisContextHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		return next(leaseRefreshRedisContext(ctx), cmds)
	}
}

func leaseRefreshRedisContext(ctx context.Context) context.Context {
	if waiter, ok := ctx.(*leaseRefreshJoinedContext); ok {
		return waiter.Context
	}
	return ctx
}

func waitForLeaseRefreshWaiters(t *testing.T, joined <-chan struct{}, count int) {
	t.Helper()
	timer := time.NewTimer(gatewayLiteLeaseRefreshTimeout)
	defer timer.Stop()
	for i := range count {
		select {
		case <-joined:
		case <-timer.C:
			t.Fatalf("timed out waiting for lease refresh waiters: joined %d/%d", i, count)
		}
	}
}

func TestGatewayLiteLeaseRefreshDoesNotStartSecondWaveWhenSharedCapacityIsConsumed(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), PoolSize: 16})
	// miniredis 用 Go 内 Lua 执行器；限制同时建立的连接，避免模拟器开销挤占真实 3s 续租边界。
	client.AddHook(leaseRefreshRedisContextHook{})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "lease-capacity")
	const (
		userID = int64(910004)
		region = "capacity-1"
	)
	expiresAt := time.Now().Add(time.Hour).Unix()
	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID: "lease-capacity", UserID: userID, Region: region,
		AllocatedCents: 1, SpentCents: 1, Version: 1, ExpiresAt: expiresAt,
	}))

	var acquireCount atomic.Int32
	acquireStarted := make(chan struct{})
	releaseAcquire := make(chan struct{})
	var startedOnce sync.Once
	stub := gatewayLiteControlStub{
		acquireCount: &acquireCount,
		acquireRespFn: func(_ gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error) {
			startedOnce.Do(func() { close(acquireStarted) })
			<-releaseAcquire
			return &gatewaylite.AcquireLeaseResponse{OK: true, Lease: gatewaylite.LeaseSnapshot{
				LeaseID: "lease-capacity", UserID: userID, Region: region,
				AllocatedCents: 2, SpentCents: 1, Version: 2, ExpiresAt: expiresAt,
			}}, nil
		},
	}
	key := gatewaylite.KeySnapshot{KeyID: "key-capacity", UserID: userID}
	joined := make(chan struct{}, 2)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseAcquire) }) }
	defer release()
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for i := 0; i < 2; i++ {
		go func(index int) {
			ready.Done()
			<-start
			waiterCtx := &leaseRefreshJoinedContext{Context: ctx, joined: joined}
			_, err := gatewayLiteReserveQuota(waiterCtx, stub, quota, key, region, fmt.Sprintf("capacity-%d", index), 1)
			results <- err
		}(i)
	}
	ready.Wait()
	close(start)
	select {
	case <-acquireStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for lease refresh")
	}
	waitForLeaseRefreshWaiters(t, joined, 2)
	release()

	successes := 0
	rejections := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			successes++
			continue
		}
		var quotaErr gatewayLiteQuotaRejectedError
		require.ErrorAs(t, err, &quotaErr)
		require.Equal(t, "usage_limit_exceeded", quotaErr.Code)
		rejections++
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, rejections)
	require.EqualValues(t, 1, acquireCount.Load(), "容量被同批请求用完后不能发起第二波续租")
}

func TestGatewayLiteLeaseRefreshWithRealRedisAt1000Concurrency(t *testing.T) {
	redisAddr := strings.TrimSpace(os.Getenv("GATEWAY_LITE_TEST_REDIS_ADDR"))
	redisURL := strings.TrimSpace(os.Getenv("TOKENAV_TEST_REDIS_URL"))
	options := &redis.Options{Addr: redisAddr}
	if redisAddr == "" {
		if redisURL == "" {
			t.Skip("set GATEWAY_LITE_TEST_REDIS_ADDR or TOKENAV_TEST_REDIS_URL to run the real Redis concurrency gate")
		}
		var err error
		options, err = redis.ParseURL(redisURL)
		// URL 可能包含本机私密凭据，解析失败只报告错误类型，不输出原 URL。
		if err != nil {
			t.Fatal("TOKENAV_TEST_REDIS_URL is invalid")
		}
	}
	// 固定生产配置量级的连接池，避免默认随 CPU 核数增长的池在千请求启动时突建大量连接。
	// 请求并发仍为1000，所有 Redis/共享续租超时保持原值。
	options.PoolSize = 128
	// 随机十六进制前缀不含 Redis 模式通配符，且不会与其他测试轮次共用命名空间。
	var prefixID [16]byte
	if _, err := rand.Read(prefixID[:]); err != nil {
		t.Fatal("real Redis test prefix generation failed")
	}
	prefix := fmt.Sprintf("lease-real-%x", prefixID)
	ctx := context.Background()
	client := redis.NewClient(options)
	// 先注册收尾，Ping 或后续断言提前失败也必须清理并关闭本轮客户端。
	t.Cleanup(func() {
		require.NoError(t, cleanupLeaseRefreshRedis(client, prefix))
	})
	client.AddHook(leaseRefreshRedisContextHook{})
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal("real Redis ping failed")
	}

	quota := gatewaylite.NewRedisQuota(client, prefix)
	const (
		userID = int64(910005)
		region = "real-redis-1"
		count  = 1000
	)
	expiresAt := time.Now().Add(time.Hour).Unix()
	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID: "lease-real", UserID: userID, Region: region,
		AllocatedCents: 1, SpentCents: 1, Version: 1, ExpiresAt: expiresAt,
	}))

	var acquireCount atomic.Int32
	joined := make(chan struct{}, count)
	releaseAcquire := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseAcquire) }) }
	defer release()
	stub := gatewayLiteControlStub{
		acquireCount: &acquireCount,
		acquireRespFn: func(_ gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error) {
			<-releaseAcquire
			return &gatewaylite.AcquireLeaseResponse{OK: true, Lease: gatewaylite.LeaseSnapshot{
				LeaseID: "lease-real", UserID: userID, Region: region,
				AllocatedCents: count + 1, SpentCents: 1, Version: 2, ExpiresAt: expiresAt,
			}}, nil
		},
	}
	key := gatewaylite.KeySnapshot{KeyID: "key-real", UserID: userID}
	start := make(chan struct{})
	errorsCh := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			waiterCtx := &leaseRefreshJoinedContext{Context: ctx, joined: joined}
			_, err := gatewayLiteReserveQuota(waiterCtx, stub, quota, key, region, fmt.Sprintf("real-%d", index), 1)
			if err != nil {
				errorsCh <- err
			}
		}(i)
	}
	close(start)
	waitForLeaseRefreshWaiters(t, joined, count)
	release()
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}

	require.EqualValues(t, 1, acquireCount.Load(), "1000 并发只能触发一次主站续租")
	lease, ok, err := quota.LoadLease(ctx, userID, region)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, count, lease.ReservedCents)
	require.LessOrEqual(t, lease.ReservedCents+lease.SpentCents, lease.AllocatedCents)
	requestKeys, err := client.Keys(ctx, prefix+":req:real-*").Result()
	require.NoError(t, err)
	require.Len(t, requestKeys, count)
}

// 仅供真实 Redis 测试收尾使用；接口允许纯测试注入失败，避免依赖外部 Redis。
type leaseRefreshRedisCleanupClient interface {
	Keys(context.Context, string) *redis.StringSliceCmd
	Del(context.Context, ...string) *redis.IntCmd
	Close() error
}

func cleanupLeaseRefreshRedis(client leaseRefreshRedisCleanupClient, prefix string) (cleanupErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 关闭必须晚于清理和复查，且任一阶段失败也不能跳过关闭。
	originalClient := client
	defer func() {
		if err := originalClient.Close(); err != nil {
			cleanupErr = errors.Join(cleanupErr, errors.New("real Redis cleanup close failed"))
		}
	}()
	// 只在收尾使用遵守上下文截止时间的客户端，不改变千并发原客户端的生产预算。
	if realClient, ok := client.(*redis.Client); ok {
		options := *realClient.Options()
		options.ContextTimeoutEnabled = true
		cleanupClient := redis.NewClient(&options)
		client = cleanupClient
		defer func() {
			if err := cleanupClient.Close(); err != nil {
				cleanupErr = errors.Join(cleanupErr, errors.New("real Redis cleanup close failed"))
			}
		}()
	}
	// 防止测试 helper 的误用将模式扩大到其他轮次；错误不包含连接信息或键名。
	if prefix == "" || strings.ContainsAny(prefix, "*?[]\\") {
		return errors.New("real Redis cleanup prefix is invalid")
	}
	keys, err := client.Keys(ctx, prefix+":*").Result()
	if err != nil {
		cleanupErr = errors.New("real Redis cleanup key lookup failed")
	} else if len(keys) > 0 {
		owned := true
		for _, key := range keys {
			if !strings.HasPrefix(key, prefix+":") {
				owned = false
				break
			}
		}
		if !owned {
			cleanupErr = errors.New("real Redis cleanup returned keys outside its prefix")
		} else if err := client.Del(ctx, keys...).Err(); err != nil {
			cleanupErr = errors.New("real Redis cleanup key deletion failed")
		}
	}
	// 查询或删除失败仍复查，保留所有失败；只删除首次准确查询到的本轮键。
	remaining, err := client.Keys(ctx, prefix+":*").Result()
	if err != nil {
		cleanupErr = errors.Join(cleanupErr, errors.New("real Redis cleanup verification failed"))
	} else if len(remaining) > 0 {
		cleanupErr = errors.Join(cleanupErr, errors.New("real Redis cleanup prefix keys remain"))
	}
	if ctx.Err() != nil {
		cleanupErr = errors.Join(cleanupErr, errors.New("real Redis cleanup deadline exceeded"))
	}
	return cleanupErr
}

func TestGatewayLiteLeaseRefreshRedisCleanupRemovesOnlyOwnPrefixBeforeClose(t *testing.T) {
	mr := miniredis.RunT(t)
	const prefix = "lease-cleanup"
	require.NoError(t, mr.Set(prefix+":lease:1", "own lease"))
	require.NoError(t, mr.Set(prefix+":req:1", "own reservation"))
	require.NoError(t, mr.Set(prefix+"-other:req:1", "other test"))
	require.NoError(t, mr.Set("unrelated:key", "other data"))
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	// 子测试结束后执行真实 Cleanup，模拟 Ping 之后的提前返回。
	require.True(t, t.Run("early_return", func(t *testing.T) {
		t.Cleanup(func() {
			require.NoError(t, cleanupLeaseRefreshRedis(client, prefix))
		})
		require.NoError(t, client.Ping(context.Background()).Err())
	}))
	require.Equal(t, []string{prefix + "-other:req:1", "unrelated:key"}, mr.Keys())
	require.ErrorIs(t, client.Ping(context.Background()).Err(), redis.ErrClosed)
}

func TestGatewayLiteLeaseRefreshRedisCleanupPropagatesSafeFailuresAndClosesLast(t *testing.T) {
	const prefix = "lease-cleanup"
	sensitiveErr := errors.New("sensitive cleanup details")
	for _, tc := range []struct {
		name       string
		keys       []string
		lookupErr  error
		deleteErr  error
		verifyErr  error
		remaining  []string
		closeErr   error
		wantErr    string
		wantDelete bool
	}{
		{name: "empty_prefix_keys"},
		{name: "lookup_failure", lookupErr: sensitiveErr, wantErr: "real Redis cleanup key lookup failed"},
		{name: "delete_failure", keys: []string{prefix + ":req:1"}, deleteErr: sensitiveErr,
			wantErr: "real Redis cleanup key deletion failed", wantDelete: true},
		{name: "verification_failure", keys: []string{prefix + ":req:1"}, verifyErr: sensitiveErr,
			wantErr: "real Redis cleanup verification failed", wantDelete: true},
		{name: "remaining_keys", keys: []string{prefix + ":req:1"}, remaining: []string{prefix + ":req:1"},
			wantErr: "real Redis cleanup prefix keys remain", wantDelete: true},
		{name: "close_failure", keys: []string{prefix + ":req:1"}, closeErr: sensitiveErr,
			wantErr: "real Redis cleanup close failed", wantDelete: true},
		{name: "multiple_failures", keys: []string{prefix + ":req:1"}, deleteErr: sensitiveErr,
			verifyErr: sensitiveErr, closeErr: sensitiveErr, wantDelete: true,
			wantErr: "real Redis cleanup key deletion failed\nreal Redis cleanup verification failed\nreal Redis cleanup close failed"},
		{name: "foreign_key_response", keys: []string{prefix + ":req:1", prefix + "-other:req:1"},
			wantErr: "real Redis cleanup returned keys outside its prefix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			var cleanupCtx context.Context
			lookups := 0
			checkContext := func(ctx context.Context) {
				t.Helper()
				if cleanupCtx == nil {
					cleanupCtx = ctx
					deadline, ok := ctx.Deadline()
					require.True(t, ok)
					require.WithinDuration(t, time.Now().Add(10*time.Second), deadline, time.Second)
				}
				require.Equal(t, cleanupCtx, ctx, "查询、删除和复查必须共用一个有界上下文")
				require.NoError(t, ctx.Err())
			}
			client := leaseRefreshRedisCleanupFake{
				keys: func(ctx context.Context, pattern string) *redis.StringSliceCmd {
					checkContext(ctx)
					require.Equal(t, prefix+":*", pattern)
					calls = append(calls, "keys")
					lookups++
					cmd := redis.NewStringSliceCmd(ctx)
					if lookups == 1 {
						cmd.SetVal(tc.keys)
						cmd.SetErr(tc.lookupErr)
					} else {
						cmd.SetVal(tc.remaining)
						cmd.SetErr(tc.verifyErr)
					}
					return cmd
				},
				del: func(ctx context.Context, keys ...string) *redis.IntCmd {
					checkContext(ctx)
					require.Equal(t, tc.keys, keys)
					calls = append(calls, "del")
					cmd := redis.NewIntCmd(ctx)
					cmd.SetErr(tc.deleteErr)
					return cmd
				},
				close: func() error {
					calls = append(calls, "close")
					return tc.closeErr
				},
			}
			err := cleanupLeaseRefreshRedis(client, prefix)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, tc.wantErr)
			}
			wantCalls := []string{"keys", "keys", "close"}
			if tc.wantDelete {
				wantCalls = []string{"keys", "del", "keys", "close"}
			}
			require.Equal(t, wantCalls, calls)
			require.ErrorIs(t, cleanupCtx.Err(), context.Canceled)
		})
	}
}

type leaseRefreshRedisCleanupFake struct {
	keys  func(context.Context, string) *redis.StringSliceCmd
	del   func(context.Context, ...string) *redis.IntCmd
	close func() error
}

func (f leaseRefreshRedisCleanupFake) Keys(ctx context.Context, pattern string) *redis.StringSliceCmd {
	return f.keys(ctx, pattern)
}

func (f leaseRefreshRedisCleanupFake) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	return f.del(ctx, keys...)
}

func (f leaseRefreshRedisCleanupFake) Close() error {
	return f.close()
}
