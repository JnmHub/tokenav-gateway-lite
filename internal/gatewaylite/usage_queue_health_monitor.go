package gatewaylite

import (
	"context"
	"log"
	"time"
)

type GatewayHealthReportClient interface {
	ReportGatewayHealth(ctx context.Context, req GatewayHealthReportRequest) (*GatewayHealthReportResponse, error)
}

type UsageQueueHealthMonitor struct {
	client           GatewayHealthReportClient
	reporter         *RedisUsageReporter
	region           string
	gatewayCode      string
	interval         time.Duration
	pendingThreshold int64
	deadThreshold    int64
}

func NewUsageQueueHealthMonitor(client GatewayHealthReportClient, reporter *RedisUsageReporter, region, gatewayCode string, interval time.Duration, pendingThreshold, deadThreshold int64) *UsageQueueHealthMonitor {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if pendingThreshold <= 0 {
		pendingThreshold = 1000
	}
	if deadThreshold <= 0 {
		deadThreshold = 1
	}
	return &UsageQueueHealthMonitor{
		client:           client,
		reporter:         reporter,
		region:           region,
		gatewayCode:      gatewayCode,
		interval:         interval,
		pendingThreshold: pendingThreshold,
		deadThreshold:    deadThreshold,
	}
}

func (m *UsageQueueHealthMonitor) Start(ctx context.Context) {
	if m == nil || m.client == nil || m.reporter == nil || !m.reporter.Enabled() {
		return
	}
	go m.run(ctx)
}

func (m *UsageQueueHealthMonitor) run(ctx context.Context) {
	m.reportOnce(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reportOnce(ctx)
		}
	}
}

func (m *UsageQueueHealthMonitor) reportOnce(ctx context.Context) {
	stats, err := m.reporter.UsageQueueStats(ctx)
	status := "healthy"
	message := ""
	if err != nil {
		status = "warning"
		message = "usage queue stats unavailable"
	} else if stats.DeadCount >= m.deadThreshold {
		status = "critical"
		message = "usage queue dead stream has events"
	} else if stats.PendingCount >= m.pendingThreshold {
		status = "warning"
		message = "usage queue backlog exceeds threshold"
	}
	req := GatewayHealthReportRequest{
		GatewayCode:            m.gatewayCode,
		Region:                 m.gatewayCode,
		HealthStatus:           "healthy",
		Message:                message,
		UsageQueueStatus:       status,
		UsageQueueStreamLength: stats.StreamLength,
		UsageQueuePendingCount: stats.PendingCount,
		UsageQueueDeadCount:    stats.DeadCount,
		Metadata: map[string]any{
			"source":                  "gateway-lite-usage-queue-monitor",
			"usage_queue_pending_max": m.pendingThreshold,
			"usage_queue_dead_max":    m.deadThreshold,
		},
	}
	reportCtx, cancel := context.WithTimeout(ctx, defaultUsageReportTimeout)
	defer cancel()
	resp, reportErr := m.client.ReportGatewayHealth(reportCtx, req)
	if reportErr != nil {
		log.Printf("gateway-lite: usage queue health report failed: %v", reportErr)
		return
	}
	if resp != nil && !resp.OK {
		log.Printf("gateway-lite: usage queue health report rejected: %s", resp.Error)
	}
}
