package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	modelpricing "github.com/Wei-Shaw/sub2api/resources/model-pricing"
	"go.uber.org/zap"
)

var (
	openAIModelDatePattern = regexp.MustCompile(`-\d{8}$`)
	openAIModelBasePattern = regexp.MustCompile(`^(gpt-\d+(?:\.\d+)?)(?:-|$)`)

	// cacheTierPricePattern 匹配 cache 侧的长上下文绝对价字段名
	// （cache_creation_input_token_cost_above_272k_tokens、cache_read_input_token_cost_above_272k_tokens_priority、
	// cache_creation_input_token_cost_above_1hr_above_272k_tokens 等）。
	// 阈值数字不限定具体档位：目录契约是「键名数字 × 1000 = 阈值」，跟随源数据演进。
	cacheTierPricePattern = regexp.MustCompile(`^(cache_(?:creation|read)_input_token_cost)(_above_1hr)?_above_\d+k_tokens((?:_[a-z]+)?)$`)

	// 这组价格是 2026-07-10 从 OpenAI 官方价格页和模型页核对后的受控快照。
	// 它只在远程目录缺少模型时兜底；远程目录是可独立更新的官方价格来源，
	// 不能被编译进二进制的旧快照反向覆盖。
	officialOpenAITextPricing = map[string]*LiteLLMModelPricing{
		// 固定价源 tokenav-catalog@451ff802（2026.10.03.4）：独立审查模型不可借通用 Codex 猜价。
		"codex-auto-review": {
			InputCostPerToken:                5e-6,
			InputCostPerTokenPriority:        10e-6,
			OutputCostPerToken:               30e-6,
			OutputCostPerTokenPriority:       60e-6,
			CacheReadInputTokenCost:          0.5e-6,
			CacheReadInputTokenCostPriority:  1e-6,
			InputCostPerTokenAbove272K:       10e-6,
			OutputCostPerTokenAbove272K:      45e-6,
			CacheReadInputTokenCostAbove272K: 1e-6,
			LongContextInputTokenThreshold:   272000,
			LongContextInputCostMultiplier:   2,
			LongContextOutputCostMultiplier:  1.5,
			LiteLLMProvider:                  "openai",
			Mode:                             "chat",
			SupportsPromptCaching:            true,
			SupportsServiceTier:              true,
		},
		// GPT-6 Astra（2026-09-03 发布）：标准 $10/$50，缓存读 $1、缓存写 $12.5；
		// >272K 整请求 2x input/cache、1.5x output；Fast/priority 2x。
		"gpt-6-astra": {
			InputCostPerToken:                    1e-05,
			InputCostPerTokenPriority:            2e-05,
			OutputCostPerToken:                   5e-05,
			OutputCostPerTokenPriority:           1e-04,
			CacheCreationInputTokenCost:          1.25e-05,
			CacheCreationInputTokenCostPriority:  2.5e-05,
			CacheReadInputTokenCost:              1e-06,
			CacheReadInputTokenCostPriority:      2e-06,
			InputCostPerTokenAbove272K:           2e-05,
			OutputCostPerTokenAbove272K:          7.5e-05,
			CacheCreationInputTokenCostAbove272K: 2.5e-05,
			CacheReadInputTokenCostAbove272K:     2e-06,
			LongContextInputTokenThreshold:       272000,
			LongContextInputCostMultiplier:       2.0,
			LongContextOutputCostMultiplier:      1.5,
			LiteLLMProvider:                      "openai",
			Mode:                                 "chat",
			SupportsPromptCaching:                true,
			SupportsServiceTier:                  true,
		},
		"gpt-5.4": {
			InputCostPerToken:                2.5e-06,
			InputCostPerTokenPriority:        5e-06,
			OutputCostPerToken:               1.5e-05,
			OutputCostPerTokenPriority:       3e-05,
			CacheReadInputTokenCost:          2.5e-07,
			CacheReadInputTokenCostPriority:  5e-07,
			InputCostPerTokenAbove272K:       5e-06,
			OutputCostPerTokenAbove272K:      2.25e-05,
			CacheReadInputTokenCostAbove272K: 5e-07,
			LongContextInputTokenThreshold:   272000,
			LongContextInputCostMultiplier:   2.0,
			LongContextOutputCostMultiplier:  1.5,
			LiteLLMProvider:                  "openai",
			Mode:                             "chat",
			SupportsPromptCaching:            true,
			SupportsServiceTier:              true,
		},
		"gpt-5.5": {
			InputCostPerToken:                5e-06,
			InputCostPerTokenPriority:        1.25e-05,
			OutputCostPerToken:               3e-05,
			OutputCostPerTokenPriority:       7.5e-05,
			CacheReadInputTokenCost:          5e-07,
			CacheReadInputTokenCostPriority:  1.25e-06,
			InputCostPerTokenAbove272K:       1e-05,
			OutputCostPerTokenAbove272K:      4.5e-05,
			CacheReadInputTokenCostAbove272K: 1e-06,
			LongContextInputTokenThreshold:   272000,
			LongContextInputCostMultiplier:   2.0,
			LongContextOutputCostMultiplier:  1.5,
			LiteLLMProvider:                  "openai",
			Mode:                             "chat",
			SupportsPromptCaching:            true,
			SupportsServiceTier:              true,
		},
		"gpt-5.5-pro": {
			InputCostPerToken:           30e-06,
			OutputCostPerToken:          180e-06,
			CacheCreationInputTokenCost: 30e-06,
			// tokenav-catalog@451ff802 的缓存读价 $3/MTok，长上下文为 $6/MTok。
			CacheReadInputTokenCost:              3e-06,
			InputCostPerTokenAbove272K:           60e-06,
			OutputCostPerTokenAbove272K:          270e-06,
			CacheCreationInputTokenCostAbove272K: 60e-06,
			CacheReadInputTokenCostAbove272K:     6e-06,
			LongContextInputTokenThreshold:       272000,
			LongContextInputCostMultiplier:       2.0,
			LongContextOutputCostMultiplier:      1.5,
			LiteLLMProvider:                      "openai",
			Mode:                                 "chat",
			SupportsPromptCaching:                true,
		},
		"gpt-5.6-sol": {
			InputCostPerToken:                    5e-06,
			InputCostPerTokenPriority:            1e-05,
			OutputCostPerToken:                   3e-05,
			OutputCostPerTokenPriority:           6e-05,
			CacheCreationInputTokenCost:          6.25e-06,
			CacheCreationInputTokenCostPriority:  1.25e-05,
			CacheReadInputTokenCost:              5e-07,
			CacheReadInputTokenCostPriority:      1e-06,
			InputCostPerTokenAbove272K:           1e-05,
			OutputCostPerTokenAbove272K:          4.5e-05,
			CacheCreationInputTokenCostAbove272K: 1.25e-05,
			CacheReadInputTokenCostAbove272K:     1e-06,
			LongContextInputTokenThreshold:       272000,
			LongContextInputCostMultiplier:       2.0,
			LongContextOutputCostMultiplier:      1.5,
			LiteLLMProvider:                      "openai",
			Mode:                                 "chat",
			SupportsPromptCaching:                true,
			SupportsServiceTier:                  true,
		},
		"gpt-5.6-terra": {
			InputCostPerToken:                    2e-06,
			InputCostPerTokenPriority:            4e-06,
			OutputCostPerToken:                   1.2e-05,
			OutputCostPerTokenPriority:           2.4e-05,
			CacheCreationInputTokenCost:          2.5e-06,
			CacheCreationInputTokenCostPriority:  5e-06,
			CacheReadInputTokenCost:              2e-07,
			CacheReadInputTokenCostPriority:      4e-07,
			InputCostPerTokenAbove272K:           4e-06,
			OutputCostPerTokenAbove272K:          1.8e-05,
			CacheCreationInputTokenCostAbove272K: 5e-06,
			CacheReadInputTokenCostAbove272K:     4e-07,
			LongContextInputTokenThreshold:       272000,
			LongContextInputCostMultiplier:       2.0,
			LongContextOutputCostMultiplier:      1.5,
			LiteLLMProvider:                      "openai",
			Mode:                                 "chat",
			SupportsPromptCaching:                true,
			SupportsServiceTier:                  true,
		},
		"gpt-5.6-luna": {
			InputCostPerToken:                    2e-07,
			InputCostPerTokenPriority:            4e-07,
			OutputCostPerToken:                   1.2e-06,
			OutputCostPerTokenPriority:           2.4e-06,
			CacheCreationInputTokenCost:          2.5e-07,
			CacheCreationInputTokenCostPriority:  5e-07,
			CacheReadInputTokenCost:              2e-08,
			CacheReadInputTokenCostPriority:      4e-08,
			InputCostPerTokenAbove272K:           4e-07,
			OutputCostPerTokenAbove272K:          1.8e-06,
			CacheCreationInputTokenCostAbove272K: 5e-07,
			CacheReadInputTokenCostAbove272K:     4e-08,
			LongContextInputTokenThreshold:       272000,
			LongContextInputCostMultiplier:       2.0,
			LongContextOutputCostMultiplier:      1.5,
			LiteLLMProvider:                      "openai",
			Mode:                                 "chat",
			SupportsPromptCaching:                true,
			SupportsServiceTier:                  true,
		},
	}
	openAIGPT54MiniFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:               7.5e-07,
		InputCostPerTokenPriority:       1.5e-06,
		OutputCostPerToken:              4.5e-06,
		OutputCostPerTokenPriority:      9e-06,
		CacheReadInputTokenCost:         7.5e-08,
		CacheReadInputTokenCostPriority: 1.5e-07,
		LiteLLMProvider:                 "openai",
		Mode:                            "chat",
		SupportsPromptCaching:           true,
		SupportsServiceTier:             true,
	}
	openAIGPT54NanoFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:       2e-07,
		OutputCostPerToken:      1.25e-06,
		CacheReadInputTokenCost: 2e-08,
		LiteLLMProvider:         "openai",
		Mode:                    "chat",
		SupportsPromptCaching:   true,
	}
	// 这组价格是 2026-07-19 从 Z.AI 与 Kimi 官方 API 价格页核对后的受控快照。
	// 第三方 OpenAI-compatible（OpenAI 兼容）上游经常先暴露新模型、后等待
	// LiteLLM 目录收录；本地兜底可避免模型已经可调度却因缺价无法计费和展示。
	officialCompatibleTextPricing = map[string]*LiteLLMModelPricing{
		"deepseek-v4-flash": {
			InputCostPerToken:       1.4e-7,
			OutputCostPerToken:      2.8e-7,
			CacheReadInputTokenCost: 2.8e-9,
			LiteLLMProvider:         "deepseek",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		// 兼容上游公开的精确模型 ID，不改写请求模型名，只复用同一官方 SKU 价格。
		"deepseek-v4-flash-0731": {
			InputCostPerToken:       1.4e-7,
			OutputCostPerToken:      2.8e-7,
			CacheReadInputTokenCost: 2.8e-9,
			LiteLLMProvider:         "deepseek",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		"deepseek-v4-flash-vip": {
			InputCostPerToken:       1.4e-7,
			OutputCostPerToken:      2.8e-7,
			CacheReadInputTokenCost: 2.8e-9,
			LiteLLMProvider:         "deepseek",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		"glm-5.1": {
			InputCostPerToken:       1.4e-6,
			OutputCostPerToken:      4.4e-6,
			CacheReadInputTokenCost: 0.26e-6,
			LiteLLMProvider:         "zai",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		"glm-5.1-vip": {
			InputCostPerToken:       1.4e-6,
			OutputCostPerToken:      4.4e-6,
			CacheReadInputTokenCost: 0.26e-6,
			LiteLLMProvider:         "zai",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		"GLM-5.2": {
			InputCostPerToken:       1.4e-6,
			OutputCostPerToken:      4.4e-6,
			CacheReadInputTokenCost: 0.26e-6,
			LiteLLMProvider:         "zai",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		"glm-5.2": {
			InputCostPerToken:       1.4e-6,
			OutputCostPerToken:      4.4e-6,
			CacheReadInputTokenCost: 0.26e-6,
			LiteLLMProvider:         "zai",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		"glm-5.2-vip": {
			InputCostPerToken:       1.4e-6,
			OutputCostPerToken:      4.4e-6,
			CacheReadInputTokenCost: 0.26e-6,
			LiteLLMProvider:         "zai",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		"glm-5.2-vip1": {
			InputCostPerToken:       1.4e-6,
			OutputCostPerToken:      4.4e-6,
			CacheReadInputTokenCost: 0.26e-6,
			LiteLLMProvider:         "zai",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		"kimi-k3": {
			InputCostPerToken:       3e-6,
			OutputCostPerToken:      15e-6,
			CacheReadInputTokenCost: 0.3e-6,
			LiteLLMProvider:         "moonshot",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		// kimi-k3-code 是当前上游单独暴露的客户模型名；按用户要求不做映射，
		// 作为独立模型沿用 kimi-k3 官方价格。
		"kimi-k3-code": {
			InputCostPerToken:       3e-6,
			OutputCostPerToken:      15e-6,
			CacheReadInputTokenCost: 0.3e-6,
			LiteLLMProvider:         "moonshot",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		"kimi-k3-vip": {
			InputCostPerToken:       3e-6,
			OutputCostPerToken:      15e-6,
			CacheReadInputTokenCost: 0.3e-6,
			LiteLLMProvider:         "moonshot",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		"kimi-k3-vip2": {
			InputCostPerToken:       3e-6,
			OutputCostPerToken:      15e-6,
			CacheReadInputTokenCost: 0.3e-6,
			LiteLLMProvider:         "moonshot",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		"kimi-k3-vip3": {
			InputCostPerToken:       3e-6,
			OutputCostPerToken:      15e-6,
			CacheReadInputTokenCost: 0.3e-6,
			LiteLLMProvider:         "moonshot",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		// Kimi Code 官方端点使用 bare model ID。它们和 kimi-k3-code 一样作为
		// 独立可请求模型保存，只复用 Kimi K3 官方价格，不做请求模型映射。
		"k3": {
			InputCostPerToken:       3e-6,
			OutputCostPerToken:      15e-6,
			CacheReadInputTokenCost: 0.3e-6,
			LiteLLMProvider:         "moonshot",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		"k3-256k": {
			InputCostPerToken:       3e-6,
			OutputCostPerToken:      15e-6,
			CacheReadInputTokenCost: 0.3e-6,
			LiteLLMProvider:         "moonshot",
			Mode:                    "chat",
			BillingMode:             "token",
			SupportsPromptCaching:   true,
		},
		"minimax-m2.1": {
			InputCostPerToken:           0.3e-6,
			OutputCostPerToken:          1.2e-6,
			CacheCreationInputTokenCost: 0.375e-6,
			CacheReadInputTokenCost:     0.03e-6,
			LiteLLMProvider:             "minimax",
			Mode:                        "chat",
			BillingMode:                 "token",
			SupportsPromptCaching:       true,
		},
		"minimax-m2.5": {
			InputCostPerToken:           0.3e-6,
			OutputCostPerToken:          1.2e-6,
			CacheCreationInputTokenCost: 0.375e-6,
			CacheReadInputTokenCost:     0.03e-6,
			LiteLLMProvider:             "minimax",
			Mode:                        "chat",
			BillingMode:                 "token",
			SupportsPromptCaching:       true,
		},
		// Qwen3.5 Plus 在总输入超过 256K token 时整次请求切换到长上下文档。
		// 现有 Above272K 字段是兼容旧协议的字段名，实际边界由 threshold 决定。
		"qwen3.5-plus": {
			InputCostPerToken:                    0.4e-6,
			OutputCostPerToken:                   2.4e-6,
			CacheCreationInputTokenCost:          0.5e-6,
			CacheReadInputTokenCost:              0.04e-6,
			InputCostPerTokenAbove272K:           0.5e-6,
			OutputCostPerTokenAbove272K:          3e-6,
			CacheCreationInputTokenCostAbove272K: 0.625e-6,
			CacheReadInputTokenCostAbove272K:     0.05e-6,
			LongContextInputTokenThreshold:       256000,
			LongContextInputCostMultiplier:       1.25,
			LongContextOutputCostMultiplier:      1.25,
			LiteLLMProvider:                      "qwen",
			Mode:                                 "chat",
			BillingMode:                          "token",
			SupportsPromptCaching:                true,
		},
		"qwen3.8-max": {
			InputCostPerToken:           2e-6,
			OutputCostPerToken:          6e-6,
			CacheCreationInputTokenCost: 2.5e-6,
			CacheReadInputTokenCost:     0.25e-6,
			LiteLLMProvider:             "qwen",
			Mode:                        "chat",
			BillingMode:                 "token",
			SupportsPromptCaching:       true,
		},
	}
	// MiMo pricing is not always present in the mirrored LiteLLM catalog yet.
	// Keep an official USD fallback so gateway price reporting does not depend on
	// the runtime cache file being refreshed.
	mimoV25ProFallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:           4.35e-07, // $0.435 per MTok
		OutputCostPerToken:          8.7e-07,  // $0.87 per MTok
		CacheReadInputTokenCost:     3.6e-09,  // $0.0036 per MTok
		CacheCreationInputTokenCost: 0,
		LiteLLMProvider:             "xiaomi",
		Mode:                        "chat",
		SupportsPromptCaching:       true,
	}
	mimoV25FallbackPricing = &LiteLLMModelPricing{
		InputCostPerToken:           1.4e-07, // $0.14 per MTok
		OutputCostPerToken:          2.8e-07, // $0.28 per MTok
		CacheReadInputTokenCost:     2.8e-09, // $0.0028 per MTok
		CacheCreationInputTokenCost: 0,
		LiteLLMProvider:             "xiaomi",
		Mode:                        "chat",
		SupportsPromptCaching:       true,
	}
	officialGrokPricing = map[string]*LiteLLMModelPricing{
		"grok-4.5": {
			InputCostPerToken:               2e-6,
			OutputCostPerToken:              6e-6,
			CacheReadInputTokenCost:         0.3e-6,
			LongContextInputTokenThreshold:  200000,
			LongContextThresholdInclusive:   true,
			LongContextInputCostMultiplier:  2,
			LongContextOutputCostMultiplier: 2,
			LiteLLMProvider:                 "xai",
			Mode:                            "chat",
			BillingMode:                     "token",
			SupportsPromptCaching:           true,
		},
		"grok-4.6": {
			InputCostPerToken:               2e-6,
			OutputCostPerToken:              6e-6,
			CacheReadInputTokenCost:         0.5e-6,
			LongContextInputTokenThreshold:  200000,
			LongContextThresholdInclusive:   true,
			LongContextInputCostMultiplier:  2,
			LongContextOutputCostMultiplier: 2,
			LiteLLMProvider:                 "xai",
			Mode:                            "chat",
			BillingMode:                     "token",
			SupportsPromptCaching:           true,
		},
		"grok-imagine": {
			OutputCostPerImage: 0.05,
			LiteLLMProvider:    "xai",
			Mode:               "image_generation",
			BillingMode:        "image",
		},
		"grok-imagine-image": {
			OutputCostPerImage: 0.02,
			LiteLLMProvider:    "xai",
			Mode:               "image_generation",
			BillingMode:        "image",
		},
		"grok-imagine-image-quality": {
			OutputCostPerImage: 0.05,
			LiteLLMProvider:    "xai",
			Mode:               "image_generation",
			BillingMode:        "image",
		},
		"grok-imagine-edit": {
			OutputCostPerImage: 0.02,
			LiteLLMProvider:    "xai",
			Mode:               "image_edit",
			BillingMode:        "image",
		},
		"grok-imagine-video": {
			LiteLLMProvider:         "xai",
			Mode:                    "video_generation",
			BillingMode:             "video",
			VideoPricePerSecond480P: defaultGrokImagineVideoPrice480P,
			VideoPricePerSecond720P: defaultGrokImagineVideoPrice720P,
		},
		"grok-imagine-video-1.5": {
			LiteLLMProvider:          "xai",
			Mode:                     "video_generation",
			BillingMode:              "video",
			VideoPricePerSecond480P:  defaultGrokImagineVideo15Price480P,
			VideoPricePerSecond720P:  defaultGrokImagineVideo15Price720P,
			VideoPricePerSecond1080P: defaultGrokImagineVideo15Price1080P,
		},
	}
)

const (
	// MaxPricingCatalogBytes 同时用于远程 HTTP 读取和服务层校验，避免异常响应
	// 在进入 JSON 解析前占用不受控内存。
	MaxPricingCatalogBytes     = 16 << 20
	MaxPricingManifestBytes    = 128 << 10
	minRemotePricingEntries    = 50
	maxRemotePricingEntries    = 100000
	minPricingRetentionPercent = 80
)

type pricingCatalogManifest struct {
	SchemaVersion  int                        `json:"schema_version"`
	CatalogVersion string                     `json:"catalog_version"`
	PublishedAt    string                     `json:"published_at"`
	Sequence       uint64                     `json:"sequence"`
	ModelCount     int                        `json:"model_count"`
	PricingCatalog pricingCatalogManifestFile `json:"pricing_catalog"`
}

type pricingCatalogManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// LiteLLMModelPricing LiteLLM价格数据结构
// 只保留我们需要的字段，使用指针来处理可能缺失的值
type LiteLLMModelPricing struct {
	InputCostPerToken                            float64 `json:"input_cost_per_token"`
	InputCostPerTokenPriority                    float64 `json:"input_cost_per_token_priority"`
	OutputCostPerToken                           float64 `json:"output_cost_per_token"`
	OutputCostPerTokenPriority                   float64 `json:"output_cost_per_token_priority"`
	CacheCreationInputTokenCost                  float64 `json:"cache_creation_input_token_cost"`
	CacheCreationInputTokenCostPriority          float64 `json:"cache_creation_input_token_cost_priority"`
	CacheCreationInputTokenCostAbove1hr          float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheReadInputTokenCost                      float64 `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostPriority              float64 `json:"cache_read_input_token_cost_priority"`
	InputCostPerTokenAbove272K                   float64 `json:"input_cost_per_token_above_272k_tokens,omitempty"`
	InputCostPerTokenPriorityAbove272K           float64 `json:"input_cost_per_token_priority_above_272k_tokens,omitempty"`
	OutputCostPerTokenAbove272K                  float64 `json:"output_cost_per_token_above_272k_tokens,omitempty"`
	OutputCostPerTokenPriorityAbove272K          float64 `json:"output_cost_per_token_priority_above_272k_tokens,omitempty"`
	CacheCreationInputTokenCostAbove272K         float64 `json:"cache_creation_input_token_cost_above_272k_tokens,omitempty"`
	CacheCreationInputTokenCostPriorityAbove272K float64 `json:"cache_creation_input_token_cost_priority_above_272k_tokens,omitempty"`
	CacheReadInputTokenCostAbove272K             float64 `json:"cache_read_input_token_cost_above_272k_tokens,omitempty"`
	CacheReadInputTokenCostPriorityAbove272K     float64 `json:"cache_read_input_token_cost_priority_above_272k_tokens,omitempty"`
	LongContextInputTokenThreshold               int     `json:"long_context_input_token_threshold,omitempty"`
	LongContextThresholdInclusive                bool    `json:"long_context_threshold_inclusive,omitempty"`
	LongContextInputCostMultiplier               float64 `json:"long_context_input_cost_multiplier,omitempty"`
	LongContextOutputCostMultiplier              float64 `json:"long_context_output_cost_multiplier,omitempty"`
	SupportsServiceTier                          bool    `json:"supports_service_tier"`
	LiteLLMProvider                              string  `json:"litellm_provider"`
	// 展示元数据只保留目录声明；价格 provider（渠道）不能当模型厂商。
	ModelVendor                  string   `json:"model_vendor,omitempty"`
	InputModalities              []string `json:"input_modalities,omitempty"`
	MaxInputTokens               int      `json:"max_input_tokens,omitempty"`
	MetadataSource               string   `json:"metadata_source,omitempty"`
	PriceFields                  []string `json:"price_fields"`
	Mode                         string   `json:"mode"`
	BillingMode                  string   `json:"billing_mode,omitempty"`
	SupportsPromptCaching        bool     `json:"supports_prompt_caching"`
	OutputCostPerImage           float64  `json:"output_cost_per_image"`                       // 图片生成模型每张图片价格
	OutputCostPerImageToken      float64  `json:"output_cost_per_image_token"`                 // 图片输出 token 价格
	InputCostPerImageToken       *float64 `json:"input_cost_per_image_token,omitempty"`        // nil 继承文本输入价，显式 0 表示免费
	CacheReadInputImageTokenCost *float64 `json:"cache_read_input_image_token_cost,omitempty"` // [v0.2.5 新增字段] 图片缓存读取价
	PerRequestPrice              float64  `json:"per_request_price,omitempty"`                 // 按次计费默认价格
	VideoPricePerSecond480P      float64  `json:"video_price_per_second_480p,omitempty"`
	VideoPricePerSecond720P      float64  `json:"video_price_per_second_720p,omitempty"`
	VideoPricePerSecond1080P     float64  `json:"video_price_per_second_1080p,omitempty"`
	Disabled                     bool     `json:"disabled,omitempty"` // 本地禁用后不再对客户暴露或放行请求
	// TokenPricingAbsent 区分“价格显式为 0”和“源数据根本没有 Token 价格”。
	TokenPricingAbsent bool `json:"-"`
}

// PricingRemoteClient 远程价格数据获取接口
type PricingRemoteClient interface {
	FetchPricingJSON(ctx context.Context, url string) ([]byte, error)
	FetchHashText(ctx context.Context, url string) (string, error)
}

// LiteLLMRawEntry 用于解析原始JSON数据
type LiteLLMRawEntry struct {
	InputCostPerToken                            *float64 `json:"input_cost_per_token"`
	InputCostPerTokenPriority                    *float64 `json:"input_cost_per_token_priority"`
	OutputCostPerToken                           *float64 `json:"output_cost_per_token"`
	OutputCostPerTokenPriority                   *float64 `json:"output_cost_per_token_priority"`
	CacheCreationInputTokenCost                  *float64 `json:"cache_creation_input_token_cost"`
	CacheCreationInputTokenCostPriority          *float64 `json:"cache_creation_input_token_cost_priority"`
	CacheCreationInputTokenCostAbove1hr          *float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheReadInputTokenCost                      *float64 `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostPriority              *float64 `json:"cache_read_input_token_cost_priority"`
	InputCostPerTokenAbove272K                   *float64 `json:"input_cost_per_token_above_272k_tokens"`
	InputCostPerTokenPriorityAbove272K           *float64 `json:"input_cost_per_token_priority_above_272k_tokens"`
	OutputCostPerTokenAbove272K                  *float64 `json:"output_cost_per_token_above_272k_tokens"`
	OutputCostPerTokenPriorityAbove272K          *float64 `json:"output_cost_per_token_priority_above_272k_tokens"`
	CacheCreationInputTokenCostAbove272K         *float64 `json:"cache_creation_input_token_cost_above_272k_tokens"`
	CacheCreationInputTokenCostPriorityAbove272K *float64 `json:"cache_creation_input_token_cost_priority_above_272k_tokens"`
	CacheReadInputTokenCostAbove272K             *float64 `json:"cache_read_input_token_cost_above_272k_tokens"`
	CacheReadInputTokenCostPriorityAbove272K     *float64 `json:"cache_read_input_token_cost_priority_above_272k_tokens"`
	LongContextInputTokenThreshold               *int     `json:"long_context_input_token_threshold"`
	LongContextInputCostMultiplier               *float64 `json:"long_context_input_cost_multiplier"`
	LongContextOutputCostMultiplier              *float64 `json:"long_context_output_cost_multiplier"`
	SupportsServiceTier                          bool     `json:"supports_service_tier"`
	LiteLLMProvider                              string   `json:"litellm_provider"`
	ModelVendor                                  string   `json:"model_vendor"`
	InputModalities                              []string `json:"input_modalities"`
	SupportedModalities                          []string `json:"supported_modalities"`
	SupportsVision                               *bool    `json:"supports_vision"`
	MaxInputTokens                               *int     `json:"max_input_tokens"`
	MetadataSource                               string   `json:"metadata_source"`
	PriceFields                                  []string `json:"price_fields"`
	Mode                                         string   `json:"mode"`
	BillingMode                                  string   `json:"billing_mode"`
	SupportsPromptCaching                        bool     `json:"supports_prompt_caching"`
	OutputCostPerImage                           *float64 `json:"output_cost_per_image"`
	OutputCostPerImageToken                      *float64 `json:"output_cost_per_image_token"`
	InputCostPerImageToken                       *float64 `json:"input_cost_per_image_token"`
	CacheReadInputImageTokenCost                 *float64 `json:"cache_read_input_image_token_cost"` // [v0.2.5 新增字段]
	PerRequestPrice                              *float64 `json:"per_request_price"`
	VideoPricePerSecond480P                      *float64 `json:"video_price_per_second_480p"`
	VideoPricePerSecond720P                      *float64 `json:"video_price_per_second_720p"`
	VideoPricePerSecond1080P                     *float64 `json:"video_price_per_second_1080p"`
	Disabled                                     *bool    `json:"disabled"`
}

// PricingService 动态价格服务
type PricingService struct {
	cfg              *config.Config
	remoteClient     PricingRemoteClient
	mu               sync.RWMutex
	basePricingData  map[string]*LiteLLMModelPricing
	pricingOverrides map[string]*LiteLLMModelPricing
	pricingData      map[string]*LiteLLMModelPricing
	lastUpdated      time.Time
	localHash        string
	lastSuccessAt    time.Time
	lastErrorAt      time.Time
	lastError        string
	source           string
	refreshing       bool
	catalogVersion   string
	catalogSequence  uint64
	remoteModelCount int
	refreshMu        sync.Mutex

	// 停止信号
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewPricingService 创建价格服务
func NewPricingService(cfg *config.Config, remoteClient PricingRemoteClient) *PricingService {
	s := &PricingService{
		cfg:              cfg,
		remoteClient:     remoteClient,
		basePricingData:  make(map[string]*LiteLLMModelPricing),
		pricingOverrides: make(map[string]*LiteLLMModelPricing),
		pricingData:      make(map[string]*LiteLLMModelPricing),
		stopCh:           make(chan struct{}),
	}
	return s
}

// Initialize 初始化价格服务
func (s *PricingService) Initialize() error {
	// 确保数据目录存在
	if err := os.MkdirAll(s.cfg.Pricing.DataDir, 0755); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to create data directory: %v", err)
	}

	// 启动只读取磁盘最近有效版本或内置快照；GitHub 故障不能把服务启动
	// 阻塞在远程超时上。远程检查在当前完整快照可用后由后台执行。
	if err := s.loadBestPricingCache(); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Local cache unavailable, using fallback: %v", err)
		if fallbackErr := s.useFallbackPricing(); fallbackErr != nil {
			return fmt.Errorf("failed to load pricing data: %w", fallbackErr)
		}
	}

	// 启动定时更新
	s.startUpdateScheduler()
	s.startInitialRemoteRefresh()

	logger.LegacyPrintf("service.pricing", "[Pricing] Service initialized with %d models", len(s.pricingData))
	return nil
}

func (s *PricingService) startInitialRemoteRefresh() {
	if s == nil || s.cfg == nil || !pricingRemoteConfigured(s.cfg) {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.syncWithRemote(); err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Initial background sync failed: %v", err)
		}
	}()
}

// Stop 停止价格服务
func (s *PricingService) Stop() {
	close(s.stopCh)
	s.wg.Wait()
	logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Service stopped")
}

// startUpdateScheduler 启动定时更新调度器
func (s *PricingService) startUpdateScheduler() {
	if s == nil || s.cfg == nil || !pricingRemoteConfigured(s.cfg) {
		logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Remote sync disabled: pricing remote URL is empty")
		return
	}

	// 定期检查哈希更新
	hashInterval := time.Duration(s.cfg.Pricing.HashCheckIntervalMinutes) * time.Minute
	if hashInterval < time.Minute {
		hashInterval = 10 * time.Minute
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(hashInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := s.syncWithRemote(); err != nil {
					logger.LegacyPrintf("service.pricing", "[Pricing] Sync failed: %v", err)
				}
			case <-s.stopCh:
				return
			}
		}
	}()

	logger.LegacyPrintf("service.pricing", "[Pricing] Update scheduler started (check every %v)", hashInterval)
}

// checkAndUpdatePricing 检查并更新价格数据
func (s *PricingService) checkAndUpdatePricing() error {
	pricingFile := s.getPricingFilePath()

	// 检查本地文件是否存在
	if _, err := os.Stat(pricingFile); os.IsNotExist(err) {
		logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Local pricing file not found, downloading...")
		if strings.TrimSpace(s.cfg.Pricing.RemoteURL) == "" {
			return fmt.Errorf("pricing cache is missing and remote URL is empty")
		}
		return s.downloadPricingData()
	}

	// 先加载本地文件（确保服务可用），再检查是否需要更新
	if err := s.loadPricingData(pricingFile); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to load local file, downloading: %v", err)
		if strings.TrimSpace(s.cfg.Pricing.RemoteURL) == "" {
			return fmt.Errorf("pricing cache is invalid and remote URL is empty")
		}
		return s.downloadPricingData()
	}

	// 如果配置了哈希URL，通过远程哈希检查是否有更新
	if s.cfg.Pricing.HashURL != "" {
		remoteHash, err := s.fetchRemoteHash()
		if err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Failed to fetch remote hash on startup: %v", err)
			s.recordPricingError(fmt.Errorf("fetch remote pricing hash: %w", err))
			return nil // 已加载本地文件，哈希获取失败不影响启动
		}

		s.mu.RLock()
		localHash := s.localHash
		s.mu.RUnlock()

		if localHash == "" || remoteHash != localHash {
			logger.LegacyPrintf("service.pricing", "[Pricing] Remote hash differs on startup (local=%s remote=%s), downloading...",
				localHash[:min(8, len(localHash))], remoteHash[:min(8, len(remoteHash))])
			if err := s.downloadPricingData(); err != nil {
				logger.LegacyPrintf("service.pricing", "[Pricing] Download failed, using existing file: %v", err)
			}
		}
		return nil
	}

	// 没有哈希URL时，基于文件年龄检查
	info, err := os.Stat(pricingFile)
	if err != nil {
		return nil // 已加载本地文件
	}

	fileAge := time.Since(info.ModTime())
	maxAge := time.Duration(s.cfg.Pricing.UpdateIntervalHours) * time.Hour

	if fileAge > maxAge {
		logger.LegacyPrintf("service.pricing", "[Pricing] Local file is %v old, updating...", fileAge.Round(time.Hour))
		if err := s.downloadPricingData(); err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Download failed, using existing file: %v", err)
		}
	}

	return nil
}

// syncWithRemote 与远程同步（基于哈希校验）
func (s *PricingService) syncWithRemote() error {
	if strings.TrimSpace(s.cfg.Pricing.ManifestURL) != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		manifest, _, _, err := s.fetchPricingManifest(ctx)
		if err != nil {
			s.recordPricingError(fmt.Errorf("fetch remote pricing manifest: %w", err))
			return nil
		}
		s.mu.RLock()
		sequence := s.catalogSequence
		localHash := s.localHash
		s.mu.RUnlock()
		if manifest.Sequence == sequence && strings.EqualFold(manifest.PricingCatalog.SHA256, localHash) {
			logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Manifest check passed, no update needed")
			s.clearPricingError()
			return nil
		}
		return s.downloadPricingData()
	}
	// 如果配置了哈希URL，从远程获取哈希进行比对
	if s.cfg.Pricing.HashURL != "" {
		remoteHash, err := s.fetchRemoteHash()
		if err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] Failed to fetch remote hash: %v", err)
			s.recordPricingError(fmt.Errorf("fetch remote pricing hash: %w", err))
			return nil // 哈希获取失败不影响正常使用
		}

		s.mu.RLock()
		localHash := s.localHash
		s.mu.RUnlock()

		if localHash == "" || remoteHash != localHash {
			logger.LegacyPrintf("service.pricing", "[Pricing] Remote hash differs (local=%s remote=%s), downloading new version...",
				localHash[:min(8, len(localHash))], remoteHash[:min(8, len(remoteHash))])
			return s.downloadPricingData()
		}
		logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Hash check passed, no update needed")
		s.clearPricingError()
		return nil
	}

	// 没有哈希URL时，基于时间检查
	pricingFile := s.getPricingFilePath()
	info, err := os.Stat(pricingFile)
	if err != nil {
		return s.downloadPricingData()
	}

	fileAge := time.Since(info.ModTime())
	maxAge := time.Duration(s.cfg.Pricing.UpdateIntervalHours) * time.Hour

	if fileAge > maxAge {
		logger.LegacyPrintf("service.pricing", "[Pricing] File is %v old, downloading...", fileAge.Round(time.Hour))
		return s.downloadPricingData()
	}

	return nil
}

// downloadPricingData 从远程下载价格数据
func (s *PricingService) downloadPricingData() error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	s.setPricingRefreshing(true)
	defer s.setPricingRefreshing(false)

	if err := s.downloadPricingDataLocked(); err != nil {
		s.recordPricingError(err)
		return err
	}
	s.clearPricingError()
	return nil
}

func (s *PricingService) downloadPricingDataLocked() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var manifest *pricingCatalogManifest
	var manifestRaw []byte
	var remoteURL string
	var remoteHash string
	var err error
	if strings.TrimSpace(s.cfg.Pricing.ManifestURL) != "" {
		manifest, manifestRaw, remoteURL, err = s.fetchPricingManifest(ctx)
		if err != nil {
			return fmt.Errorf("fetch remote pricing manifest: %w", err)
		}
		remoteHash = manifest.PricingCatalog.SHA256
		if err := s.validatePricingManifestTransition(manifest); err != nil {
			return err
		}
	} else {
		remoteURL, err = s.validatePricingURL(s.cfg.Pricing.RemoteURL)
		if err != nil {
			return err
		}
	}
	logger.LegacyPrintf("service.pricing", "[Pricing] Downloading from %s", remoteURL)

	// 配置了摘要时，正文必须与远程 SHA-256 完全一致。价格属于金额
	// 计算依据，不能在摘要和正文发布不同步时冒险切换。
	if manifest == nil && strings.TrimSpace(s.cfg.Pricing.HashURL) != "" {
		remoteHash, err = s.fetchRemoteHash()
		if err != nil {
			return fmt.Errorf("fetch remote pricing hash: %w", err)
		}
	}

	body, err := s.remoteClient.FetchPricingJSON(ctx, remoteURL)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	if len(body) > MaxPricingCatalogBytes {
		return fmt.Errorf("pricing catalog exceeds %d bytes", MaxPricingCatalogBytes)
	}

	dataHash := sha256.Sum256(body)
	dataHashStr := hex.EncodeToString(dataHash[:])
	if remoteHash != "" && !strings.EqualFold(remoteHash, dataHashStr) {
		return fmt.Errorf("pricing catalog hash mismatch: expected=%s actual=%s",
			remoteHash[:8], dataHashStr[:8])
	}

	remoteData, err := s.parsePricingDataWithFallback(body, false)
	if err != nil {
		return fmt.Errorf("parse pricing data: %w", err)
	}
	if err := validateRemotePricingCatalog(body, remoteData); err != nil {
		return fmt.Errorf("validate pricing data: %w", err)
	}
	if manifest != nil {
		if int64(len(body)) != manifest.PricingCatalog.Size {
			return fmt.Errorf("pricing catalog size mismatch: expected=%d actual=%d", manifest.PricingCatalog.Size, len(body))
		}
		if len(remoteData) != manifest.ModelCount {
			return fmt.Errorf("pricing catalog model count mismatch: expected=%d actual=%d", manifest.ModelCount, len(remoteData))
		}
	}
	data, err := s.parsePricingData(body)
	if err != nil {
		return fmt.Errorf("apply pricing fallbacks: %w", err)
	}

	// 正文与清单无法靠两次 rename（重命名）形成单一文件系统事务，因此切换前
	// 先保留上一组已验证配对。进程若在两次替换之间退出，下次启动会读取该配对，
	// 不会因一个半写入的新版本丢失 last-known-good（最近有效版本）。
	if err := s.persistPricingSnapshot(body, manifestRaw); err != nil {
		return err
	}

	syncHash := dataHashStr
	hashFile := s.getHashFilePath()
	if err := writeFileAtomically(hashFile, []byte(syncHash+"\n"), 0644); err != nil {
		// 哈希缓存不参与启动恢复；正文已经完整验证并原子落盘，因此这里只告警。
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to save hash: %v", err)
	}

	now := time.Now()

	s.mu.Lock()
	overrides, overridesErr := s.loadPricingOverrides()
	if overridesErr != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to load pricing overrides, retaining active overrides: %v", overridesErr)
		overrides = clonePricingMap(s.pricingOverrides)
	}
	merged := mergePricingMaps(data, overrides)
	s.basePricingData = clonePricingMap(data)
	s.pricingOverrides = clonePricingMap(overrides)
	s.pricingData = merged
	s.lastUpdated = now
	s.localHash = syncHash
	s.lastSuccessAt = now
	s.source = "remote"
	if manifest != nil {
		s.catalogVersion = manifest.CatalogVersion
		s.catalogSequence = manifest.Sequence
		s.remoteModelCount = manifest.ModelCount
	}
	s.mu.Unlock()

	logger.LegacyPrintf("service.pricing", "[Pricing] Downloaded %d models successfully", len(data))
	return nil
}

// deriveAbove272KPrice 在「原始条目未声明该 >272K 键」且基础价可用时，
// 用「基础价 × 长上下文倍率」补齐 >272K 绝对价。
// 存在的意义：价格文件有两种等价形状（显式 *_above_272k_tokens 与 long_context_ 阈值+倍率），
// 而下游（长上下文计价、Priority 的 fail-closed 完整性校验、展示价）只读绝对价字段；
// 显式声明优先，因此这里只在缺键时补齐，不覆盖文件里的任何价格。
func deriveAbove272KPrice(base float64, declaredInFile bool, above *float64, multiplier float64) {
	if declaredInFile || multiplier <= 0 || base <= 0 || above == nil || *above > 0 {
		return
	}
	*above = base * multiplier
}

// parsePricingData 解析价格数据（处理各种格式）
func (s *PricingService) parsePricingData(body []byte) (map[string]*LiteLLMModelPricing, error) {
	return s.parsePricingDataWithFallback(body, true)
}

func (s *PricingService) parsePricingDataWithFallback(body []byte, applyFallback bool) (map[string]*LiteLLMModelPricing, error) {
	// 首先解析为 map[string]json.RawMessage
	var rawData map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawData); err != nil {
		return nil, fmt.Errorf("parse raw JSON: %w", err)
	}

	result := make(map[string]*LiteLLMModelPricing)
	skipped := 0
	var orphanCacheTiers, lopsidedLadders []string

	for modelName, rawEntry := range rawData {
		// 跳过 sample_spec 等文档条目
		if modelName == "sample_spec" {
			continue
		}

		// 尝试解析每个条目
		var entry LiteLLMRawEntry
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			skipped++
			continue
		}

		// 只保留有有效价格的条目。图片模型可能只有 output_cost_per_image，
		// 覆盖价文件也允许只配置生图价格。
		if entry.InputCostPerToken == nil && entry.InputCostPerTokenPriority == nil &&
			entry.OutputCostPerToken == nil && entry.OutputCostPerTokenPriority == nil &&
			entry.CacheCreationInputTokenCost == nil && entry.CacheCreationInputTokenCostPriority == nil &&
			entry.CacheReadInputTokenCost == nil && entry.CacheReadInputTokenCostPriority == nil &&
			entry.InputCostPerTokenAbove272K == nil && entry.InputCostPerTokenPriorityAbove272K == nil &&
			entry.OutputCostPerTokenAbove272K == nil && entry.OutputCostPerTokenPriorityAbove272K == nil &&
			entry.CacheCreationInputTokenCostAbove272K == nil && entry.CacheCreationInputTokenCostPriorityAbove272K == nil &&
			entry.CacheReadInputTokenCostAbove272K == nil && entry.CacheReadInputTokenCostPriorityAbove272K == nil &&
			entry.OutputCostPerImage == nil && entry.OutputCostPerImageToken == nil && entry.InputCostPerImageToken == nil && entry.PerRequestPrice == nil &&
			entry.VideoPricePerSecond480P == nil && entry.VideoPricePerSecond720P == nil && entry.VideoPricePerSecond1080P == nil && entry.Disabled == nil {
			continue
		}

		pricing := &LiteLLMModelPricing{
			LiteLLMProvider:       entry.LiteLLMProvider,
			Mode:                  entry.Mode,
			BillingMode:           entry.BillingMode,
			SupportsPromptCaching: entry.SupportsPromptCaching,
			SupportsServiceTier:   entry.SupportsServiceTier,
			TokenPricingAbsent:    entry.InputCostPerToken == nil && entry.OutputCostPerToken == nil,
		}
		applyModelPricingDisplayMetadata(pricing, entry, rawEntry)
		if entry.Disabled != nil {
			pricing.Disabled = *entry.Disabled
		}

		if entry.InputCostPerToken != nil {
			pricing.InputCostPerToken = *entry.InputCostPerToken
		}
		if entry.InputCostPerTokenPriority != nil {
			pricing.InputCostPerTokenPriority = *entry.InputCostPerTokenPriority
		}
		if entry.OutputCostPerToken != nil {
			pricing.OutputCostPerToken = *entry.OutputCostPerToken
		}
		if entry.OutputCostPerTokenPriority != nil {
			pricing.OutputCostPerTokenPriority = *entry.OutputCostPerTokenPriority
		}
		if entry.CacheCreationInputTokenCost != nil {
			pricing.CacheCreationInputTokenCost = *entry.CacheCreationInputTokenCost
		}
		if entry.CacheCreationInputTokenCostPriority != nil {
			pricing.CacheCreationInputTokenCostPriority = *entry.CacheCreationInputTokenCostPriority
		}
		if entry.CacheCreationInputTokenCostAbove1hr != nil {
			pricing.CacheCreationInputTokenCostAbove1hr = *entry.CacheCreationInputTokenCostAbove1hr
		}
		if entry.CacheReadInputTokenCost != nil {
			pricing.CacheReadInputTokenCost = *entry.CacheReadInputTokenCost
		}
		if entry.CacheReadInputTokenCostPriority != nil {
			pricing.CacheReadInputTokenCostPriority = *entry.CacheReadInputTokenCostPriority
		}
		if entry.InputCostPerTokenAbove272K != nil {
			pricing.InputCostPerTokenAbove272K = *entry.InputCostPerTokenAbove272K
			pricing.LongContextInputTokenThreshold = 272000
		}
		if entry.InputCostPerTokenPriorityAbove272K != nil {
			pricing.InputCostPerTokenPriorityAbove272K = *entry.InputCostPerTokenPriorityAbove272K
			pricing.LongContextInputTokenThreshold = 272000
		}
		if entry.OutputCostPerTokenAbove272K != nil {
			pricing.OutputCostPerTokenAbove272K = *entry.OutputCostPerTokenAbove272K
			pricing.LongContextInputTokenThreshold = 272000
		}
		if entry.OutputCostPerTokenPriorityAbove272K != nil {
			pricing.OutputCostPerTokenPriorityAbove272K = *entry.OutputCostPerTokenPriorityAbove272K
			pricing.LongContextInputTokenThreshold = 272000
		}
		if entry.CacheCreationInputTokenCostAbove272K != nil {
			pricing.CacheCreationInputTokenCostAbove272K = *entry.CacheCreationInputTokenCostAbove272K
			pricing.LongContextInputTokenThreshold = 272000
		}
		if entry.CacheCreationInputTokenCostPriorityAbove272K != nil {
			pricing.CacheCreationInputTokenCostPriorityAbove272K = *entry.CacheCreationInputTokenCostPriorityAbove272K
			pricing.LongContextInputTokenThreshold = 272000
		}
		if entry.CacheReadInputTokenCostAbove272K != nil {
			pricing.CacheReadInputTokenCostAbove272K = *entry.CacheReadInputTokenCostAbove272K
			pricing.LongContextInputTokenThreshold = 272000
		}
		if entry.CacheReadInputTokenCostPriorityAbove272K != nil {
			pricing.CacheReadInputTokenCostPriorityAbove272K = *entry.CacheReadInputTokenCostPriorityAbove272K
			pricing.LongContextInputTokenThreshold = 272000
		}
		// 新形状（官方 v0.2.13 起）：长上下文只给 long_context_* 阈值与倍率，
		// 不给上面那一排 *_above_272k_tokens 绝对价。阈值与倍率必须先落库，
		// 下面的补齐/校准才有依据；老形状没有这些键，行为完全不变。
		if entry.LongContextInputTokenThreshold != nil && *entry.LongContextInputTokenThreshold > 0 {
			pricing.LongContextInputTokenThreshold = *entry.LongContextInputTokenThreshold
		}
		if entry.LongContextInputCostMultiplier != nil && *entry.LongContextInputCostMultiplier > 0 {
			pricing.LongContextInputCostMultiplier = *entry.LongContextInputCostMultiplier
		}
		if entry.LongContextOutputCostMultiplier != nil && *entry.LongContextOutputCostMultiplier > 0 {
			pricing.LongContextOutputCostMultiplier = *entry.LongContextOutputCostMultiplier
		}
		if pricing.LongContextInputTokenThreshold > 0 {
			// 新形状只给倍率、不给 >272K 绝对价，这里用「基础价 × 倍率」补齐绝对价：
			// LongContext*PricePerToken 参与长上下文计价与 Priority 的 fail-closed 完整性校验，
			// 缺了会让这些模型被误判为「价格不完整」。缓存读写沿用输入倍率（与官方语义一致）。
			// 仅在原始条目**没有**声明该 >272K 键时补齐，保证文件里的显式价格永远优先。
			if inMult := pricing.LongContextInputCostMultiplier; inMult > 0 {
				deriveAbove272KPrice(pricing.InputCostPerToken, entry.InputCostPerTokenAbove272K != nil, &pricing.InputCostPerTokenAbove272K, inMult)
				deriveAbove272KPrice(pricing.CacheReadInputTokenCost, entry.CacheReadInputTokenCostAbove272K != nil, &pricing.CacheReadInputTokenCostAbove272K, inMult)
				deriveAbove272KPrice(pricing.CacheCreationInputTokenCost, entry.CacheCreationInputTokenCostAbove272K != nil, &pricing.CacheCreationInputTokenCostAbove272K, inMult)
				deriveAbove272KPrice(pricing.InputCostPerTokenPriority, entry.InputCostPerTokenPriorityAbove272K != nil, &pricing.InputCostPerTokenPriorityAbove272K, inMult)
				deriveAbove272KPrice(pricing.CacheReadInputTokenCostPriority, entry.CacheReadInputTokenCostPriorityAbove272K != nil, &pricing.CacheReadInputTokenCostPriorityAbove272K, inMult)
				deriveAbove272KPrice(pricing.CacheCreationInputTokenCostPriority, entry.CacheCreationInputTokenCostPriorityAbove272K != nil, &pricing.CacheCreationInputTokenCostPriorityAbove272K, inMult)
			}
			if outMult := pricing.LongContextOutputCostMultiplier; outMult > 0 {
				deriveAbove272KPrice(pricing.OutputCostPerToken, entry.OutputCostPerTokenAbove272K != nil, &pricing.OutputCostPerTokenAbove272K, outMult)
				deriveAbove272KPrice(pricing.OutputCostPerTokenPriority, entry.OutputCostPerTokenPriorityAbove272K != nil, &pricing.OutputCostPerTokenPriorityAbove272K, outMult)
			}
			// 反向校准：条目同时给了绝对价与倍率时，以绝对价为准（沿用既有语义）。
			if pricing.InputCostPerToken > 0 && pricing.InputCostPerTokenAbove272K > 0 {
				pricing.LongContextInputCostMultiplier = pricing.InputCostPerTokenAbove272K / pricing.InputCostPerToken
			}
			if pricing.OutputCostPerToken > 0 && pricing.OutputCostPerTokenAbove272K > 0 {
				pricing.LongContextOutputCostMultiplier = pricing.OutputCostPerTokenAbove272K / pricing.OutputCostPerToken
			}
		}
		if entry.OutputCostPerImage != nil {
			pricing.OutputCostPerImage = *entry.OutputCostPerImage
		}
		if entry.OutputCostPerImageToken != nil {
			pricing.OutputCostPerImageToken = *entry.OutputCostPerImageToken
		}
		if entry.InputCostPerImageToken != nil {
			value := *entry.InputCostPerImageToken
			pricing.InputCostPerImageToken = &value
		}
		if entry.CacheReadInputImageTokenCost != nil {
			value := *entry.CacheReadInputImageTokenCost
			pricing.CacheReadInputImageTokenCost = &value
		}
		if entry.PerRequestPrice != nil {
			pricing.PerRequestPrice = *entry.PerRequestPrice
		}
		if entry.VideoPricePerSecond480P != nil {
			pricing.VideoPricePerSecond480P = *entry.VideoPricePerSecond480P
		}
		if entry.VideoPricePerSecond720P != nil {
			pricing.VideoPricePerSecond720P = *entry.VideoPricePerSecond720P
		}
		if entry.VideoPricePerSecond1080P != nil {
			pricing.VideoPricePerSecond1080P = *entry.VideoPricePerSecond1080P
		}
		if applyFallback {
			fillMissingOfficialPricingFields(rawEntry, modelName, pricing)
			if pricing.TokenPricingAbsent && (pricing.InputCostPerToken != 0 || pricing.OutputCostPerToken != 0) {
				pricing.TokenPricingAbsent = false
			}
		}

		// 目录契约哨兵：cache 侧 above 档缺基础价、长上下文单侧附加费都是
		// 目录数据受损的早期信号，加载时告警便于第一时间修目录而非等计费对账发现。
		if orphans := orphanCacheTierFields(rawEntry); len(orphans) > 0 {
			orphanCacheTiers = append(orphanCacheTiers, modelName+"("+strings.Join(orphans, ",")+")")
		}
		if isLopsidedLongContextLadder(pricing) {
			inputMultiplier, outputMultiplier := 1.0, 1.0
			if pricing.InputCostPerToken > 0 && pricing.InputCostPerTokenAbove272K > 0 {
				inputMultiplier = pricing.InputCostPerTokenAbove272K / pricing.InputCostPerToken
			}
			if pricing.OutputCostPerToken > 0 && pricing.OutputCostPerTokenAbove272K > 0 {
				outputMultiplier = pricing.OutputCostPerTokenAbove272K / pricing.OutputCostPerToken
			}
			lopsidedLadders = append(lopsidedLadders, fmt.Sprintf("%s(input x%.2f, output x%.2f)", modelName, inputMultiplier, outputMultiplier))
		}

		finalizeModelPricingDisplayFields(pricing, rawEntry)
		result[modelName] = pricing
	}

	if skipped > 0 {
		logger.LegacyPrintf("service.pricing", "[Pricing] Skipped %d invalid entries", skipped)
	}
	warnOrphanCacheTierFields(orphanCacheTiers)
	warnLopsidedLongContextLadders(lopsidedLadders)

	if len(result) == 0 {
		return nil, fmt.Errorf("no valid pricing entries found")
	}
	if applyFallback {
		applyOfficialOpenAITextPricing(result)
		applyOfficialCompatibleTextPricing(result)
		applyOfficialGrokPricing(result)
	}

	return result, nil
}

// orphanCacheTierFields 返回条目中没有对应基础价的 cache 侧 above 档字段名。
// 计费里 cache 分项在阈值以下按基础价、阈值以上按 above 档价；基础价缺失或为 0 时，
// 该缓存分项在阈值以下恒按 0 计费（above 档再高也收不到钱）。计费对变体有回落：
// 服务档变体（_priority/_flex）缺自身基础价时用标准基础价，1h 缓存写入缺 above_1hr
// 价时全部按 5m 价——因此沿回落链任一基础价存在即不算孤儿。
func orphanCacheTierFields(rawEntry json.RawMessage) []string {
	if !bytes.Contains(rawEntry, []byte("_above_")) {
		return nil
	}
	var fields map[string]any
	if err := json.Unmarshal(rawEntry, &fields); err != nil {
		return nil
	}
	positive := func(key string) bool {
		price, ok := fields[key].(float64)
		return ok && price > 0
	}
	var orphans []string
	for key := range fields {
		m := cacheTierPricePattern.FindStringSubmatch(key)
		if m == nil || !positive(key) {
			continue
		}
		stem, hourly, tier := m[1], m[2], m[3]
		if positive(stem+hourly+tier) || positive(stem+hourly) || positive(stem+tier) || positive(stem) {
			continue
		}
		orphans = append(orphans, key)
	}
	sort.Strings(orphans)
	return orphans
}

// warnOrphanCacheTierFields 对带 cache 侧 above 档却没有基础价的条目打 WARN：
// 该缓存分项在阈值以下按 0 计费，目录或管理端价格覆盖补上基础价即可消除。
func warnOrphanCacheTierFields(entries []string) {
	if len(entries) == 0 {
		return
	}
	sort.Strings(entries)
	total := len(entries)
	if total > 20 {
		entries = append(entries[:20], "...")
	}
	logger.LegacyPrintf("service.pricing", "[Pricing] Warning: %d model(s) carry cache above-tier prices without a base cache price; that cache item bills at $0 below the long-context threshold until the catalog/override supplies the base: %s", total, strings.Join(entries, ", "))
}

// isLopsidedLongContextLadder 判断长上下文档位是否只有一侧带附加费。官方阶梯
// （OpenAI、Google、Anthropic、xAI）都同时抬高 input 与 output；单侧附加费意味着
// 条目的基础价与 above 档来自不同价格版本（如基础价被手工 pin、above 档随上游
// 更新），该模型的长上下文计费已失真。
func isLopsidedLongContextLadder(pricing *LiteLLMModelPricing) bool {
	if pricing == nil || pricing.LongContextInputTokenThreshold <= 0 {
		return false
	}
	inputSurcharged := pricing.InputCostPerToken > 0 && pricing.InputCostPerTokenAbove272K > pricing.InputCostPerToken
	outputSurcharged := pricing.OutputCostPerToken > 0 && pricing.OutputCostPerTokenAbove272K > pricing.OutputCostPerToken
	return inputSurcharged != outputSurcharged
}

// warnLopsidedLongContextLadders 对单侧附加费的长上下文档位打 WARN：应成组修正
// 该条目的基础价与 above 档（远程目录或管理端价格覆盖）。
func warnLopsidedLongContextLadders(entries []string) {
	if len(entries) == 0 {
		return
	}
	sort.Strings(entries)
	total := len(entries)
	if total > 20 {
		entries = append(entries[:20], "...")
	}
	logger.LegacyPrintf("service.pricing", "[Pricing] Warning: %d model(s) derive a one-sided long-context ladder (surcharge on only input or only output); base prices and above-tier prices likely come from different price versions: %s", total, strings.Join(entries, ", "))
}

// fillMissingOfficialPricingFields fills only fields that the remote entry did
// not send. Presence is checked against the raw JSON so an explicit zero or
// false remains an authoritative remote value.
func fillMissingOfficialPricingFields(raw json.RawMessage, model string, pricing *LiteLLMModelPricing) {
	if pricing == nil {
		return
	}
	fallback := officialPricingForExactModel(model)
	if fallback == nil {
		return
	}
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return
	}
	destination := reflect.ValueOf(pricing).Elem()
	source := reflect.ValueOf(fallback).Elem()
	typeInfo := destination.Type()
	for i := 0; i < destination.NumField(); i++ {
		fieldInfo := typeInfo.Field(i)
		jsonName := strings.Split(fieldInfo.Tag.Get("json"), ",")[0]
		if jsonName == "" || jsonName == "-" {
			continue
		}
		if _, explicitlySet := present[jsonName]; explicitlySet {
			continue
		}
		fallbackValue := source.Field(i)
		if fallbackValue.IsZero() {
			continue
		}
		destination.Field(i).Set(fallbackValue)
	}
}

func officialPricingForExactModel(model string) *LiteLLMModelPricing {
	if pricing, ok := officialOpenAITextPricing[model]; ok {
		return pricing
	}
	if pricing, ok := officialCompatibleTextPricing[model]; ok {
		return pricing
	}
	if pricing, ok := officialGrokPricing[model]; ok {
		return pricing
	}
	normalized := strings.ToLower(strings.TrimSpace(model))
	if pricing, ok := officialOpenAITextPricing[normalized]; ok {
		return pricing
	}
	for candidate, pricing := range officialCompatibleTextPricing {
		if strings.ToLower(candidate) == normalized {
			return pricing
		}
	}
	if pricing, ok := officialGrokPricing[normalized]; ok {
		return pricing
	}
	return nil
}

func applyOfficialOpenAITextPricing(target map[string]*LiteLLMModelPricing) {
	for model, pricing := range officialOpenAITextPricing {
		if _, exists := target[model]; !exists {
			target[model] = cloneLiteLLMModelPricing(pricing)
		}
	}
}

func applyOfficialCompatibleTextPricing(target map[string]*LiteLLMModelPricing) {
	for model, pricing := range officialCompatibleTextPricing {
		if _, exists := target[model]; !exists {
			target[model] = cloneLiteLLMModelPricing(pricing)
		}
	}
}

func applyOfficialGrokPricing(target map[string]*LiteLLMModelPricing) {
	for model, pricing := range officialGrokPricing {
		if _, exists := target[model]; !exists {
			target[model] = cloneLiteLLMModelPricing(pricing)
		}
	}
}

func cloneLiteLLMModelPricing(pricing *LiteLLMModelPricing) *LiteLLMModelPricing {
	if pricing == nil {
		return nil
	}
	cloned := *pricing
	return &cloned
}

func officialOpenAITextPricingForModel(model string) *LiteLLMModelPricing {
	pricing := officialOpenAITextPricing[strings.ToLower(strings.TrimSpace(model))]
	return cloneLiteLLMModelPricing(pricing)
}

func officialCompatibleTextPricingForModel(model string) *LiteLLMModelPricing {
	normalized := strings.ToLower(strings.TrimSpace(model))
	for candidate, pricing := range officialCompatibleTextPricing {
		if strings.ToLower(candidate) == normalized {
			return cloneLiteLLMModelPricing(pricing)
		}
	}
	return nil
}

// loadPricingData 从本地文件加载价格数据
func (s *PricingService) loadPricingData(filePath string) error {
	manifestPath := ""
	sourceLabel := ""
	if filepath.Clean(filePath) == filepath.Clean(s.getPricingFilePath()) && strings.TrimSpace(s.cfg.Pricing.ManifestURL) != "" {
		manifestPath = s.getPricingManifestFilePath()
		sourceLabel = "cache"
	}
	return s.loadPricingSnapshot(filePath, manifestPath, sourceLabel)
}

func (s *PricingService) loadBestPricingCache() error {
	activeErr := s.loadPricingData(s.getPricingFilePath())
	if activeErr == nil || strings.TrimSpace(s.cfg.Pricing.ManifestURL) == "" {
		return activeErr
	}
	backupErr := s.loadPricingSnapshot(s.getPricingBackupFilePath(), s.getPricingBackupManifestFilePath(), "cache")
	if backupErr == nil {
		logger.LegacyPrintf("service.pricing", "%s", "[Pricing] Recovered last-known-good pricing snapshot")
		return nil
	}
	return fmt.Errorf("active cache invalid: %v; last-known-good cache invalid: %w", activeErr, backupErr)
}

func (s *PricingService) loadPricingSnapshot(filePath, manifestPath, sourceLabel string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file failed: %w", err)
	}
	modTime := time.Time{}
	if info, statErr := os.Stat(filePath); statErr == nil {
		modTime = info.ModTime()
	}
	var manifest *pricingCatalogManifest
	if manifestPath != "" {
		manifestRaw, readErr := os.ReadFile(manifestPath)
		if readErr != nil {
			return fmt.Errorf("read cached pricing manifest: %w", readErr)
		}
		manifest, err = s.validatePricingSnapshot(data, manifestRaw)
		if err != nil {
			return fmt.Errorf("validate cached pricing snapshot: %w", err)
		}
	}
	if err := s.loadPricingDataBytes(data, filePath, modTime); err != nil {
		return err
	}
	if manifest != nil {
		s.mu.Lock()
		s.catalogVersion = manifest.CatalogVersion
		s.catalogSequence = manifest.Sequence
		s.remoteModelCount = manifest.ModelCount
		if sourceLabel == "" {
			sourceLabel = "cache"
		}
		s.source = sourceLabel
		s.mu.Unlock()
	}
	return nil
}

func (s *PricingService) validatePricingSnapshot(data, manifestRaw []byte) (*pricingCatalogManifest, error) {
	manifest, err := parsePricingCatalogManifest(manifestRaw)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != manifest.PricingCatalog.Size {
		return nil, fmt.Errorf("cached pricing size mismatch")
	}
	digest := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), manifest.PricingCatalog.SHA256) {
		return nil, fmt.Errorf("cached pricing sha256 mismatch")
	}
	remoteData, err := s.parsePricingDataWithFallback(data, false)
	if err != nil {
		return nil, fmt.Errorf("parse cached pricing data: %w", err)
	}
	if err := validateRemotePricingCatalog(data, remoteData); err != nil {
		return nil, fmt.Errorf("validate cached pricing data: %w", err)
	}
	if len(remoteData) != manifest.ModelCount {
		return nil, fmt.Errorf("cached pricing model count mismatch: expected=%d actual=%d", manifest.ModelCount, len(remoteData))
	}
	return manifest, nil
}

func (s *PricingService) loadPricingDataBytes(data []byte, source string, modTime time.Time) error {
	// 使用灵活的解析方式
	pricingData, err := s.parsePricingData(data)
	if err != nil {
		return fmt.Errorf("parse pricing data: %w", err)
	}

	// 计算哈希
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	overrides := s.loadPricingOverridesForRefresh()
	merged := mergePricingMaps(pricingData, overrides)
	now := time.Now()

	s.mu.Lock()
	s.basePricingData = clonePricingMap(pricingData)
	s.pricingOverrides = clonePricingMap(overrides)
	s.pricingData = merged
	s.localHash = hashStr

	if modTime.IsZero() {
		s.lastUpdated = now
	} else {
		s.lastUpdated = modTime
	}
	s.lastSuccessAt = now
	s.source = pricingLoadSourceLabel(source, s.getPricingFilePath())
	s.mu.Unlock()

	logger.LegacyPrintf("service.pricing", "[Pricing] Loaded %d models from %s", len(pricingData), source)
	return nil
}

// useFallbackPricing 优先使用运维侧回退文件；文件缺失时使用编译进
// 不可变二进制的同版价格快照，避免 systemd 节点因工作目录缺文件而无法启动。
func (s *PricingService) useFallbackPricing() error {
	fallbackFile := s.cfg.Pricing.FallbackFile
	data, err := os.ReadFile(fallbackFile)
	source := fallbackFile
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read fallback failed: %w", err)
		}
		data = modelpricing.DefaultPricing()
		source = "embedded model pricing"
		if len(data) == 0 {
			return fmt.Errorf("fallback file not found and embedded pricing is empty: %s", fallbackFile)
		}
	}
	logger.LegacyPrintf("service.pricing", "[Pricing] Using fallback source: %s", source)

	// 将实际加载的价格落到数据目录，后续更新和审计继续使用统一路径。
	pricingFile := s.getPricingFilePath()
	if err := writeFileAtomically(pricingFile, data, 0644); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to copy fallback: %v", err)
	}
	return s.loadPricingDataBytes(data, source, time.Time{})
}

func (s *PricingService) getPricingOverridesFilePath() string {
	if s.cfg == nil || strings.TrimSpace(s.cfg.Pricing.DataDir) == "" {
		return filepath.Join(".", "data", "model_pricing_overrides.json")
	}
	return filepath.Join(s.cfg.Pricing.DataDir, "model_pricing_overrides.json")
}

func (s *PricingService) getPricingManifestFilePath() string {
	if s.cfg == nil || strings.TrimSpace(s.cfg.Pricing.DataDir) == "" {
		return filepath.Join(".", "data", "model_pricing_manifest.json")
	}
	return filepath.Join(s.cfg.Pricing.DataDir, "model_pricing_manifest.json")
}

func (s *PricingService) getPricingBackupFilePath() string {
	if s.cfg == nil || strings.TrimSpace(s.cfg.Pricing.DataDir) == "" {
		return filepath.Join(".", "data", "model_pricing.last-good.json")
	}
	return filepath.Join(s.cfg.Pricing.DataDir, "model_pricing.last-good.json")
}

func (s *PricingService) getPricingBackupManifestFilePath() string {
	if s.cfg == nil || strings.TrimSpace(s.cfg.Pricing.DataDir) == "" {
		return filepath.Join(".", "data", "model_pricing_manifest.last-good.json")
	}
	return filepath.Join(s.cfg.Pricing.DataDir, "model_pricing_manifest.last-good.json")
}

func (s *PricingService) loadPricingOverridesWithLog() map[string]*LiteLLMModelPricing {
	overrides, err := s.loadPricingOverrides()
	if err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Failed to load pricing overrides: %v", err)
		return map[string]*LiteLLMModelPricing{}
	}
	return overrides
}

// loadPricingOverridesForRefresh keeps the last validated in-memory overrides
// when the persisted file is temporarily unreadable or corrupt. A remote catalog
// refresh must never erase an administrator's active billing decision merely
// because one local read failed.
func (s *PricingService) loadPricingOverridesForRefresh() map[string]*LiteLLMModelPricing {
	overrides, err := s.loadPricingOverrides()
	if err == nil {
		return overrides
	}
	logger.LegacyPrintf("service.pricing", "[Pricing] Failed to load pricing overrides, retaining active overrides: %v", err)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clonePricingMap(s.pricingOverrides)
}

func (s *PricingService) loadPricingOverrides() (map[string]*LiteLLMModelPricing, error) {
	path := s.getPricingOverridesFilePath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return map[string]*LiteLLMModelPricing{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pricing overrides: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]*LiteLLMModelPricing{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err == nil && len(raw) == 0 {
		return map[string]*LiteLLMModelPricing{}, nil
	}
	parsed, err := s.parsePricingDataWithFallback(data, false)
	if err != nil {
		return nil, fmt.Errorf("parse pricing overrides: %w", err)
	}
	return parsed, nil
}

func (s *PricingService) savePricingOverrides(overrides map[string]*LiteLLMModelPricing) error {
	path := s.getPricingOverridesFilePath()
	data, err := json.MarshalIndent(overrides, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal pricing overrides: %w", err)
	}
	if err := writeFileAtomically(path, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("write pricing overrides: %w", err)
	}
	return nil
}

func clonePricingMap(in map[string]*LiteLLMModelPricing) map[string]*LiteLLMModelPricing {
	out := make(map[string]*LiteLLMModelPricing, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		cp := *v
		cp.InputCostPerImageToken = cloneFloat64Ptr(v.InputCostPerImageToken)
		out[k] = &cp
	}
	return out
}

func mergePricingMaps(base, overrides map[string]*LiteLLMModelPricing) map[string]*LiteLLMModelPricing {
	merged := clonePricingMap(base)
	for k, v := range overrides {
		if v == nil {
			continue
		}
		cp := *v
		cp.InputCostPerImageToken = cloneFloat64Ptr(v.InputCostPerImageToken)
		merged[k] = &cp
	}
	return merged
}

func validateRemotePricingCatalog(body []byte, pricing map[string]*LiteLLMModelPricing) error {
	if len(strings.TrimSpace(string(body))) == 0 {
		return fmt.Errorf("catalog is empty")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("decode catalog envelope: %w", err)
	}
	delete(raw, "sample_spec")
	if len(raw) < minRemotePricingEntries || len(pricing) < minRemotePricingEntries {
		return fmt.Errorf("catalog has too few entries: %d", len(raw))
	}
	// 单个模型字段类型错误时解析器会跳过该项，便于兼容上游新增的非价格
	// 文档条目；但不能接受大批条目静默消失，否则一次坏发布会清空计费覆盖面。
	if len(pricing)*100 < len(raw)*95 {
		return fmt.Errorf("catalog parsed only %d of %d entries", len(pricing), len(raw))
	}
	if len(raw) > maxRemotePricingEntries || len(pricing) > maxRemotePricingEntries {
		return fmt.Errorf("catalog has too many entries: %d", len(raw))
	}
	seenModels := make(map[string]string, len(pricing))
	for model, entry := range pricing {
		if strings.TrimSpace(model) == "" || entry == nil {
			return fmt.Errorf("catalog contains an empty model entry")
		}
		normalizedModel := strings.ToLower(strings.TrimSpace(model))
		if previous, exists := seenModels[normalizedModel]; exists {
			return fmt.Errorf("catalog model names differ only by case: %q and %q", previous, model)
		}
		seenModels[normalizedModel] = model
		if rawEntry, exists := raw[model]; exists {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(rawEntry, &fields); err != nil {
				return fmt.Errorf("model %q has an invalid entry", model)
			}
			if _, remotelyDisabled := fields["disabled"]; remotelyDisabled {
				return fmt.Errorf("model %q contains forbidden remote disabled field", model)
			}
		}
		value := reflect.ValueOf(*entry)
		typeInfo := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := value.Field(i)
			name := typeInfo.Field(i).Name
			switch field.Kind() {
			case reflect.Float64:
				price := field.Float()
				if math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
					return fmt.Errorf("model %q has invalid %s", model, name)
				}
			case reflect.Ptr:
				if !field.IsNil() && field.Elem().Kind() == reflect.Float64 {
					price := field.Elem().Float()
					if math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
						return fmt.Errorf("model %q has invalid %s", model, name)
					}
				}
			}
		}
		if entry.LongContextInputTokenThreshold < 0 {
			return fmt.Errorf("model %q has a negative long-context threshold", model)
		}
	}
	return nil
}

func parsePricingCatalogManifest(raw []byte) (*pricingCatalogManifest, error) {
	if len(raw) == 0 || len(raw) > MaxPricingManifestBytes {
		return nil, fmt.Errorf("pricing manifest is empty or too large")
	}
	var manifest pricingCatalogManifest
	if err := decodeStrictJSON(raw, &manifest); err != nil {
		return nil, fmt.Errorf("decode pricing manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported pricing manifest schema_version %d", manifest.SchemaVersion)
	}
	if strings.TrimSpace(manifest.CatalogVersion) == "" || len(manifest.CatalogVersion) > 80 {
		return nil, fmt.Errorf("invalid pricing catalog version")
	}
	if _, err := time.Parse(time.RFC3339, manifest.PublishedAt); err != nil {
		return nil, fmt.Errorf("invalid pricing catalog published_at")
	}
	if manifest.Sequence == 0 {
		return nil, fmt.Errorf("pricing catalog sequence must be positive")
	}
	if manifest.ModelCount < minRemotePricingEntries || manifest.ModelCount > maxRemotePricingEntries {
		return nil, fmt.Errorf("pricing manifest model count is out of range: %d", manifest.ModelCount)
	}
	if strings.TrimSpace(manifest.PricingCatalog.Path) == "" || !validSHA256Hex(manifest.PricingCatalog.SHA256) {
		return nil, fmt.Errorf("invalid pricing catalog resource metadata")
	}
	if manifest.PricingCatalog.Size <= 0 || manifest.PricingCatalog.Size > MaxPricingCatalogBytes {
		return nil, fmt.Errorf("pricing catalog declared size is out of range: %d", manifest.PricingCatalog.Size)
	}
	return &manifest, nil
}

func (s *PricingService) fetchPricingManifest(ctx context.Context) (*pricingCatalogManifest, []byte, string, error) {
	manifestURL, err := s.validatePricingURL(s.cfg.Pricing.ManifestURL)
	if err != nil {
		return nil, nil, "", err
	}
	raw, err := s.remoteClient.FetchPricingJSON(ctx, manifestURL)
	if err != nil {
		return nil, nil, "", err
	}
	manifest, err := parsePricingCatalogManifest(raw)
	if err != nil {
		return nil, nil, "", err
	}
	pricingURL, err := resolveKeywordCatalogFileURL(manifestURL, manifest.PricingCatalog.Path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("resolve pricing catalog resource: %w", err)
	}
	return manifest, raw, pricingURL, nil
}

func (s *PricingService) validatePricingManifestTransition(manifest *pricingCatalogManifest) error {
	if manifest == nil {
		return fmt.Errorf("pricing manifest is required")
	}
	s.mu.RLock()
	currentSequence := s.catalogSequence
	currentHash := s.localHash
	currentRemoteCount := s.remoteModelCount
	s.mu.RUnlock()
	if currentSequence > 0 && manifest.Sequence < currentSequence {
		return fmt.Errorf("pricing catalog sequence rollback: current=%d candidate=%d", currentSequence, manifest.Sequence)
	}
	if currentSequence > 0 && manifest.Sequence == currentSequence && currentHash != "" && !strings.EqualFold(currentHash, manifest.PricingCatalog.SHA256) {
		return fmt.Errorf("pricing catalog sequence %d was reused with different content", manifest.Sequence)
	}
	if currentRemoteCount > 0 && manifest.ModelCount*100 < currentRemoteCount*minPricingRetentionPercent {
		return fmt.Errorf("pricing catalog shrank unexpectedly: current=%d candidate=%d", currentRemoteCount, manifest.ModelCount)
	}
	return nil
}

func writeFileAtomically(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pricing-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFileAtomically(tmpPath, path); err != nil {
		return err
	}
	return nil
}

func (s *PricingService) persistPricingSnapshot(body, manifestRaw []byte) error {
	pricingFile := s.getPricingFilePath()
	if len(manifestRaw) == 0 {
		if err := writeFileAtomically(pricingFile, body, 0644); err != nil {
			return fmt.Errorf("persist pricing catalog: %w", err)
		}
		return nil
	}

	backupReady, err := s.backupCurrentPricingSnapshot()
	if err != nil {
		return fmt.Errorf("preserve current pricing snapshot: %w", err)
	}
	if err := writeFileAtomically(pricingFile, body, 0644); err != nil {
		return fmt.Errorf("persist pricing catalog: %w", err)
	}
	manifestBody := append([]byte(strings.TrimSpace(string(manifestRaw))), '\n')
	if err := writeFileAtomically(s.getPricingManifestFilePath(), manifestBody, 0644); err != nil {
		if backupReady {
			if restoreErr := s.restorePricingSnapshotFromBackup(); restoreErr != nil {
				return fmt.Errorf("persist pricing manifest: %v; restore previous snapshot: %w", err, restoreErr)
			}
		}
		return fmt.Errorf("persist pricing manifest: %w", err)
	}
	return nil
}

func (s *PricingService) backupCurrentPricingSnapshot() (bool, error) {
	body, err := os.ReadFile(s.getPricingFilePath())
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	manifestRaw, err := os.ReadFile(s.getPricingManifestFilePath())
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := s.validatePricingSnapshot(body, manifestRaw); err != nil {
		logger.LegacyPrintf("service.pricing", "[Pricing] Current disk snapshot is not eligible for backup: %v", err)
		return false, nil
	}
	if err := writeFileAtomically(s.getPricingBackupFilePath(), body, 0644); err != nil {
		return false, err
	}
	if err := writeFileAtomically(s.getPricingBackupManifestFilePath(), manifestRaw, 0644); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PricingService) restorePricingSnapshotFromBackup() error {
	body, err := os.ReadFile(s.getPricingBackupFilePath())
	if err != nil {
		return err
	}
	manifestRaw, err := os.ReadFile(s.getPricingBackupManifestFilePath())
	if err != nil {
		return err
	}
	if _, err := s.validatePricingSnapshot(body, manifestRaw); err != nil {
		return err
	}
	if err := writeFileAtomically(s.getPricingFilePath(), body, 0644); err != nil {
		return err
	}
	return writeFileAtomically(s.getPricingManifestFilePath(), manifestRaw, 0644)
}

func pricingLoadSourceLabel(source, pricingFile string) string {
	if source == "embedded model pricing" {
		return "embedded"
	}
	if filepath.Clean(source) == filepath.Clean(pricingFile) {
		return "cache"
	}
	return "fallback"
}

func pricingRemoteURL(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if manifestURL := strings.TrimSpace(cfg.Pricing.ManifestURL); manifestURL != "" {
		return manifestURL
	}
	return strings.TrimSpace(cfg.Pricing.RemoteURL)
}

func pricingRemoteConfigured(cfg *config.Config) bool {
	return cfg != nil && (strings.TrimSpace(cfg.Pricing.ManifestURL) != "" || strings.TrimSpace(cfg.Pricing.RemoteURL) != "")
}

func pricingRemoteHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func (s *PricingService) setPricingRefreshing(refreshing bool) {
	s.mu.Lock()
	s.refreshing = refreshing
	s.mu.Unlock()
}

func (s *PricingService) recordPricingError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.lastErrorAt = time.Now()
	s.lastError = err.Error()
	s.mu.Unlock()
}

func (s *PricingService) clearPricingError() {
	s.mu.Lock()
	s.lastErrorAt = time.Time{}
	s.lastError = ""
	s.mu.Unlock()
}

// fetchRemoteHash 从远程获取哈希值
func (s *PricingService) fetchRemoteHash() (string, error) {
	hashURL, err := s.validatePricingURL(s.cfg.Pricing.HashURL)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hash, err := s.remoteClient.FetchHashText(ctx, hashURL)
	if err != nil {
		return "", err
	}
	// 同时兼容纯摘要和 sha256sum 的标准“摘要  文件名”格式。只读取
	// 第一个字段，后面的文件名不参与路径解析或任何文件系统操作。
	fields := strings.Fields(hash)
	if len(fields) == 0 || len(fields[0]) != sha256.Size*2 {
		return "", fmt.Errorf("invalid remote pricing hash format")
	}
	hash = fields[0]
	if _, err := hex.DecodeString(hash); err != nil {
		return "", fmt.Errorf("invalid remote pricing hash: %w", err)
	}
	return strings.ToLower(hash), nil
}

func (s *PricingService) validatePricingURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid pricing url: credentials, query strings and fragments are not allowed")
	}
	if s.cfg != nil && !s.cfg.Security.URLAllowlist.Enabled {
		normalized, err := urlvalidator.ValidateURLFormat(raw, s.cfg.Security.URLAllowlist.AllowInsecureHTTP)
		if err != nil {
			return "", fmt.Errorf("invalid pricing url: %w", err)
		}
		return normalized, nil
	}
	normalized, err := urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
		AllowedHosts:     s.cfg.Security.URLAllowlist.PricingHosts,
		RequireAllowlist: true,
		AllowPrivate:     s.cfg.Security.URLAllowlist.AllowPrivateHosts,
	})
	if err != nil {
		return "", fmt.Errorf("invalid pricing url: %w", err)
	}
	return normalized, nil
}

// GetModelPricing 获取模型价格（带模糊匹配与家族/默认模型兜底）。
// 计费路径用它：拿不到精确价格时允许按家族或默认测试模型兜底，并打告警。
func (s *PricingService) GetModelPricing(modelName string) *LiteLLMModelPricing {
	return s.getModelPricing(modelName, true)
}

// GetModelPricingForReport 获取「只靠目录/官方快照/已知别名能支撑」的模型价格：
// 与 GetModelPricing 走同一套候选与匹配链路，但**不做**最后那层
// 「未知 gpt-* → 默认测试模型（gpt-5.4）」的猜测，拿不到就返回 nil。
//
// 为什么需要单独一个入口：网关会把账号/分组实际公开的模型清单逐条取价并上报给主站做
// 价格镜像，而上游账号的清单里可能混入目录里没有的名字（实测：上游账号 credentials 中的
// gpt-reserve）。若上报走 GetModelPricing，这些模型会被静默按 gpt-5.4 价格上报（污染管理端
// 的价格镜像，让人以为它有价），而且每次上报都打一条 pricing fallback 告警
// （实测 6 小时 1480 条，把真正的缺价告警淹掉）。计费路径仍走 GetModelPricing，
// 所以「真的按兜底价计费」这件事依旧会告警。
func (s *PricingService) GetModelPricingForReport(modelName string) *LiteLLMModelPricing {
	return s.getModelPricing(modelName, false)
}

func (s *PricingService) getModelPricing(modelName string, allowDefaultGuess bool) *LiteLLMModelPricing {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if modelName == "" {
		return nil
	}

	// 标准化模型名称（同时兼容 "models/xxx"、VertexAI 资源名等前缀）
	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	lookupCandidates := s.buildModelLookupCandidates(modelLower)
	if pricing := s.lookupIdentifiedModelPricingLocked(lookupCandidates); pricing != nil {
		return pricing
	}

	// 独立审查模型使用自己的固定价卡；放在目录精确匹配之后，保留有效远程及管理员覆盖。
	if lookupCandidates[0] == "codex-auto-review" {
		return officialOpenAITextPricingForModel(lookupCandidates[0])
	}

	// 4. 基于模型系列匹配（Claude）
	if pricing := s.matchByModelFamily(lookupCandidates[0]); pricing != nil {
		return pricing
	}

	// 5. OpenAI 模型回退策略
	if strings.HasPrefix(lookupCandidates[0], "gpt-") {
		return s.matchOpenAIModel(lookupCandidates[0], allowDefaultGuess)
	}

	// 6. MiMo 模型回退策略
	if strings.HasPrefix(lookupCandidates[0], "mimo-") {
		return s.matchMimoModel(lookupCandidates[0])
	}

	// GLM/Kimi 新模型必须在远程目录尚未更新或本地缓存损坏时仍可安全计费。
	if pricing := officialCompatibleTextPricingForModel(lookupCandidates[0]); pricing != nil {
		return pricing
	}

	// Grok official models must remain available when the remote LiteLLM file
	// is stale or unavailable. Return a clone so callers cannot mutate the
	// shared built-in catalog.
	for _, candidate := range lookupCandidates {
		canonical := canonicalGrokPricingModel(candidate)
		if pricing, ok := officialGrokPricing[canonical]; ok {
			return cloneLiteLLMModelPricing(pricing)
		}
	}

	return nil
}

// lookupIdentifiedModelPricingLocked 只允许精确名、已知拼写变体和去掉日期后缀的同名条目。
// 响应模型来自外部，不能用模型家族或未知 GPT 的兜底价猜测。
func (s *PricingService) lookupIdentifiedModelPricingLocked(lookupCandidates []string) *LiteLLMModelPricing {
	if len(lookupCandidates) == 0 {
		return nil
	}

	// 1. 精确匹配
	for _, candidate := range lookupCandidates {
		if candidate == "" {
			continue
		}
		if pricing, ok := s.pricingData[candidate]; ok {
			return pricing
		}
	}

	// 2. 处理常见的模型名称变体
	// claude-opus-4-5-20251101 -> claude-opus-4.5-20251101
	for _, candidate := range lookupCandidates {
		normalized := strings.ReplaceAll(candidate, "-4-5-", "-4.5-")
		if pricing, ok := s.pricingData[normalized]; ok {
			return pricing
		}
	}

	// 3. 尝试模糊匹配（去掉版本号后缀）
	// claude-opus-4-5-20251101 -> claude-opus-4.5
	baseName := s.extractBaseName(lookupCandidates[0])
	for key, pricing := range s.pricingData {
		keyBase := s.extractBaseName(strings.ToLower(key))
		if keyBase == baseName {
			return pricing
		}
	}

	return nil
}

// GetIdentifiedModelPricing 返回价格目录中可确定性识别的条目。
func (s *PricingService) GetIdentifiedModelPricing(modelName string) *LiteLLMModelPricing {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	if modelLower == "" {
		return nil
	}
	return s.lookupIdentifiedModelPricingLocked(s.buildModelLookupCandidates(modelLower))
}

func canonicalGrokPricingModel(model string) string {
	model = xai.CanonicalModelID(xai.StripGrokProviderPrefix(model))

	// Some compatible upstreams expose the current video model with a
	// preview/version suffix. Billing already treats the whole 1.5 family as
	// one SKU, so price reporting must use the same rule or the model is usable
	// but disappears from the control-plane price page.
	if strings.HasPrefix(strings.ToLower(model), "grok-imagine-video-1.5") {
		return "grok-imagine-video-1.5"
	}
	return model
}

func (s *PricingService) buildModelLookupCandidates(modelLower string) []string {
	// Prefer canonical model name first (this also improves billing compatibility with "models/xxx").
	candidates := []string{
		normalizeModelNameForPricing(modelLower),
		modelLower,
	}
	candidates = append(candidates,
		strings.TrimPrefix(modelLower, "models/"),
		lastSegment(modelLower),
		lastSegment(strings.TrimPrefix(modelLower, "models/")),
	)

	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	if len(out) == 0 {
		return []string{modelLower}
	}
	return out
}

func normalizeModelNameForPricing(model string) string {
	// Common Gemini/VertexAI forms:
	// - models/gemini-2.0-flash-exp
	// - publishers/google/models/gemini-2.5-pro
	// - projects/.../locations/.../publishers/google/models/gemini-2.5-pro
	model = strings.TrimSpace(model)
	model = strings.TrimLeft(model, "/")
	model = strings.TrimPrefix(model, "models/")
	model = strings.TrimPrefix(model, "publishers/google/models/")

	if idx := strings.LastIndex(model, "/publishers/google/models/"); idx != -1 {
		model = model[idx+len("/publishers/google/models/"):]
	}
	if idx := strings.LastIndex(model, "/models/"); idx != -1 {
		model = model[idx+len("/models/"):]
	}

	model = strings.TrimLeft(model, "/")
	if canonical := canonicalizeOpenAIModelAliasSpelling(model); canonical != "" {
		if matchesKnownOpenAIModelVariant(canonical, "gpt-6-astra") ||
			canonical == "gpt-6-astar" || strings.HasPrefix(canonical, "gpt-6-astar-") {
			// astar 是 astra 的常见误记，计价必须收敛到官方型号 id。
			return "gpt-6-astra"
		}
		if matchesKnownOpenAIModelVariant(canonical, "gpt-5.6") {
			return "gpt-5.6-sol"
		}
		return canonical
	}
	return model
}

func lastSegment(model string) string {
	if idx := strings.LastIndex(model, "/"); idx != -1 {
		return model[idx+1:]
	}
	return model
}

// extractBaseName 提取基础模型名称（去掉日期版本号）
func (s *PricingService) extractBaseName(model string) string {
	// 移除日期后缀 (如 -20251101, -20241022)
	parts := strings.Split(model, "-")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		// 跳过看起来像日期的部分（8位数字）
		if len(part) == 8 && isNumeric(part) {
			continue
		}
		// 跳过版本号（如 v1:0）
		if strings.Contains(part, ":") {
			continue
		}
		result = append(result, part)
	}
	return strings.Join(result, "-")
}

// matchByModelFamily 基于模型系列匹配
func (s *PricingService) matchByModelFamily(model string) *LiteLLMModelPricing {
	// modelFamily 定义一个模型系列的匹配和定价查找规则。
	type modelFamily struct {
		name    string   // 系列名称
		match   []string // 用于将模型归类到此系列的模式（strings.Contains 匹配）
		pricing []string // 用于在定价数据中查找价格的模式（nil 则复用 match；可包含低版本 fallback）
	}

	// 按特异性降序排列：高版本号在前，避免 "claude-opus-4"（opus-4 系列）
	// 因子串关系误匹配 "claude-opus-4-7"（opus-4.7 系列）。
	// 注意：原 map 实现存在 Go map 迭代随机性导致的同类 bug，此处改为有序切片修复。
	families := []modelFamily{
		{name: "opus-5", match: []string{"claude-opus-5"}, pricing: []string{"claude-opus-5", "claude-opus-4-8"}},
		{name: "opus-4.8", match: []string{"claude-opus-4-8", "claude-opus-4.8"}, pricing: []string{"claude-opus-4-8", "claude-opus-4.8", "claude-opus-4-7"}},
		{name: "opus-4.7", match: []string{"claude-opus-4-7", "claude-opus-4.7"}, pricing: []string{"claude-opus-4-7", "claude-opus-4.7", "claude-opus-4-6"}},
		{name: "opus-4.6", match: []string{"claude-opus-4-6", "claude-opus-4.6"}},
		{name: "opus-4.5", match: []string{"claude-opus-4-5", "claude-opus-4.5"}},
		{name: "opus-4", match: []string{"claude-opus-4", "claude-3-opus"}},
		{name: "sonnet-4.5", match: []string{"claude-sonnet-4-5", "claude-sonnet-4.5"}},
		{name: "sonnet-4", match: []string{"claude-sonnet-4", "claude-3-5-sonnet"}},
		{name: "sonnet-3.5", match: []string{"claude-3-5-sonnet", "claude-3.5-sonnet"}},
		{name: "sonnet-3", match: []string{"claude-3-sonnet"}},
		{name: "haiku-3.5", match: []string{"claude-3-5-haiku", "claude-3.5-haiku"}},
		{name: "haiku-3", match: []string{"claude-3-haiku"}},
	}

	// Phase 1: 按有序切片归类（最具体的系列优先匹配）
	var matched *modelFamily
	for i := range families {
		for _, pattern := range families[i].match {
			if strings.Contains(model, pattern) || strings.Contains(model, strings.ReplaceAll(pattern, "-", "")) {
				matched = &families[i]
				break
			}
		}
		if matched != nil {
			break
		}
	}

	// Phase 2: 二次兜底——当模型 ID 不含已知模式串时，按关键字粗分
	if matched == nil {
		var fallbackName string
		switch {
		case strings.Contains(model, "opus"):
			switch {
			case strings.Contains(model, "opus-5") || strings.Contains(model, "opus5"):
				fallbackName = "opus-5"
			case strings.Contains(model, "4.8") || strings.Contains(model, "4-8"):
				fallbackName = "opus-4.8"
			case strings.Contains(model, "4.7") || strings.Contains(model, "4-7"):
				fallbackName = "opus-4.7"
			case strings.Contains(model, "4.6") || strings.Contains(model, "4-6"):
				fallbackName = "opus-4.6"
			case strings.Contains(model, "4.5") || strings.Contains(model, "4-5"):
				fallbackName = "opus-4.5"
			default:
				fallbackName = "opus-4"
			}
		case strings.Contains(model, "sonnet"):
			switch {
			case strings.Contains(model, "4.5") || strings.Contains(model, "4-5"):
				fallbackName = "sonnet-4.5"
			case strings.Contains(model, "3-5") || strings.Contains(model, "3.5"):
				fallbackName = "sonnet-3.5"
			default:
				fallbackName = "sonnet-4"
			}
		case strings.Contains(model, "haiku"):
			switch {
			case strings.Contains(model, "3-5") || strings.Contains(model, "3.5"):
				fallbackName = "haiku-3.5"
			default:
				fallbackName = "haiku-3"
			}
		}
		if fallbackName != "" {
			for i := range families {
				if families[i].name == fallbackName {
					matched = &families[i]
					break
				}
			}
		}
	}

	if matched == nil {
		return nil
	}

	// Phase 3: 在定价数据中查找该系列的价格
	lookups := matched.pricing
	if lookups == nil {
		lookups = matched.match
	}
	for _, pattern := range lookups {
		for key, pricing := range s.pricingData {
			keyLower := strings.ToLower(key)
			if strings.Contains(keyLower, pattern) {
				logger.LegacyPrintf("service.pricing", "[Pricing] Fuzzy matched %s -> %s", model, key)
				return pricing
			}
		}
	}

	return nil
}

// matchOpenAIModel OpenAI 模型回退匹配策略
// 回退顺序：
// 1. gpt-5.3-codex-spark* -> gpt-5.1-codex（按业务要求固定计费）
// 2. gpt-5.2-codex -> gpt-5.2（去掉后缀如 -codex, -mini, -max 等）
// 3. gpt-5.2-20251222 -> gpt-5.2（去掉日期版本号）
// 4. gpt-5.3-codex -> gpt-5.2-codex
// 5. gpt-5.4* -> 业务静态兜底价
// 6. 最终回退到 DefaultTestModel（仅当 allowDefaultGuess 为 true，即计费路径）
// allowDefaultGuess=false 时（价格上报等「只认目录事实」的场景）跳过第 6 步：
// 未知 gpt-* 名字不应该被当成某个默认模型来对外报价格。
func (s *PricingService) matchOpenAIModel(model string, allowDefaultGuess bool) *LiteLLMModelPricing {
	if strings.HasPrefix(model, "gpt-6") || strings.HasPrefix(model, "gpt-5.5") || strings.HasPrefix(model, "gpt-5.6") {
		normalized := normalizeKnownOpenAICodexModel(model)
		if normalized == "" {
			return nil
		}
		// 精确目录项在进入本函数前已经命中；这里只允许官方受控快照
		// 承接合法别名。gpt-5.5-pro 等无本地快照的型号缺价时直接报错。
		return officialOpenAITextPricingForModel(normalized)
	}

	if strings.HasPrefix(model, "gpt-5.3-codex-spark") {
		if pricing, ok := s.pricingData["gpt-5.1-codex"]; ok {
			logger.LegacyPrintf("service.pricing", "[Pricing][SparkBilling] %s -> %s billing", model, "gpt-5.1-codex")
			logger.With(zap.String("component", "service.pricing")).
				Info(fmt.Sprintf("[Pricing] OpenAI fallback matched %s -> %s", model, "gpt-5.1-codex"))
			return pricing
		}
	}

	// 尝试的回退变体
	variants := s.generateOpenAIModelVariants(model, openAIModelDatePattern)

	for _, variant := range variants {
		if pricing, ok := s.pricingData[variant]; ok {
			logger.With(zap.String("component", "service.pricing")).
				Info(fmt.Sprintf("[Pricing] OpenAI fallback matched %s -> %s", model, variant))
			return pricing
		}
	}

	if strings.HasPrefix(model, "gpt-5.3-codex") {
		if pricing, ok := s.pricingData["gpt-5.2-codex"]; ok {
			logger.With(zap.String("component", "service.pricing")).
				Info(fmt.Sprintf("[Pricing] OpenAI fallback matched %s -> %s", model, "gpt-5.2-codex"))
			return pricing
		}
	}

	// mini / nano 必须在通用 GPT-5.4 官方回退之前判断，否则 variants 中的
	// gpt-5.4 会抢先命中并把小模型按旗舰型号价格计费。
	if strings.HasPrefix(model, "gpt-5.4-mini") {
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] OpenAI fallback matched %s -> %s", model, "gpt-5.4-mini(static)"))
		return cloneLiteLLMModelPricing(openAIGPT54MiniFallbackPricing)
	}

	if strings.HasPrefix(model, "gpt-5.4-nano") {
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] OpenAI fallback matched %s -> %s", model, "gpt-5.4-nano(static)"))
		return cloneLiteLLMModelPricing(openAIGPT54NanoFallbackPricing)
	}

	// 官方已发布型号只能命中自己的价格，禁止跨版本静默回退。
	// 日期后缀和紧凑别名会在 variants 阶段先归一化到这里。
	for _, candidate := range append([]string{model}, variants...) {
		if pricing := officialOpenAITextPricingForModel(candidate); pricing != nil {
			logger.With(zap.String("component", "service.pricing")).
				Info(fmt.Sprintf("[Pricing] OpenAI official fallback matched %s -> %s", model, candidate))
			return pricing
		}
	}

	// GPT-5.5/5.6/6 已经进入独立价格时代。未命中精确型号或官方别名时必须
	// 返回缺价，不能继续落到默认测试模型，否则新型号可能被按旧型号收费。
	if strings.HasPrefix(model, "gpt-6") || strings.HasPrefix(model, "gpt-5.5") || strings.HasPrefix(model, "gpt-5.6") {
		return nil
	}

	if strings.HasPrefix(model, "gpt-5.4") {
		return officialOpenAITextPricingForModel("gpt-5.4")
	}

	if isOpenAIImageGenerationModel(model) {
		for _, candidate := range []string{"gpt-image-2", "gpt-image-1.5", "gpt-image-1"} {
			if pricing, ok := s.pricingData[candidate]; ok {
				logger.LegacyPrintf("service.pricing", "[Pricing] OpenAI image fallback matched %s -> %s", model, candidate)
				return pricing
			}
		}
		return nil
	}

	// 最终回退到 DefaultTestModel：只对计费路径开放（见函数注释）。
	if !allowDefaultGuess {
		return nil
	}
	// 到这里说明是计费路径，允许按默认测试模型兜底并告警。
	defaultModel := strings.ToLower(openai.DefaultTestModel)
	if pricing, ok := s.pricingData[defaultModel]; ok {
		logger.LegacyPrintf("service.pricing", "[Pricing] OpenAI fallback to default model %s -> %s", model, defaultModel)
		return pricing
	}

	return nil
}

// matchMimoModel maps deprecated V2 names to the V2.5 billing family described
// by Xiaomi MiMo. ASR/TTS are intentionally excluded here because they are not
// charged as input/output tokens in the current official price sheet.
func (s *PricingService) matchMimoModel(model string) *LiteLLMModelPricing {
	switch {
	case strings.Contains(model, "asr") || strings.Contains(model, "tts"):
		return nil
	case strings.HasPrefix(model, "mimo-v2.5-pro") || strings.HasPrefix(model, "mimo-v2-pro"):
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] MiMo fallback matched %s -> %s", model, "mimo-v2.5-pro(static)"))
		return mimoV25ProFallbackPricing
	case strings.HasPrefix(model, "mimo-v2.5") ||
		strings.HasPrefix(model, "mimo-v2-flash") ||
		strings.HasPrefix(model, "mimo-v2-omni"):
		logger.With(zap.String("component", "service.pricing")).
			Info(fmt.Sprintf("[Pricing] MiMo fallback matched %s -> %s", model, "mimo-v2.5(static)"))
		return mimoV25FallbackPricing
	default:
		return nil
	}
}

// generateOpenAIModelVariants 生成 OpenAI 模型的回退变体列表
func (s *PricingService) generateOpenAIModelVariants(model string, datePattern *regexp.Regexp) []string {
	seen := make(map[string]bool)
	var variants []string

	addVariant := func(v string) {
		if v != model && !seen[v] {
			seen[v] = true
			variants = append(variants, v)
		}
	}

	// 1. 去掉日期版本号: gpt-5.2-20251222 -> gpt-5.2
	withoutDate := datePattern.ReplaceAllString(model, "")
	if withoutDate != model {
		addVariant(withoutDate)
	}

	// 2. 提取基础版本号: gpt-5.2-codex -> gpt-5.2
	// 只匹配纯数字版本号格式 gpt-X 或 gpt-X.Y，不匹配 gpt-4o 这种带字母后缀的
	if matches := openAIModelBasePattern.FindStringSubmatch(model); len(matches) > 1 {
		addVariant(matches[1])
	}

	// 3. 同时去掉日期后再提取基础版本号
	if withoutDate != model {
		if matches := openAIModelBasePattern.FindStringSubmatch(withoutDate); len(matches) > 1 {
			addVariant(matches[1])
		}
	}

	return variants
}

// GetStatus 获取服务状态
func (s *PricingService) GetStatus() map[string]any {
	if s == nil {
		return map[string]any{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]any{
		"enabled":            pricingRemoteConfigured(s.cfg),
		"model_count":        len(s.pricingData),
		"override_count":     len(s.pricingOverrides),
		"last_updated":       s.lastUpdated,
		"last_success_at":    s.lastSuccessAt,
		"last_error_at":      s.lastErrorAt,
		"last_error":         s.lastError,
		"local_hash":         s.localHash,
		"source":             s.source,
		"refreshing":         s.refreshing,
		"remote_url":         pricingRemoteURL(s.cfg),
		"remote_host":        pricingRemoteHost(pricingRemoteURL(s.cfg)),
		"catalog_version":    s.catalogVersion,
		"catalog_sequence":   s.catalogSequence,
		"remote_model_count": s.remoteModelCount,
	}
}

// ForceUpdate 强制更新
func (s *PricingService) ForceUpdate() error {
	if s == nil || s.cfg == nil || !pricingRemoteConfigured(s.cfg) {
		return fmt.Errorf("pricing remote URL is not configured")
	}
	return s.downloadPricingData()
}

// getPricingFilePath 获取价格文件路径
func (s *PricingService) getPricingFilePath() string {
	return filepath.Join(s.cfg.Pricing.DataDir, "model_pricing.json")
}

// getHashFilePath 获取哈希文件路径
func (s *PricingService) getHashFilePath() string {
	return filepath.Join(s.cfg.Pricing.DataDir, "model_pricing.sha256")
}

// ListModelNamesByProvider returns all model names in the catalog whose
// LiteLLMProvider matches the given provider string (case-insensitive).
// The returned slice is sorted alphabetically.
func (s *PricingService) ListModelNamesByProvider(provider string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	provider = strings.ToLower(strings.TrimSpace(provider))
	names := make([]string, 0)
	for name, p := range s.pricingData {
		if strings.ToLower(p.LiteLLMProvider) == provider {
			if p.Disabled {
				continue
			}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

type ModelPricingCatalogEntry struct {
	Model                               string
	LiteLLMProvider                     string
	ModelVendor                         string
	InputModalities                     []string
	MaxInputTokens                      int
	MetadataSource                      string
	PriceFields                         []string
	Mode                                string
	BillingMode                         string
	InputCostPerToken                   float64
	InputCostPerTokenPriority           float64
	OutputCostPerToken                  float64
	OutputCostPerTokenPriority          float64
	CacheCreationCost                   float64
	CacheCreationCostPriority           float64
	CacheReadCost                       float64
	CacheReadCostPriority               float64
	InputCostPerTokenAbove272K          float64
	InputCostPerTokenPriorityAbove272K  float64
	OutputCostPerTokenAbove272K         float64
	OutputCostPerTokenPriorityAbove272K float64
	CacheCreationCostAbove272K          float64
	CacheCreationCostPriorityAbove272K  float64
	CacheReadCostAbove272K              float64
	CacheReadCostPriorityAbove272K      float64
	LongContextInputTokenThreshold      int
	OutputCostPerImage                  float64
	OutputCostPerImageToken             float64
	InputCostPerImageToken              *float64
	PerRequestPrice                     float64
	VideoPricePerSecond480P             float64
	VideoPricePerSecond720P             float64
	VideoPricePerSecond1080P            float64
	SupportsPromptCaching               bool
	SupportsServiceTier                 bool
	Source                              string
	IsOverride                          bool
	Disabled                            bool
}

// ListModelPricingCatalog returns the effective model pricing catalog. It
// includes persisted overrides and static MiMo fallbacks that may not exist in
// an old runtime pricing cache yet.
func (s *PricingService) ListModelPricingCatalog() []ModelPricingCatalogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]ModelPricingCatalogEntry, 0, len(s.pricingData)+len(officialGrokPricing)+len(officialCompatibleTextPricing)+5)
	for model, pricing := range s.pricingData {
		if pricing == nil {
			continue
		}
		_, override := s.pricingOverrides[model]
		entries = append(entries, modelPricingCatalogEntry(model, pricing, pricingSourceLabel(override), override))
	}

	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		seen[strings.ToLower(entry.Model)] = struct{}{}
	}
	for model, pricing := range staticMimoPricingCatalog() {
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		entries = append(entries, modelPricingCatalogEntry(model, pricing, "static_fallback", false))
	}
	for model, pricing := range officialGrokPricing {
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		entries = append(entries, modelPricingCatalogEntry(model, pricing, "static_fallback", false))
	}
	exactSeen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		exactSeen[entry.Model] = struct{}{}
	}
	for model, pricing := range officialCompatibleTextPricing {
		if _, ok := exactSeen[model]; ok {
			continue
		}
		entries = append(entries, modelPricingCatalogEntry(model, pricing, "static_fallback", false))
		exactSeen[model] = struct{}{}
	}

	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Model) < strings.ToLower(entries[j].Model)
	})
	return entries
}

func modelPricingCatalogEntry(model string, pricing *LiteLLMModelPricing, source string, override bool) ModelPricingCatalogEntry {
	cacheCreation := pricing.CacheCreationInputTokenCost
	if cacheCreation == 0 {
		cacheCreation = pricing.CacheCreationInputTokenCostAbove1hr
	}
	return ModelPricingCatalogEntry{
		Model:                               model,
		LiteLLMProvider:                     pricing.LiteLLMProvider,
		ModelVendor:                         pricing.ModelVendor,
		InputModalities:                     append([]string(nil), pricing.InputModalities...),
		MaxInputTokens:                      pricing.MaxInputTokens,
		MetadataSource:                      pricing.MetadataSource,
		PriceFields:                         pricing.PriceFields,
		Mode:                                pricing.Mode,
		BillingMode:                         modelPricingBillingMode(pricing),
		InputCostPerToken:                   pricing.InputCostPerToken,
		InputCostPerTokenPriority:           pricing.InputCostPerTokenPriority,
		OutputCostPerToken:                  pricing.OutputCostPerToken,
		OutputCostPerTokenPriority:          pricing.OutputCostPerTokenPriority,
		CacheCreationCost:                   cacheCreation,
		CacheCreationCostPriority:           pricing.CacheCreationInputTokenCostPriority,
		CacheReadCost:                       pricing.CacheReadInputTokenCost,
		CacheReadCostPriority:               pricing.CacheReadInputTokenCostPriority,
		InputCostPerTokenAbove272K:          pricing.InputCostPerTokenAbove272K,
		InputCostPerTokenPriorityAbove272K:  pricing.InputCostPerTokenPriorityAbove272K,
		OutputCostPerTokenAbove272K:         pricing.OutputCostPerTokenAbove272K,
		OutputCostPerTokenPriorityAbove272K: pricing.OutputCostPerTokenPriorityAbove272K,
		CacheCreationCostAbove272K:          pricing.CacheCreationInputTokenCostAbove272K,
		CacheCreationCostPriorityAbove272K:  pricing.CacheCreationInputTokenCostPriorityAbove272K,
		CacheReadCostAbove272K:              pricing.CacheReadInputTokenCostAbove272K,
		CacheReadCostPriorityAbove272K:      pricing.CacheReadInputTokenCostPriorityAbove272K,
		LongContextInputTokenThreshold:      pricing.LongContextInputTokenThreshold,
		OutputCostPerImage:                  pricing.OutputCostPerImage,
		OutputCostPerImageToken:             pricing.OutputCostPerImageToken,
		InputCostPerImageToken:              cloneFloat64Ptr(pricing.InputCostPerImageToken),
		PerRequestPrice:                     modelPricingPerRequestPrice(pricing),
		VideoPricePerSecond480P:             pricing.VideoPricePerSecond480P,
		VideoPricePerSecond720P:             pricing.VideoPricePerSecond720P,
		VideoPricePerSecond1080P:            pricing.VideoPricePerSecond1080P,
		SupportsPromptCaching:               pricing.SupportsPromptCaching,
		SupportsServiceTier:                 pricing.SupportsServiceTier,
		Source:                              source,
		IsOverride:                          override,
		Disabled:                            pricing.Disabled,
	}
}

func modelPricingBillingMode(pricing *LiteLLMModelPricing) string {
	if pricing == nil {
		return "token"
	}
	mode := strings.ToLower(strings.TrimSpace(pricing.BillingMode))
	switch mode {
	case "token", "per_request", "image", "video":
		return mode
	}
	if strings.TrimSpace(pricing.Mode) == "image_generation" || pricing.OutputCostPerImage > 0 {
		return "image"
	}
	if pricing.PerRequestPrice > 0 {
		return "per_request"
	}
	return "token"
}

func modelPricingPerRequestPrice(pricing *LiteLLMModelPricing) float64 {
	if pricing == nil {
		return 0
	}
	if pricing.PerRequestPrice > 0 {
		return pricing.PerRequestPrice
	}
	return pricing.OutputCostPerImage
}

func pricingSourceLabel(override bool) string {
	if override {
		return "override"
	}
	return "catalog"
}

func staticMimoPricingCatalog() map[string]*LiteLLMModelPricing {
	return map[string]*LiteLLMModelPricing{
		"mimo-v2.5-pro": mimoV25ProFallbackPricing,
		"mimo-v2-pro":   mimoV25ProFallbackPricing,
		"mimo-v2.5":     mimoV25FallbackPricing,
		"mimo-v2-flash": mimoV25FallbackPricing,
		"mimo-v2-omni":  mimoV25FallbackPricing,
	}
}

// UpsertModelPricingOverride writes an administrator-managed model price
// override. Overrides are intentionally stored separately from the mirrored
// catalog so remote price syncs cannot erase local business decisions.
func (s *PricingService) UpsertModelPricingOverride(model string, pricing LiteLLMModelPricing) (ModelPricingCatalogEntry, error) {
	model = normalizeModelNameForPricing(strings.ToLower(strings.TrimSpace(model)))
	if model == "" {
		return ModelPricingCatalogEntry{}, fmt.Errorf("model is required")
	}
	if strings.TrimSpace(pricing.LiteLLMProvider) == "" {
		pricing.LiteLLMProvider = "custom"
	}
	if strings.TrimSpace(pricing.Mode) == "" {
		pricing.Mode = "chat"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cp := pricing
	nextOverrides := clonePricingMap(s.pricingOverrides)
	nextOverrides[model] = &cp
	if err := s.savePricingOverrides(nextOverrides); err != nil {
		return ModelPricingCatalogEntry{}, err
	}
	s.pricingOverrides = nextOverrides
	s.pricingData = mergePricingMaps(s.basePricingData, nextOverrides)
	return modelPricingCatalogEntry(model, &cp, "override", true), nil
}

// IsModelDisabled reports whether an administrator disabled a model locally.
// Unknown models stay enabled so legacy account mappings continue to work.
func (s *PricingService) IsModelDisabled(model string) bool {
	if s == nil {
		return false
	}
	model = normalizeModelNameForPricing(strings.ToLower(strings.TrimSpace(model)))
	if model == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if pricing, ok := s.pricingData[model]; ok && pricing != nil {
		return pricing.Disabled
	}
	return false
}

// DeleteModelPricingOverride removes a local override. The effective price
// falls back to the mirrored/default catalog, or the static MiMo fallback when
// applicable.
func (s *PricingService) DeleteModelPricingOverride(model string) (ModelPricingCatalogEntry, bool, error) {
	model = normalizeModelNameForPricing(strings.ToLower(strings.TrimSpace(model)))
	if model == "" {
		return ModelPricingCatalogEntry{}, false, fmt.Errorf("model is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.pricingOverrides[model]; !ok {
		return ModelPricingCatalogEntry{}, false, nil
	}
	nextOverrides := clonePricingMap(s.pricingOverrides)
	delete(nextOverrides, model)
	if err := s.savePricingOverrides(nextOverrides); err != nil {
		return ModelPricingCatalogEntry{}, false, err
	}
	s.pricingOverrides = nextOverrides
	s.pricingData = mergePricingMaps(s.basePricingData, nextOverrides)

	if current, ok := s.pricingData[model]; ok && current != nil {
		return modelPricingCatalogEntry(model, current, "catalog", false), true, nil
	}
	if fallback, ok := staticMimoPricingCatalog()[model]; ok {
		return modelPricingCatalogEntry(model, fallback, "static_fallback", false), true, nil
	}
	return ModelPricingCatalogEntry{Model: model}, true, nil
}

// isNumeric 检查字符串是否为纯数字
func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
