package server

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/redis/go-redis/v9"
)

func TestGatewayLiteAuthorityRuntimeMetricsDefaultLocalAuthority(t *testing.T) {
	metrics := gatewayLiteAuthorityRuntimeMetrics{}.RuntimeMetrics(context.Background())

	if metrics["gateway_lite_runtime_authority"] != "local" {
		t.Fatalf("runtime authority = %v, want local", metrics["gateway_lite_runtime_authority"])
	}
	if metrics["legacy_snapshot_scheduler_authority"] != false {
		t.Fatalf("legacy snapshot authority = %v, want false", metrics["legacy_snapshot_scheduler_authority"])
	}
	if metrics["snapshot_accounts_groups_compatibility_only"] != true {
		t.Fatalf("snapshot compatibility flag = %v, want true", metrics["snapshot_accounts_groups_compatibility_only"])
	}
	if metrics["local_accounts_groups_scheduler_authoritative"] != true {
		t.Fatalf("local authority flag = %v, want true", metrics["local_accounts_groups_scheduler_authoritative"])
	}
}

func TestGatewayLiteRedisRuntimeMetricsExposePoolPressure(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })

	metrics := (gatewayLiteRedisRuntimeMetricsProvider{client: client}).RuntimeMetrics(context.Background())
	for _, key := range []string{
		"redis_pool_wait_count_total",
		"redis_pool_wait_duration_ms_total",
		"redis_pool_timeouts_total",
		"redis_pool_total_conns",
		"redis_pool_idle_conns",
	} {
		if _, ok := metrics[key]; !ok {
			t.Fatalf("runtime metrics missing %s: %#v", key, metrics)
		}
	}
}

func TestGatewayLiteAuthorityRuntimeMetricsLegacyAuthority(t *testing.T) {
	metrics := gatewayLiteAuthorityRuntimeMetrics{legacySnapshotSchedulerAuthority: true}.RuntimeMetrics(context.Background())

	if metrics["legacy_snapshot_scheduler_authority"] != true {
		t.Fatalf("legacy snapshot authority = %v, want true", metrics["legacy_snapshot_scheduler_authority"])
	}
	if metrics["snapshot_accounts_groups_compatibility_only"] != false {
		t.Fatalf("snapshot compatibility flag = %v, want false", metrics["snapshot_accounts_groups_compatibility_only"])
	}
	if metrics["local_accounts_groups_scheduler_authoritative"] != false {
		t.Fatalf("local authority flag = %v, want false", metrics["local_accounts_groups_scheduler_authoritative"])
	}
}

func TestGatewayLiteAuthorityRuntimeMetricsFollowRuntimeConfig(t *testing.T) {
	runtimeRef := gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{})
	provider := gatewayLiteAuthorityRuntimeMetrics{legacySnapshotSchedulerAuthority: true, runtimeRef: runtimeRef}

	metrics := provider.RuntimeMetrics(context.Background())
	if metrics["legacy_snapshot_scheduler_authority"] != false {
		t.Fatalf("legacy snapshot authority with runtime ref = %v, want false", metrics["legacy_snapshot_scheduler_authority"])
	}

	runtimeRef.Set(gatewaylite.RuntimeConfig{LegacySnapshotSchedulerAuthority: true})
	metrics = provider.RuntimeMetrics(context.Background())
	if metrics["legacy_snapshot_scheduler_authority"] != true {
		t.Fatalf("legacy snapshot authority after runtime update = %v, want true", metrics["legacy_snapshot_scheduler_authority"])
	}
	if metrics["local_accounts_groups_scheduler_authoritative"] != false {
		t.Fatalf("local authority after runtime update = %v, want false", metrics["local_accounts_groups_scheduler_authoritative"])
	}
}
