package gatewaylite

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	usageCommitBatchWorkers = 4
	usageCommitBatchSize    = 64
	usageCommitBatchDelay   = 250 * time.Microsecond
	usageCommitQueueSize    = 4096
)

type usageCommitBatchRequest struct {
	requestKey           string
	streamKey            string
	actualCents          int64
	body                 []byte
	suppressReportActual bool
	result               chan usageCommitBatchResult
}

type usageCommitBatchResult struct {
	actualCents int64
	refill      leaseRefillSignal
	err         error
}

// usageCommitBatcher 用 pipeline（流水线）批量发送彼此独立的原子 Lua 脚本。
// 请求仍要等自己的脚本得到 Redis 确认后才能返回，所以这里只降低连接争用，
// 不会把“先响应、后记账”的风险重新带回热路径。
type usageCommitBatcher struct {
	workers workerLifetime
	client *redis.Client
	queue  chan usageCommitBatchRequest
	once   sync.Once
}

func newUsageCommitBatcher(client *redis.Client) *usageCommitBatcher {
	if client == nil {
		return nil
	}
	return &usageCommitBatcher{
		client: client,
		queue:  make(chan usageCommitBatchRequest, usageCommitQueueSize),
	}
}

func (b *usageCommitBatcher) commit(
	ctx context.Context,
	requestKey string,
	streamKey string,
	actualCents int64,
	body []byte,
) (int64, error) {
	result, err := b.commitWithRefillSignal(ctx, requestKey, streamKey, actualCents, body, false)
	return result.actualCents, err
}

func (b *usageCommitBatcher) commitWithRefillSignal(
	ctx context.Context,
	requestKey string,
	streamKey string,
	actualCents int64,
	body []byte,
	suppressReportActual bool,
) (usageCommitBatchResult, error) {
	if b == nil || b.client == nil {
		return usageCommitBatchResult{}, errors.New("usage commit batcher is unavailable")
	}
	b.once.Do(func() {
		loadCtx, cancel := context.WithTimeout(context.Background(), defaultUsageReportTimeout)
		_, _ = commitAndQueueUsageScript.Load(loadCtx, b.client).Result()
		cancel()
		for range usageCommitBatchWorkers {
			b.workers.Go(context.Background(), b.run)
		}
	})
	result := make(chan usageCommitBatchResult, 1)
	request := usageCommitBatchRequest{
		requestKey:           requestKey,
		streamKey:            streamKey,
		actualCents:          actualCents,
		body:                 append([]byte(nil), body...),
		suppressReportActual: suppressReportActual,
		result:               result,
	}
	select {
	case <-b.workers.Context().Done():
		return usageCommitBatchResult{}, errors.New("usage commit batcher stopped")
	case b.queue <- request:
	case <-ctx.Done():
		return usageCommitBatchResult{}, ctx.Err()
	}
	select {
	case resolved := <-result:
		return resolved, resolved.err
	case <-ctx.Done():
		return usageCommitBatchResult{}, ctx.Err()
	}
}

func (b *usageCommitBatcher) run(ctx context.Context) {
	for {
		var first usageCommitBatchRequest
		select {
		case <-ctx.Done():
			return
		case first = <-b.queue:
		}
		batch := make([]usageCommitBatchRequest, 0, usageCommitBatchSize)
		batch = append(batch, first)
		timer := time.NewTimer(usageCommitBatchDelay)
	collect:
		for len(batch) < usageCommitBatchSize {
			select {
			case request := <-b.queue:
				batch = append(batch, request)
			case <-timer.C:
				break collect
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		b.flush(batch)
	}
}

func (b *usageCommitBatcher) flush(batch []usageCommitBatchRequest) {
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultUsageReportTimeout)
	defer cancel()
	pipe := b.client.Pipeline()
	commands := make([]*redis.Cmd, len(batch))
	for i, request := range batch {
		commands[i] = pipe.EvalSha(
			ctx,
			commitAndQueueUsageScript.Hash(),
			[]string{request.requestKey, request.streamKey},
			request.actualCents,
			request.body,
			boolToRedisInt(request.suppressReportActual),
		)
	}
	_, _ = pipe.Exec(ctx)
	for i, command := range commands {
		values, err := command.Slice()
		if err != nil && redis.HasErrorPrefix(err, "NOSCRIPT") {
			request := batch[i]
			values, err = commitAndQueueUsageScript.Run(
				ctx,
				b.client,
				[]string{request.requestKey, request.streamKey},
				request.actualCents,
				request.body,
				boolToRedisInt(request.suppressReportActual),
			).Slice()
		}
		result := usageCommitBatchResult{err: err}
		if err == nil {
			if len(values) == 0 {
				result.err = errors.New("empty usage commit result")
			} else {
				result.actualCents = redisScriptInt64(values[0])
				result.refill = parseLeaseRefillSignal(values)
			}
		}
		batch[i].result <- result
	}
}

func boolToRedisInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
