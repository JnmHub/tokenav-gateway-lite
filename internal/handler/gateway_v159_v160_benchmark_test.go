package handler

import (
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var gatewayIntentBenchmarkSink bool

// BenchmarkGatewayIntentClassificationV159V160 compares only gateway-local
// endpoint and image-intent work. Upstream latency is intentionally excluded so
// a slow or fast provider cannot hide a regression introduced on every request.
// The custom p95-ns metric is sampled separately from Go's average ns/op value.
func BenchmarkGatewayIntentClassificationV159V160(b *testing.B) {
	tests := []struct {
		name     string
		path     string
		platform string
		model    string
		body     []byte
	}{
		{
			name:     "ordinary_text",
			path:     "/v1/responses",
			platform: service.PlatformOpenAI,
			model:    "gpt-5.6-sol",
			body:     []byte(`{"model":"gpt-5.6-sol","input":"hello"}`),
		},
		{
			name:     "alpha_search",
			path:     "/backend-api/codex/alpha/search",
			platform: service.PlatformOpenAI,
			model:    "gpt-5.6-sol",
			body:     []byte(`{"query":"latest gateway status"}`),
		},
		{
			name:     "passive_image_namespace",
			path:     "/v1/responses",
			platform: service.PlatformOpenAI,
			model:    "gpt-5.6-sol",
			body:     []byte(`{"model":"gpt-5.6-sol","input":"hello","tools":[{"type":"namespace","name":"image_gen","functions":[{"name":"imagegen"}]}]}`),
		},
		{
			name:     "explicit_image_generation",
			path:     "/v1/responses",
			platform: service.PlatformOpenAI,
			model:    "gpt-5.6-sol",
			body:     []byte(`{"model":"gpt-5.6-sol","input":"draw a lighthouse","tools":[{"type":"image_generation","model":"gpt-image-2"}]}`),
		},
		{
			name:     "grok_text",
			path:     "/v1/responses",
			platform: service.PlatformGrok,
			model:    "grok-4.5",
			body:     []byte(`{"model":"grok-4.5","input":"hello","tools":[{"type":"function","name":"lookup"}]}`),
		},
	}

	for _, tt := range tests {
		tt := tt
		b.Run(tt.name, func(b *testing.B) {
			run := func() bool {
				inbound := NormalizeInboundEndpoint(tt.path)
				upstream := DeriveUpstreamEndpoint(inbound, tt.path, tt.platform)
				if inbound == EndpointAlphaSearch {
					return upstream == EndpointAlphaSearch
				}
				return service.IsExplicitImageGenerationIntent(inbound, tt.model, tt.body)
			}

			for i := 0; i < 1_000; i++ {
				gatewayIntentBenchmarkSink = run()
			}
			runtime.GC()
			// Windows' monotonic clock can have coarser resolution than these
			// sub-microsecond operations. Measure small batches and divide by the
			// batch size so the reported P95 cannot collapse to a misleading zero.
			const (
				samples     = 300
				sampleBatch = 32_768
			)
			latencies := make([]int64, samples)
			for i := range latencies {
				started := time.Now()
				for j := 0; j < sampleBatch; j++ {
					gatewayIntentBenchmarkSink = run()
				}
				latencies[i] = time.Since(started).Nanoseconds() / sampleBatch
			}
			sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
			p95 := latencies[(samples*95+99)/100-1]

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				gatewayIntentBenchmarkSink = run()
			}
			b.StopTimer()
			b.ReportMetric(float64(p95), "batched-p95-ns/op")
		})
	}
}
