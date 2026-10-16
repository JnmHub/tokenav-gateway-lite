package gatewaylite

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestControlPlaneClientReturnsTypedHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"ok":false,"error":"content_moderation_subject_mismatch"}`))
	}))
	defer server.Close()

	client, err := NewControlPlaneClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("NewControlPlaneClient error=%v", err)
	}
	_, err = client.ReportContentModerationPenalty(context.Background(), ContentModerationPenaltyRequest{RequestID: "typed-error"})
	var httpErr *ControlPlaneHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error=%T %v, want *ControlPlaneHTTPError", err, err)
	}
	if httpErr.StatusCode != http.StatusForbidden || httpErr.ErrorCode != "content_moderation_subject_mismatch" {
		t.Fatalf("http error=%+v", httpErr)
	}
}

func TestNewControlPlaneClientUsesTunedTransport(t *testing.T) {
	client, err := NewControlPlaneClient("https://control.example", "", time.Second)
	if err != nil {
		t.Fatalf("NewControlPlaneClient error=%v", err)
	}
	if client.httpClient == nil {
		t.Fatal("http client is nil")
	}
	if client.usageHTTPClient == nil {
		t.Fatal("usage http client is nil")
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("transport=%T, want *http.Transport", client.httpClient.Transport)
	}
	if transport.MaxIdleConns != controlPlaneMaxIdleConns {
		t.Fatalf("MaxIdleConns=%d, want %d", transport.MaxIdleConns, controlPlaneMaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != controlPlaneMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost=%d, want %d", transport.MaxIdleConnsPerHost, controlPlaneMaxIdleConnsPerHost)
	}
	if transport.MaxConnsPerHost != controlPlaneMaxConnsPerHost {
		t.Fatalf("MaxConnsPerHost=%d, want %d", transport.MaxConnsPerHost, controlPlaneMaxConnsPerHost)
	}
	if transport.IdleConnTimeout != controlPlaneIdleConnTimeout {
		t.Fatalf("IdleConnTimeout=%s, want %s", transport.IdleConnTimeout, controlPlaneIdleConnTimeout)
	}
	if client.usageHTTPClient.Transport != client.httpClient.Transport {
		t.Fatal("usage and control requests must share one transport pool")
	}
	if client.usageHTTPClient.Timeout != controlPlaneUsageTimeout {
		t.Fatalf("usage timeout=%s, want %s", client.usageHTTPClient.Timeout, controlPlaneUsageTimeout)
	}
}

func TestControlPlaneClientResolveKeyPreservesPeakRateGroupPolicy(t *testing.T) {
	var received ResolveKeyRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/key/resolve" {
			t.Fatalf("path=%s, want /internal/key/resolve", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode resolve request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"key":{"key_id":"peak-key","user_id":42,"status":"active","group_id":7001,"group_name":"Peak Group","gateway_strategy":"fixed_group","key_default_gateway_node_id":7,"group":{"id":7001,"local_group_id":17,"name":"Peak Group","platform":"openai","status":"active","rate_multiplier":1.25,"peak_rate_enabled":true,"peak_start":"22:00","peak_end":"06:00","peak_rate_multiplier":1.6,"peak_timezone":"Asia/Shanghai"}}}`))
	}))
	defer server.Close()

	client, err := NewControlPlaneClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("NewControlPlaneClient error=%v", err)
	}
	response, err := client.ResolveKey(context.Background(), ResolveKeyRequest{
		KeyID:          "peak-key",
		Region:         "us-1",
		GatewayCode:    "us-1",
		BillingExempt:  true,
		SkipUsageTouch: true,
	})
	if err != nil {
		t.Fatalf("ResolveKey error=%v", err)
	}
	if response == nil || !response.OK || response.Key.Group == nil {
		t.Fatalf("response=%+v, want populated group", response)
	}
	if received.KeyID != "peak-key" || !received.BillingExempt || !received.SkipUsageTouch {
		t.Fatalf("resolve request=%+v, want read-only billing introspection flags", received)
	}
	group := response.Key.Group
	if group.LocalGroupID != 17 || group.RateMultiplier != 1.25 || !group.PeakRateEnabled ||
		group.PeakStart != "22:00" || group.PeakEnd != "06:00" ||
		group.PeakRateMultiplier != 1.6 || group.PeakTimezone != "Asia/Shanghai" {
		t.Fatalf("peak group policy not preserved: %+v", group)
	}
}

func TestControlPlaneClientUsageBatchDoesNotUseShortControlTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/usage/report-batch" {
			t.Fatalf("path=%s, want /internal/usage/report-batch", r.URL.Path)
		}
		time.Sleep(80 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"applied_count":1}`))
	}))
	defer server.Close()

	client, err := NewControlPlaneClient(server.URL, "", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("NewControlPlaneClient error=%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.ReportUsageBatch(ctx, []UsageEvent{{RequestID: "req-usage-timeout", UserID: 1}}); err != nil {
		t.Fatalf("ReportUsageBatch unexpectedly used short control timeout: %v", err)
	}
}

func TestControlPlaneClientReportsConfigPushWithGrokVideoPrices(t *testing.T) {
	var received GatewayConfigPushRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/gateway/config-push" {
			t.Fatalf("path=%s, want /internal/gateway/config-push", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer internal-token" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode config push: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client, err := NewControlPlaneClient(server.URL, "internal-token", time.Second)
	if err != nil {
		t.Fatalf("NewControlPlaneClient error=%v", err)
	}
	response, err := client.ReportGatewayConfigPush(context.Background(), GatewayConfigPushRequest{
		GatewayCode:     "jp-grok",
		AccountFull:     true,
		GroupsFull:      true,
		ModelPricesFull: true,
		ModelPrices: []GatewayModelPrice{{
			Model:                    "grok-imagine-video-1.5",
			BillingMode:              "video",
			VideoPricePerSecond480P:  0.08,
			VideoPricePerSecond720P:  0.14,
			VideoPricePerSecond1080P: 0.25,
		}},
	})
	if err != nil {
		t.Fatalf("ReportGatewayConfigPush error=%v", err)
	}
	if response == nil || !response.OK {
		t.Fatalf("response=%+v, want ok", response)
	}
	if !received.AccountFull || !received.GroupsFull || !received.ModelPricesFull {
		t.Fatalf("full flags not preserved: %+v", received)
	}
	if len(received.ModelPrices) != 1 {
		t.Fatalf("model prices len=%d, want 1", len(received.ModelPrices))
	}
	price := received.ModelPrices[0]
	if price.VideoPricePerSecond480P != 0.08 || price.VideoPricePerSecond720P != 0.14 || price.VideoPricePerSecond1080P != 0.25 {
		t.Fatalf("video prices=%+v", price)
	}
}

func TestControlPlaneClientReportsStrictPrioritySnapshotFields(t *testing.T) {
	var received GatewayConfigPushRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/gateway/config-push" {
			t.Fatalf("path=%s, want /internal/gateway/config-push", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode config push: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client, err := NewControlPlaneClient(server.URL, "", time.Second)
	require.NoError(t, err)
	_, err = client.ReportGatewayConfigPush(context.Background(), GatewayConfigPushRequest{
		GatewayCode: "strict-priority-node",
		Accounts: []GatewayAccountSnapshot{{
			ID:              41,
			Priority:        50,
			GroupIDs:        []int64{7, 8},
			GroupPriorities: map[int64]int{7: 1, 8: 9},
		}},
		Groups: []GatewayGroupSnapshot{{
			ID:             7,
			Name:           "strict-pro",
			SchedulingMode: "strict_priority",
		}},
	})
	require.NoError(t, err)
	require.Len(t, received.Accounts, 1)
	require.Equal(t, map[int64]int{7: 1, 8: 9}, received.Accounts[0].GroupPriorities)
	require.Len(t, received.Groups, 1)
	require.Equal(t, "strict_priority", received.Groups[0].SchedulingMode)
}
