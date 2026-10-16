package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
)

const (
	upstreamBillingProbeRunnerTick        = time.Minute
	upstreamBillingProbeLeaderTTL         = 75 * time.Second
	upstreamBillingProbeLeaderRenew       = 20 * time.Second
	upstreamBillingProbeLeaderCallTimeout = 3 * time.Second
)

type upstreamBillingProbeDueLister interface {
	ListDueUpstreamBillingProbeAccountIDs(context.Context, time.Time, int) ([]int64, error)
}

type upstreamBillingProbeExecutor interface {
	ProbeAccount(context.Context, int64) (*UpstreamBillingProbeSnapshot, error)
}

// UpstreamBillingProbeRunner runs outside the customer request path. It is
// fail-closed: without a renewable Redis leader lock it performs no DB scan and
// no upstream request, so a Redis outage cannot create a multi-node probe storm.
type UpstreamBillingProbeRunner struct {
	lister upstreamBillingProbeDueLister
	probe  upstreamBillingProbeExecutor
	lock   RenewableLeaderLockCache

	lockKey       string
	owner         string
	tickInterval  time.Duration
	lockTTL       time.Duration
	renewInterval time.Duration
	now           func() time.Time

	startOnce sync.Once
	stopOnce  sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func NewUpstreamBillingProbeRunner(
	accountRepo AccountRepository,
	probe *UpstreamBillingProbeService,
	lockCache LeaderLockCache,
	cfg *config.Config,
) *UpstreamBillingProbeRunner {
	lister, _ := accountRepo.(upstreamBillingProbeDueLister)
	renewable, _ := lockCache.(RenewableLeaderLockCache)
	prefix := ""
	if cfg != nil {
		prefix = strings.Trim(strings.TrimSpace(cfg.GatewayLite.RedisPrefix), ":")
	}
	if prefix != "" {
		prefix += ":"
	}
	return &UpstreamBillingProbeRunner{
		lister:        lister,
		probe:         probe,
		lock:          renewable,
		lockKey:       prefix + "upstream_billing_probe:leader",
		owner:         uuid.NewString(),
		tickInterval:  upstreamBillingProbeRunnerTick,
		lockTTL:       upstreamBillingProbeLeaderTTL,
		renewInterval: upstreamBillingProbeLeaderRenew,
		now:           time.Now,
	}
}

func (r *UpstreamBillingProbeRunner) Start() {
	if r == nil || r.lister == nil || r.probe == nil || r.lock == nil {
		return
	}
	r.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		r.cancel = cancel
		r.wg.Add(1)
		go r.loop(ctx)
	})
}

func (r *UpstreamBillingProbeRunner) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
	})
	r.wg.Wait()
}

func (r *UpstreamBillingProbeRunner) loop(ctx context.Context) {
	defer r.wg.Done()
	r.runOnce(ctx)
	ticker := time.NewTicker(r.tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runOnce(ctx)
		}
	}
}

func (r *UpstreamBillingProbeRunner) runOnce(parent context.Context) {
	settings := CurrentUpstreamBillingRuntimeSettings()
	// 总开关关闭时必须做到零 Redis、零数据库、零 HTTP。
	if !settings.AutoProbeEnabled || r == nil || r.lock == nil || r.lister == nil || r.probe == nil {
		return
	}
	lockCtx, lockCancel := context.WithTimeout(parent, upstreamBillingProbeLeaderCallTimeout)
	acquired, err := r.lock.TryAcquireLeaderLock(lockCtx, r.lockKey, r.owner, r.lockTTL)
	lockCancel()
	if err != nil || !acquired {
		if err != nil {
			logger.LegacyPrintf("service.upstream_billing_probe", "auto probe leader lock failed; skipping cycle: %v", err)
		}
		return
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), upstreamBillingProbeLeaderCallTimeout)
		defer cancel()
		_ = r.lock.ReleaseLeaderLock(releaseCtx, r.lockKey, r.owner)
	}()

	batchCtx, batchCancel := context.WithCancel(parent)
	defer batchCancel()
	renewDone := make(chan struct{})
	go r.renewLeadership(batchCtx, batchCancel, renewDone)
	defer func() {
		batchCancel()
		<-renewDone
	}()

	ids, err := r.lister.ListDueUpstreamBillingProbeAccountIDs(batchCtx, r.currentTime().UTC(), settings.ProbeBatchSize)
	if err != nil || batchCtx.Err() != nil {
		if err != nil {
			logger.LegacyPrintf("service.upstream_billing_probe", "list due accounts failed: %v", err)
		}
		return
	}
	if len(ids) > settings.ProbeBatchSize {
		ids = ids[:settings.ProbeBatchSize]
	}
	workers := settings.ProbeConcurrency
	if workers < 1 {
		workers = 1
	}
	if workers > maxUpstreamBillingProbeConcurrency {
		workers = maxUpstreamBillingProbeConcurrency
	}
	semaphore := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, accountID := range ids {
		select {
		case <-batchCtx.Done():
			wg.Wait()
			return
		case semaphore <- struct{}{}:
		}
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			defer func() { <-semaphore }()
			if _, probeErr := r.probe.ProbeAccount(batchCtx, id); probeErr != nil && batchCtx.Err() == nil {
				logger.LegacyPrintf("service.upstream_billing_probe", "auto probe account %d failed: %v", id, probeErr)
			}
		}(accountID)
	}
	wg.Wait()
}

func (r *UpstreamBillingProbeRunner) renewLeadership(ctx context.Context, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(r.renewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewCtx, renewCancel := context.WithTimeout(ctx, upstreamBillingProbeLeaderCallTimeout)
			owned, err := r.lock.RenewLeaderLock(renewCtx, r.lockKey, r.owner, r.lockTTL)
			renewCancel()
			if err != nil || !owned {
				// 续租失败立即取消当前批次；禁止继续领取或启动新的探测。
				cancel()
				return
			}
		}
	}
}

func (r *UpstreamBillingProbeRunner) currentTime() time.Time {
	if r != nil && r.now != nil {
		return r.now()
	}
	return time.Now()
}
