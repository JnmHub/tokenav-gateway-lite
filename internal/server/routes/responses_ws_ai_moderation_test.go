package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type responsesWSAIScenario struct {
	name  string
	turns []string
}

func TestResponsesWSAIModerationRegisteredRoutes(t *testing.T) {
	// 全局生产依赖由夹具保存/恢复；禁止 Parallel，与其他 owner 的新夹具无依赖。
	f := newResponsesWSAIFixture(t)
	cases := []responsesWSAIScenario{{"two_allow", []string{"allow", "allow"}}}
	for _, outcome := range []string{"block", "429", "503", "invalid_schema", "missing_categories", "missing_reason", "previous_response_id", "conversation", "remote_image", "unknown_field", "long_text", "skipped"} {
		cases = append(cases, responsesWSAIScenario{"first_" + outcome, []string{outcome}},
			responsesWSAIScenario{"allow_then_" + outcome, []string{"allow", outcome}})
	}
	for _, route := range []struct{ name, path string }{{"v1_responses", "/v1/responses"}, {"responses", "/responses"}} {
		for _, scenario := range cases {
			var owner *responsesWSAIConnection
			t.Run(route.name+"/"+scenario.name, func(t *testing.T) {
				owner = f.connection()
				runResponsesWSAIScenario(t, f, owner, route.path, scenario)
			})
			// -run 筛选未执行的子例不会建立连接，也不能等候它的关闭通知。
			if owner != nil {
				responsesWSAICollect(t, f, owner)
			}
		}
	}
	require.Positive(t, f.control.resolves.Load(), "real auth must resolve an ordinary main-site key")
	require.Positive(t, f.control.leases.Load(), "allowed turns must traverse production lease admission")
	require.Zero(t, f.repo.legacyPenalties.Load(), "AI must never call old local punishment/counters")
}

func responsesWSAIAwait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	timer := time.NewTimer(responsesWSAIWait)
	defer timer.Stop()
	select {
	case value := <-ch:
		return value
	case <-timer.C:
		t.Fatalf("bounded wait expired: %s", what)
		var zero T
		return zero
	}
}

func responsesWSAIMatching[T any](entries []T, match func(T) bool) []T {
	var found []T
	for _, entry := range entries {
		if match(entry) {
			found = append(found, entry)
		}
	}
	return found
}

func responsesWSAIRecorded[T any](t *testing.T, records *responsesWSAIRecords[T], match func(T) bool, what string) T {
	t.Helper()
	timer := time.NewTimer(responsesWSAIWait)
	defer timer.Stop()
	for {
		entries, changed := records.snapshot()
		found := responsesWSAIMatching(entries, match)
		if len(found) > 0 {
			assert.Len(t, found, 1, "duplicate record: %s", what)
			return found[0]
		}
		select {
		case <-changed:
		case <-timer.C:
			t.Errorf("bounded wait expired: %s", what)
			var zero T
			return zero
		}
	}
}

func responsesWSAIReviewFor(t *testing.T, owner *responsesWSAIConnection, text string, frames <-chan responsesWSAIFrame) (responsesWSAIReview, *responsesWSAIFrame) {
	t.Helper()
	timer := time.NewTimer(responsesWSAIWait)
	defer timer.Stop()
	for {
		entries, changed := owner.reviews.snapshot()
		found := responsesWSAIMatching(entries, func(r responsesWSAIReview) bool { return r.Text == text })
		if len(found) > 0 {
			require.Len(t, found, 1, "each turn must call the provider exactly once")
			return found[0], nil
		}
		select {
		case <-changed:
		case frame := <-frames:
			return responsesWSAIReview{}, &frame
		case <-timer.C:
			t.Fatal("no AI provider request or websocket result within bounded wait")
		}
	}
}

func responsesWSAIReviewable(outcome string) bool {
	switch outcome {
	case "previous_response_id", "conversation", "remote_image", "skipped":
		return false
	default:
		return true
	}
}

func responsesWSAIAllowed(outcome string) bool {
	switch outcome {
	case "allow", "unknown_field", "long_text", "skipped":
		return true
	default:
		return false
	}
}

func responsesWSAICompletedTurn(t *testing.T, owner *responsesWSAIConnection, turn int) string {
	t.Helper()
	responsesWSAIAwait(t, owner.done, "completed admission observer")
	require.NotEmpty(t, owner.end.Admission)
	require.EqualValues(t, turn, owner.end.Turn, "do not reuse a previous turn's identity for an unreviewed frame")
	return service.ContentModerationAdmissionTurnID(owner.end.Admission, uint64(turn))
}

// 在子例 Cleanup 之后仍检查留存记录；早期 Fatal 不会跳过归档，也不消费下一例的证据。
func responsesWSAICollect(t *testing.T, f *responsesWSAIFixture, owner *responsesWSAIConnection) {
	t.Helper()
	responsesWSAIAwait(t, owner.done, "connection completion before next case")
	expected := map[string]string{}
	usageIDs := map[string]bool{}
	penalties := f.store.snapshot()
	providerTotal := 0
	f.connections.Range(func(_, value any) bool {
		c := value.(*responsesWSAIConnection)
		select {
		case <-c.done:
		default:
			t.Errorf("connection %s still active before next case", c.ID)
			return true
		}
		resources, _ := c.resources.snapshot()
		for _, done := range resources {
			select {
			case <-done:
			default:
				t.Errorf("connection %s still owns an active resource", c.ID)
			}
		}
		sent, _ := c.sent.snapshot()
		released, _ := c.released.snapshot()
		reviews, _ := c.reviews.snapshot()
		dispatched, _ := c.dispatched.snapshot()
		wantProvider, wantDispatch := 0, 0
		for _, outcome := range sent {
			if responsesWSAIReviewable(outcome) {
				wantProvider++
			}
			if outcome == "skipped" {
				wantDispatch++
			}
		}
		for _, outcome := range released {
			if responsesWSAIAllowed(outcome) {
				wantDispatch++
			}
		}
		assert.Len(t, reviews, wantProvider, "provider records must belong to submitted reviewable turns")
		assert.Len(t, dispatched, wantDispatch, "only allowed or skipped turns may dispatch")
		providerTotal += len(reviews)
		for turn := range dispatched {
			usageIDs[fmt.Sprintf("client:%s:ws-turn:%d", c.ID, turn+1)] = true
		}
		assert.EqualValues(t, len(sent), c.end.Turn, "each submitted frame must have its own admission")
		for turn := uint64(1); turn <= c.end.Turn; turn++ {
			id := service.ContentModerationAdmissionTurnID(c.end.Admission, turn)
			assert.NotContains(t, expected, id, "server cm must be unique across connections/turns")
			expected[id] = c.ID
		}
		return true
	})
	for turn := uint64(1); turn <= owner.end.Turn; turn++ {
		id := service.ContentModerationAdmissionTurnID(owner.end.Admission, turn)
		responsesWSAIRecorded(t, &f.repo.created, func(a responsesWSAIAudit) bool { return a.ID == id }, "retained audit "+id)
	}
	audits, _ := f.repo.created.snapshot()
	assert.Len(t, audits, len(expected), "unexpected or duplicate safe audit remains retained")
	seenAudit, blocks := map[string]bool{}, map[string]bool{}
	for _, audit := range audits {
		assert.Contains(t, expected, audit.ID, "audit belongs to no observed server admission")
		assert.False(t, seenAudit[audit.ID], "duplicate audit retained")
		seenAudit[audit.ID] = true
		assert.NotContains(t, audit.ID, responsesWSAITrace)
		assert.NotContains(t, audit.Text, "review-synthetic")
		assert.NotContains(t, audit.Text, "https://example.invalid/")
		assert.Equal(t, responsesWSAIKeyID, audit.Key)
		assert.EqualValues(t, 33, audit.UserID)
		assert.Equal(t, "user", audit.BillingSubjectType)
		assert.EqualValues(t, 33, audit.BillingSubjectID)
		if value, ok := f.connections.Load(expected[audit.ID]); ok {
			assert.Equal(t, value.(*responsesWSAIConnection).path, audit.Endpoint)
		}
		if audit.Action == "block" {
			blocks[audit.ID] = true
		}
	}
	assert.EqualValues(t, providerTotal, f.providerCalls.Load(), "every provider call must have an owned review record")
	assert.Len(t, penalties, len(blocks))
	for id := range blocks {
		assert.Len(t, responsesWSAIMatching(penalties, func(v responsesWSAIPenalty) bool { return v.ID == id }), 1)
	}
	usage, _ := f.logs.created.snapshot()
	assert.Len(t, usage, len(usageIDs))
	assert.Len(t, f.billing.snapshot(), len(usageIDs))
	for id := range usageIDs {
		assert.Len(t, responsesWSAIMatching(usage, func(v responsesWSAIUsage) bool { return v.ID == id }), 1)
	}
	errors, _ := f.errors.snapshot()
	assert.Empty(t, errors)
}

func responsesWSAIWrite(t *testing.T, conn *coderws.Conn, body []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), responsesWSAIWait)
	defer cancel()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, body))
}

func responsesWSAIRead(conn *coderws.Conn) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), responsesWSAIWait)
	defer cancel()
	_, body, err := conn.Read(ctx)
	return body, err
}

type responsesWSAIFrame struct {
	Body []byte
	Err  error
}

func responsesWSAIReadAsync(conn *coderws.Conn, owner *responsesWSAIConnection) <-chan responsesWSAIFrame {
	frame := make(chan responsesWSAIFrame, 1)
	finished := owner.resource()
	go func() {
		defer finished()
		body, err := responsesWSAIRead(conn)
		frame <- responsesWSAIFrame{Body: body, Err: err}
	}()
	return frame
}

func responsesWSAIErrorCode(outcome string) string {
	switch outcome {
	case "block":
		return "content_policy_violation"
	case "429":
		return "ai_provider_rate_limit"
	case "503":
		return "ai_provider_http_error"
	case "invalid_schema", "missing_categories", "missing_reason":
		return "ai_result_schema"
	case "previous_response_id", "conversation", "remote_image":
		return "ai_strict_frame_rejected"
	default:
		return ""
	}
}

func responsesWSAIReadRejection(t *testing.T, conn *coderws.Conn, outcome string, first responsesWSAIFrame) bool {
	t.Helper()
	body, err := first.Body, first.Err
	frameSeen := assert.NoError(t, err, "rejection must send type:error/error.code before the policy close")
	if frameSeen {
		assert.Equal(t, "error", gjson.GetBytes(body, "type").String())
		assert.Equal(t, responsesWSAIErrorCode(outcome), gjson.GetBytes(body, "error.code").String())
		_, err = responsesWSAIRead(conn)
	}
	assert.Error(t, err, "rejection must end this websocket, not leave it idle")
	assert.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err), "a read timeout/EOF is not a policy close")
	return frameSeen
}

type responsesWSAIMoney struct {
	Requests map[string]map[string]string
	Messages []redis.XMessage
	Lease    gatewaylite.LeaseSnapshot
	HasLease bool
}

func (f *responsesWSAIFixture) money(t *testing.T) responsesWSAIMoney {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), responsesWSAIWait)
	defer cancel()
	out := responsesWSAIMoney{Requests: map[string]map[string]string{}}
	keys, err := f.redis.Keys(ctx, f.prefix+":req:*").Result()
	require.NoError(t, err)
	for _, key := range keys {
		out.Requests[key], err = f.redis.HGetAll(ctx, key).Result()
		require.NoError(t, err)
	}
	out.Messages, err = f.redis.XRange(ctx, f.prefix+":usage:stream", "-", "+").Result()
	require.NoError(t, err)
	out.Lease, out.HasLease, err = f.quota.LoadLease(ctx, 33, responsesWSAINode)
	require.NoError(t, err)
	return out
}

func responsesWSAIQueued(t *testing.T, message redis.XMessage) (gatewaylite.UsageEvent, int64) {
	t.Helper()
	raw, ok := message.Values["event"].(string)
	require.True(t, ok)
	var queued struct {
		Event gatewaylite.UsageEvent `json:"event"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &queued))
	cents, err := strconv.ParseInt(fmt.Sprint(message.Values["resolved_actual_cents"]), 10, 64)
	require.NoError(t, err)
	return queued.Event, cents
}

func runResponsesWSAIScenario(t *testing.T, f *responsesWSAIFixture, owner *responsesWSAIConnection, path string, scenario responsesWSAIScenario) {
	t.Helper()
	owner.path = path
	startCreates, startProviders := f.creates.Load(), f.providerCalls.Load()
	startPenalties := f.store.snapshot()
	startBilling := f.billing.snapshot()
	money := f.money(t)
	ctx, cancel := context.WithTimeout(context.Background(), responsesWSAIWait)
	conn, _, err := coderws.Dial(ctx, f.url+path, &coderws.DialOptions{HTTPHeader: map[string][]string{
		"Authorization": {"Bearer " + responsesWSAIKey}, "X-Request-ID": {owner.ID},
		"X-Client-Request-ID": {responsesWSAITrace}, "User-Agent": {owner.ID},
	}})
	cancel()
	t.Cleanup(owner.cancel)
	closed := false
	finish := func() {
		if closed {
			return
		}
		closed = true
		if conn != nil {
			_ = conn.CloseNow()
		}
		owner.cancel() // 失败时解除本连接的 provider 闸门与上游读等待。
		responsesWSAIAwait(t, owner.done, "own production route termination")
		resources, _ := owner.resources.snapshot()
		for _, done := range resources {
			responsesWSAIAwait(t, done, "own provider/business socket termination")
		}
	}
	t.Cleanup(finish)
	require.NoError(t, err, "handshake must pass real RegisterGatewayRoutes and ordinary-key auth")
	// 即使 require 提前退出，也由父例收取所有实际身份对应的审计并检查额外记录。
	require.Equal(t, money, f.money(t), "handshake cannot create a business reservation or fee")
	allowed, providerCount := 0, 0
	auditOutcomes := map[string]string{}
	usages := make([]responsesWSAIUsage, 0, 2)
	// 业务用量沿用客户端规范身份；审查身份从日志观察，关闭后再与 handler 的真实轮次核对。
	canonical := "client:" + owner.ID
	for index, outcome := range scenario.turns {
		input := fmt.Sprintf("Synthetic ordinary-key input connection %s turn %d. Keep this exact text.", owner.ID, index+1)
		if outcome == "long_text" {
			input += strings.Repeat("完整段落。", 13000) + " complete-tail"
		}
		f.inputs.Store(input, owner)
		payload := map[string]any{"type": "response.create", "event_id": responsesWSAITrace, "model": responsesWSAIModel,
			"max_output_tokens": 32, "input": input}
		switch outcome {
		case "previous_response_id":
			payload["previous_response_id"] = "resp_hidden_history"
		case "conversation":
			payload["conversation"] = "hidden_conversation"
		case "remote_image":
			payload["input"] = []any{map[string]any{"role": "user", "content": []any{
				map[string]string{"type": "input_text", "text": input},
				map[string]string{"type": "input_image", "image_url": "https://example.invalid/synthetic.png"},
			}}}
		case "unknown_field", "skipped":
			payload["vendor_extension"] = map[string]string{"note": "unselected synthetic extension"}
			if outcome == "skipped" {
				payload["input"] = ""
				// 生成约束也属于固定送审字段；清空正文但留下它不构成无选中内容。
				delete(payload, "max_output_tokens")
			}
		}
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		beforeReview := f.money(t)
		priorAudits, _ := f.repo.created.snapshot()
		priorAuditIDs := make(map[string]bool, len(priorAudits))
		for _, audit := range priorAudits {
			priorAuditIDs[audit.ID] = true
		}
		owner.sent.add(outcome)
		responsesWSAIWrite(t, conn, body)
		nextFrame := responsesWSAIReadAsync(conn, owner)
		var auditID string
		if responsesWSAIReviewable(outcome) {
			pending, premature := responsesWSAIReviewFor(t, owner, input, nextFrame)
			if premature != nil {
				if premature.Err == nil {
					_, closeErr := responsesWSAIRead(conn)
					assert.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(closeErr))
				}
				finish()
				assert.Equal(t, startCreates+int64(allowed), f.creates.Load())
				assert.Equal(t, beforeReview, f.money(t))
				auditID = responsesWSAICompletedTurn(t, owner, index+1)
				audit := responsesWSAIRecorded(t, &f.repo.created, func(a responsesWSAIAudit) bool { return a.ID == auditID }, "premature rejection audit")
				t.Fatalf("legal response.create rejected before AI provider: frame_type=%q code=%q audit_action=%q audit_code=%q read_error=%v",
					gjson.GetBytes(premature.Body, "type").String(), gjson.GetBytes(premature.Body, "error.code").String(), audit.Action, audit.Code, premature.Err)
			}
			providerCount++
			require.Equal(t, startProviders+int64(providerCount), f.providerCalls.Load(), "exactly one provider request before releasing this review")
			require.Equal(t, service.ContentModerationProtocolOpenAIResponses, pending.Input.Protocol)
			require.NotEmpty(t, pending.Input.InputHash)
			require.Len(t, pending.Input.Fragments, 2, "input and its generation constraint each remain one complete field")
			inputFragments := responsesWSAIMatching(pending.Input.Fragments, func(f service.ContentModerationAIFragment) bool { return f.ParentID == "input" })
			require.Len(t, inputFragments, 1, "a long input must not be split into fragments")
			fragment := inputFragments[0]
			require.Equal(t, input, fragment.Text, "the current original input must be reviewed in full, not the previous turn")
			require.Zero(t, fragment.StartRune)
			require.Equal(t, utf8.RuneCountInString(input), fragment.EndRune)
			constraints := responsesWSAIMatching(pending.Input.Fragments, func(f service.ContentModerationAIFragment) bool { return f.ParentID == "max_output_tokens" })
			require.Len(t, constraints, 1)
			require.Equal(t, "32", constraints[0].Text)
			if outcome == "unknown_field" {
				require.Contains(t, pending.Input.UncoveredFields, "field:vendor_extension")
			}
			// HTTP 审查仍被通道闸门拦住；create 与区域资金状态此刻都不得变化。
			require.Equal(t, startCreates+int64(allowed), f.creates.Load(), "business create advanced before review completed")
			require.Equal(t, beforeReview, f.money(t), "review cannot consume a customer's business reservation")
			owner.released.add(outcome)
			pending.Reply <- outcome
		}
		if responsesWSAIAllowed(outcome) {
			frame := responsesWSAIAwait(t, nextFrame, "allowed business terminal frame")
			response, readErr := frame.Body, frame.Err
			require.NoError(t, readErr)
			require.Equal(t, "response.completed", gjson.GetBytes(response, "type").String())
			allowed++
			dispatch := responsesWSAIRecorded(t, &owner.dispatched, func(d responsesWSAIDispatch) bool { return d.Number == startCreates+int64(allowed) }, "actual business create")
			require.Equal(t, startCreates+int64(allowed), dispatch.Number)
			require.Equal(t, body, dispatch.Payload, "review and skipped turns must preserve the exact original business body")
			require.Equal(t, payload["input"], gjson.GetBytes(dispatch.Payload, "input").String())
			require.Equal(t, responsesWSAITrace, gjson.GetBytes(dispatch.Payload, "event_id").String())
			require.Equal(t, responsesWSAIModel, gjson.GetBytes(dispatch.Payload, "model").String())
			if outcome == "skipped" {
				require.False(t, gjson.GetBytes(dispatch.Payload, "max_output_tokens").Exists())
			} else {
				require.EqualValues(t, 32, gjson.GetBytes(dispatch.Payload, "max_output_tokens").Int())
			}
			require.Equal(t, fmt.Sprintf("resp_ws_ai_%d", dispatch.Number), gjson.GetBytes(response, "response.id").String())
			wantUsageID := fmt.Sprintf("%s:ws-turn:%d", canonical, index+1)
			usage := responsesWSAIRecorded(t, &f.logs.created, func(u responsesWSAIUsage) bool { return u.ID == wantUsageID }, "real handler usage record")
			require.Positive(t, usage.Cost)
			require.Equal(t, 10000, usage.Input)
			require.Equal(t, 5, usage.Output)
			require.Equal(t, wantUsageID, usage.ID)
			if len(usages) > 0 {
				require.NotEqual(t, usages[0].ID, usage.ID)
			}
			usages = append(usages, usage)
			after := f.money(t)
			require.Len(t, after.Messages, len(money.Messages)+1)
			for i, prior := range money.Messages {
				require.Equal(t, prior, after.Messages[i], "a new turn must not overwrite prior usage")
			}
			event, cents := responsesWSAIQueued(t, after.Messages[len(after.Messages)-1])
			require.Equal(t, usage.ID, event.RequestID)
			require.Equal(t, responsesWSAIKeyID, event.KeyID)
			require.EqualValues(t, 33, event.UserID)
			require.EqualValues(t, usage.Input, event.TokensIn)
			require.EqualValues(t, usage.Output, event.TokensOut)
			require.True(t, event.Final)
			require.Empty(t, event.FundingAuthorizationID)
			require.Nil(t, event.OAuthInvocationReservation)
			require.Positive(t, cents, "fixture usage is large enough to expose cent-level refund regressions")
			require.Len(t, after.Requests, len(money.Requests)+1)
			for key, prior := range money.Requests {
				require.Equal(t, prior, after.Requests[key])
			}
			request := after.Requests[f.prefix+":req:"+usage.ID]
			require.Equal(t, "1", request["settlement_usage_enqueued"])
			require.Equal(t, strconv.FormatInt(cents, 10), request["settlement_actual_cents"])
			require.Equal(t, money.Lease.SpentCents+cents, after.Lease.SpentCents)
			require.Zero(t, after.Lease.ReservedCents)
			money = after
		} else {
			frame := responsesWSAIAwait(t, nextFrame, "AI rejection frame")
			responsesWSAIReadRejection(t, conn, outcome, frame)
			finish()
			// 断言拒绝帧即使失败，也继续检查第一轮费用是否退款/覆盖或新增第二轮费用。
			assert.Equal(t, beforeReview, f.money(t), "blocked/error turn changed committed first-turn charges or created a second charge")
			if auditID == "" {
				// 未进入 provider 时只能用完成后的真实轮次；不得拿上一轮 cm 冒充本轮。
				if !assert.EqualValues(t, index+1, owner.end.Turn, "rejected turn never entered AI admission; no matching audit identity") {
					break
				}
				auditID = responsesWSAICompletedTurn(t, owner, index+1)
			}
		}
		// 本例串行且前轮审计已收齐；只观察本轮新增日志，连接关闭后再验证真实 admission/turn。
		audit := responsesWSAIRecorded(t, &f.repo.created, func(a responsesWSAIAudit) bool {
			if auditID != "" {
				return a.ID == auditID
			}
			return !priorAuditIDs[a.ID] && a.Key == responsesWSAIKeyID && a.Endpoint == path
		}, "safe AI audit projection")
		_, repeated := auditOutcomes[audit.ID]
		require.False(t, repeated, "AI server identity must be distinct for each turn")
		auditOutcomes[audit.ID] = outcome
		require.Equal(t, responsesWSAIKeyID, audit.Key)
		require.Equal(t, path, audit.Endpoint)
		require.True(t, strings.HasPrefix(audit.ID, "cm:"))
		require.Len(t, audit.ID, len("cm:")+64)
		require.NotContains(t, audit.ID, responsesWSAITrace)
		wantAction, wantCode := "allow", ""
		if outcome == "block" {
			wantAction = "block"
		} else if !responsesWSAIAllowed(outcome) {
			wantAction, wantCode = "error", responsesWSAIErrorCode(outcome)
		}
		require.Equal(t, wantAction, audit.Action)
		require.Equal(t, wantCode, audit.Code)
		require.Equal(t, outcome == "block", audit.Flagged)
		require.Equal(t, responsesWSAIReviewable(outcome), audit.ProviderCalled)
		if outcome == "skipped" {
			require.Equal(t, "skipped", audit.Phase)
			require.Empty(t, audit.Text)
			require.Zero(t, audit.Coverage.FragmentCount)
		} else {
			require.Contains(t, audit.Text, input, "audit must retain the full extracted paragraph")
			require.NotEmpty(t, audit.Excerpt)
			require.Equal(t, 2, audit.Coverage.FragmentCount)
		}
		if audit.ProviderCalled {
			require.Equal(t, audit.Text, audit.ReviewedText)
		} else {
			require.Empty(t, audit.ReviewedText, "unreviewed evidence cannot be presented as sent to the model")
		}
		completed := responsesWSAIReviewable(outcome) && (responsesWSAIAllowed(outcome) || outcome == "block")
		require.Equal(t, completed, audit.Coverage.Complete)
		if completed {
			require.Equal(t, "completed", audit.Phase)
			require.Equal(t, 2, audit.Coverage.ReviewedCount)
			require.NotEmpty(t, audit.Reason)
		} else {
			require.Zero(t, audit.Coverage.ReviewedCount)
		}
		if outcome == "block" {
			require.Equal(t, []string{"violence"}, audit.Categories)
		} else {
			require.Empty(t, audit.Categories)
		}
		switch outcome {
		case "unknown_field", "skipped":
			require.Contains(t, audit.Coverage.UncoveredFields, "field:vendor_extension")
			require.NotContains(t, audit.Text, "unselected synthetic extension")
		case "previous_response_id", "conversation":
			require.Contains(t, audit.Coverage.UncoveredFields, "history:"+outcome)
		case "remote_image":
			require.Contains(t, audit.Coverage.UncoveredFields, "image_not_inline:input/0/content/1")
		}
	}
	finish()
	assert.True(t, owner.end.Authenticated, "completion observer must see real auth RequestMeta")
	wantCanonical := canonical
	if allowed > 0 {
		wantCanonical = fmt.Sprintf("%s:ws-turn:%d", canonical, allowed)
	}
	assert.Equal(t, wantCanonical, owner.end.Canonical, "a rejection must preserve the last admitted business identity")
	assert.EqualValues(t, len(scenario.turns), owner.end.Turn, "every submitted turn must enter AI admission")
	for index := range scenario.turns {
		if uint64(index+1) <= owner.end.Turn {
			id := service.ContentModerationAdmissionTurnID(owner.end.Admission, uint64(index+1))
			assert.Contains(t, auditOutcomes, id, "audit must belong to the actual server admission/turn")
		}
	}
	assert.Equal(t, startCreates+int64(allowed), f.creates.Load(), "each allowed turn must create exactly once; rejections never dispatch")
	assert.Equal(t, startProviders+int64(providerCount), f.providerCalls.Load())
	assert.Equal(t, money, f.money(t), "connection close must not refund or duplicate completed turns")
	billing := f.billing.snapshot()
	require.Len(t, billing, len(startBilling)+allowed, "blocked/error turns must not reach business billing")
	require.Equal(t, startBilling, append([]responsesWSAIUsage(nil), billing[:len(startBilling)]...))
	for _, usage := range usages {
		matches := responsesWSAIMatching(billing, func(v responsesWSAIUsage) bool { return v.ID == usage.ID })
		require.Len(t, matches, 1)
		require.InDelta(t, usage.Cost, matches[0].Cost, 1e-8)
	}
	penalties := f.store.snapshot()
	blocked := scenario.turns[len(scenario.turns)-1] == "block"
	wantPenalties := len(startPenalties)
	if blocked {
		wantPenalties++
	}
	require.Len(t, penalties, wantPenalties, "only a valid block enqueues one penalty; errors enqueue none")
	require.Equal(t, startPenalties, append([]responsesWSAIPenalty(nil), penalties[:len(startPenalties)]...))
	if blocked {
		blockID := service.ContentModerationAdmissionTurnID(owner.end.Admission, owner.end.Turn)
		matches := responsesWSAIMatching(penalties, func(p responsesWSAIPenalty) bool { return p.ID == blockID })
		require.Len(t, matches, 1)
		penalty := matches[0]
		require.Equal(t, "block", auditOutcomes[penalty.ID])
		require.NotContains(t, penalty.ID, responsesWSAITrace)
		require.Equal(t, responsesWSAIKeyID, penalty.Key)
		require.EqualValues(t, 33, penalty.UserID)
		require.EqualValues(t, 25, penalty.Amount)
		require.Equal(t, "block", penalty.Action)
		require.Equal(t, "violence", penalty.Category)
		for _, prior := range startPenalties {
			require.NotEqual(t, prior.ID, penalty.ID)
		}
	}
	errors, _ := f.errors.snapshot()
	require.Empty(t, errors)
	allUsage, _ := f.logs.created.snapshot()
	require.Len(t, responsesWSAIMatching(allUsage, func(u responsesWSAIUsage) bool { return strings.HasPrefix(u.ID, "client:"+owner.ID+":ws-turn:") }), allowed)
	dispatches, _ := owner.dispatched.snapshot()
	require.Len(t, dispatches, allowed, "unexpected extra business create")
	reviews, _ := owner.reviews.snapshot()
	require.Len(t, reviews, providerCount, "unexpected extra AI provider request")
}
