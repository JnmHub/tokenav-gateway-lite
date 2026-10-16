package handler

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
)

type grokStandaloneSearchRequest struct {
	Query                    string   `json:"query"`
	Input                    string   `json:"input"`
	MaxResults               *int     `json:"max_results"`
	AllowedXHandles          []string `json:"allowed_x_handles"`
	ExcludedXHandles         []string `json:"excluded_x_handles"`
	FromDate                 string   `json:"from_date"`
	ToDate                   string   `json:"to_date"`
	EnableImageUnderstanding *bool    `json:"enable_image_understanding"`
	EnableVideoUnderstanding *bool    `json:"enable_video_understanding"`
}

// XSearch 只标记独立 X 搜索协议，业务链继续复用 WebSearch，
// 以保证审计、换号、并发门禁和按次计费只有一份实现。
func (h *GatewayHandler) XSearch(c *gin.Context) {
	c.Set("grok_x_search_endpoint", true)
	h.WebSearch(c)
}

func resolveGrokStandaloneSearchModel() string {
	model := strings.TrimSpace(xai.RuntimeModelMappingOptions().DefaultText)
	if model == "" {
		return xai.DefaultTextModel
	}
	return model
}

func buildGrokXSearchResponsesBody(req grokStandaloneSearchRequest, model string) ([]byte, error) {
	input := strings.TrimSpace(req.Query)
	if input == "" {
		input = strings.TrimSpace(req.Input)
	}
	tool := map[string]any{"type": "x_search"}
	if len(req.AllowedXHandles) > 0 {
		tool["allowed_x_handles"] = req.AllowedXHandles
	}
	if len(req.ExcludedXHandles) > 0 {
		tool["excluded_x_handles"] = req.ExcludedXHandles
	}
	if fromDate := strings.TrimSpace(req.FromDate); fromDate != "" {
		tool["from_date"] = fromDate
	}
	if toDate := strings.TrimSpace(req.ToDate); toDate != "" {
		tool["to_date"] = toDate
	}
	if req.EnableImageUnderstanding != nil {
		tool["enable_image_understanding"] = *req.EnableImageUnderstanding
	}
	if req.EnableVideoUnderstanding != nil {
		tool["enable_video_understanding"] = *req.EnableVideoUnderstanding
	}
	maxResults := 0
	if req.MaxResults != nil {
		maxResults = *req.MaxResults
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = xai.DefaultTextModel
	}
	return json.Marshal(map[string]any{
		"model":       model,
		"input":       buildGrokXSearchPrompt(input, maxResults),
		"tools":       []map[string]any{tool},
		"tool_choice": "required",
		"include":     []string{"x_search_call.action.sources"},
		"store":       false,
		"stream":      false,
	})
}

func buildGrokXSearchPrompt(query string, maxResults int) string {
	return fmt.Sprintf(`Search X for the user query below. Return ONLY valid JSON with this exact shape: {"results":[{"url":"https://...","title":"post or page title","snippet":"concise factual summary"}]}. Return at most %d unique results. Every URL must be an actual x_search source. Populate a non-empty title and snippet for every result. Do not wrap the JSON in markdown.

User query:
%s`, normalizeGrokWebSearchMaxResults(maxResults), query)
}
