package server

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 覆盖价可能把正数改成 0，必须按指针存在性保留“免费”的展示合同。
// 切换计费方式时删除不再适用的 Token 存在性，避免清零字段被误标免费。
func applyGatewayLiteDisplayPriceFields(price *gatewaylite.GatewayModelPrice, catalog gatewaylite.GatewayModelPrice, override service.ChannelModelPricing, mode string) {
	fields := map[string]bool{}
	if mode == "token" {
		for _, field := range catalog.PriceFields {
			fields[field] = true
		}
		// 旧目录 null（未知）只能从已有正价确认存在性；显式 [] 不得推导残价。
		if catalog.PriceFields == nil {
			raw, _ := json.Marshal(catalog)
			var data map[string]json.RawMessage
			if json.Unmarshal(raw, &data) == nil {
				for field, value := range data {
					if !strings.Contains(field, "cost_per_") && field != "per_request_price" && !strings.HasPrefix(field, "video_price_per_second_") {
						continue
					}
					var amount float64
					if json.Unmarshal(value, &amount) == nil && amount > 0 {
						fields[field] = true
					}
				}
			}
		}
	}
	for _, entry := range []struct {
		field string
		value *float64
	}{
		{"input_cost_per_1m_tokens", override.InputPrice},
		{"output_cost_per_1m_tokens", override.OutputPrice},
		{"cache_read_cost_per_1m_tokens", override.CacheReadPrice},
		{"cache_creation_cost_per_1m_tokens", override.CacheWritePrice},
	} {
		if entry.value == nil || mode != "token" {
			continue
		}
		fields[entry.field] = true
		fields[entry.field+"_priority"] = true
		// 长上下文只有基础目录已给出附加价时才由现有逻辑生成。
		for _, field := range catalog.PriceFields {
			if strings.HasPrefix(field, strings.TrimSuffix(entry.field, "_tokens")) && strings.HasSuffix(field, "_above_272k") {
				fields[field] = true
			}
		}
	}
	if override.ImageInputPrice != nil {
		fields["input_cost_per_image_token"] = true
	}
	if override.ImageOutputPrice != nil {
		fields["output_cost_per_image_token"] = true
	}
	if override.PerRequestPrice != nil {
		switch mode {
		case "image":
			fields["output_cost_per_image"] = true
		case "video":
			for _, tier := range []string{"480p", "720p", "1080p"} {
				fields["video_price_per_second_"+tier] = true
			}
		default:
			fields["per_request_price"] = true
		}
	}
	for _, interval := range override.Intervals {
		if interval.PerRequestPrice == nil {
			continue
		}
		if mode == "video" {
			tier := strings.ToLower(strings.TrimSpace(interval.TierLabel))
			if tier == "480p" || tier == "720p" || tier == "1080p" {
				fields["video_price_per_second_"+tier] = true
			}
		}
		// 图片区间在现有逻辑里同时生成默认每张价。
		if mode == "image" {
			fields["output_cost_per_image"] = true
		}
	}
	if len(fields) == 0 && catalog.PriceFields == nil && mode == "token" {
		price.PriceFields = nil
		return
	}
	price.PriceFields = make([]string, 0, len(fields))
	for field := range fields {
		price.PriceFields = append(price.PriceFields, field)
	}
	sort.Strings(price.PriceFields)
}
