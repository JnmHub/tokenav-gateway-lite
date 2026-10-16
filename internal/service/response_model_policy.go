package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	ResponseModelModeInherit  = "inherit"
	ResponseModelModeRequest  = "request"
	ResponseModelModeUpstream = "upstream"
	ResponseModelModeCustom   = "custom"

	ResponseModelModeExtraKey   = "response_model_mode"
	ResponseModelCustomExtraKey = "response_model_custom"

	responseModelCustomMaxRunes    = 200
	responseModelTargetContextKey  = "response_model_target"
	responseModelRewriteContextKey = "response_model_rewrite"
)

// 保存分组/渠道映射前的客户模型。响应声明策略必须使用它，路由和上游审计
// 仍然独立使用映射后的模型，避免两种口径互相污染。
type clientRequestedResponseModelContextKey struct{}

// WithClientRequestedResponseModel 记录响应声明改写使用的客户原始模型。
// 它与已经映射给上游的转发模型分开保存，确保 request（跟随请求）语义稳定。
func WithClientRequestedResponseModel(ctx context.Context, model string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	// Composite（聚合）路由中间件会先把客户模型保存为 RequestedPublicModel，
	// 随后才把请求体中的 model 改写成实际发送给上游的模型。处理器可能从
	// 已改写的请求体再次调用本函数；此时必须优先使用路由保存的公开模型，
	// 否则 request（跟随请求）响应声明会偶发泄露上游模型。
	if requested, ok := RequestedPublicModelFromContext(ctx); ok {
		model = requested
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return ctx
	}
	// 普通请求没有 Composite（聚合）路由的 RequestedPublicModel 保护时，
	// 也必须只接受第一次非空模型。处理链后续可能再次从已映射的请求体
	// 调用本函数；若允许覆盖，request（跟随请求）模式就会把上游模型泄露
	// 给客户端。HTTP 请求上下文只代表一次请求，因此首次值就是可信的客户声明。
	if existing, ok := ctx.Value(clientRequestedResponseModelContextKey{}).(string); ok && strings.TrimSpace(existing) != "" {
		return ctx
	}
	return context.WithValue(ctx, clientRequestedResponseModelContextKey{}, model)
}

func clientRequestedResponseModel(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	// Composite（聚合）路由的公开模型是最早、最可信的客户声明。它可能在
	// 处理器保存模型之后才写入上下文，因此读取时也要优先于旧的私有值。
	if requested, ok := RequestedPublicModelFromContext(ctx); ok {
		return requested, true
	}
	model, ok := ctx.Value(clientRequestedResponseModelContextKey{}).(string)
	model = strings.TrimSpace(model)
	return model, ok && model != ""
}

func normalizeGroupResponseModelMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return ResponseModelModeRequest
	}
	return mode
}

func validGroupResponseModelMode(mode string) bool {
	switch mode {
	case ResponseModelModeRequest, ResponseModelModeUpstream, ResponseModelModeCustom:
		return true
	default:
		return false
	}
}

func normalizeAccountResponseModelPolicyExtra(extra map[string]any) (map[string]any, error) {
	if extra == nil {
		return nil, nil
	}
	rawMode, exists := extra[ResponseModelModeExtraKey]
	if !exists {
		return extra, nil
	}
	mode, ok := rawMode.(string)
	if !ok {
		return nil, fmt.Errorf("response_model_mode must be a string")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = ResponseModelModeInherit
	}
	switch mode {
	case ResponseModelModeInherit, ResponseModelModeRequest, ResponseModelModeUpstream, ResponseModelModeCustom:
	default:
		return nil, fmt.Errorf("response_model_mode must be inherit, request, upstream, or custom")
	}
	custom := ""
	if rawCustom, present := extra[ResponseModelCustomExtraKey]; present {
		var customOK bool
		custom, customOK = rawCustom.(string)
		if !customOK {
			return nil, fmt.Errorf("response_model_custom must be a string")
		}
		custom = strings.TrimSpace(custom)
	}
	if len([]rune(custom)) > responseModelCustomMaxRunes {
		return nil, fmt.Errorf("response_model_custom must not exceed %d characters", responseModelCustomMaxRunes)
	}
	if mode == ResponseModelModeCustom && custom == "" {
		return nil, fmt.Errorf("response_model_custom is required when response_model_mode is custom")
	}
	normalized := make(map[string]any, len(extra))
	for key, value := range extra {
		normalized[key] = value
	}
	normalized[ResponseModelModeExtraKey] = mode
	if custom == "" {
		delete(normalized, ResponseModelCustomExtraKey)
	} else {
		normalized[ResponseModelCustomExtraKey] = custom
	}
	return normalized, nil
}

func accountResponseModelPolicy(account *Account) (string, string) {
	if account == nil || account.Extra == nil {
		return ResponseModelModeInherit, ""
	}
	mode, _ := account.Extra[ResponseModelModeExtraKey].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case ResponseModelModeRequest, ResponseModelModeUpstream, ResponseModelModeCustom:
	default:
		mode = ResponseModelModeInherit
	}
	custom, _ := account.Extra[ResponseModelCustomExtraKey].(string)
	return mode, strings.TrimSpace(custom)
}

func groupFromResponseModelContext(ctx context.Context) *Group {
	if ctx == nil {
		return nil
	}
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	return group
}

type frozenClientResponseModelPolicyContextKey struct{}

type frozenClientResponseModelPolicy struct {
	target  string
	rewrite bool
}

// withFrozenClientResponseModelPolicy keeps the source group's public response
// declaration stable while a request is routed through a failover group. The
// target group and its account may change delivery, but must not silently
// change what the client sees in response.model.
func withFrozenClientResponseModelPolicy(ctx context.Context, group *Group) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	requestedModel, _ := clientRequestedResponseModel(ctx)
	policyCtx := context.WithValue(ctx, ctxkey.Group, group)
	target, rewrite := resolveClientResponseModel(policyCtx, nil, requestedModel)
	return context.WithValue(ctx, frozenClientResponseModelPolicyContextKey{}, frozenClientResponseModelPolicy{
		target: target, rewrite: rewrite,
	})
}

func frozenClientResponseModelPolicyFromContext(ctx context.Context) (frozenClientResponseModelPolicy, bool) {
	if ctx == nil {
		return frozenClientResponseModelPolicy{}, false
	}
	policy, ok := ctx.Value(frozenClientResponseModelPolicyContextKey{}).(frozenClientResponseModelPolicy)
	return policy, ok
}

// resolveClientResponseModel 只决定客户端看到的声明，不参与账号选择、模型映射、计费或上游审计。
func resolveClientResponseModel(ctx context.Context, account *Account, requestedModel string) (string, bool) {
	group := groupFromResponseModelContext(ctx)
	mode := ResponseModelModeRequest
	custom := ""
	if group != nil {
		mode = normalizeGroupResponseModelMode(group.ModelsListConfig.ResponseModelMode)
		custom = strings.TrimSpace(group.ModelsListConfig.ResponseModelCustom)
	}
	accountMode, accountCustom := accountResponseModelPolicy(account)
	if accountMode != ResponseModelModeInherit {
		mode = accountMode
		custom = accountCustom
	}
	switch mode {
	case ResponseModelModeUpstream:
		return "", false
	case ResponseModelModeCustom:
		if custom != "" {
			return custom, true
		}
		return strings.TrimSpace(requestedModel), strings.TrimSpace(requestedModel) != ""
	default:
		requestedModel = strings.TrimSpace(requestedModel)
		return requestedModel, requestedModel != ""
	}
}

func resolveClientResponseModelFromGin(c *gin.Context, account *Account, requestedModel string) (string, bool) {
	if c == nil || c.Request == nil {
		return strings.TrimSpace(requestedModel), strings.TrimSpace(requestedModel) != ""
	}
	return resolveClientResponseModel(c.Request.Context(), account, requestedModel)
}

func beginClientResponseModelPolicy(c *gin.Context, ctx context.Context, account *Account, requestedModel string) {
	if c == nil {
		return
	}
	if originalModel, ok := clientRequestedResponseModel(ctx); ok {
		requestedModel = originalModel
	}
	if policy, ok := frozenClientResponseModelPolicyFromContext(ctx); ok {
		c.Set(responseModelTargetContextKey, policy.target)
		c.Set(responseModelRewriteContextKey, policy.rewrite)
		return
	}
	target, rewrite := resolveClientResponseModel(ctx, account, requestedModel)
	c.Set(responseModelTargetContextKey, target)
	c.Set(responseModelRewriteContextKey, rewrite)
}

// rewriteResponseModelJSON 只改已经存在的声明字段，避免给没有 model 字段的协议事件凭空增加字段。
func rewriteResponseModelJSON(payload []byte, target string) []byte {
	if strings.TrimSpace(target) == "" || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	out := payload
	for _, path := range []string{"model", "response.model", "message.model", "session.model", "modelVersion", "response.modelVersion", "response.response.modelVersion", "session.modelVersion"} {
		if !gjson.GetBytes(out, path).Exists() {
			continue
		}
		updated, err := sjson.SetBytes(out, path, target)
		if err == nil {
			out = updated
		}
	}
	return out
}

func responseModelRewriteTarget(c *gin.Context, account *Account, requestedModel string) (string, bool) {
	if c != nil {
		target, targetOK := c.Get(responseModelTargetContextKey)
		rewrite, rewriteOK := c.Get(responseModelRewriteContextKey)
		if targetOK && rewriteOK {
			targetString, _ := target.(string)
			rewriteBool, _ := rewrite.(bool)
			return targetString, rewriteBool
		}
	}
	return resolveClientResponseModelFromGin(c, account, requestedModel)
}

// responseModelRewriteTargetForWebSocketTurn 按相同的策略来源解析单个 WebSocket
// turn。一个连接可以连续发送不同模型的 response.create，不能复用连接首帧缓存的
// request 模型，否则第二轮响应会错误声明为第一轮模型。
func responseModelRewriteTargetForWebSocketTurn(ctx context.Context, account *Account, requestedModel string) (string, bool) {
	return resolveClientResponseModel(ctx, account, requestedModel)
}

// clientResponseModelForSyntheticOutput 返回协议转换后新构造响应的模型声明。
// 这类响应没有上游原始 model 字段可保留；upstream（跟随上游）模式回退为实际
// 发送给上游的模型，request（跟随请求）/custom（自定义）模式则使用配置目标。
func clientResponseModelForSyntheticOutput(c *gin.Context, account *Account, requestedModel, upstreamModel string) string {
	target, rewrite := responseModelRewriteTarget(c, account, requestedModel)
	if rewrite && strings.TrimSpace(target) != "" {
		return target
	}
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		return upstreamModel
	}
	return strings.TrimSpace(requestedModel)
}

func rewriteResponseModelForClient(c *gin.Context, account *Account, requestedModel string, payload []byte) []byte {
	target, rewrite := responseModelRewriteTarget(c, account, requestedModel)
	if !rewrite {
		return payload
	}
	return rewriteResponseModelJSON(payload, target)
}

func responseModelSSEDataMayContainDeclaration(data string) bool {
	// 大多数流式事件只有 delta，没有模型声明。先按原始字节查找字段名，
	// 避免为每个 token 事件分配 []byte 并执行完整 JSON 解析。
	// 保留 Unicode 转义兜底，避免 `"\u006dodel"` 这类合法字段名被漏改。
	return strings.Contains(data, `"model"`) ||
		strings.Contains(data, `"modelVersion"`) ||
		strings.Contains(data, `\u`)
}

func rewriteResponseModelSSELineForClient(c *gin.Context, account *Account, requestedModel, line string) string {
	data, ok := extractOpenAISSEDataLine(line)
	if !ok {
		data, ok = extractAnthropicSSEDataLine(line)
	}
	if !ok || strings.TrimSpace(data) == "" || strings.TrimSpace(data) == "[DONE]" {
		return line
	}
	if !responseModelSSEDataMayContainDeclaration(data) {
		return line
	}
	target, rewrite := responseModelRewriteTarget(c, account, requestedModel)
	if !rewrite {
		return line
	}
	rewritten := rewriteResponseModelJSON([]byte(data), target)
	if string(rewritten) == data {
		return line
	}
	return "data: " + string(rewritten)
}

// rewriteResponseModelSSEPayload 改写协议转换器一次生成的完整 SSE 事件。
// Antigravity 的转换器会把 message_start 和后续事件合并成一个字节块，
// 不能只调用单行改写，否则客户仍可能看到内部映射模型。
func rewriteResponseModelSSEPayload(c *gin.Context, account *Account, requestedModel string, payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	raw := string(payload)
	var builder strings.Builder
	for _, segment := range strings.SplitAfter(raw, "\n") {
		line := strings.TrimSuffix(segment, "\n")
		line = strings.TrimSuffix(line, "\r")
		rewritten := rewriteResponseModelSSELineForClient(c, account, requestedModel, line)
		builder.WriteString(rewritten)
		if strings.HasSuffix(segment, "\n") {
			builder.WriteByte('\n')
		}
	}
	return []byte(builder.String())
}

func rewriteResponseModelSSEBodyForClient(c *gin.Context, account *Account, requestedModel, body string) string {
	lines := strings.Split(body, "\n")
	for index, line := range lines {
		lines[index] = rewriteResponseModelSSELineForClient(c, account, requestedModel, line)
	}
	return strings.Join(lines, "\n")
}
