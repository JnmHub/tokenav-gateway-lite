package gatewaylite

import (
	"strings"
	"sync/atomic"
	"time"
)

type RuntimeConfig struct {
	Region                           string
	GatewayCode                      string
	RedisPrefix                      string
	RuntimeHealthIntervalSeconds     int
	RuntimeActiveWindowSeconds       int
	ConfigSyncIntervalSeconds        int
	CacheInvalidationIntervalSeconds int
	LegacySnapshotSchedulerAuthority bool
	UsageQueuePendingAlertThreshold  int64
	UsageQueueDeadAlertThreshold     int64
	AllowLive                        bool
}

// LiveEnabled 从原子运行配置读取节点总开关，请求热路径不会访问数据库或配置文件。
func LiveEnabled() bool {
	ref := DefaultRuntimeConfigRef()
	return ref != nil && ref.Current().AllowLive
}

type RuntimeConfigRef struct {
	value atomic.Value
}

func NewRuntimeConfigRef(cfg RuntimeConfig) *RuntimeConfigRef {
	ref := &RuntimeConfigRef{}
	ref.Set(cfg)
	return ref
}

func (r *RuntimeConfigRef) Set(cfg RuntimeConfig) {
	if r == nil {
		return
	}
	cfg = NormalizeRuntimeConfig(cfg)
	r.value.Store(cfg)
}

func (r *RuntimeConfigRef) Current() RuntimeConfig {
	if r == nil {
		return NormalizeRuntimeConfig(RuntimeConfig{})
	}
	if value := r.value.Load(); value != nil {
		if cfg, ok := value.(RuntimeConfig); ok {
			return NormalizeRuntimeConfig(cfg)
		}
	}
	return NormalizeRuntimeConfig(RuntimeConfig{})
}

func NormalizeRuntimeConfig(cfg RuntimeConfig) RuntimeConfig {
	cfg.Region = strings.TrimSpace(cfg.Region)
	if cfg.Region == "" {
		cfg.Region = strings.TrimSpace(cfg.GatewayCode)
	}
	if cfg.Region == "" {
		cfg.Region = "default"
	}
	cfg.GatewayCode = strings.TrimSpace(cfg.GatewayCode)
	if cfg.GatewayCode == "" {
		cfg.GatewayCode = cfg.Region
	}
	cfg.RedisPrefix = NormalizeRedisPrefix(cfg.RedisPrefix)
	if cfg.RuntimeHealthIntervalSeconds <= 0 {
		cfg.RuntimeHealthIntervalSeconds = 15
	}
	if cfg.RuntimeActiveWindowSeconds <= 0 {
		cfg.RuntimeActiveWindowSeconds = 300
	}
	if cfg.ConfigSyncIntervalSeconds <= 0 {
		cfg.ConfigSyncIntervalSeconds = 30
	}
	if cfg.CacheInvalidationIntervalSeconds <= 0 {
		cfg.CacheInvalidationIntervalSeconds = 5
	}
	if cfg.UsageQueuePendingAlertThreshold <= 0 {
		cfg.UsageQueuePendingAlertThreshold = 1000
	}
	if cfg.UsageQueueDeadAlertThreshold <= 0 {
		cfg.UsageQueueDeadAlertThreshold = 1
	}
	return cfg
}

func (cfg RuntimeConfig) GatewayScope() string {
	cfg = NormalizeRuntimeConfig(cfg)
	if cfg.GatewayCode != "" {
		return cfg.GatewayCode
	}
	return cfg.Region
}

func NormalizeRedisPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "gl"
	}
	return prefix
}

func (cfg RuntimeConfig) RuntimeHealthInterval() time.Duration {
	return time.Duration(NormalizeRuntimeConfig(cfg).RuntimeHealthIntervalSeconds) * time.Second
}

func (cfg RuntimeConfig) RuntimeActiveWindow() time.Duration {
	return time.Duration(NormalizeRuntimeConfig(cfg).RuntimeActiveWindowSeconds) * time.Second
}

func (cfg RuntimeConfig) ConfigSyncInterval() time.Duration {
	return time.Duration(NormalizeRuntimeConfig(cfg).ConfigSyncIntervalSeconds) * time.Second
}

func (cfg RuntimeConfig) CacheInvalidationInterval() time.Duration {
	return time.Duration(NormalizeRuntimeConfig(cfg).CacheInvalidationIntervalSeconds) * time.Second
}
