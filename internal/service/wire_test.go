package service

import (
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/zeromicro/go-zero/core/collection"
)

func TestMediaProvidersDisabledOutsideGatewayLite(t *testing.T) {
	validSecret := "a-secret-long-enough-for-media-proxy-tests"
	for _, runMode := range []string{config.RunModeStandard, config.RunModeSimple} {
		cfg := &config.Config{
			RunMode: runMode,
			JWT:     config.JWTConfig{Secret: validSecret},
			GatewayLite: config.GatewayLiteConfig{
				MediaProxyEnabled: true,
			},
		}
		if got := ProvideMediaProxy(cfg, nil); got != nil {
			t.Fatalf("run mode %q returned a media proxy", runMode)
		}
		if got := ProvideSyncMediaCache(cfg, nil, nil); got != nil {
			t.Fatalf("run mode %q returned a synchronous media cache", runMode)
		}
	}
}

func TestMediaProvidersEnableOnlyGatewayLite(t *testing.T) {
	cfg := &config.Config{
		RunMode: config.RunModeGatewayLite,
		JWT:     config.JWTConfig{Secret: "a-secret-long-enough-for-media-proxy-tests"},
		GatewayLite: config.GatewayLiteConfig{
			MediaProxyEnabled: true,
		},
	}
	proxy := ProvideMediaProxy(cfg, nil)
	if proxy == nil {
		t.Fatal("gateway-lite should create the media proxy with a stable secret")
	}
	if got := ProvideSyncMediaCache(cfg, nil, proxy); got != nil {
		t.Fatal("synchronous media cache must require an object store")
	}
}

func TestProvideTimingWheelService_ReturnsError(t *testing.T) {
	original := newTimingWheel
	t.Cleanup(func() { newTimingWheel = original })

	newTimingWheel = func(_ time.Duration, _ int, _ collection.Execute) (*collection.TimingWheel, error) {
		return nil, errors.New("boom")
	}

	svc, err := ProvideTimingWheelService()
	if err == nil {
		t.Fatalf("期望返回 error，但得到 nil")
	}
	if svc != nil {
		t.Fatalf("期望返回 nil svc，但得到非空")
	}
}

func TestProvideTimingWheelService_Success(t *testing.T) {
	svc, err := ProvideTimingWheelService()
	if err != nil {
		t.Fatalf("期望 err 为 nil，但得到: %v", err)
	}
	if svc == nil {
		t.Fatalf("期望 svc 非空，但得到 nil")
	}
	svc.Stop()
}
