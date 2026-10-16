package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const maxChannelMonitorNameRunes = 100

func duplicateChannelMonitorOperationID(sourceID int64, actorScope, operationKey string) string {
	operationKey = strings.TrimSpace(operationKey)
	if operationKey == "" {
		return ""
	}
	actorScope = strings.TrimSpace(actorScope)
	if actorScope == "" {
		actorScope = "admin:0"
	}
	payload := "admin.channel_monitors.duplicate\x00" + actorScope + "\x00" + strconv.FormatInt(sourceID, 10) + "\x00" + operationKey
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", digest)
}

func duplicateChannelMonitorName(sourceName string) string {
	const suffix = " (Copy)"
	base := []rune(strings.TrimSpace(sourceName))
	maxBase := maxChannelMonitorNameRunes - len([]rune(suffix))
	if maxBase < 0 {
		maxBase = 0
	}
	if len(base) > maxBase {
		base = base[:maxBase]
	}
	return string(base) + suffix
}

func cloneChannelMonitorHeaders(value map[string]string) map[string]string {
	if value == nil {
		return map[string]string{}
	}
	cloned := make(map[string]string, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func cloneChannelMonitorJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, item := range typed {
			cloned[key] = cloneChannelMonitorJSONValue(item)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneChannelMonitorJSONValue(item)
		}
		return cloned
	default:
		return value
	}
}

func cloneChannelMonitorForDuplicate(source *ChannelMonitor, createdBy int64, operationID string) *ChannelMonitor {
	duplicate := &ChannelMonitor{
		Name:                 duplicateChannelMonitorName(source.Name),
		Provider:             source.Provider,
		APIMode:              source.APIMode,
		Endpoint:             source.Endpoint,
		APIKey:               source.APIKey, // repository 返回密文；复制全程不解密、不下发浏览器。
		PrimaryModel:         source.PrimaryModel,
		ExtraModels:          append([]string(nil), source.ExtraModels...),
		GroupName:            source.GroupName,
		Enabled:              false,
		IntervalSeconds:      source.IntervalSeconds,
		JitterSeconds:        source.JitterSeconds,
		CreatedBy:            createdBy,
		DuplicateOperationID: operationID,
		ExtraHeaders:         cloneChannelMonitorHeaders(source.ExtraHeaders),
		BodyOverrideMode:     source.BodyOverrideMode,
	}
	if source.TemplateID != nil {
		templateID := *source.TemplateID
		duplicate.TemplateID = &templateID
	}
	if source.BodyOverride != nil {
		duplicate.BodyOverride, _ = cloneChannelMonitorJSONValue(source.BodyOverride).(map[string]any)
	}
	return duplicate
}

func (s *ChannelMonitorService) duplicateRepository() (ChannelMonitorDuplicateRepository, error) {
	repo, ok := s.repo.(ChannelMonitorDuplicateRepository)
	if !ok {
		return nil, errors.New("channel monitor duplicate repository is not configured")
	}
	return repo, nil
}

// RecoverDuplicate 只读恢复相同管理员、源监控和幂等键已经提交的副本。
// 返回前清空 APIKey，避免 handler 把密文前缀误当作明文脱敏结果。
func (s *ChannelMonitorService) RecoverDuplicate(ctx context.Context, id int64, actorScope, operationKey string) (*ChannelMonitor, error) {
	operationID := duplicateChannelMonitorOperationID(id, actorScope, operationKey)
	if operationID == "" {
		return nil, nil
	}
	repo, err := s.duplicateRepository()
	if err != nil {
		return nil, err
	}
	monitor, err := repo.FindByDuplicateOperationID(ctx, operationID)
	if err != nil {
		return nil, err
	}
	if monitor != nil {
		monitor.APIKey = ""
	}
	return monitor, nil
}

// Duplicate 在服务端加密域内复制渠道监控配置。副本默认停用，且不继承
// last_checked_at、历史明细、聚合结果或当前调度任务。
func (s *ChannelMonitorService) Duplicate(ctx context.Context, id, createdBy int64, actorScope, operationKey string) (*ChannelMonitor, error) {
	normalizedKey, err := NormalizeIdempotencyKey(operationKey)
	if err != nil {
		return nil, err
	}
	if normalizedKey == "" {
		return nil, ErrIdempotencyKeyRequired
	}
	if recovered, recoverErr := s.RecoverDuplicate(ctx, id, actorScope, normalizedKey); recoverErr != nil {
		return nil, recoverErr
	} else if recovered != nil {
		return recovered, nil
	}

	source, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	duplicate := cloneChannelMonitorForDuplicate(source, createdBy, duplicateChannelMonitorOperationID(id, actorScope, normalizedKey))
	if err := s.repo.Create(ctx, duplicate); err != nil {
		// 唯一键竞争或“数据库已提交但响应丢失”都先按 operation identity 恢复，
		// 防止多节点/降级路径重复创建携带同一密钥的监控副本。
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		recovered, recoveryErr := s.RecoverDuplicate(recoveryCtx, id, actorScope, normalizedKey)
		if recoveryErr == nil && recovered != nil {
			return recovered, nil
		}
		if recoveryErr != nil {
			return nil, fmt.Errorf("create channel monitor duplicate: %w (recovery lookup failed: %v)", err, recoveryErr)
		}
		return nil, fmt.Errorf("create channel monitor duplicate: %w", err)
	}

	// API 响应只表达“已配置密钥”的脱敏状态，不解密也不返回密文前缀。
	duplicate.APIKey = ""
	return duplicate, nil
}
