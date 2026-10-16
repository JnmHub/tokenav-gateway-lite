package gatewaylite

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const defaultCacheInvalidationInterval = 5 * time.Second

type CacheInvalidationClient interface {
	FetchCacheInvalidations(ctx context.Context, req CacheInvalidationRequest) (*CacheInvalidationResponse, error)
}

type ConfigRefresher interface {
	SyncFull(ctx context.Context) error
}

type KeyRegionInvalidator interface {
	DeleteRegion(ctx context.Context, region string) error
}

type keyScopedInvalidator interface {
	DeleteKey(ctx context.Context, keyID, region string) error
}

type userScopedInvalidator interface {
	DeleteUser(ctx context.Context, userID int64, region string) error
}

type organizationScopedInvalidator interface {
	DeleteOrganization(ctx context.Context, organizationID int64, region string) error
}

type LeaseSnapshotInvalidator interface {
	InvalidateLeaseSnapshot(ctx context.Context, userID int64, region, leaseID string, version int64) (bool, error)
}

type scopedLeaseSnapshotInvalidator interface {
	InvalidateLeaseSnapshotForSubject(ctx context.Context, subject LeaseSubject, region, leaseID string, version int64) (bool, error)
}

var (
	ErrControlEventGatewayMismatch                  = errors.New("control event targets another gateway")
	ErrOrganizationScopedKeyInvalidationUnsupported = errors.New("key invalidator does not support organization-scoped invalidation")
)

type CacheInvalidationSyncer struct {
	workers          workerLifetime
	client           CacheInvalidationClient
	configCache      *RedisConfigCache
	keyInvalidator   KeyRegionInvalidator
	leaseInvalidator LeaseSnapshotInvalidator
	configRefresher  ConfigRefresher
	mu               sync.RWMutex
	region           string
	gatewayCode      string
	interval         time.Duration
}

func (s *CacheInvalidationSyncer) WithLeaseInvalidator(invalidator LeaseSnapshotInvalidator) *CacheInvalidationSyncer {
	if s != nil {
		s.leaseInvalidator = invalidator
	}
	return s
}

func NewCacheInvalidationSyncer(client CacheInvalidationClient, configCache *RedisConfigCache, keyInvalidator KeyRegionInvalidator, configRefresher ConfigRefresher, region string, gatewayCode string, interval time.Duration) *CacheInvalidationSyncer {
	region = strings.TrimSpace(region)
	gatewayCode = strings.TrimSpace(gatewayCode)
	if gatewayCode == "" {
		gatewayCode = defaultString(region, "default")
	}
	if region == "" {
		region = gatewayCode
	}
	if interval <= 0 {
		interval = defaultCacheInvalidationInterval
	}
	return &CacheInvalidationSyncer{
		client:          client,
		configCache:     configCache,
		keyInvalidator:  keyInvalidator,
		configRefresher: configRefresher,
		region:          region,
		gatewayCode:     gatewayCode,
		interval:        interval,
	}
}

func (s *CacheInvalidationSyncer) Start(ctx context.Context) {
	if s == nil || s.client == nil || s.configCache == nil || !s.configCache.Enabled() {
		return
	}
	s.workers.Go(ctx, s.run)
}

func (s *CacheInvalidationSyncer) SyncOnce(ctx context.Context) {
	if s == nil {
		return
	}
	s.syncOnce(ctx)
}

func (s *CacheInvalidationSyncer) run(ctx context.Context) {
	s.syncOnce(ctx)
	timer := time.NewTimer(s.currentInterval())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.syncOnce(ctx)
			timer.Reset(s.currentInterval())
		}
	}
}

func (s *CacheInvalidationSyncer) syncOnce(ctx context.Context) {
	_, gatewayCode, _ := s.currentRuntimeConfig()
	scope := gatewayCode
	sinceID, err := s.configCache.InvalidationCursor(ctx)
	if err != nil {
		log.Printf("gateway-lite: cache invalidation cursor read failed: %v", err)
		return
	}

	// 同 redis_config_cache：用后台下限，避免更短的 context 期限覆盖客户端超时。
	reqCtx, cancel := context.WithTimeout(ctx, controlPlaneBackgroundTimeoutFloor)
	defer cancel()
	resp, err := s.client.FetchCacheInvalidations(reqCtx, CacheInvalidationRequest{
		GatewayCode: gatewayCode,
		Region:      scope,
		SinceID:     sinceID,
		// 主站允许的单页上限是 200。新节点第一次接入时可能面对较长历史，
		// 使用上限可更快追平，同时下方仍把同页配置事件合并为一次完整刷新。
		Limit: 200,
	})
	if err != nil {
		log.Printf("gateway-lite: cache invalidation fetch failed: %v", err)
		return
	}
	if resp == nil || !resp.OK {
		errMsg := ""
		if resp != nil {
			errMsg = resp.Error
		}
		log.Printf("gateway-lite: cache invalidation rejected: %s", errMsg)
		return
	}

	ackIDs := make([]int64, 0, len(resp.Events))
	pendingConfigEventIDs := make([]int64, 0, len(resp.Events))
	latestID := resp.LatestID
	for _, event := range resp.Events {
		if event.ID > latestID {
			latestID = event.ID
		}
		if !s.shouldProcess(event, scope, gatewayCode) {
			ackIDs = append(ackIDs, event.ID)
			continue
		}
		if event.Scope == "config:snapshot" {
			processed, err := s.configCache.ControlEventProcessed(ctx, event.ID)
			if err != nil {
				log.Printf("gateway-lite: cache invalidation event marker read failed id=%d: %v", event.ID, err)
				return
			}
			if !processed {
				pendingConfigEventIDs = append(pendingConfigEventIDs, event.ID)
			}
			ackIDs = append(ackIDs, event.ID)
			continue
		}
		if _, err := s.applyEvent(ctx, event, scope); err != nil {
			log.Printf("gateway-lite: cache invalidation event handle failed id=%d scope=%s: %v", event.ID, event.Scope, err)
			return
		}
		ackIDs = append(ackIDs, event.ID)
	}
	if len(pendingConfigEventIDs) > 0 {
		// 一个轮询批次可能积压很多配置变更。完整快照天然包含该批次全部变更，
		// 因此只拉取一次，再逐个写幂等标记，避免启动时对主站和 Redis 形成请求风暴。
		if err := s.handleEvent(ctx, CacheInvalidationEvent{Scope: "config:snapshot"}, scope); err != nil {
			log.Printf("gateway-lite: coalesced config invalidation refresh failed events=%d: %v", len(pendingConfigEventIDs), err)
			return
		}
		for _, eventID := range pendingConfigEventIDs {
			if err := s.configCache.MarkControlEventProcessed(ctx, eventID); err != nil {
				log.Printf("gateway-lite: cache invalidation event marker write failed id=%d: %v", eventID, err)
				return
			}
		}
	}

	if len(ackIDs) > 0 {
		// Redis 中的 polling cursor（轮询游标）只能在主站确认收到 ACK 后推进。
		// 事件副作用已经由 processed marker（处理标记）保护，因此 ACK 失败时
		// 下轮可以安全重放同一页，而不会重复清缓存或重复拉完整配置。
		if err := s.ack(ctx, latestID, ackIDs, scope, gatewayCode); err != nil {
			log.Printf("gateway-lite: cache invalidation ack failed: %v", err)
			return
		}
	}
	if latestID > sinceID {
		if err := s.configCache.SetInvalidationCursor(ctx, latestID); err != nil {
			log.Printf("gateway-lite: cache invalidation cursor write failed: %v", err)
			return
		}
	}
}

// ApplyPushedEvent handles the same event stream used by polling. It does not
// move the polling cursor; the next poll will observe the processed marker,
// acknowledge the event, and keep the permanent recovery path intact.
func (s *CacheInvalidationSyncer) ApplyPushedEvent(ctx context.Context, event CacheInvalidationEvent) (bool, error) {
	if s == nil {
		return false, errors.New("cache invalidation syncer is not configured")
	}
	if event.ID <= 0 {
		return false, errors.New("event_id is required")
	}
	region, gatewayCode, _ := s.currentRuntimeConfig()
	scope := gatewayCode
	if !s.shouldProcess(event, region, gatewayCode) {
		return false, ErrControlEventGatewayMismatch
	}
	return s.applyEvent(ctx, event, scope)
}

func (s *CacheInvalidationSyncer) applyEvent(ctx context.Context, event CacheInvalidationEvent, region string) (bool, error) {
	if s.configCache != nil {
		processed, err := s.configCache.ControlEventProcessed(ctx, event.ID)
		if err != nil {
			return false, err
		}
		if processed {
			return true, nil
		}
	}
	if err := s.handleEvent(ctx, event, region); err != nil {
		return false, err
	}
	if s.configCache != nil {
		if err := s.configCache.MarkControlEventProcessed(ctx, event.ID); err != nil {
			return false, err
		}
	}
	return false, nil
}

func (s *CacheInvalidationSyncer) SetRuntimeConfig(region, gatewayCode string, interval time.Duration) {
	if s == nil {
		return
	}
	region = strings.TrimSpace(region)
	gatewayCode = strings.TrimSpace(gatewayCode)
	if gatewayCode == "" {
		gatewayCode = defaultString(region, "default")
	}
	if region == "" {
		region = gatewayCode
	}
	if interval <= 0 {
		interval = defaultCacheInvalidationInterval
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.region = region
	s.gatewayCode = gatewayCode
	s.interval = interval
}

func (s *CacheInvalidationSyncer) currentInterval() time.Duration {
	_, _, interval := s.currentRuntimeConfig()
	return interval
}

func (s *CacheInvalidationSyncer) currentRuntimeConfig() (string, string, time.Duration) {
	if s == nil {
		return "default", "default", defaultCacheInvalidationInterval
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	region := s.region
	gatewayCode := s.gatewayCode
	interval := s.interval
	if region == "" {
		region = gatewayCode
	}
	if gatewayCode == "" {
		gatewayCode = defaultString(region, "default")
	}
	if interval <= 0 {
		interval = defaultCacheInvalidationInterval
	}
	return region, gatewayCode, interval
}

func (s *CacheInvalidationSyncer) shouldProcess(event CacheInvalidationEvent, region, gatewayCode string) bool {
	if event.GatewayCode != "" && event.GatewayCode != gatewayCode {
		return false
	}
	if event.GatewayCode == "" && event.Region != "" && event.Region != region {
		return false
	}
	return true
}

func (s *CacheInvalidationSyncer) handleEvent(ctx context.Context, event CacheInvalidationEvent, region string) error {
	switch event.Scope {
	case "config:snapshot":
		if s.configRefresher != nil {
			return s.configRefresher.SyncFull(ctx)
		}
	case "key:snapshot":
		if s.keyInvalidator != nil {
			if keyID := cacheInvalidationPayloadString(event.Payload, "key_id"); keyID != "" {
				if scoped, ok := s.keyInvalidator.(keyScopedInvalidator); ok {
					return scoped.DeleteKey(ctx, keyID, region)
				}
			}
			if organizationID := cacheInvalidationPayloadInt64(event.Payload, "organization_id"); organizationID > 0 {
				if scoped, ok := s.keyInvalidator.(organizationScopedInvalidator); ok {
					return scoped.DeleteOrganization(ctx, organizationID, region)
				}
				// 企业策略事件往往同时携带员工 user_id。这里禁止退化成用户或整区
				// 清理，否则会误删员工个人 Key，或在大节点上制造缓存击穿。
				return ErrOrganizationScopedKeyInvalidationUnsupported
			}
			if userID := cacheInvalidationPayloadInt64(event.Payload, "user_id"); userID > 0 {
				if scoped, ok := s.keyInvalidator.(userScopedInvalidator); ok {
					return scoped.DeleteUser(ctx, userID, region)
				}
			}
			return s.keyInvalidator.DeleteRegion(ctx, region)
		}
	case "lease:snapshot":
		if s.leaseInvalidator != nil {
			userID := cacheInvalidationPayloadInt64(event.Payload, "user_id")
			organizationID := cacheInvalidationPayloadInt64(event.Payload, "organization_id")
			leaseID := cacheInvalidationPayloadString(event.Payload, "lease_id")
			version := cacheInvalidationPayloadInt64(event.Payload, "version")
			if scoped, ok := s.leaseInvalidator.(scopedLeaseSnapshotInvalidator); ok {
				_, err := scoped.InvalidateLeaseSnapshotForSubject(ctx, LeaseSubject{
					UserID:             userID,
					OrganizationID:     organizationID,
					BillingSubjectType: cacheInvalidationPayloadString(event.Payload, "billing_subject_type"),
					BillingSubjectID:   cacheInvalidationPayloadInt64(event.Payload, "billing_subject_id"),
				}, region, leaseID, version)
				return err
			}
			_, err := s.leaseInvalidator.InvalidateLeaseSnapshot(ctx, userID, region, leaseID, version)
			return err
		}
	case "effective-policy:snapshot":
		// effective policy（最终生效策略）的正文由网关权威计算。主站巡检只能
		// 请求重新计算并上报，不能把主站镜像反向写回真实请求规则。这里必须
		// 强制完整快照，不能让已确认的旧 checksum（校验值）降级成空心跳。
		NotifyDefaultFullConfigSnapshot("mixed", "consistency_repair", 0)
	default:
		log.Printf("gateway-lite: ignored cache invalidation event id=%d scope=%s", event.ID, event.Scope)
	}
	return nil
}

func cacheInvalidationPayloadString(payload map[string]any, key string) string {
	if len(payload) == 0 || key == "" {
		return ""
	}
	switch value := payload[key].(type) {
	case string:
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func cacheInvalidationPayloadInt64(payload map[string]any, key string) int64 {
	if len(payload) == 0 || key == "" {
		return 0
	}
	switch value := payload[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	default:
		return 0
	}
}

func (s *CacheInvalidationSyncer) ack(ctx context.Context, latestID int64, ids []int64, region, gatewayCode string) error {
	// 同 redis_config_cache：用后台下限，避免更短的 context 期限覆盖客户端超时。
	reqCtx, cancel := context.WithTimeout(ctx, controlPlaneBackgroundTimeoutFloor)
	defer cancel()
	resp, err := s.client.FetchCacheInvalidations(reqCtx, CacheInvalidationRequest{
		GatewayCode: gatewayCode,
		Region:      region,
		SinceID:     latestID,
		Limit:       1,
		AckIDs:      ids,
	})
	if err != nil {
		return err
	}
	if resp == nil {
		return errors.New("cache invalidation ack returned an empty response")
	}
	if !resp.OK {
		message := strings.TrimSpace(resp.Error)
		if message == "" {
			message = "control-plane rejected the acknowledgement"
		}
		return fmt.Errorf("cache invalidation ack rejected: %s", message)
	}
	return nil
}

func defaultString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}
