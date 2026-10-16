package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// 全文仍执行密钥脱敏，但不按长度截尾。专用密钥也做精确遮蔽，防止测试
// 服务或客户内容意外回显配置中的凭据。
func redactContentModerationAIText(text, apiKey string) string {
	if apiKey != "" {
		text = strings.ReplaceAll(text, apiKey, "[REDACTED]")
	}
	return redactContentModerationSecrets(text)
}

func buildAIReviewTextExcerpt(canonical *ContentModerationAICanonicalInput) string {
	if canonical == nil {
		return ""
	}
	var text strings.Builder
	for _, fragment := range canonical.Fragments {
		fmt.Fprintf(&text, "[%s|%s] %s\n", fragment.ParentID, fragment.Role, fragment.Text)
	}
	return text.String()
}

func (s *ContentModerationService) buildAIReviewDetail(canonical *ContentModerationAICanonicalInput, decision *ContentModerationDecision) map[string]any {
	detail := map[string]any{
		"verdict": decision.Verdict, "error_code": decision.ErrorCode,
		"phase": decision.Phase, "provider_called": decision.ProviderCalled,
		"provider_http_status": decision.ProviderHTTPStatus,
		"latency_ms":           decision.LatencyMS, "provider_latency_ms": decision.ProviderLatencyMS,
		"input_hash": decision.InputHash, "coverage": decision.Coverage,
		"reason": decision.Reason, "categories": decision.Categories,
	}
	if canonical == nil {
		return detail
	}
	fragments := make([]map[string]any, 0, len(canonical.Fragments))
	for _, fragment := range canonical.Fragments {
		fragments = append(fragments, map[string]any{
			"id": fragment.ID, "parent": fragment.ParentID, "role": fragment.Role,
			"source": fragment.Source, "start_rune": fragment.StartRune, "end_rune": fragment.EndRune,
		})
	}
	images := make([]map[string]any, 0, len(canonical.Images))
	for _, image := range canonical.Images {
		images = append(images, map[string]any{
			"id": image.ID, "parent": image.ParentID, "role": image.Role,
			"media_type": image.MediaType, "bytes": image.Bytes,
		})
	}
	// 整体判定不伪造每个段落/图片的单独分类；图片数据和外链不写入日志。
	detail["fragments"] = fragments
	detail["images"] = images
	detail["uncovered_fields"] = canonical.UncoveredFields
	return detail
}

// 详情是可选的只读仓储能力，不要求关键词测试替身实现全文读取。
func (s *ContentModerationService) GetLog(ctx context.Context, id int64) (*ContentModerationLog, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("content moderation repository unavailable")
	}
	reader, ok := s.repo.(interface {
		GetLog(context.Context, int64) (*ContentModerationLog, error)
	})
	if !ok {
		return nil, errors.New("content moderation log detail unavailable")
	}
	if id <= 0 {
		return nil, nil
	}
	return reader.GetLog(ctx, id)
}
