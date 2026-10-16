package service

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	upstreamResponseModelObserverContextKey = "upstream_response_model_observer"
	upstreamResponseModelMaxLength          = 200
)

// upstreamResponseModelObserver 记录上游声明，用于本地运维审计。
// 终态声明优先于早期声明；它不改变路由，但显式响应模型计费遇到冲突时会安全回退。
type upstreamResponseModelObserver struct {
	first    string
	terminal string
	conflict bool
}

func (o *upstreamResponseModelObserver) Observe(model string, terminal bool) {
	if o == nil {
		return
	}
	model = normalizeObservedUpstreamResponseModel(model)
	if model == "" {
		return
	}
	if current := o.Model(); current != "" && !strings.EqualFold(current, model) {
		o.conflict = true
	}
	if terminal {
		o.terminal = model
		return
	}
	if o.first == "" {
		o.first = model
	}
}

func normalizeObservedUpstreamResponseModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	runes := []rune(model)
	if len(runes) > upstreamResponseModelMaxLength {
		return string(runes[:upstreamResponseModelMaxLength])
	}
	return model
}

func (o *upstreamResponseModelObserver) ObserveOpenAI(payload []byte, eventType string) {
	if o == nil {
		return
	}
	o.Observe(firstValidTrimmedGJSONModel(payload, "response.model", "model"), isUpstreamResponseModelTerminalEvent(eventType))
}

func (o *upstreamResponseModelObserver) ObserveAnthropic(payload []byte) {
	if o == nil {
		return
	}
	o.Observe(firstValidTrimmedGJSONModel(payload, "message.model", "model"), false)
}

func (o *upstreamResponseModelObserver) ObserveGemini(payload []byte) {
	if o == nil {
		return
	}
	o.Observe(firstValidTrimmedGJSONModel(payload, "modelVersion", "response.modelVersion", "response.response.modelVersion"), true)
}

func (o *upstreamResponseModelObserver) Model() string {
	if o == nil {
		return ""
	}
	if o.terminal != "" {
		return o.terminal
	}
	return o.first
}

func (o *upstreamResponseModelObserver) Conflict() bool { return o != nil && o.conflict }

func beginUpstreamResponseModelObservation(c *gin.Context) *upstreamResponseModelObserver {
	// 兼容层可能在同一个请求中多次进入（例如协议转换、失败切换或
	// SSE/JSON 桥接）。复用已有观察器，避免后一次初始化覆盖先前已经
	// 记录的上游模型和冲突状态。
	if existing := upstreamResponseModelObserverFromContext(c); existing != nil {
		return existing
	}
	observer := &upstreamResponseModelObserver{}
	if c != nil {
		c.Set(upstreamResponseModelObserverContextKey, observer)
	}
	return observer
}

func upstreamResponseModelObserverFromContext(c *gin.Context) *upstreamResponseModelObserver {
	if c == nil {
		return nil
	}
	value, ok := c.Get(upstreamResponseModelObserverContextKey)
	if !ok {
		return nil
	}
	observer, _ := value.(*upstreamResponseModelObserver)
	return observer
}

// observeUpstreamGeminiResponseModel records the raw Gemini response before any
// protocol conversion or client-facing model rewrite. Keeping this observation
// in one helper prevents compatibility paths from accidentally auditing their
// synthetic Claude/OpenAI model instead of the upstream declaration.
func observeUpstreamGeminiResponseModel(c *gin.Context, payload []byte) {
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	observer.ObserveGemini(payload)
}

func observeUpstreamAnthropicResponseModel(c *gin.Context, payload []byte) {
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	observer.ObserveAnthropic(payload)
}

// observeUpstreamOpenAIResponseModel records an OpenAI-compatible response
// before a fallback adapter synthesizes a different protocol envelope.
func observeUpstreamOpenAIResponseModel(c *gin.Context, payload []byte, eventType string) {
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	observer.ObserveOpenAI(payload, eventType)
}

func observedUpstreamResponseModel(c *gin.Context) string {
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		return ""
	}
	return observer.Model()
}

func observedUpstreamResponseModelConflict(c *gin.Context) bool {
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		return false
	}
	return observer.Conflict()
}

func firstValidTrimmedGJSONModel(payload []byte, paths ...string) string {
	if len(payload) == 0 {
		return ""
	}
	for _, path := range paths {
		value := gjson.GetBytes(payload, path)
		if !value.Exists() || value.Type != gjson.String {
			continue
		}
		if model := strings.TrimSpace(value.String()); model != "" {
			// 大多数流式增量没有模型字段；只在发现候选值后验证完整 JSON，
			// 避免每个增量都做一次全量扫描，同时不接收畸形声明。
			if !gjson.ValidBytes(payload) {
				return ""
			}
			return model
		}
	}
	return ""
}

func isUpstreamResponseModelTerminalEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

func upstreamModelMismatch(sentModel, responseModel string) *bool {
	responseModel = strings.TrimSpace(responseModel)
	if responseModel == "" {
		return nil
	}
	sentModel = strings.TrimSpace(sentModel)
	mismatch := sentModel == "" || !upstreamModelsMatchForAudit(sentModel, responseModel)
	return &mismatch
}

// upstreamModelsMatchForAudit keeps response-model audit strict except for
// documented Grok build aliases. The aliases identify the same upstream model,
// so marking them as mismatched would create false operational alerts.
func upstreamModelsMatchForAudit(sentModel, responseModel string) bool {
	if strings.EqualFold(sentModel, responseModel) {
		return true
	}
	sentGrokModel := canonicalGrokBuildRuntimeModel(sentModel)
	return sentGrokModel != "" && sentGrokModel == canonicalGrokBuildRuntimeModel(responseModel)
}

func canonicalGrokBuildRuntimeModel(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "grok-4.5", "grok-4.5-latest", "grok-4.5-build":
		return "grok-4.5-build"
	case "grok-4.6", "grok-4.6-latest", "grok-4.6-build":
		return "grok-4.6-build"
	default:
		return ""
	}
}
