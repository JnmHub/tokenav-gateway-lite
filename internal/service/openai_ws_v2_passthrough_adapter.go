package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var errOpenAIWSPriorityServiceTierUnsupported = errors.New("upstream websocket does not support service_tier")

type openAIWSClientFrameConn struct {
	conn                 *coderws.Conn
	restoreResponseModel func([]byte) []byte
	// restoreToolNames 回程还原工具名（出站归一化的逆操作）。passthrough 直通
	// 模式没有逐事件语义解析，这个回调是客户端方向的唯一改写点。
	restoreToolNames   func([]byte) []byte
	customErrorMessage string
}

// rewriteOpenAIWSPassthroughResponseModel 处理所有合法 JSON 响应帧，只改已经
// 存在的模型声明字段。不能依赖事件类型白名单：session.created、未来新增事件
// 以及带模型字段的自定义事件同样不能把上游模型泄露给客户端。上游审计在
// BeforeWriteClient 中先读取原始帧，因此这里的改写不会污染实际返回模型记录；
// 非 JSON 二进制（如音频）不会进入此函数。
func rewriteOpenAIWSPassthroughResponseModel(ctx context.Context, account *Account, requestModel string, payload []byte) []byte {
	if !gjson.ValidBytes(payload) {
		return payload
	}
	target, rewrite := responseModelRewriteTargetForWebSocketTurn(ctx, account, requestModel)
	if !rewrite {
		return payload
	}
	return rewriteResponseModelJSON(payload, target)
}

// openAIWSPolicyEnforcingFrameConn wraps a client-side FrameConn and runs
// every client→upstream frame through the OpenAI Fast Policy. It is the
// passthrough-relay equivalent of the parseClientPayload integration in the
// ingress session path. filter returns:
//   - newPayload, nil, nil: forward the (possibly mutated) payload
//   - _, *OpenAIFastBlockedError, nil: block — the wrapper sends an error
//     event via onBlock and surfaces a transport-level error so the relay
//     stops reading from the client.
//   - _, _, err: a transport error other than block.
type openAIWSPolicyEnforcingFrameConn struct {
	inner   openaiwsv2.FrameConn
	filter  func(msgType coderws.MessageType, payload []byte) ([]byte, *OpenAIFastBlockedError, error)
	onBlock func(blocked *OpenAIFastBlockedError)
}

var _ openaiwsv2.FrameConn = (*openAIWSPolicyEnforcingFrameConn)(nil)

func (c *openAIWSPolicyEnforcingFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if c == nil || c.inner == nil {
		return coderws.MessageText, nil, errOpenAIWSConnClosed
	}
	msgType, payload, err := c.inner.ReadFrame(ctx)
	if err != nil {
		return msgType, payload, err
	}
	if c.filter == nil {
		return msgType, payload, nil
	}
	updated, blocked, filterErr := c.filter(msgType, payload)
	if filterErr != nil {
		return msgType, payload, filterErr
	}
	if blocked != nil {
		if c.onBlock != nil {
			c.onBlock(blocked)
		}
		return msgType, nil, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, blocked.Message, blocked)
	}
	return msgType, updated, nil
}

func (c *openAIWSPolicyEnforcingFrameConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	if c == nil || c.inner == nil {
		return errOpenAIWSConnClosed
	}
	return c.inner.WriteFrame(ctx, msgType, payload)
}

func (c *openAIWSPolicyEnforcingFrameConn) Close() error {
	if c == nil || c.inner == nil {
		return nil
	}
	return c.inner.Close()
}

// openAIWSPassthroughPolicyModelForFrame returns the upstream-perspective
// model name that should be passed to evaluateOpenAIFastPolicy for a single
// passthrough WS frame. Mirrors the HTTP-side normalization
// (account mapping + normalizeOpenAIModelForUpstream) so the WS path matches
// model whitelists identically.
func openAIWSPassthroughPolicyModelForFrame(account *Account, payload []byte) string {
	return openAIWSPassthroughPolicyModelForRequest(nil, account, payload)
}

// openAIWSPassthroughPolicyModelForRequest applies the mapping selected by the
// final request group. The nil-context wrapper above intentionally preserves
// existing unit-test and legacy adapter behavior.
func openAIWSPassthroughPolicyModelForRequest(ctx context.Context, account *Account, payload []byte) string {
	if account == nil || len(payload) == 0 {
		return ""
	}
	original := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
	if original == "" {
		return ""
	}
	return normalizeOpenAIModelForUpstream(account, MappedAccountModelForRequest(ctx, nil, account, original))
}

// openAIWSPassthroughPolicyModelFromSessionFrame returns the upstream model
// derived from a session.update frame's session.model field. Returns "" when
// the frame is not a session.update event or carries no session.model. Used
// by the per-frame policy filter (client→upstream direction) to keep
// capturedSessionModel in sync with the session-level model the client may
// rotate mid-session.
//
// Realtime / Responses WS lets the client change the session model after
// the WS handshake via:
//
//	{"type":"session.update","session":{"model":"gpt-5.5", ...}}
//
// If we only capture the model from the very first frame, a client can ship
// gpt-4o on the first response.create (whitelisted as pass), then
// session.update to gpt-5.5, then send response.create without "model" so
// the per-frame resolver returns "" and the stale capturedSessionModel falls
// back to gpt-4o — defeating the gpt-5.5 fast-policy filter.
func openAIWSPassthroughPolicyModelFromSessionFrame(account *Account, payload []byte) string {
	return openAIWSPassthroughPolicyModelFromSessionFrameForRequest(nil, account, payload)
}

func openAIWSPassthroughPolicyModelFromSessionFrameForRequest(ctx context.Context, account *Account, payload []byte) string {
	if account == nil || len(payload) == 0 {
		return ""
	}
	frameType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	if frameType != "session.update" {
		return ""
	}
	original := strings.TrimSpace(gjson.GetBytes(payload, "session.model").String())
	if original == "" {
		return ""
	}
	return normalizeOpenAIModelForUpstream(account, MappedAccountModelForRequest(ctx, nil, account, original))
}

type openAIWSPassthroughUsageMeta struct {
	requestedServiceTier     atomic.Pointer[string]
	serviceTier              atomic.Pointer[string]
	priorityPolicyAction     atomic.Pointer[string]
	priorityPolicyRuleID     atomic.Pointer[string]
	priorityPolicyFallback   atomic.Bool
	detectionByRequest       atomic.Bool
	detectionByResponse      atomic.Bool
	requestedReasoningEffort atomic.Pointer[string]
	reasoningEffort          atomic.Pointer[string]
	requestModel             atomic.Pointer[string]
	upstreamModel            atomic.Pointer[string]

	// 仅在 client->upstream filter goroutine 中读写；Load 侧通过上方原子指针同步。
	sessionRequestModel string
}

func newOpenAIWSPassthroughUsageMeta(initialRequestModel string, firstFrame []byte) *openAIWSPassthroughUsageMeta {
	meta := &openAIWSPassthroughUsageMeta{
		sessionRequestModel: strings.TrimSpace(initialRequestModel),
	}
	if meta.sessionRequestModel == "" {
		meta.sessionRequestModel = openAIWSPassthroughRequestModelForFrame(firstFrame)
	}
	return meta
}

func (m *openAIWSPassthroughUsageMeta) initFromFirstFrame(policyOutput []byte, mappedModel string, decisions ...OpenAIPriorityDecision) {
	if m == nil {
		return
	}
	decision := OpenAIPriorityDecision{
		RequestedServiceTier:       extractOpenAIServiceTierFromBody(policyOutput),
		ServiceTier:                extractOpenAIServiceTierFromBody(policyOutput),
		PolicyAction:               BetaPolicyActionPass,
		DetectionByRequestEnabled:  true,
		DetectionByResponseEnabled: true,
	}
	if len(decisions) > 0 {
		decision = decisions[0]
	}
	m.requestedServiceTier.Store(decision.RequestedServiceTier)
	m.serviceTier.Store(decision.ServiceTier)
	action := decision.PolicyAction
	m.priorityPolicyAction.Store(&action)
	ruleID := decision.PolicyRuleID
	m.priorityPolicyRuleID.Store(&ruleID)
	m.priorityPolicyFallback.Store(decision.PolicyRuleFallback)
	m.detectionByRequest.Store(decision.DetectionByRequestEnabled)
	m.detectionByResponse.Store(decision.DetectionByResponseEnabled)
	m.reasoningEffort.Store(extractReasoningEffortAuditFromBody(policyOutput, "reasoning.effort", "reasoning_effort"))
	m.storeTurnModels(m.sessionRequestModel, policyOutput)
}

func (m *openAIWSPassthroughUsageMeta) updateSessionRequestModel(payload []byte) {
	if m == nil {
		return
	}
	if model := openAIWSPassthroughRequestModelFromSessionFrame(payload); model != "" {
		m.sessionRequestModel = model
	}
}

func (m *openAIWSPassthroughUsageMeta) requestModelForFrame(payload []byte) string {
	if m == nil {
		return openAIWSPassthroughRequestModelForFrame(payload)
	}
	if model := openAIWSPassthroughRequestModelForFrame(payload); model != "" {
		return model
	}
	return m.sessionRequestModel
}

func (m *openAIWSPassthroughUsageMeta) updateFromResponseCreate(policyOutput []byte, mappedModel string, requestModelForFrame string, decisions ...OpenAIPriorityDecision) {
	if m == nil {
		return
	}
	decision := OpenAIPriorityDecision{
		RequestedServiceTier:       extractOpenAIServiceTierFromBody(policyOutput),
		ServiceTier:                extractOpenAIServiceTierFromBody(policyOutput),
		PolicyAction:               BetaPolicyActionPass,
		DetectionByRequestEnabled:  true,
		DetectionByResponseEnabled: true,
	}
	if len(decisions) > 0 {
		decision = decisions[0]
	}
	m.requestedServiceTier.Store(decision.RequestedServiceTier)
	m.serviceTier.Store(decision.ServiceTier)
	action := decision.PolicyAction
	m.priorityPolicyAction.Store(&action)
	ruleID := decision.PolicyRuleID
	m.priorityPolicyRuleID.Store(&ruleID)
	m.priorityPolicyFallback.Store(decision.PolicyRuleFallback)
	m.detectionByRequest.Store(decision.DetectionByRequestEnabled)
	m.detectionByResponse.Store(decision.DetectionByResponseEnabled)
	m.reasoningEffort.Store(extractReasoningEffortAuditFromBody(policyOutput, "reasoning.effort", "reasoning_effort"))
	m.storeTurnModels(requestModelForFrame, policyOutput)
}

func (m *openAIWSPassthroughUsageMeta) storeTurnModels(requestModel string, upstreamPayload []byte) {
	if m == nil {
		return
	}
	requestModel = strings.TrimSpace(requestModel)
	upstreamModel := strings.TrimSpace(gjson.GetBytes(upstreamPayload, "model").String())
	if upstreamModel == "" {
		upstreamModel = requestModel
	}
	m.requestModel.Store(openAIWSTrimmedStringPtr(requestModel))
	m.upstreamModel.Store(openAIWSTrimmedStringPtr(upstreamModel))
}

func (m *openAIWSPassthroughUsageMeta) turnModels(fallback string) (string, string) {
	requestModel := strings.TrimSpace(fallback)
	upstreamModel := requestModel
	if m == nil {
		return requestModel, upstreamModel
	}
	if current := m.requestModel.Load(); current != nil && strings.TrimSpace(*current) != "" {
		requestModel = strings.TrimSpace(*current)
	}
	if current := m.upstreamModel.Load(); current != nil && strings.TrimSpace(*current) != "" {
		upstreamModel = strings.TrimSpace(*current)
	}
	return requestModel, upstreamModel
}

func openAIWSTrimmedStringPtr(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func openAIWSDifferentModel(requestModel, upstreamModel string) string {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" || upstreamModel == strings.TrimSpace(requestModel) {
		return ""
	}
	return upstreamModel
}

// openAIWSFinalUpstreamModel returns the model that was actually sent on the
// wire. Unlike openAIWSDifferentModel, it deliberately keeps equal models: the
// account-cost pipeline needs a concrete model even when no mapping occurred.
func openAIWSFinalUpstreamModel(requestModel, upstreamModel string) string {
	return firstNonEmpty(upstreamModel, requestModel)
}

func (m *openAIWSPassthroughUsageMeta) priorityDecisionValue() OpenAIPriorityDecision {
	decision := OpenAIPriorityDecision{
		PolicyAction:               BetaPolicyActionPass,
		DetectionByRequestEnabled:  true,
		DetectionByResponseEnabled: true,
	}
	if m == nil {
		return decision
	}
	decision.RequestedServiceTier = m.requestedServiceTier.Load()
	decision.ServiceTier = m.serviceTier.Load()
	if value := m.priorityPolicyAction.Load(); value != nil {
		decision.PolicyAction = *value
	}
	if value := m.priorityPolicyRuleID.Load(); value != nil {
		decision.PolicyRuleID = *value
	}
	decision.PolicyRuleFallback = m.priorityPolicyFallback.Load()
	decision.DetectionByRequestEnabled = m.detectionByRequest.Load()
	decision.DetectionByResponseEnabled = m.detectionByResponse.Load()
	return decision
}

func openAIWSPassthroughRequestModelForFrame(payload []byte) string {
	if len(payload) == 0 || strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "response.create" {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(payload, "model").String())
}

func openAIWSPassthroughRequestModelFromSessionFrame(payload []byte) string {
	if len(payload) == 0 || strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "session.update" {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(payload, "session.model").String())
}

const openaiWSV2PassthroughModeFields = "ws_mode=passthrough ws_router=v2"

func validateOpenAIWSPassthroughClientFrame(msgType coderws.MessageType, payload []byte) error {
	if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
		return NewOpenAIWSClientCloseError(coderws.StatusUnsupportedData, "unsupported websocket message type", nil)
	}
	if msgType == coderws.MessageBinary && !gjson.ValidBytes(payload) {
		// Media-oriented clients may send raw audio or other opaque binary frames.
		// Passthrough mode must preserve those bytes instead of treating them as a
		// malformed Responses event.
		return nil
	}
	eventType, err := validateOpenAIWSJSONEvent(payload)
	if err != nil {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", err)
	}
	switch eventType {
	case "response.create",
		"response.cancel",
		"session.update",
		"input_audio_buffer.append",
		"input_audio_buffer.commit",
		"input_audio_buffer.clear",
		"conversation.item.create",
		"conversation.item.retrieve",
		"conversation.item.truncate",
		"conversation.item.delete",
		"output_audio_buffer.clear":
		return nil
	default:
		return NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			fmt.Sprintf("unsupported websocket event type: %s", eventType),
			nil,
		)
	}
}

func validateOpenAIWSStrictClientFrame(msgType coderws.MessageType, payload []byte, strict bool) error {
	if !strict {
		return validateOpenAIWSPassthroughClientFrame(msgType, payload)
	}
	if msgType != coderws.MessageText || !gjson.ValidBytes(payload) {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "严格审查或资金模式不支持二进制及未验证帧", nil)
	}
	eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	if eventType == "response.cancel" {
		// Only cancellation is an input-free application control. Extra fields
		// cannot smuggle session state into a nominally harmless control frame.
		valid := true
		gjson.ParseBytes(payload).ForEach(func(key, value gjson.Result) bool {
			if key.String() != "type" && key.String() != "event_id" && key.String() != "response_id" || value.Type != gjson.String || len(value.String()) > 512 {
				valid = false
			}
			return valid
		})
		if valid {
			return nil
		}
	}
	if eventType == "response.create" {
		input := gjson.GetBytes(payload, "input")
		if !input.Exists() || input.Type != gjson.String && !input.IsArray() || gjson.GetBytes(payload, "conversation").Exists() || gjson.GetBytes(payload, "session").Exists() {
			return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "严格模式要求每轮提交完整输入，不支持服务器会话历史", nil)
		}
		var shape any
		if err := json.Unmarshal(payload, &shape); err != nil {
			return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", err)
		}
		if err := validateFundingTextShape(shape); err != nil {
			return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "严格模式不支持无法完整审查的历史或媒体输入", err)
		}
		return nil
	}
	return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "严格模式只支持完整 response.create 和 response.cancel", nil)
}

func openAIWSStrictClientFrames(hooks *OpenAIWSIngressHooks) (bool, error) {
	if hooks == nil || hooks.StrictClientFrames == nil {
		return false, nil
	}
	strict, err := hooks.StrictClientFrames()
	if err != nil {
		return true, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "长连接审查配置暂不可用", err)
	}
	return strict, nil
}

func validateOpenAIWSPassthroughUpstreamFrame(msgType coderws.MessageType, payload []byte, completedResponseIDs map[string]struct{}) error {
	if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
		return newOpenAIWSUpstreamProtocolError("unsupported_message_type", errors.New("upstream websocket returned an unsupported message type"))
	}
	if msgType == coderws.MessageBinary && !gjson.ValidBytes(payload) {
		// Opaque binary output (for example audio) is transport data, not a
		// Responses protocol event. It remains byte-for-byte passthrough.
		return nil
	}
	eventType, err := validateOpenAIWSJSONEvent(payload)
	if err != nil {
		return newOpenAIWSUpstreamProtocolError("invalid_event_json", fmt.Errorf("upstream websocket returned an invalid Responses event: %w", err))
	}
	responseIDs := gjson.GetManyBytes(payload, "response.id", "response_id", "id")
	responseID := strings.TrimSpace(responseIDs[0].String())
	if responseID == "" {
		responseID = strings.TrimSpace(responseIDs[1].String())
	}
	if responseID == "" && isOpenAIWSTerminalEvent(eventType) {
		// 仅 terminal 事件读取顶层 id，避免把普通 event id 当成 response id。
		responseID = strings.TrimSpace(responseIDs[2].String())
	}
	if responseID != "" {
		if _, completed := completedResponseIDs[responseID]; completed {
			return newOpenAIWSUpstreamProtocolError(
				"event_after_terminal",
				fmt.Errorf("upstream websocket sent event %s after terminal response %s", eventType, responseID),
			)
		}
		if isOpenAIWSTerminalEvent(eventType) {
			completedResponseIDs[responseID] = struct{}{}
		}
	}
	return nil
}

var _ openaiwsv2.FrameConn = (*openAIWSClientFrameConn)(nil)

func (c *openAIWSClientFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if c == nil || c.conn == nil {
		return coderws.MessageText, nil, errOpenAIWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.conn.Read(ctx)
}

func (c *openAIWSClientFrameConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	if c == nil || c.conn == nil {
		return errOpenAIWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if (msgType == coderws.MessageText || msgType == coderws.MessageBinary) && gjson.ValidBytes(payload) {
		if normalized, changed := normalizeCompletedImageGenerationStatus(payload); changed {
			payload = normalized
		}
		if c.restoreResponseModel != nil {
			payload = c.restoreResponseModel(payload)
		}
		if c.restoreToolNames != nil {
			payload = c.restoreToolNames(payload)
		}
		if rewritten, changed := RewriteGatewayClientErrorJSON(payload, c.customErrorMessage); changed {
			payload = rewritten
		}
	}
	return c.conn.Write(ctx, msgType, payload)
}

func (c *openAIWSClientFrameConn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	_ = c.conn.Close(coderws.StatusNormalClosure, "")
	_ = c.conn.CloseNow()
	return nil
}

func (s *OpenAIGatewayService) proxyResponsesWebSocketV2Passthrough(
	ctx context.Context,
	c *gin.Context,
	clientConn *coderws.Conn,
	account *Account,
	token string,
	firstClientMessage []byte,
	firstMessageType coderws.MessageType,
	hooks *OpenAIWSIngressHooks,
	wsDecision OpenAIWSProtocolDecision,
) error {
	if s == nil {
		return errors.New("service is nil")
	}
	if clientConn == nil {
		return errors.New("client websocket is nil")
	}
	if account == nil {
		return errors.New("account is nil")
	}
	if err := validateOpenAIWSBearerToken(account, token); err != nil {
		return err
	}
	strictFrames, strictErr := openAIWSStrictClientFrames(hooks)
	if strictErr != nil {
		return strictErr
	}
	if err := validateOpenAIWSStrictClientFrame(firstMessageType, firstClientMessage, strictFrames); err != nil {
		return err
	}
	firstRequestedMessage := append([]byte(nil), firstClientMessage...)
	if account.IsOpenAI() && isOpenAIResponsesLiteWebSocketPayload(firstClientMessage) {
		liteFirstMessage, _, liteErr := normalizeOpenAIResponsesLitePayloadForAccount(firstClientMessage, account)
		if liteErr != nil {
			return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, liteErr.Error(), liteErr)
		}
		firstClientMessage = liteFirstMessage
	}
	if hooks != nil && (hooks.MaxReasoningEffort != "" || len(hooks.ReasoningEffortMappings) > 0) {
		if policyPayload, changed := ApplyOpenAIReasoningEffortPolicy(firstClientMessage, hooks.MaxReasoningEffort, hooks.ReasoningEffortMappings); changed {
			firstClientMessage = policyPayload
		}
	}
	requestModel := strings.TrimSpace(gjson.GetBytes(firstClientMessage, "model").String())
	requestPreviousResponseID := strings.TrimSpace(gjson.GetBytes(firstClientMessage, "previous_response_id").String())
	logOpenAIWSV2Passthrough(
		"relay_start account_id=%d model=%s previous_response_id=%s first_message_type=%s first_message_bytes=%d",
		account.ID,
		truncateOpenAIWSLogValue(requestModel, openAIWSLogValueMaxLen),
		truncateOpenAIWSLogValue(requestPreviousResponseID, openAIWSIDValueMaxLen),
		openaiwsv2RelayMessageTypeName(firstMessageType),
		len(firstClientMessage),
	)

	// Apply OpenAI Fast Policy on the first response.create frame. Subsequent
	// frames are filtered via a wrapping FrameConn below so every client→
	// upstream frame goes through the same policy evaluator/normalize/scope as
	// HTTP entrypoints.
	//
	// We capture the session-level model from the first frame here so the
	// per-frame filter (below) can fall back to it when a follow-up frame
	// omits "model" — Realtime clients are allowed to send response.create
	// without re-stating the model, in which case the upstream uses the model
	// negotiated at session.update time. Without this fallback, an empty
	// model would miss any admin-configured model whitelist and be silently
	// passed through, defeating that policy on every frame after the first.
	initialRequestModel := ""
	if hooks != nil {
		initialRequestModel = strings.TrimSpace(hooks.InitialRequestModel)
	}
	if initialRequestModel == "" {
		initialRequestModel = openAIWSPassthroughRequestModelForFrame(firstClientMessage)
	}
	if hooks != nil && hooks.MapRequestModel != nil {
		mappedModel, mapErr := hooks.MapRequestModel(1, initialRequestModel)
		if mapErr != nil {
			return mapErr
		}
		if mappedModel = strings.TrimSpace(mappedModel); mappedModel != "" {
			firstClientMessage = s.ReplaceModelInBody(firstClientMessage, mappedModel)
		}
	}
	capturedSessionModel := openAIWSPassthroughPolicyModelForRequest(ctx, account, firstClientMessage)
	if capturedSessionModel != "" && capturedSessionModel != strings.TrimSpace(gjson.GetBytes(firstClientMessage, "model").String()) {
		firstClientMessage = s.ReplaceModelInBody(firstClientMessage, capturedSessionModel)
	}
	usageMeta := newOpenAIWSPassthroughUsageMeta(initialRequestModel, firstClientMessage)
	updatedFirst, firstPriorityDecision, blocked, policyErr := s.applyOpenAIPriorityPolicyToWSResponseCreate(ctx, account, capturedSessionModel, firstClientMessage)
	if policyErr != nil {
		return fmt.Errorf("apply openai fast policy on first ws frame: %w", policyErr)
	}
	if blocked != nil {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
		// coder/websocket@v1.8.14 Conn.Write is synchronous: it acquires
		// writeFrameMu, writes the entire frame, and Flushes the underlying
		// bufio writer before returning (write.go:42 → write.go:307-311).
		// The subsequent close handshake re-acquires the same writeFrameMu
		// to send the close frame, so the error event is guaranteed to
		// reach the kernel send buffer before any close frame is queued.
		// No explicit flush hop is required here.
		eventBytes := buildOpenAIFastPolicyBlockedWSEvent(blocked)
		if eventBytes != nil {
			writeCtx, cancelWrite := context.WithTimeout(ctx, s.openAIWSWriteTimeout())
			_ = clientConn.Write(writeCtx, coderws.MessageText, eventBytes)
			cancelWrite()
		}
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, blocked.Message, blocked)
	}
	firstClientMessage = updatedFirst
	fingerprintIDs := resolveCodexFingerprintIDsFromRequest(account, c.Request.Header)
	fingerprintFirstMessage, _, fingerprintErr := applyCodexFingerprintClientMetadataBytes(firstClientMessage, fingerprintIDs)
	if fingerprintErr != nil {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket client metadata", fingerprintErr)
	}
	firstClientMessage = fingerprintFirstMessage
	// 工具名归一化：passthrough 首帧（session.update 声明工具 / response.create）
	// 同样要改成上游合法名，否则上游直接 400。首帧自己写上游，不走 filter，
	// 所以这里单独处理一次；后续帧由 policyClientConn 的 filter 统一处理。
	if normalizedFirst, _, firstChanged, firstNameErr := normalizeOpenAIWSToolNamesFrame(c, firstClientMessage); firstNameErr != nil {
		logOpenAIWSV2Passthrough("tool_name_normalize_failed account_id=%d stage=first_frame err=%v", account.ID, firstNameErr)
	} else if firstChanged {
		firstClientMessage = normalizedFirst
	}

	// 在 policy filter 之后再提取 service_tier / reasoning_effort 用于
	// usage 上报：filter
	// 命中时 service_tier 已经从 firstClientMessage 中删除，billing 应当
	// 反映上游实际处理的 tier（nil = default），而不是用户最初请求的
	// "priority"。HTTP 入口（line ~2728 extractOpenAIServiceTier(reqBody)）
	// 与 WS ingress（openai_ws_forwarder.go:2991 取自 payload）的语义一致。
	//
	// 多轮 passthrough：OpenAI Realtime / Responses WS 协议允许客户端在
	// 同一连接的不同 response.create 帧上发送不同 service_tier（参考
	// codex-rs/core/src/client.rs build_responses_request 每次重新填值）。
	// 因此使用 atomic.Pointer[string] 在 filter（runClientToUpstream
	// goroutine）和 OnTurnComplete / final result（runUpstreamToClient
	// goroutine）之间同步当前 turn 的 usage metadata。
	usageMeta.requestedReasoningEffort.Store(extractReasoningEffortAuditFromBody(firstRequestedMessage, "reasoning.effort", "reasoning_effort"))
	usageMeta.initFromFirstFrame(firstClientMessage, capturedSessionModel, firstPriorityDecision)
	promptCacheKey := strings.TrimSpace(gjson.GetBytes(firstClientMessage, "prompt_cache_key").String())

	wsURL, err := s.buildOpenAIResponsesWSURL(account)
	if err != nil {
		return fmt.Errorf("build ws url: %w", err)
	}
	wsHost := "-"
	wsPath := "-"
	if parsedURL, parseErr := url.Parse(wsURL); parseErr == nil && parsedURL != nil {
		wsHost = normalizeOpenAIWSLogValue(parsedURL.Host)
		wsPath = normalizeOpenAIWSLogValue(parsedURL.Path)
	}
	logOpenAIWSV2Passthrough(
		"relay_dial_start account_id=%d ws_host=%s ws_path=%s proxy_enabled=%v",
		account.ID,
		wsHost,
		wsPath,
		account.ProxyID != nil && account.Proxy != nil,
	)

	isCodexCLI := false
	if c != nil {
		isCodexCLI = openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator"))
	}
	if s.cfg != nil && s.cfg.Gateway.ForceCodexCLI {
		isCodexCLI = true
	}
	turnState := ""
	turnMetadata := ""
	if c != nil {
		turnState = strings.TrimSpace(c.GetHeader(openAIWSTurnStateHeader))
		turnMetadata = strings.TrimSpace(c.GetHeader(openAIWSTurnMetadataHeader))
	}
	headers, _, buildHdrErr := s.buildOpenAIWSHeaders(
		ctx,
		c,
		account,
		token,
		wsDecision,
		isCodexCLI,
		turnState,
		turnMetadata,
		promptCacheKey,
		gjson.GetBytes(firstClientMessage, "model").String(),
		gjson.GetBytes(firstClientMessage, "service_tier").String(),
		fingerprintIDs,
	)
	if buildHdrErr != nil {
		return fmt.Errorf("build ws headers: %w", buildHdrErr)
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	dialer := s.getOpenAIWSPassthroughDialer()
	if dialer == nil {
		return errors.New("openai ws passthrough dialer is nil")
	}

	var handshakeHeaders http.Header
	agentTaskRecoveryTried := false
	var dialUpstream func() (openaiwsv2.FrameConn, error)
	dialUpstream = func() (openaiwsv2.FrameConn, error) {
		freshHeaders, authErr := s.refreshOpenAIAgentIdentityHeaders(ctx, account, headers)
		if authErr != nil {
			return nil, fmt.Errorf("build Agent Identity websocket authentication: %w", authErr)
		}
		dialCtx, cancelDial := context.WithTimeout(ctx, s.openAIWSDialTimeout())
		defer cancelDial()
		upstreamConn, statusCode, responseHeaders, dialErr := dialer.Dial(dialCtx, wsURL, freshHeaders, proxyURL)
		handshakeHeaders = cloneHeader(responseHeaders)
		if dialErr != nil {
			var handshakeErr *openAIWSHandshakeError
			if account.IsOpenAIAgentIdentity() && !agentTaskRecoveryTried && errors.As(dialErr, &handshakeErr) && handshakeErr != nil && isAgentIdentityTaskInvalidHTTPResponse(statusCode, handshakeErr.Body) {
				agentTaskRecoveryTried = true
				if recoveryErr := s.recoverAgentIdentityTask(ctx, account, strings.TrimSpace(account.GetCredential("task_id"))); recoveryErr != nil {
					return nil, fmt.Errorf("agent identity task recovery failed: %w", recoveryErr)
				}
				return dialUpstream()
			}
			logOpenAIWSV2Passthrough(
				"relay_dial_failed account_id=%d status_code=%d err=%s",
				account.ID,
				statusCode,
				truncateOpenAIWSLogValue(dialErr.Error(), openAIWSLogValueMaxLen),
			)
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				ProxyHost:          safeProxyHost(proxyURL),
				UpstreamStatusCode: statusCode,
				UpstreamRequestID:  openAIWSHeaderValueForLog(handshakeHeaders, "x-request-id"),
				UpstreamURL:        safeUpstreamURL(wsURL),
				Kind:               "ws_dial_error",
				Message:            dialErr.Error(),
			})
			if statusCode == http.StatusTooManyRequests {
				s.persistOpenAIWSRateLimitSignal(ctx, account, handshakeHeaders, nil, "rate_limit_exceeded", "rate_limit_error", strings.TrimSpace(dialErr.Error()))
				return nil, &UpstreamFailoverError{
					StatusCode:      http.StatusTooManyRequests,
					ResponseHeaders: cloneHeader(handshakeHeaders),
				}
			}
			return nil, s.mapOpenAIWSPassthroughDialError(dialErr, statusCode, handshakeHeaders)
		}
		logOpenAIWSV2Passthrough(
			"relay_dial_ok account_id=%d status_code=%d upstream_request_id=%s",
			account.ID,
			statusCode,
			openAIWSHeaderValueForLog(handshakeHeaders, "x-request-id"),
		)
		upstreamFrameConn, ok := upstreamConn.(openaiwsv2.FrameConn)
		if !ok {
			_ = upstreamConn.Close()
			return nil, errors.New("openai ws passthrough upstream connection does not support frame relay")
		}
		return upstreamFrameConn, nil
	}

	upstreamFrameConn, err := dialUpstream()
	if err != nil {
		return err
	}

	completedTurns := atomic.Int32{}
	// Responses WebSocket 同一连接只允许一个 response.create 在途。handler
	// 的槽位保存结构是连接级的；若允许客户端在上一轮 terminal 事件前
	// 连发 response.create，后一个 BeforeTurn 会释放前一轮槽位，造成多个
	// 上游请求只占一个并发槽。首帧已经写入上游，因此初始值为 true。
	turnInFlight := atomic.Bool{}
	turnInFlight.Store(true)
	clientFrameConn := &openAIWSClientFrameConn{
		conn:               clientConn,
		customErrorMessage: GatewayCustomErrorMessageFromContext(ctx),
		restoreResponseModel: func(payload []byte) []byte {
			requestModel, _ := usageMeta.turnModels("")
			return rewriteOpenAIWSPassthroughResponseModel(ctx, account, requestModel, payload)
		},
		restoreToolNames: func(payload []byte) []byte {
			return restoreCodexToolNamesFromContext(c, payload)
		},
	}
	policyClientConn := &openAIWSPolicyEnforcingFrameConn{
		inner: clientFrameConn,
		// 注意线程安全：filter 仅在 runClientToUpstream 这一条
		// goroutine 中被调用（passthrough_relay.go: ReadFrame loop），
		// capturedSessionModel 的读写都发生在该 goroutine 内，因此无需
		// 加锁/原子化。
		filter: func(msgType coderws.MessageType, payload []byte) ([]byte, *OpenAIFastBlockedError, error) {
			strictFrames, strictErr := openAIWSStrictClientFrames(hooks)
			if strictErr != nil {
				return nil, nil, strictErr
			}
			if validateErr := validateOpenAIWSStrictClientFrame(msgType, payload, strictFrames); validateErr != nil {
				// Observe the same model resolution as normal admission, without
				// updating session/turn state for a frame already rejected.
				originalModel := usageMeta.requestModelForFrame(payload)
				if originalModel == "" {
					originalModel = capturedSessionModel
				}
				notifyOpenAIWSStrictRejection(hooks, msgType, payload, strictFrames, originalModel)
				return nil, nil, validateErr
			}
			if msgType == coderws.MessageBinary && !gjson.ValidBytes(payload) {
				return payload, nil, nil
			}
			if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.create" &&
				account.IsOpenAI() && isOpenAIResponsesLiteWebSocketPayload(payload) {
				litePayload, _, liteErr := normalizeOpenAIResponsesLitePayloadForAccount(payload, account)
				if liteErr != nil {
					return payload, nil, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, liteErr.Error(), liteErr)
				}
				payload = litePayload
			}
			requestedPayload := append([]byte(nil), payload...)
			if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.create" && hooks != nil && (hooks.MaxReasoningEffort != "" || len(hooks.ReasoningEffortMappings) > 0) {
				if policyPayload, changed := ApplyOpenAIReasoningEffortPolicy(payload, hooks.MaxReasoningEffort, hooks.ReasoningEffortMappings); changed {
					payload = policyPayload
				}
			}
			isResponseCreate := strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.create"
			if isResponseCreate && turnInFlight.Load() {
				return payload, nil, NewOpenAIWSClientCloseError(
					coderws.StatusPolicyViolation,
					"overlapping response.create is not allowed; wait for the previous response to finish",
					nil,
				)
			}
			turnNo := int(completedTurns.Load()) + 1
			if turnNo < 2 {
				turnNo = 2
			}
			requestModelForThisFrame := ""
			if isResponseCreate {
				requestModelForThisFrame = usageMeta.requestModelForFrame(payload)
				if requestModelForThisFrame == "" {
					requestModelForThisFrame = capturedSessionModel
				}
				if hooks != nil && hooks.BeforeRequest != nil {
					preparedPayload, err := hooks.BeforeRequest(turnNo, payload, requestModelForThisFrame)
					if err != nil {
						return payload, nil, err
					}
					if preparedPayload != nil {
						payload = preparedPayload
					}
				}
				if hooks != nil && hooks.MapRequestModel != nil {
					upstreamModel, err := hooks.MapRequestModel(turnNo, requestModelForThisFrame)
					if err != nil {
						return payload, nil, err
					}
					if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
						payload = s.ReplaceModelInBody(payload, upstreamModel)
					}
				}
			}
			// 在评估策略前先刷新 capturedSessionModel：客户端可能通过
			// session.update 修改 session-level model（Realtime /
			// Responses WS 协议允许），如果不刷新就会出现
			// "首帧 model=gpt-4o（pass）→ session.update 改成 gpt-5.5
			// → 不带 model 的 response.create fallback 到 gpt-4o" 的
			// 绕过路径。这里只看 session.update 事件中的 session.model
			// 字段，response.create 自己的 model 仍然由其本帧字段决定。
			if updated := openAIWSPassthroughPolicyModelFromSessionFrameForRequest(ctx, account, payload); updated != "" {
				capturedSessionModel = updated
			}
			usageMeta.updateSessionRequestModel(payload)
			if requestModelForThisFrame == "" {
				requestModelForThisFrame = usageMeta.requestModelForFrame(payload)
			}
			// Per-frame model first; if the client omits "model" on a
			// follow-up frame (legal in Realtime), fall back to the
			// session-level model captured from the first frame so the
			// model whitelist still resolves. An empty model would miss
			// any whitelist and silently fall back to pass.
			model := openAIWSPassthroughPolicyModelForRequest(ctx, account, payload)
			if model == "" {
				model = capturedSessionModel
			}
			if isResponseCreate && model != "" && model != strings.TrimSpace(gjson.GetBytes(payload, "model").String()) {
				payload = s.ReplaceModelInBody(payload, model)
			}
			out, priorityDecision, blocked, policyErr := s.applyOpenAIPriorityPolicyToWSResponseCreate(ctx, account, model, payload)
			if policyErr == nil && blocked == nil && isResponseCreate {
				turnFingerprintIDs := resolveCodexFingerprintIDsFromRequest(account, c.Request.Header)
				fingerprintPayload, _, fingerprintErr := applyCodexFingerprintClientMetadataBytes(out, turnFingerprintIDs)
				if fingerprintErr != nil {
					return payload, nil, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket client metadata", fingerprintErr)
				}
				out = fingerprintPayload
			}
			// 多轮 passthrough usage：仅在成功（non-block / non-err）
			// 的 response.create 帧上更新 usageMeta，使用
			// filter 处理后的 payload，与首帧 policy-after-extract 语义
			// 保持一致（参见上方 extractOpenAIServiceTierFromBody 注释）。
			//   - 非 response.create 帧（response.cancel /
			//     conversation.item.create / session.update 等）不携带
			//     per-response metadata，不应覆盖前一轮值。
			//   - blocked != nil：该帧不会发送上游，usage metadata 应保持
			//     上一轮值。
			//   - policyErr != nil：异常路径，保持上一轮值。
			//   - 不带 service_tier 的 response.create 会让
			//     extractOpenAIServiceTierFromBody 返回 nil；这里有意
			//     覆盖（Store(nil)），因为 OpenAI 上游对该帧实际不传
			//     service_tier 时按 default 处理，billing 应如实反映。
			if policyErr == nil && blocked == nil && isResponseCreate {
				// OnTurnComplete 与客户端读取分别运行在两个 goroutine；CAS
				// 防止 terminal 回调边界上的竞争让两轮同时进入上游。
				if !turnInFlight.CompareAndSwap(false, true) {
					return payload, nil, NewOpenAIWSClientCloseError(
						coderws.StatusPolicyViolation,
						"overlapping response.create is not allowed; wait for the previous response to finish",
						nil,
					)
				}
				// passthrough（直通）模式的首轮槽位由 handler 在进入 service
				// 前持有；后续 response.create 要在所有本地策略通过后、真正
				// 写上游前逐轮重抢。该 filter 对每个客户端帧只执行一次，
				// 同一 turn 的 Priority 降级重试复用已保存的 payload，不会
				// 再次经过这里，因此不会重复占槽。
				if hooks != nil && hooks.BeforeTurn != nil {
					if err := hooks.BeforeTurn(turnNo); err != nil {
						turnInFlight.Store(false)
						return payload, nil, err
					}
				}
				usageMeta.requestedReasoningEffort.Store(extractReasoningEffortAuditFromBody(requestedPayload, "reasoning.effort", "reasoning_effort"))
				usageMeta.updateFromResponseCreate(out, model, requestModelForThisFrame, priorityDecision)
			}
			// 工具名归一化：passthrough 把客户端帧原样转发，非法工具名（MCP 风格
			// fs.read_file、mcp__srv.tool、中文名）会被上游直接 400。这里在真正
			// 写上游前统一改名，回程由 openAIWSClientFrameConn.restoreToolNames 还原。
			// fail-open：归一化失败就放行原帧，让上游给出真实报错。
			if normalizedFrame, _, nameChanged, nameErr := normalizeOpenAIWSToolNamesFrame(c, out); nameErr != nil {
				logOpenAIWSV2Passthrough("tool_name_normalize_failed account_id=%d turn=%d err=%v", account.ID, turnNo, nameErr)
			} else if nameChanged {
				out = normalizedFrame
			}
			if isResponseCreate && hooks != nil && hooks.BeforeDispatch != nil {
				if err := hooks.BeforeDispatch(turnNo, out, requestModelForThisFrame); err != nil {
					turnInFlight.Store(false)
					return payload, nil, err
				}
			}
			return out, blocked, policyErr
		},
		onBlock: func(blocked *OpenAIFastBlockedError) {
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
			// See note above on Conn.Write being synchronous w.r.t. flush;
			// no explicit flush is required to ensure the error event lands
			// before the close frame.
			eventBytes := buildOpenAIFastPolicyBlockedWSEvent(blocked)
			if eventBytes == nil {
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, s.openAIWSWriteTimeout())
			_ = clientConn.Write(writeCtx, coderws.MessageText, eventBytes)
			cancel()
		},
	}
	var priorityRetryMu sync.Mutex
	completedUpstreamResponseIDs := make(map[string]struct{})
	priorityRetryPayload := append([]byte(nil), firstClientMessage...)
	priorityRetryUsed := false
	setPriorityRetryPayload := func(payload []byte) {
		priorityRetryMu.Lock()
		priorityRetryPayload = append(priorityRetryPayload[:0], payload...)
		priorityRetryUsed = false
		priorityRetryMu.Unlock()
	}
	canRetryPriorityStandard := func() bool {
		priorityRetryMu.Lock()
		defer priorityRetryMu.Unlock()
		return !priorityRetryUsed && len(priorityRetryPayload) > 0
	}
	takePriorityRetryPayload := func() ([]byte, bool) {
		priorityRetryMu.Lock()
		defer priorityRetryMu.Unlock()
		if priorityRetryUsed || len(priorityRetryPayload) == 0 {
			return nil, false
		}
		priorityRetryUsed = true
		return append([]byte(nil), priorityRetryPayload...), true
	}

	readNextClientFrame := func(readCtx context.Context, conn openaiwsv2.FrameConn) (coderws.MessageType, []byte, error) {
		for {
			msgType, payload, readErr := conn.ReadFrame(readCtx)
			if readErr != nil {
				return msgType, payload, readErr
			}
			if (msgType == coderws.MessageText || msgType == coderws.MessageBinary) && gjson.ValidBytes(payload) && strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.create" {
				setPriorityRetryPayload(payload)
				return msgType, payload, nil
			}
			if writeErr := upstreamFrameConn.WriteFrame(readCtx, msgType, payload); writeErr != nil {
				return msgType, payload, writeErr
			}
		}
	}

	var relayResult openaiwsv2.RelayResult
	var relayExit *openaiwsv2.RelayExit
	currentFirstMessage := append([]byte(nil), firstClientMessage...)
	for {
		if hooks != nil && hooks.BeforeDispatch != nil {
			if err := hooks.BeforeDispatch(int(completedTurns.Load())+1, currentFirstMessage, usageMeta.requestModelForFrame(currentFirstMessage)); err != nil {
				return err
			}
		}
		firstWriteCtx, cancelFirstWrite := context.WithTimeout(ctx, s.openAIWSWriteTimeout())
		firstWriteErr := upstreamFrameConn.WriteFrame(firstWriteCtx, firstMessageType, currentFirstMessage)
		cancelFirstWrite()
		if firstWriteErr != nil {
			return wrapOpenAIWSIngressTurnError(
				"write_upstream",
				fmt.Errorf("write first upstream websocket request: %w", firstWriteErr),
				false,
			)
		}

		relayResult, relayExit = openaiwsv2.RunEntry(openaiwsv2.EntryInput{
			Ctx:                ctx,
			ClientConn:         policyClientConn,
			UpstreamConn:       upstreamFrameConn,
			FirstClientMessage: currentFirstMessage,
			Options: openaiwsv2.RelayOptions{
				WriteTimeout:                    s.openAIWSWriteTimeout(),
				IdleTimeout:                     s.openAIWSPassthroughIdleTimeout(),
				FirstMessageType:                firstMessageType,
				FirstMessageSent:                true,
				StartClientAfterFirstDownstream: true,
				ReadClientFrame:                 readNextClientFrame,
				OnUsageParseFailure: func(eventType string, usageRaw string) {
					logOpenAIWSV2Passthrough(
						"usage_parse_failed event_type=%s usage_raw=%s",
						truncateOpenAIWSLogValue(eventType, openAIWSLogValueMaxLen),
						truncateOpenAIWSLogValue(usageRaw, openAIWSLogValueMaxLen),
					)
				},
				OnTurnComplete: func(turn openaiwsv2.RelayTurnResult) {
					turnNo := int(completedTurns.Add(1))
					turnRequestModel, turnUpstreamModel := usageMeta.turnModels(turn.RequestModel)
					observedServiceTier := normalizeOpenAIServiceTier(turn.ServiceTier)
					sentServiceTier := usageMeta.serviceTier.Load()
					logOpenAIServiceTierOverride(ctx, account, turn.RequestModel, sentServiceTier, observedServiceTier)
					decision := usageMeta.priorityDecisionValue()
					decision.ServiceTier = cloneOptionalString(sentServiceTier)
					turnResult := &OpenAIForwardResult{
						RequestID: turn.RequestID,
						Usage: OpenAIUsage{
							InputTokens:              turn.Usage.InputTokens,
							ImageInputTokens:         turn.Usage.ImageInputTokens,
							OutputTokens:             turn.Usage.OutputTokens,
							CacheCreationInputTokens: turn.Usage.CacheCreationInputTokens,
							CacheReadInputTokens:     turn.Usage.CacheReadInputTokens,
							ImageOutputTokens:        turn.Usage.ImageOutputTokens,
						},
						Model:                         turnRequestModel,
						UpstreamModel:                 openAIWSFinalUpstreamModel(turnRequestModel, turnUpstreamModel),
						UpstreamResponseModel:         turn.ResponseModel,
						UpstreamResponseModelConflict: turn.ResponseModelConflict,
						UpstreamModelMismatch:         upstreamModelMismatch(turnUpstreamModel, turn.ResponseModel),
						RequestedReasoningEffort:      usageMeta.requestedReasoningEffort.Load(),
						ReasoningEffort:               usageMeta.reasoningEffort.Load(),
						Stream:                        true,
						OpenAIWSMode:                  true,
						ResponseHeaders:               cloneHeader(handshakeHeaders),
						Duration:                      turn.Duration,
						FirstTokenMs:                  turn.FirstTokenMs,
					}
					applyOpenAIPriorityDecisionToResult(turnResult, decision, observedServiceTier)
					logOpenAIWSV2Passthrough(
						"relay_turn_completed account_id=%d turn=%d request_id=%s terminal_event=%s turn_requested_model=%s turn_upstream_model=%s duration_ms=%d first_token_ms=%d input_tokens=%d output_tokens=%d cache_read_tokens=%d",
						account.ID,
						turnNo,
						truncateOpenAIWSLogValue(turnResult.RequestID, openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(turn.TerminalEventType, openAIWSLogValueMaxLen),
						truncateOpenAIWSLogValue(turnRequestModel, openAIWSLogValueMaxLen),
						truncateOpenAIWSLogValue(turnUpstreamModel, openAIWSLogValueMaxLen),
						turnResult.Duration.Milliseconds(),
						openAIWSFirstTokenMsForLog(turnResult.FirstTokenMs),
						turnResult.Usage.InputTokens,
						turnResult.Usage.OutputTokens,
						turnResult.Usage.CacheReadInputTokens,
					)
					if hooks != nil && hooks.AfterTurn != nil {
						hooks.AfterTurn(turnNo, turnResult, nil)
					}
					// 必须在 AfterTurn 释放旧轮次槽位后再允许下一轮；否则已排队
					// 的 response.create 会抢到新槽，随后被旧 AfterTurn 误释放。
					turnInFlight.Store(false)
				},
				BeforeWriteClient: func(msgType coderws.MessageType, payload []byte, wroteDownstream bool) error {
					if (msgType == coderws.MessageText || msgType == coderws.MessageBinary) && gjson.ValidBytes(payload) {
						observer := upstreamResponseModelObserverFromContext(c)
						if observer == nil {
							observer = beginUpstreamResponseModelObservation(c)
						}
						observer.ObserveOpenAI(payload, strings.TrimSpace(gjson.GetBytes(payload, "type").String()))
					}
					if validateErr := validateOpenAIWSPassthroughUpstreamFrame(msgType, payload, completedUpstreamResponseIDs); validateErr != nil {
						return validateErr
					}
					if wroteDownstream {
						return nil
					}
					if eventType, _, _ := parseOpenAIWSEventEnvelope(payload); eventType != "error" {
						return nil
					}
					errCodeRaw, errTypeRaw, errMsgRaw := parseOpenAIWSErrorEventFields(payload)
					sentServiceTier := usageMeta.serviceTier.Load()
					if sentServiceTier != nil && normalizeBillingServiceTier(*sentServiceTier) == OpenAIFastTierPriority &&
						openAIServiceTierUnsupportedText(strings.Join([]string{errCodeRaw, errTypeRaw, errMsgRaw}, " ")) &&
						canRetryPriorityStandard() {
						logOpenAIPriorityStandardRetry(ctx, account, usageMeta.requestModelForFrame(currentFirstMessage), "service_tier_unsupported")
						return errOpenAIWSPriorityServiceTierUnsupported
					}
					if !isOpenAIWSRateLimitError(errCodeRaw, errTypeRaw, errMsgRaw) {
						return nil
					}
					s.persistOpenAIWSRateLimitSignal(ctx, account, handshakeHeaders, payload, errCodeRaw, errTypeRaw, errMsgRaw)
					logOpenAIWSV2Passthrough(
						"relay_rate_limit_failover account_id=%d err_code=%s err_type=%s err_message=%s",
						account.ID,
						truncateOpenAIWSLogValue(errCodeRaw, openAIWSLogValueMaxLen),
						truncateOpenAIWSLogValue(errTypeRaw, openAIWSLogValueMaxLen),
						truncateOpenAIWSLogValue(errMsgRaw, openAIWSLogValueMaxLen),
					)
					return &UpstreamFailoverError{
						StatusCode:      http.StatusTooManyRequests,
						ResponseBody:    append([]byte(nil), payload...),
						ResponseHeaders: cloneHeader(handshakeHeaders),
					}
				},
				OnTrace: func(event openaiwsv2.RelayTraceEvent) {
					logOpenAIWSV2Passthrough(
						"relay_trace account_id=%d stage=%s direction=%s msg_type=%s bytes=%d graceful=%v wrote_downstream=%v err=%s",
						account.ID,
						truncateOpenAIWSLogValue(event.Stage, openAIWSLogValueMaxLen),
						truncateOpenAIWSLogValue(event.Direction, openAIWSLogValueMaxLen),
						truncateOpenAIWSLogValue(event.MessageType, openAIWSLogValueMaxLen),
						event.PayloadBytes,
						event.Graceful,
						event.WroteDownstream,
						truncateOpenAIWSLogValue(event.Error, openAIWSLogValueMaxLen),
					)
				},
			},
		})
		if relayExit == nil || !errors.Is(relayExit.Err, errOpenAIWSPriorityServiceTierUnsupported) {
			break
		}
		retryPayload, ok := takePriorityRetryPayload()
		if !ok {
			break
		}
		standardPayload, stripErr := sjson.DeleteBytes(retryPayload, "service_tier")
		if stripErr != nil {
			return wrapOpenAIWSIngressTurnError(
				"priority_standard_retry",
				fmt.Errorf("strip service_tier for websocket retry: %w", stripErr),
				false,
			)
		}
		usageMeta.serviceTier.Store(nil)
		currentFirstMessage = standardPayload
		upstreamFrameConn, err = dialUpstream()
		if err != nil {
			return err
		}
	}

	observedServiceTier := normalizeOpenAIServiceTier(relayResult.ServiceTier)
	sentServiceTier := usageMeta.serviceTier.Load()
	logOpenAIServiceTierOverride(ctx, account, relayResult.RequestModel, sentServiceTier, observedServiceTier)
	decision := usageMeta.priorityDecisionValue()
	decision.ServiceTier = cloneOptionalString(sentServiceTier)
	resultRequestModel, resultUpstreamModel := usageMeta.turnModels(relayResult.RequestModel)
	result := &OpenAIForwardResult{
		RequestID: relayResult.RequestID,
		Usage: OpenAIUsage{
			InputTokens:              relayResult.Usage.InputTokens,
			ImageInputTokens:         relayResult.Usage.ImageInputTokens,
			OutputTokens:             relayResult.Usage.OutputTokens,
			CacheCreationInputTokens: relayResult.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     relayResult.Usage.CacheReadInputTokens,
			ImageOutputTokens:        relayResult.Usage.ImageOutputTokens,
		},
		Model:                         resultRequestModel,
		UpstreamModel:                 openAIWSFinalUpstreamModel(resultRequestModel, resultUpstreamModel),
		UpstreamResponseModel:         relayResult.ResponseModel,
		UpstreamResponseModelConflict: relayResult.ResponseModelConflict,
		UpstreamModelMismatch:         upstreamModelMismatch(resultUpstreamModel, relayResult.ResponseModel),
		RequestedReasoningEffort:      usageMeta.requestedReasoningEffort.Load(),
		ReasoningEffort:               usageMeta.reasoningEffort.Load(),
		Stream:                        true,
		OpenAIWSMode:                  true,
		ResponseHeaders:               cloneHeader(handshakeHeaders),
		Duration:                      relayResult.Duration,
		FirstTokenMs:                  relayResult.FirstTokenMs,
	}
	applyOpenAIPriorityDecisionToResult(result, decision, observedServiceTier)

	turnCount := int(completedTurns.Load())
	if relayExit == nil {
		logOpenAIWSV2Passthrough(
			"relay_completed account_id=%d request_id=%s terminal_event=%s duration_ms=%d c2u_frames=%d u2c_frames=%d dropped_frames=%d turns=%d",
			account.ID,
			truncateOpenAIWSLogValue(result.RequestID, openAIWSIDValueMaxLen),
			truncateOpenAIWSLogValue(relayResult.TerminalEventType, openAIWSLogValueMaxLen),
			result.Duration.Milliseconds(),
			relayResult.ClientToUpstreamFrames,
			relayResult.UpstreamToClientFrames,
			relayResult.DroppedDownstreamFrames,
			turnCount,
		)
		// 正常路径按 terminal 事件逐 turn 已回调；仅在零 turn 场景兜底回调一次。
		if turnCount == 0 && hooks != nil && hooks.AfterTurn != nil {
			hooks.AfterTurn(1, result, nil)
		}
		return nil
	}
	logOpenAIWSV2Passthrough(
		"relay_failed account_id=%d stage=%s wrote_downstream=%v err=%s duration_ms=%d c2u_frames=%d u2c_frames=%d dropped_frames=%d turns=%d",
		account.ID,
		truncateOpenAIWSLogValue(relayExit.Stage, openAIWSLogValueMaxLen),
		relayExit.WroteDownstream,
		truncateOpenAIWSLogValue(relayErrorText(relayExit.Err), openAIWSLogValueMaxLen),
		result.Duration.Milliseconds(),
		relayResult.ClientToUpstreamFrames,
		relayResult.UpstreamToClientFrames,
		relayResult.DroppedDownstreamFrames,
		turnCount,
	)

	relayErr := relayExit.Err
	if relayExit.Stage == "idle_timeout" {
		relayErr = NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			"client websocket idle timeout",
			relayErr,
		)
	}
	turnErr := wrapOpenAIWSIngressTurnError(
		relayExit.Stage,
		relayErr,
		relayExit.WroteDownstream,
	)
	if hooks != nil && hooks.AfterTurn != nil {
		hooks.AfterTurn(turnCount+1, nil, turnErr)
	}
	return turnErr
}

func (s *OpenAIGatewayService) mapOpenAIWSPassthroughDialError(
	err error,
	statusCode int,
	handshakeHeaders http.Header,
) error {
	if err == nil {
		return nil
	}
	wrappedErr := err
	var dialErr *openAIWSDialError
	if !errors.As(err, &dialErr) {
		var handshakeErr *openAIWSHandshakeError
		var responseBody []byte
		if errors.As(err, &handshakeErr) && handshakeErr != nil {
			responseBody = append([]byte(nil), handshakeErr.Body...)
		}
		wrappedErr = &openAIWSDialError{
			StatusCode:      statusCode,
			ResponseHeaders: cloneHeader(handshakeHeaders),
			ResponseBody:    responseBody,
			Err:             err,
		}
	}

	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return NewOpenAIWSClientCloseError(
			coderws.StatusTryAgainLater,
			"upstream websocket connect timeout",
			wrappedErr,
		)
	}
	if statusCode == http.StatusTooManyRequests {
		return NewOpenAIWSClientCloseError(
			coderws.StatusTryAgainLater,
			"upstream websocket is busy, please retry later",
			wrappedErr,
		)
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			"upstream websocket authentication failed",
			wrappedErr,
		)
	}
	if statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError {
		return NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			"upstream websocket handshake rejected",
			wrappedErr,
		)
	}
	return fmt.Errorf("openai ws passthrough dial: %w", wrappedErr)
}

func openaiwsv2RelayMessageTypeName(msgType coderws.MessageType) string {
	switch msgType {
	case coderws.MessageText:
		return "text"
	case coderws.MessageBinary:
		return "binary"
	default:
		return fmt.Sprintf("unknown(%d)", msgType)
	}
}

func relayErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func openAIWSFirstTokenMsForLog(firstTokenMs *int) int {
	if firstTokenMs == nil {
		return -1
	}
	return *firstTokenMs
}

func logOpenAIWSV2Passthrough(format string, args ...any) {
	logger.LegacyPrintf(
		"service.openai_ws_v2",
		"[OpenAI WS v2 passthrough] %s "+format,
		append([]any{openaiWSV2PassthroughModeFields}, args...)...,
	)
}
