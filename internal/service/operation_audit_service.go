package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	operationAuditQueueSize = 4096
	operationAuditBatchSize = 100
)

type OperationAuditService struct {
	repo OperationAuditRepository
	cfg  *config.Config

	queue  chan *OperationAuditLog
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once

	dropped       atomic.Uint64
	failed        atomic.Uint64
	written       atomic.Uint64
	cleaned       atomic.Uint64
	cleanupFailed atomic.Uint64
}

type OperationAuditStats struct {
	Queued        int    `json:"queued"`
	Written       uint64 `json:"written"`
	Dropped       uint64 `json:"dropped"`
	Failed        uint64 `json:"failed"`
	Cleaned       uint64 `json:"cleaned"`
	CleanupFailed uint64 `json:"cleanup_failed"`
}

func NewOperationAuditService(repo OperationAuditRepository, cfg *config.Config) *OperationAuditService {
	ctx, cancel := context.WithCancel(context.Background())
	return &OperationAuditService{
		repo:   repo,
		cfg:    cfg,
		queue:  make(chan *OperationAuditLog, operationAuditQueueSize),
		ctx:    ctx,
		cancel: cancel,
	}
}

func ProvideOperationAuditService(repo OperationAuditRepository, cfg *config.Config) *OperationAuditService {
	service := NewOperationAuditService(repo, cfg)
	service.Start()
	return service
}

func (s *OperationAuditService) Start() {
	if s == nil || s.repo == nil {
		return
	}
	s.once.Do(func() {
		s.wg.Add(2)
		go s.writerLoop()
		go s.retentionLoop()
	})
}

func (s *OperationAuditService) Stop() {
	if s == nil {
		return
	}
	s.cancel()
	s.wg.Wait()
}

// Record queues ordinary management operations; failures are observable but do not break availability.
func (s *OperationAuditService) Record(entry *OperationAuditLog) {
	if s == nil || entry == nil {
		return
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	entry.Summary = SanitizeOperationAuditSummary(entry.Summary)
	entry.Extra = SanitizeOperationAuditExtra(entry.Extra)
	select {
	case s.queue <- entry:
	default:
		s.dropped.Add(1)
		slog.Error("operation audit queue full", "action", entry.Action, "request_id", entry.RequestID)
	}
}

// BeginCritical persists an intent before a security-critical handler runs.
// If this fails, the caller must fail closed and must not execute the operation.
func (s *OperationAuditService) BeginCritical(ctx context.Context, entry *OperationAuditLog) (int64, error) {
	if s == nil || s.repo == nil || entry == nil {
		return 0, fmt.Errorf("operation audit service unavailable")
	}
	entry.Result = AuditResultPending
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	entry.Summary = SanitizeOperationAuditSummary(entry.Summary)
	entry.Extra = SanitizeOperationAuditExtra(entry.Extra)
	return s.repo.Insert(ctx, entry)
}

func (s *OperationAuditService) FinishCritical(ctx context.Context, id int64, result string, statusCode int, latency time.Duration, summary map[string]any) error {
	if s == nil || s.repo == nil || id <= 0 {
		return fmt.Errorf("operation audit service unavailable")
	}
	return s.repo.UpdateResult(ctx, id, result, statusCode, latency.Milliseconds(), SanitizeOperationAuditSummary(summary))
}

func (s *OperationAuditService) List(ctx context.Context, filter *OperationAuditFilter) (*OperationAuditList, error) {
	result, err := s.repo.List(ctx, filter)
	if err != nil || result == nil {
		return result, err
	}
	for _, item := range result.Items {
		if item != nil {
			item.Summary = SanitizeOperationAuditSummary(item.Summary)
			item.Extra = SanitizeOperationAuditExtra(item.Extra)
		}
	}
	return result, nil
}

func (s *OperationAuditService) GetByID(ctx context.Context, id int64) (*OperationAuditLog, error) {
	item, err := s.repo.GetByID(ctx, id)
	if err == nil && item != nil {
		item.Summary = SanitizeOperationAuditSummary(item.Summary)
		item.Extra = SanitizeOperationAuditExtra(item.Extra)
	}
	return item, err
}

func (s *OperationAuditService) ClearAllWithTrace(ctx context.Context, trace *OperationAuditLog) (int64, error) {
	if trace != nil {
		trace.Summary = SanitizeOperationAuditSummary(trace.Summary)
		trace.Extra = SanitizeOperationAuditExtra(trace.Extra)
	}
	return s.repo.ClearAllWithTrace(ctx, trace)
}

func (s *OperationAuditService) PreviewClear(ctx context.Context) (*OperationAuditClearPreview, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("operation audit service unavailable")
	}
	repo, ok := s.repo.(OperationAuditPreviewRepository)
	if !ok {
		return nil, fmt.Errorf("operation audit clear preview unavailable")
	}
	return repo.PreviewClear(ctx)
}

// Stats exposes value-only counters for health checks and tests without leaking audit content.
func (s *OperationAuditService) Stats() OperationAuditStats {
	if s == nil {
		return OperationAuditStats{}
	}
	return OperationAuditStats{
		Queued:        len(s.queue),
		Written:       s.written.Load(),
		Dropped:       s.dropped.Load(),
		Failed:        s.failed.Load(),
		Cleaned:       s.cleaned.Load(),
		CleanupFailed: s.cleanupFailed.Load(),
	}
}

func (s *OperationAuditService) writerLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	batch := make([]*OperationAuditLog, 0, operationAuditBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		count, err := s.repo.BatchInsert(ctx, batch)
		cancel()
		if err != nil {
			s.failed.Add(uint64(len(batch)))
			slog.Error("operation audit batch write failed", "error", err, "batch_size", len(batch))
		} else {
			s.written.Add(uint64(count))
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-s.ctx.Done():
			for {
				select {
				case item := <-s.queue:
					if item != nil {
						batch = append(batch, item)
					}
					if len(batch) >= operationAuditBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case item := <-s.queue:
			if item != nil {
				batch = append(batch, item)
			}
			if len(batch) >= operationAuditBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (s *OperationAuditService) retentionLoop() {
	defer s.wg.Done()
	interval := 24 * time.Hour
	days := 180
	batchSize := 5000
	if s.cfg != nil {
		if s.cfg.Security.AuditLogCleanupIntervalMinutes > 0 {
			interval = time.Duration(s.cfg.Security.AuditLogCleanupIntervalMinutes) * time.Minute
		}
		days = s.cfg.Security.AuditLogRetentionDays
		if s.cfg.Security.AuditLogCleanupBatchSize > 0 {
			batchSize = s.cfg.Security.AuditLogCleanupBatchSize
		}
	}
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return
	case <-timer.C:
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if days > 0 {
			cutoff := time.Now().UTC().AddDate(0, 0, -days)
			for {
				ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
				deleted, err := s.repo.DeleteBefore(ctx, cutoff, batchSize)
				cancel()
				if err != nil {
					s.cleanupFailed.Add(1)
					slog.Error("operation audit retention cleanup failed", "error", err)
					break
				}
				if deleted == 0 {
					break
				}
				s.cleaned.Add(uint64(deleted))
				slog.Info("operation audit retention cleanup completed", "deleted", deleted, "cutoff", cutoff)
				select {
				case <-s.ctx.Done():
					return
				default:
				}
			}
		}
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
