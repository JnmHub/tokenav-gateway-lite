package server

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGatewayLiteUpstreamRuntimeMetricsProviderAggregatesAndCaches(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	mock.ExpectQuery("WITH successful").
		WithArgs(now.Add(-gatewayLiteUpstreamMetricsWindow), now).
		WillReturnRows(sqlmock.NewRows([]string{"success_count", "failure_count"}).AddRow(int64(80), int64(20)))
	provider := &gatewayLiteUpstreamRuntimeMetricsProvider{db: db, queryNow: func() time.Time { return now }}
	first := provider.RuntimeMetrics(context.Background())
	if first["upstream_request_count_5m"] != int64(100) {
		t.Fatalf("request_count=%#v, want 100", first["upstream_request_count_5m"])
	}
	if first["upstream_failure_rate_percent_5m"] != float64(20) {
		t.Fatalf("failure_rate=%#v, want 20", first["upstream_failure_rate_percent_5m"])
	}
	second := provider.RuntimeMetrics(context.Background())
	if second["upstream_failure_count_5m"] != int64(20) {
		t.Fatalf("cached failure_count=%#v, want 20", second["upstream_failure_count_5m"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestGatewayLiteUpstreamRuntimeMetricsProviderDoesNotExposeQueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	mock.ExpectQuery("WITH successful").WillReturnError(context.DeadlineExceeded)
	provider := &gatewayLiteUpstreamRuntimeMetricsProvider{db: db, queryNow: func() time.Time { return now }}
	metrics := provider.RuntimeMetrics(context.Background())
	if metrics["upstream_metrics_available"] != false || metrics["upstream_metrics_error"] != "query_failed" {
		t.Fatalf("metrics=%#v", metrics)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
