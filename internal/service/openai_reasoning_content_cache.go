package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// ErrReasoningContentNotFound 表示 reasoning item ID 没有对应缓存。
var ErrReasoningContentNotFound = errors.New("reasoning content not found")

// ReasoningContentStore 由 GatewayCache 的 Redis 实现可选提供，避免扩大共享接口。
type ReasoningContentStore interface {
	SetReasoningContent(ctx context.Context, itemID string, content string, ttl time.Duration) error
	GetReasoningContent(ctx context.Context, itemID string) (string, error)
}

// Codex 会话可能跨多天恢复；7 天兼顾续接能力与敏感思考内容的自动清理。
const responsesReasoningCacheTTL = 7 * 24 * time.Hour

func (s *OpenAIGatewayService) reasoningContentStore() ReasoningContentStore {
	if s == nil || s.cache == nil {
		return nil
	}
	store, _ := s.cache.(ReasoningContentStore)
	return store
}

// reasoningContentByID 为桥接层提供 fail-open 回查；缓存异常不能阻断请求。
func (s *OpenAIGatewayService) reasoningContentByID(itemID string) string {
	store := s.reasoningContentStore()
	if store == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	content, err := store.GetReasoningContent(ctx, itemID)
	if err != nil {
		return ""
	}
	return content
}

// recacheReasoningItemsFromInput 用客户端仍携带的明文 summary 自愈缓存并刷新 TTL。
func (s *OpenAIGatewayService) recacheReasoningItemsFromInput(inputRaw json.RawMessage) {
	if s.reasoningContentStore() == nil {
		return
	}
	inputRaw = bytes.TrimSpace(inputRaw)
	if len(inputRaw) == 0 || inputRaw[0] != '[' {
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(inputRaw, &items); err != nil {
		return
	}
	for _, raw := range items {
		itemID, content, ok := apicompat.ExtractResponsesReasoningItem(raw)
		if ok && itemID != "" && content != "" {
			s.setReasoningContent(itemID, content)
		}
	}
}

func (s *OpenAIGatewayService) cacheReasoningItemsFromEvents(events []apicompat.ResponsesStreamEvent) {
	for _, event := range events {
		if event.Type == "response.output_item.done" {
			s.cacheReasoningItem(event.Item)
		}
	}
}

func (s *OpenAIGatewayService) cacheReasoningItemsFromOutput(output []apicompat.ResponsesOutput) {
	for index := range output {
		s.cacheReasoningItem(&output[index])
	}
}

func (s *OpenAIGatewayService) cacheReasoningItem(item *apicompat.ResponsesOutput) {
	if item == nil || item.Type != "reasoning" || item.ID == "" {
		return
	}
	parts := make([]string, 0, len(item.Summary))
	for _, summary := range item.Summary {
		if text := strings.TrimSpace(summary.Text); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) > 0 {
		s.setReasoningContent(item.ID, strings.Join(parts, "\n"))
	}
}

// setReasoningContent 使用脱离请求的短超时上下文；客户端断开后仍需缓存已排空的上游结果。
func (s *OpenAIGatewayService) setReasoningContent(itemID, content string) {
	store := s.reasoningContentStore()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := store.SetReasoningContent(ctx, itemID, content, responsesReasoningCacheTTL); err != nil {
		logger.L().Warn("openai responses chat fallback: cache reasoning content failed",
			zap.Error(err),
			zap.String("item_id", itemID),
		)
	}
}
