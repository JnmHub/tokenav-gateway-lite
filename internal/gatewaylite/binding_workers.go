package gatewaylite

import (
	"context"
	"sync"
)

// workerLifetime fences new work before cancel/join. Restart is refused if a
// generation fails to join, so an old destination cannot run beside a new one.
type workerLifetime struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	ctx     context.Context
	stopped bool
	wg      sync.WaitGroup
}

func (w *workerLifetime) Go(parent context.Context, fn func(context.Context)) {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	if w.ctx == nil {
		if parent == nil {
			parent = context.Background()
		}
		w.ctx, w.cancel = context.WithCancel(parent)
	}
	ctx := w.ctx
	w.wg.Add(1)
	w.mu.Unlock()
	go func() { defer w.wg.Done(); fn(ctx) }()
}
func (w *workerLifetime) Context() context.Context {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ctx == nil {
		w.ctx, w.cancel = context.WithCancel(context.Background())
	}
	return w.ctx
}
func (w *workerLifetime) Stop(ctx context.Context) error {
	w.mu.Lock()
	w.stopped = true
	if w.cancel != nil {
		w.cancel()
	}
	w.mu.Unlock()
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func StopBindingWorkers(ctx context.Context) error {
	var stops []func(context.Context) error
	stops = append(stops, bindingActivationWorkers.Stop)
	if r, ok := DefaultUsageReporter().(*DurableUsageReporter); ok {
		stops = append(stops, r.Stop)
	}
	if s := DefaultConfigSyncer(); s != nil {
		stops = append(stops, s.workers.Stop)
	}
	if s := DefaultCacheInvalidationSyncer(); s != nil {
		stops = append(stops, s.workers.Stop)
	}
	if m := DefaultRuntimeHealthMonitor(); m != nil {
		stops = append(stops, m.workers.Stop)
	}
	if n := DefaultConfigChangeNotifier(); n != nil {
		stops = append(stops, n.Stop)
	}
	if o := DefaultOAuthInvocationRecoveryOutbox(); o != nil {
		stops = append(stops, o.Stop)
	}
	// Cancel every worker before waiting, without serially spending each timeout.
	var wg sync.WaitGroup
	errors := make(chan error, len(stops))
	for _, stop := range stops {
		wg.Add(1)
		go func(stop func(context.Context) error) {
			defer wg.Done()
			if err := stop(ctx); err != nil {
				errors <- err
			}
		}(stop)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		return err
	}
	return nil
}
func (r *DurableUsageReporter) Stop(ctx context.Context) error {
	if err := r.workers.Stop(ctx); err != nil {
		return err
	}
	if inner, ok := r.inner.(*RedisUsageReporter); ok {
		if err := inner.workers.Stop(ctx); err != nil {
			return err
		}
		if inner.commitBatcher != nil {
			return inner.commitBatcher.workers.Stop(ctx)
		}
	}
	return nil
}
func (n *ConfigChangeNotifier) Stop(ctx context.Context) error {
	n.mu.Lock()
	n.stopped = true
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
	n.mu.Unlock()
	return n.workers.Stop(ctx)
}
