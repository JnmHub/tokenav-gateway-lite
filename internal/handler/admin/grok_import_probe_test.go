//go:build unit

package admin

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type grokImportProbeRecorder struct {
	mu        sync.Mutex
	calls     []int64
	active    int
	maxActive int
	delay     time.Duration
	block     <-chan struct{}
	started   chan int64
	done      chan struct{}
}

func (p *grokImportProbeRecorder) QueryQuota(ctx context.Context, accountID int64) (*service.GrokQuotaProbeResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, accountID)
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.mu.Unlock()
	if p.started != nil {
		p.started <- accountID
	}

	if p.block != nil {
		select {
		case <-p.block:
		case <-ctx.Done():
		}
	} else {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
		}
	}

	p.mu.Lock()
	p.active--
	p.mu.Unlock()
	if p.done != nil {
		p.done <- struct{}{}
	}
	return &service.GrokQuotaProbeResult{Source: "billing_probe", StatusCode: 200}, nil
}

func (p *grokImportProbeRecorder) snapshot() ([]int64, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int64(nil), p.calls...), p.maxActive
}

func TestGrokImportProbeSchedulerFiltersNonOAuthAccounts(t *testing.T) {
	t.Parallel()
	prober := &grokImportProbeRecorder{done: make(chan struct{}, 1)}
	scheduler := newGrokImportProbeScheduler(1, time.Second)

	scheduler.schedule(prober, &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
	scheduler.schedule(prober, &service.Account{ID: 2, Platform: service.PlatformGrok, Type: service.AccountTypeAPIKey})
	time.Sleep(20 * time.Millisecond)

	calls, maxActive := prober.snapshot()
	require.Empty(t, calls)
	require.Zero(t, maxActive)
}

func TestGrokImportProbeSchedulerBoundsConcurrencyAndRunsEveryTask(t *testing.T) {
	t.Parallel()
	const taskCount = 7
	prober := &grokImportProbeRecorder{
		delay: 30 * time.Millisecond,
		done:  make(chan struct{}, taskCount),
	}
	scheduler := newGrokImportProbeScheduler(2, time.Second)

	for accountID := int64(1); accountID <= taskCount; accountID++ {
		scheduler.schedule(prober, &service.Account{ID: accountID, Platform: service.PlatformGrok, Type: service.AccountTypeOAuth})
	}
	for i := 0; i < taskCount; i++ {
		select {
		case <-prober.done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for Grok import probes")
		}
	}

	calls, maxActive := prober.snapshot()
	require.Len(t, calls, taskCount)
	require.LessOrEqual(t, maxActive, 2)
	require.Greater(t, maxActive, 0)
}

func TestGrokImportProbeSchedulerDeduplicatesPendingAndInFlightAccounts(t *testing.T) {
	release := make(chan struct{})
	prober := &grokImportProbeRecorder{
		block:   release,
		started: make(chan int64, 2),
		done:    make(chan struct{}, 2),
	}
	scheduler := newGrokImportProbeScheduler(1, time.Second)
	account := &service.Account{ID: 501, Platform: service.PlatformGrok, Type: service.AccountTypeOAuth}
	queued := &service.Account{ID: 502, Platform: service.PlatformGrok, Type: service.AccountTypeOAuth}

	scheduler.schedule(prober, account)
	require.Equal(t, int64(501), <-prober.started)
	scheduler.schedule(prober, account)
	scheduler.schedule(prober, queued)
	scheduler.schedule(prober, queued)

	scheduler.mu.Lock()
	require.Len(t, scheduler.queue, 1)
	require.Contains(t, scheduler.inFlight, int64(501))
	require.Contains(t, scheduler.pending, int64(502))
	scheduler.mu.Unlock()

	close(release)
	for i := 0; i < 2; i++ {
		select {
		case <-prober.done:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for deduplicated Grok probes")
		}
	}
	calls, _ := prober.snapshot()
	require.Equal(t, []int64{501, 502}, calls)
}

func TestGrokImportProbeSchedulerBoundsPendingQueue(t *testing.T) {
	release := make(chan struct{})
	prober := &grokImportProbeRecorder{
		block:   release,
		started: make(chan int64, grokImportProbeQueueLimit+1),
		done:    make(chan struct{}, grokImportProbeQueueLimit+1),
	}
	scheduler := newGrokImportProbeScheduler(1, time.Second)
	scheduler.schedule(prober, &service.Account{ID: 600, Platform: service.PlatformGrok, Type: service.AccountTypeOAuth})
	require.Equal(t, int64(600), <-prober.started)
	for accountID := int64(601); accountID < 601+grokImportProbeQueueLimit+10; accountID++ {
		scheduler.schedule(prober, &service.Account{ID: accountID, Platform: service.PlatformGrok, Type: service.AccountTypeOAuth})
	}

	scheduler.mu.Lock()
	require.Len(t, scheduler.queue, grokImportProbeQueueLimit)
	scheduler.mu.Unlock()

	close(release)
	for i := 0; i < grokImportProbeQueueLimit+1; i++ {
		select {
		case <-prober.done:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for bounded Grok probes")
		}
	}
}
