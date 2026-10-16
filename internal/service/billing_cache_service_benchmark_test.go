package service

import (
	"sort"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	billingCacheBenchmarkSamples = 40
	// Windows' monotonic clock is coarser than a sub-microsecond submission.
	// Large batches keep percentile metrics from collapsing to zero or one tick.
	billingCacheBenchmarkSampleBatch = 32768
)

func BenchmarkBillingCacheWriteBurst(b *testing.B) {
	cfg := &config.Config{}
	cfg.Billing.CacheWrite = config.BillingCacheWriteConfig{Preset: config.BillingCacheWritePresetStandard}
	svc := NewBillingCacheService(&billingCacheWorkerStub{}, nil, nil, nil, nil, nil, cfg, nil)
	peakPending := 0
	nextUserID := int64(1)
	operation := func() {
		svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: nextUserID, balance: 100})
		nextUserID++
	}
	latencies := sampleBillingCacheOperation(
		billingCacheBenchmarkSamples,
		billingCacheBenchmarkSampleBatch,
		operation,
		func() {
			if pending := svc.RuntimeStats().QueueLength; pending > peakPending {
				peakPending = pending
			}
		},
	)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		operation()
	}
	b.StopTimer()
	if pending := svc.RuntimeStats().QueueLength; pending > peakPending {
		peakPending = pending
	}
	svc.Stop()
	reportBillingCacheBenchmark(b, latencies, peakPending)
}

func BenchmarkBillingCacheWriteQueueFullMerge(b *testing.B) {
	svc := &BillingCacheService{
		cache:                    &billingCacheWorkerStub{},
		bestEffortCacheWriteChan: make(chan cacheWriteTask, 1),
		cacheWriteOptions: cacheWritePoolOptions{
			preset:            config.BillingCacheWritePresetLow,
			bestEffortWorkers: 1, bestEffortBuffer: 1,
			timeout: time.Second,
		},
		bestEffortDeferredLimit: 1,
	}
	svc.bestEffortCacheWriteChan <- cacheWriteTask{kind: cacheWriteSetBalance, userID: 99}
	nextBalance := 0.0
	operation := func() {
		svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: 1, balance: nextBalance})
		nextBalance++
	}
	latencies := sampleBillingCacheOperation(
		billingCacheBenchmarkSamples,
		billingCacheBenchmarkSampleBatch,
		operation,
		nil,
	)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		operation()
	}
	b.StopTimer()
	if deferred, ok := svc.takeBestEffortDeferred(); ok {
		completeCacheWriteSequence(deferred)
		svc.finishCacheWriteTask(deferred)
	}
	reportBillingCacheBenchmark(b, latencies, 2)
}

func BenchmarkBillingCacheWriteReload(b *testing.B) {
	svc := NewBillingCacheService(&billingCacheWorkerStub{}, nil, nil, nil, nil, nil, &config.Config{}, nil)
	presets := []string{config.BillingCacheWritePresetLow, config.BillingCacheWritePresetStandard}
	nextPreset := 0
	operation := func() {
		if err := svc.ReloadCacheWriteConfig(config.BillingCacheWriteConfig{Preset: presets[nextPreset%len(presets)]}); err != nil {
			b.Fatal(err)
		}
		nextPreset++
	}
	latencies := sampleBillingCacheOperation(billingCacheBenchmarkSamples, 64, operation, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		operation()
	}
	b.StopTimer()
	svc.Stop()
	reportBillingCacheBenchmark(b, latencies, 0)
}

func reportBillingCacheBenchmark(b *testing.B, samples []time.Duration, peakPending int) {
	b.Helper()
	if len(samples) == 0 {
		return
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	b.ReportMetric(float64(durationPercentile(samples, 50).Nanoseconds()), "submit-p50-ns")
	b.ReportMetric(float64(durationPercentile(samples, 95).Nanoseconds()), "submit-p95-ns")
	b.ReportMetric(float64(durationPercentile(samples, 99).Nanoseconds()), "submit-p99-ns")
	b.ReportMetric(float64(peakPending), "peak-pending")
	if peakRSS, err := peakProcessRSSBytes(); err == nil {
		b.ReportMetric(float64(peakRSS)/(1024*1024), "peak-rss-MiB")
	}
}

func durationPercentile(sorted []time.Duration, percentile int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := (len(sorted) - 1) * percentile / 100
	return sorted[index]
}

func sampleBillingCacheOperation(samples, batchSize int, operation func(), afterBatch func()) []time.Duration {
	latencies := make([]time.Duration, samples)
	for sample := range latencies {
		started := time.Now()
		for batch := 0; batch < batchSize; batch++ {
			operation()
		}
		latencies[sample] = time.Since(started) / time.Duration(batchSize)
		if afterBatch != nil {
			afterBatch()
		}
	}
	return latencies
}
