package service

import (
	"encoding/json"
	"sort"
	"strings"
)

// displayPriceFields 对齐主站的每百万 Token 字段。记录原始键存在性，避免
// float64 零值和 omitempty 抹掉「显式免费」与「缺价」的区别；不参与扣费计算。
var displayPriceFields = map[string]string{
	"input_cost_per_token":                                       "input_cost_per_1m_tokens",
	"output_cost_per_token":                                      "output_cost_per_1m_tokens",
	"cache_read_input_token_cost":                                "cache_read_cost_per_1m_tokens",
	"cache_creation_input_token_cost":                            "cache_creation_cost_per_1m_tokens",
	"cache_creation_input_token_cost_above_1hr":                  "cache_creation_cost_per_1m_tokens",
	"input_cost_per_token_priority":                              "input_cost_per_1m_tokens_priority",
	"output_cost_per_token_priority":                             "output_cost_per_1m_tokens_priority",
	"cache_read_input_token_cost_priority":                       "cache_read_cost_per_1m_tokens_priority",
	"cache_creation_input_token_cost_priority":                   "cache_creation_cost_per_1m_tokens_priority",
	"input_cost_per_token_above_272k_tokens":                     "input_cost_per_1m_tokens_above_272k",
	"output_cost_per_token_above_272k_tokens":                    "output_cost_per_1m_tokens_above_272k",
	"cache_read_input_token_cost_above_272k_tokens":              "cache_read_cost_per_1m_tokens_above_272k",
	"cache_creation_input_token_cost_above_272k_tokens":          "cache_creation_cost_per_1m_tokens_above_272k",
	"input_cost_per_token_priority_above_272k_tokens":            "input_cost_per_1m_tokens_priority_above_272k",
	"output_cost_per_token_priority_above_272k_tokens":           "output_cost_per_1m_tokens_priority_above_272k",
	"cache_read_input_token_cost_priority_above_272k_tokens":     "cache_read_cost_per_1m_tokens_priority_above_272k",
	"cache_creation_input_token_cost_priority_above_272k_tokens": "cache_creation_cost_per_1m_tokens_priority_above_272k",
	"input_cost_per_image_token":                                 "input_cost_per_image_token",
	"output_cost_per_image_token":                                "output_cost_per_image_token",
	"output_cost_per_image":                                      "output_cost_per_image",
	"per_request_price":                                          "per_request_price",
	"video_price_per_second_480p":                                "video_price_per_second_480p",
	"video_price_per_second_720p":                                "video_price_per_second_720p",
	"video_price_per_second_1080p":                               "video_price_per_second_1080p",
}

func applyModelPricingDisplayMetadata(pricing *LiteLLMModelPricing, entry LiteLLMRawEntry, raw json.RawMessage) {
	pricing.ModelVendor = strings.TrimSpace(entry.ModelVendor)
	declared := entry.InputModalities
	if len(declared) == 0 {
		declared = entry.SupportedModalities
	}
	seen := map[string]bool{}
	for _, modality := range declared {
		modality = strings.ToLower(strings.TrimSpace(modality))
		switch modality {
		case "text", "image", "audio", "video", "embedding":
			if !seen[modality] {
				pricing.InputModalities = append(pricing.InputModalities, modality)
				seen[modality] = true
			}
		}
	}
	// supports_vision 明确确认图片输入；不能据 chat 模式或模型名补文本/音频/视频。
	if len(pricing.InputModalities) == 0 && entry.SupportsVision != nil && *entry.SupportsVision {
		pricing.InputModalities = []string{"image"}
	}
	if entry.MaxInputTokens != nil && *entry.MaxInputTokens > 0 {
		pricing.MaxInputTokens = *entry.MaxInputTokens
	}
	if pricing.ModelVendor != "" || len(pricing.InputModalities) > 0 || pricing.MaxInputTokens > 0 {
		pricing.MetadataSource = strings.TrimSpace(entry.MetadataSource)
		if pricing.MetadataSource == "" {
			pricing.MetadataSource = "pricing-catalog"
		}
	}
	if entry.PriceFields != nil {
		pricing.PriceFields = append([]string{}, entry.PriceFields...)
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return
	}
	// 已序列化的旧覆盖对象带 null 表示未知，不把对象内的 Go 默认零反推为配置。
	if _, hasContract := fields["price_fields"]; hasContract {
		return
	}
	configured := map[string]bool{}
	for sourceField, targetField := range displayPriceFields {
		value, ok := fields[sourceField]
		if ok && string(value) != "null" {
			configured[targetField] = true
		}
	}
	pricing.PriceFields = make([]string, 0, len(configured))
	for field := range configured {
		pricing.PriceFields = append(pricing.PriceFields, field)
	}
	sort.Strings(pricing.PriceFields)
}

// finalizeModelPricingDisplayFields 保留 parser（解析器）按真实倍率生成的长上下文价格。
// 源已声明 price_fields 时完全服从；未声明时才合并派生正价，不能猜零价。
func finalizeModelPricingDisplayFields(pricing *LiteLLMModelPricing, raw json.RawMessage) {
	var source map[string]json.RawMessage
	if json.Unmarshal(raw, &source) != nil {
		return
	}
	if _, declared := source["price_fields"]; declared {
		return
	}
	encoded, err := json.Marshal(pricing)
	if err != nil {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil {
		return
	}
	configured := map[string]bool{}
	for _, field := range pricing.PriceFields {
		configured[field] = true
	}
	for sourceField, targetField := range displayPriceFields {
		var amount float64
		if json.Unmarshal(fields[sourceField], &amount) == nil && amount > 0 {
			configured[targetField] = true
		}
	}
	pricing.PriceFields = make([]string, 0, len(configured))
	for field := range configured {
		pricing.PriceFields = append(pricing.PriceFields, field)
	}
	sort.Strings(pricing.PriceFields)
}

// ModelPricingDisplayVendor 在分组将 provider 改成协议平台前保留目录供应商。
// 显式厂商优先；回退只影响展示，不作为输入模态或上下文能力的证据。
func ModelPricingDisplayVendor(modelVendor, provider string) string {
	if vendor := strings.TrimSpace(modelVendor); vendor != "" {
		return vendor
	}
	return strings.TrimSpace(provider)
}

// GetModelPricingDisplayMetadata 仅从精确模型目录读取能力；价格模糊匹配、家族兜底
// 不能作为客户自定义别名的模型能力事实。元数据缺失保持未知。
func (s *PricingService) GetModelPricingDisplayMetadata(model string) *LiteLLMModelPricing {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	model = strings.TrimSpace(model)
	pricing := s.pricingData[model]
	if pricing == nil {
		pricing = s.pricingData[strings.ToLower(model)]
	}
	if pricing == nil {
		return nil
	}
	copyPrice := *pricing
	// 管理员只覆盖价格时，继续保留同一精确目录条目的能力事实。
	base := s.basePricingData[model]
	if base == nil {
		base = s.basePricingData[strings.ToLower(model)]
	}
	if base != nil {
		if copyPrice.ModelVendor == "" {
			copyPrice.ModelVendor = base.ModelVendor
		}
		if len(copyPrice.InputModalities) == 0 {
			copyPrice.InputModalities = base.InputModalities
		}
		if copyPrice.MaxInputTokens == 0 {
			copyPrice.MaxInputTokens = base.MaxInputTokens
		}
		if copyPrice.MetadataSource == "" {
			copyPrice.MetadataSource = base.MetadataSource
		}
	}
	copyPrice.InputModalities = append([]string(nil), copyPrice.InputModalities...)
	copyPrice.PriceFields = append([]string(nil), pricing.PriceFields...)
	return &copyPrice
}
