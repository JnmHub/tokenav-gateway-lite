//go:build unit

package service

import (
	"testing"
	"time"
)

func BenchmarkOperationAuditRecordQueue(b *testing.B) {
	svc := NewOperationAuditService(nil, nil)
	b.Cleanup(svc.cancel)
	done := make(chan struct{})
	consumed := make(chan struct{})
	go func() {
		for range svc.queue {
			consumed <- struct{}{}
		}
		close(done)
	}()
	entry := &OperationAuditLog{
		CreatedAt:    time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC),
		Action:       "benchmark.audit.record",
		ResourceType: "benchmark",
		Result:       AuditResultSuccess,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		svc.Record(entry)
		<-consumed
	}
	b.StopTimer()
	close(svc.queue)
	<-done
}
