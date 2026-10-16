package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	grokVideoTaskRunnerBatchSize   = 100
	grokVideoTaskRunnerConcurrency = 8
	grokVideoTaskPollTimeout       = 30 * time.Second
	grokVideoTaskCleanupInterval   = time.Hour
)

type grokVideoTaskAPIKeyReader interface {
	GetByID(ctx context.Context, id int64) (*APIKey, error)
}

type grokVideoTaskSubscriptionReader interface {
	GetByID(ctx context.Context, id int64) (*UserSubscription, error)
}

// GrokVideoTaskRunner polls tasks even when the original client never asks for
// status. PostgreSQL keeps the work queue durable; row leases prevent multiple
// gateway instances from polling or settling the same task concurrently.
type GrokVideoTaskRunner struct {
	store              GrokVideoTaskStore
	gateway            *OpenAIGatewayService
	apiKeyReader       grokVideoTaskAPIKeyReader
	subscriptionReader grokVideoTaskSubscriptionReader
	apiKeyQuotaUpdater APIKeyQuotaUpdater
	interval           time.Duration

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	wg        sync.WaitGroup

	lastCleanup time.Time
}

func NewGrokVideoTaskRunner(
	store GrokVideoTaskStore,
	gateway *OpenAIGatewayService,
	apiKeyReader grokVideoTaskAPIKeyReader,
	subscriptionReader grokVideoTaskSubscriptionReader,
	apiKeyQuotaUpdater APIKeyQuotaUpdater,
	interval time.Duration,
) *GrokVideoTaskRunner {
	if interval <= 0 {
		interval = GrokVideoTaskPollInterval
	}
	return &GrokVideoTaskRunner{
		store:              store,
		gateway:            gateway,
		apiKeyReader:       apiKeyReader,
		subscriptionReader: subscriptionReader,
		apiKeyQuotaUpdater: apiKeyQuotaUpdater,
		interval:           interval,
		stopCh:             make(chan struct{}),
	}
}

func (r *GrokVideoTaskRunner) Start() {
	if r == nil || r.store == nil || r.gateway == nil {
		return
	}
	r.startOnce.Do(func() {
		r.wg.Add(1)
		go r.loop()
	})
}

func (r *GrokVideoTaskRunner) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stopCh) })
	r.wg.Wait()
}

func (r *GrokVideoTaskRunner) loop() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.runOnce()
	for {
		select {
		case <-ticker.C:
			r.runOnce()
		case <-r.stopCh:
			return
		}
	}
}

func (r *GrokVideoTaskRunner) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	tasks, err := r.store.ListDue(ctx, time.Now().UTC(), grokVideoTaskRunnerBatchSize)
	cancel()
	if err != nil {
		logger.L().With(zap.String("component", "service.grok_video_task_runner")).Error("grok_video_task.list_due_failed", zap.Error(err))
		return
	}

	semaphore := make(chan struct{}, grokVideoTaskRunnerConcurrency)
	var workers sync.WaitGroup
	for _, task := range tasks {
		if task == nil {
			continue
		}
		select {
		case <-r.stopCh:
			workers.Wait()
			return
		case semaphore <- struct{}{}:
		}
		workers.Add(1)
		go func(task *GrokVideoTask) {
			defer workers.Done()
			defer func() { <-semaphore }()
			r.processTask(task)
		}(task)
	}
	workers.Wait()

	if r.lastCleanup.IsZero() || time.Since(r.lastCleanup) >= grokVideoTaskCleanupInterval {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		removed, cleanupErr := r.store.CleanupFinalized(cleanupCtx, time.Now().UTC().Add(-GrokVideoTaskFinalRetention))
		cleanupCancel()
		if cleanupErr != nil {
			logger.L().With(zap.String("component", "service.grok_video_task_runner")).Warn("grok_video_task.cleanup_failed", zap.Error(cleanupErr))
		} else {
			r.lastCleanup = time.Now()
			if removed > 0 {
				logger.L().With(zap.String("component", "service.grok_video_task_runner")).Info("grok_video_task.cleanup_completed", zap.Int64("removed", removed))
			}
		}
	}
}

func (r *GrokVideoTaskRunner) processTask(candidate *GrokVideoTask) {
	ctx, cancel := context.WithTimeout(context.Background(), grokVideoTaskPollTimeout)
	defer cancel()

	locked, err := r.store.TryLockPolling(ctx, candidate.RequestID, grokVideoTaskPollLockTTL)
	if err != nil || !locked {
		if err != nil {
			r.logTaskError(candidate, "grok_video_task.poll_lock_failed", err)
		}
		return
	}
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer unlockCancel()
		if unlockErr := r.store.UnlockPolling(unlockCtx, candidate.RequestID); unlockErr != nil {
			r.logTaskError(candidate, "grok_video_task.poll_unlock_failed", unlockErr)
		}
	}()

	task, err := r.store.Get(ctx, candidate.RequestID)
	if err != nil {
		if !errors.Is(err, ErrGrokVideoTaskNotFound) {
			r.logTaskError(candidate, "grok_video_task.reload_failed", err)
		}
		return
	}
	if !task.NeedsBackgroundWork() {
		return
	}
	if task.State == GrokVideoTaskStateFailed || task.State == GrokVideoTaskStateCancelled {
		r.finalizeTerminalTask(ctx, task, GrokVideoTaskStatus{State: task.State, Message: task.LastStatusMessage}, nil)
		return
	}
	if task.State != GrokVideoTaskStateSucceeded && time.Since(task.CreatedAt) >= GrokVideoTaskMaxPendingAge {
		r.finalizeTerminalTask(ctx, task, GrokVideoTaskStatus{
			State:   GrokVideoTaskStateFailed,
			Message: "video task status polling timed out",
		}, nil)
		return
	}

	if task.State == GrokVideoTaskStateSucceeded {
		r.settleTask(ctx, task)
		return
	}

	account, err := r.gateway.GetGrokVideoTaskAccount(ctx, task)
	if err != nil {
		r.deferTaskAfterError(ctx, task, "video task account is unavailable", err, GrokVideoTaskPollInterval)
		return
	}
	maxConcurrency := account.Concurrency
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	acquired, err := r.gateway.tryAcquireAccountSlot(ctx, account.ID, maxConcurrency)
	if err != nil {
		r.deferTaskAfterError(ctx, task, "video task account concurrency check failed", err, GrokVideoTaskPollInterval)
		return
	}
	if acquired == nil || !acquired.Acquired {
		r.deferTaskAfterError(ctx, task, "video task account is busy", nil, GrokVideoTaskPollInterval)
		return
	}
	if acquired.ReleaseFunc != nil {
		defer acquired.ReleaseFunc()
	}

	status, err := r.gateway.PollGrokVideoTaskStatus(ctx, account, task.RequestID)
	if err != nil {
		retryAfter := time.Duration(0)
		var pollErr *GrokVideoTaskPollError
		if errors.As(err, &pollErr) {
			retryAfter = pollErr.RetryAfter
		}
		r.deferTaskAfterError(ctx, task, "video task status request failed", err, retryAfter)
		return
	}
	if status.State == "" {
		r.deferTaskAfterError(ctx, task, "video task returned an unrecognized status", nil, GrokVideoTaskPollInterval)
		return
	}
	if status.State == GrokVideoTaskStateSucceeded || status.State == GrokVideoTaskStateFailed || status.State == GrokVideoTaskStateCancelled {
		r.settleTaskWithContext(ctx, task, status, account)
		return
	}
	if err := r.gateway.UpdateGrokVideoTaskStatus(ctx, task, status); err != nil {
		r.logTaskError(task, "grok_video_task.status_save_failed", err)
	}
}

func (r *GrokVideoTaskRunner) settleTask(ctx context.Context, task *GrokVideoTask) {
	account, err := r.gateway.GetGrokVideoTaskAccount(ctx, task)
	if err != nil {
		r.deferTaskAfterError(ctx, task, "video task account is unavailable for settlement", err, GrokVideoTaskPollInterval)
		return
	}
	r.settleTaskWithContext(ctx, task, GrokVideoTaskStatus{State: GrokVideoTaskStateSucceeded}, account)
}

func (r *GrokVideoTaskRunner) settleTaskWithContext(ctx context.Context, task *GrokVideoTask, status GrokVideoTaskStatus, account *Account) {
	if status.State == GrokVideoTaskStateFailed || status.State == GrokVideoTaskStateCancelled {
		r.finalizeTerminalTask(ctx, task, status, account)
		return
	}
	apiKey, subscription, updater, err := r.billingContext(ctx, task)
	if err != nil {
		r.deferTaskAfterError(ctx, task, "video task billing context is unavailable", err, GrokVideoTaskPollInterval)
		return
	}
	if err := r.gateway.FinalizeGrokVideoTask(ctx, task, status, apiKey, subscription, account, updater); err != nil {
		r.deferTaskAfterError(ctx, task, "video task settlement failed", err, 0)
	}
}

func (r *GrokVideoTaskRunner) finalizeTerminalTask(ctx context.Context, task *GrokVideoTask, status GrokVideoTaskStatus, account *Account) {
	if err := r.gateway.FinalizeGrokVideoTask(ctx, task, status, nil, nil, account, nil); err != nil {
		r.deferTaskAfterError(ctx, task, "video task quota release failed", err, 0)
	}
}

func (r *GrokVideoTaskRunner) billingContext(ctx context.Context, task *GrokVideoTask) (*APIKey, *UserSubscription, APIKeyQuotaUpdater, error) {
	if task == nil || task.UserID <= 0 {
		return nil, nil, nil, errors.New("invalid task billing identity")
	}

	var apiKey *APIKey
	var updater APIKeyQuotaUpdater
	if task.APIKeyID > 0 && r.apiKeyReader != nil {
		loaded, err := r.apiKeyReader.GetByID(ctx, task.APIKeyID)
		if err == nil && loaded != nil {
			apiKey = loaded
			updater = r.apiKeyQuotaUpdater
		} else if err != nil && !errors.Is(err, ErrAPIKeyNotFound) {
			return nil, nil, nil, fmt.Errorf("load api key %d: %w", task.APIKeyID, err)
		}
	}
	if apiKey == nil {
		// A deleted key must not erase an already incurred upstream cost. The
		// synthetic key preserves the immutable billing identity while skipping
		// future per-key quota counters that no longer have a backing row.
		apiKey = &APIKey{ID: task.APIKeyID, UserID: task.UserID, Status: StatusAPIKeyDisabled}
	}
	if apiKey.UserID != 0 && apiKey.UserID != task.UserID {
		return nil, nil, nil, errors.New("video task api key owner changed")
	}
	apiKey.UserID = task.UserID
	if apiKey.User == nil {
		apiKey.User = &User{ID: task.UserID}
	} else if apiKey.User.ID != task.UserID {
		return nil, nil, nil, errors.New("video task api key user mismatch")
	}
	if task.GroupSnapshot != nil {
		apiKey.Group = task.GroupSnapshot.Group()
		groupID := task.GroupSnapshot.ID
		apiKey.GroupID = &groupID
	} else if task.GroupID != nil {
		groupID := *task.GroupID
		apiKey.GroupID = &groupID
		if apiKey.Group == nil || apiKey.Group.ID != groupID {
			apiKey.Group = &Group{ID: groupID, Platform: PlatformGrok, Status: StatusActive, Hydrated: true, RateMultiplier: 1}
		}
	}

	var subscription *UserSubscription
	if task.SubscriptionID != nil && *task.SubscriptionID > 0 {
		if r.subscriptionReader != nil {
			loaded, err := r.subscriptionReader.GetByID(ctx, *task.SubscriptionID)
			if err == nil {
				subscription = loaded
			} else {
				logger.L().With(
					zap.String("component", "service.grok_video_task_runner"),
					zap.String("request_id", task.RequestID),
					zap.Int64("subscription_id", *task.SubscriptionID),
				).Warn("grok_video_task.subscription_reload_failed", zap.Error(err))
			}
		}
		if subscription == nil {
			groupID := int64(0)
			if task.GroupID != nil {
				groupID = *task.GroupID
			}
			subscription = &UserSubscription{ID: *task.SubscriptionID, UserID: task.UserID, GroupID: groupID}
		}
	}
	return apiKey, subscription, updater, nil
}

func (r *GrokVideoTaskRunner) deferTaskAfterError(ctx context.Context, task *GrokVideoTask, message string, cause error, retryAfter time.Duration) {
	if task == nil {
		return
	}
	detail := message
	if cause != nil {
		detail = fmt.Sprintf("%s: %v", message, cause)
	}
	task.RecordPollFailure(detail, retryAfter)
	if err := r.store.Save(ctx, task); err != nil {
		r.logTaskError(task, "grok_video_task.retry_schedule_failed", err)
	}
	if cause != nil {
		r.logTaskError(task, "grok_video_task.processing_deferred", cause)
	}
}

func (r *GrokVideoTaskRunner) logTaskError(task *GrokVideoTask, event string, err error) {
	fields := []zap.Field{
		zap.String("component", "service.grok_video_task_runner"),
		zap.String("event", event),
		zap.Error(err),
	}
	if task != nil {
		fields = append(fields,
			zap.String("request_id", task.RequestID),
			zap.Int64("account_id", task.AccountID),
			zap.Int64("api_key_id", task.APIKeyID),
			zap.String("state", task.State),
		)
	}
	logger.L().Error("grok_video_task.runner_error", fields...)
}
