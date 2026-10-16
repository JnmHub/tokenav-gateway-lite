//go:build unit

package service

import (
	"testing"
	"time"
)

func BenchmarkOpenAIAccountModelTransientState(b *testing.B) {
	now := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	b.Run("RecordFailure", func(b *testing.B) {
		state := newOpenAIAccountModelTransientState(4096)
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			state.recordFailure(35, "gpt-5.6-sol", now)
		}
	})
	b.Run("IsBlocked", func(b *testing.B) {
		state := newOpenAIAccountModelTransientState(4096)
		state.recordFailure(35, "gpt-5.6-sol", now)
		state.recordFailure(35, "gpt-5.6-sol", now.Add(time.Millisecond))
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			_ = state.isBlocked(35, "gpt-5.6-sol", now.Add(time.Second))
		}
	})
	b.Run("ParallelMixed", func(b *testing.B) {
		state := newOpenAIAccountModelTransientState(4096)
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				state.recordFailure(35, "gpt-5.6-sol", now)
				_ = state.isBlocked(35, "gpt-5.6-sol", now.Add(time.Second))
			}
		})
	})
}
