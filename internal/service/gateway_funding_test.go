package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	modelpricing "github.com/Wei-Shaw/sub2api/resources/model-pricing"
	"github.com/alicebob/miniredis/v2"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFundingTextShapeRequiresEnforceableLimits(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-4o","input":"hi"}`,
		`{"max_output_tokens":0}`, `{"max_output_tokens":1.5}`, `{"max_tokens":"100"}`,
		`{"max_tokens":16777217}`, `{"max_tokens":100,"n":129}`,
		`{"max_tokens":100,"previous_response_id":"resp_history"}`,
		`{"max_output_tokens":100,"prompt":{"id":"stored_prompt"}}`,
		`{"max_output_tokens":100,"conversation":"stored_conversation"}`,
		`{"max_output_tokens":100,"background":true}`,
		`{"max_tokens":100,"modalities":["audio"]}`,
		`{"max_tokens":100,"input":[{"type":"input_image","image_url":"https://example.test/image"}]}`,
		`{"max_tokens":100,"tools":[{"type":"web_search_preview"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			_, _, _, err := ParseFundingTextLimits([]byte(body))
			require.ErrorIs(t, err, ErrStrictCardEndpointUnsupported)
		})
	}
	body := []byte(`{"model":"gpt-4o","max_output_tokens":100,"max_tokens":200,"n":3,"input":"hello"}`)
	input, output, count, err := ParseFundingTextLimits(body)
	require.NoError(t, err)
	require.Equal(t, len(body)*8+4096, input)
	require.Equal(t, 200, output)
	require.Equal(t, 3, count)
}

func TestFundingTextShapeTreatsClientToolDataAsBoundedInput(t *testing.T) {
	// 同名字段在客户完整提供的参数结构/调用数据中只是文本；费用仍覆盖整个正文。
	for _, test := range []struct{ name, fields string }{
		{"responses_schema", `"tools":[{"type":"function","name":"inspect","parameters":{"type":"object","properties":{"session":{"type":"string"},"conversation":{"type":"string"},"prompt":{"type":"object","properties":{"id":{"type":"string"}}},"background":{"const":true},"modalities":{"const":["audio"]},"image_url":{"type":"string"},"type":{"const":"input_file"}},"$defs":{"history":{"previous_response_id":"literal"}},"examples":[{"session":"example"}]}}]`},
		{"responses_namespace_schema", `"tools":[{"type":"namespace","name":"team","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"session":{"type":"string"}}}}]}]`},
		{"responses_namespace_chain_schema", `"tools":[{"type":"namespace","name":"team","tools":[{"type":"namespace","name":"nested","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"session":{"type":"string"},"conversation":{"type":"string"}}}}]}]}]`},
		{"responses_namespace_children_schema", `"tools":[{"type":"namespace","name":"team","children":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"session":{"type":"string"}}}}]}]`},
		{"chat_schema", `"tools":[{"type":"function","function":{"name":"inspect","parameters":{"type":"object","properties":{"session":{"type":"string"},"conversation":{"type":"string"}}}}}]`},
		{"legacy_function_schema", `"functions":[{"name":"inspect","parameters":{"type":"object","properties":{"session":{"type":"string"}}}}]`},
		{"anthropic_schema", `"tools":[{"name":"inspect","input_schema":{"type":"object","properties":{"session":{"type":"string"},"conversation":{"type":"string"}}}}]`},
		{"responses_arguments", `"input":[{"type":"function_call","name":"inspect","call_id":"call_1","arguments":{"session":"literal","conversation":{"id":"literal"},"previous_response_id":"literal","image_url":"literal","type":"input_image"}}]`},
		{"chat_arguments", `"messages":[{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"inspect","arguments":{"session":"literal","prompt":{"id":"literal"},"background":true}}}]}]`},
		{"legacy_function_arguments", `"messages":[{"role":"assistant","function_call":{"name":"inspect","arguments":{"session":"literal"}}}]`},
		{"anthropic_arguments", `"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"inspect","id":"tool_1","input":{"session":"literal","conversation":"literal","modalities":["audio"]}}]}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-4o","max_output_tokens":32,"input":"hello",` + test.fields + `}`)
			input, output, count, err := ParseFundingTextLimits(body)
			frame := append([]byte(`{"type":"response.create",`), body[1:]...)
			frameErr := validateOpenAIWSStrictClientFrame(coderws.MessageText, frame, true)
			if err != nil || frameErr != nil {
				t.Errorf("client tool data rejected: parser=%v; strict websocket=%v", err, frameErr)
				return
			}
			require.Equal(t, len(body)*8+4096, input, "工具数据也必须计入完整输入上界")
			require.Equal(t, 32, output)
			require.Equal(t, 1, count)
			frameInput, _, _, err := ParseFundingTextLimits(frame)
			require.NoError(t, err)
			require.Equal(t, len(frame)*8+4096, frameInput)
			catalog := NewPricingService(&config.Config{}, nil)
			require.NoError(t, catalog.loadPricingDataBytes(modelpricing.DefaultPricing(), "embedded tool data test", time.Time{}))
			billing := NewBillingService(&config.Config{}, catalog)
			bound, err := billing.FundingTextUpperBound(context.Background(), nil, &Group{ID: 7}, "gpt-4o", frameInput, output, count, 1)
			require.NoError(t, err)
			actual, err := billing.CalculateCost("gpt-4o", UsageTokens{InputTokens: frameInput, OutputTokens: output}, 1)
			require.NoError(t, err)
			require.GreaterOrEqual(t, bound.MicroCents, int64(math.Ceil(actual.ActualCost*100_000_000)))
		})
	}
}

func TestFundingTextShapeKeepsProtocolControlsOutsideToolData(t *testing.T) {
	for _, test := range []struct{ name, fields string }{
		{"top_history", `"previous_response_id":"resp_history"`},
		{"top_conversation", `"conversation":{"id":"stored"}`},
		{"top_session", `"session":{"model":"gpt-4o"}`},
		{"top_prompt", `"prompt":{"id":"stored"}`},
		{"top_background", `"background":true`},
		{"top_audio", `"modalities":["audio"]`},
		{"protocol_history", `"response":{"conversation":"stored"}`},
		{"protocol_media", `"input":[{"type":"message","content":[{"type":"input_image","image_url":"https://example.test/image"}]}]`},
		{"builtin_tool", `"tools":[{"type":"web_search_preview","parameters":{"session":"literal"}}]`},
		{"unrelated_parameters", `"parameters":{"session":"stored"}`},
		{"unrelated_arguments", `"arguments":{"session":"stored"}`},
		{"unrelated_tool_content", `"tools":[{"type":"function","name":"inspect","session":"stored","parameters":{"type":"object"}}]`},
		{"nested_under_function", `"tools":[{"type":"function","name":"inspect","tools":[{"type":"function","name":"lookup","parameters":{"session":"stored"}}]}]`},
		{"nested_under_untyped_tool", `"tools":[{"name":"team","tools":[{"type":"function","name":"lookup","parameters":{"session":"stored"}}]}]`},
		{"namespace_without_name", `"tools":[{"type":"namespace","tools":[{"type":"function","name":"lookup","parameters":{"session":"stored"}}]}]`},
		{"namespace_outside_tool_root", `"response":{"tools":[{"type":"namespace","name":"team","tools":[{"type":"function","name":"lookup","parameters":{"session":"stored"}}]}]}`},
		{"namespace_chain_invalid_ancestor", `"tools":[{"type":"namespace","name":"team","tools":[{"name":"nested","tools":[{"type":"function","name":"lookup","parameters":{"session":"stored"}}]}]}]`},
		{"namespace_protocol_history", `"tools":[{"type":"namespace","name":"team","session":"stored","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}]`},
		{"namespace_builtin_tool", `"tools":[{"type":"namespace","name":"team","tools":[{"type":"web_search_preview","name":"lookup","parameters":{"session":"literal"}}]}]`},
		{"namespace_child_protocol_history", `"tools":[{"type":"namespace","name":"team","tools":[{"type":"function","name":"lookup","session":"stored","parameters":{"type":"object"}}]}]`},
		{"namespace_child_array", `"tools":[{"type":"namespace","name":"team","tools":[[{"type":"function","name":"lookup","parameters":{"session":"stored"}}]]}]`},
		{"namespace_child_object", `"tools":[{"type":"namespace","name":"team","tools":{"type":"function","name":"lookup","parameters":{"session":"stored"}}}]`},
		{"namespace_function_without_name", `"tools":[{"type":"namespace","name":"team","tools":[{"type":"function","parameters":{"session":"stored"}}]}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-4o","max_output_tokens":32,"input":"hello",` + test.fields + `}`)
			_, _, _, err := ParseFundingTextLimits(body)
			require.ErrorIs(t, err, ErrStrictCardEndpointUnsupported)
			frame := append([]byte(`{"type":"response.create",`), body[1:]...)
			require.Error(t, validateOpenAIWSStrictClientFrame(coderws.MessageText, frame, true))
		})
	}
}

func TestFundingTextShapeRejectsToolPathKeyCollisions(t *testing.T) {
	// 字面对象键不能冒充数组层级或多个字段段；检查两个实际入口，避免先失败的断言遮住另一个。
	for _, test := range []struct{ name, fields string }{
		{"namespace_object_array_key", `"tools":{"[]":{"type":"namespace","name":"team","tools":[{"type":"function","name":"lookup","parameters":{"session":"stored"}}]}}`},
		{"namespace_dotted_root_key", `"tools.[]":{"type":"namespace","name":"team","tools":[{"type":"function","name":"lookup","parameters":{"session":"stored"}}]}`},
		{"responses_schema_object_array_key", `"tools":{"[]":{"type":"function","name":"lookup","parameters":{"session":"stored"}}}`},
		{"responses_schema_dotted_root_key", `"tools.[]":{"type":"function","name":"lookup","parameters":{"session":"stored"}}`},
		{"chat_schema_object_array_key", `"tools":{"[]":{"type":"function","function":{"name":"lookup","parameters":{"session":"stored"}}}}`},
		{"chat_schema_dotted_function_key", `"tools.[].function":{"name":"lookup","parameters":{"session":"stored"}}`},
		{"legacy_schema_object_array_key", `"functions":{"[]":{"name":"lookup","parameters":{"session":"stored"}}}`},
		{"legacy_schema_dotted_root_key", `"functions.[]":{"name":"lookup","parameters":{"session":"stored"}}`},
		{"anthropic_schema_object_array_key", `"tools":{"[]":{"name":"lookup","input_schema":{"session":"stored"}}}`},
		{"anthropic_schema_dotted_root_key", `"tools.[]":{"name":"lookup","input_schema":{"session":"stored"}}`},
		{"responses_arguments_object_array_key", `"input":{"[]":{"type":"function_call","name":"lookup","arguments":{"session":"stored"}}}`},
		{"responses_arguments_dotted_root_key", `"input.[]":{"type":"function_call","name":"lookup","arguments":{"session":"stored"}}`},
		{"chat_arguments_object_array_key", `"messages":{"[]":{"tool_calls":{"[]":{"function":{"name":"lookup","arguments":{"session":"stored"}}}}}}`},
		{"chat_arguments_dotted_function_key", `"messages.[].tool_calls.[].function":{"name":"lookup","arguments":{"session":"stored"}}`},
		{"legacy_arguments_object_array_key", `"messages":{"[]":{"function_call":{"name":"lookup","arguments":{"session":"stored"}}}}`},
		{"legacy_arguments_dotted_function_key", `"messages.[].function_call":{"name":"lookup","arguments":{"session":"stored"}}`},
		{"anthropic_messages_object_array_key", `"messages":{"[]":{"content":{"[]":{"type":"tool_use","name":"lookup","input":{"session":"stored"}}}}}`},
		{"anthropic_messages_dotted_content_key", `"messages.[].content.[]":{"type":"tool_use","name":"lookup","input":{"session":"stored"}}`},
		{"anthropic_input_object_array_key", `"input":{"[]":{"content":{"[]":{"type":"tool_use","name":"lookup","input":{"session":"stored"}}}}}`},
		{"anthropic_input_dotted_content_key", `"input.[].content.[]":{"type":"tool_use","name":"lookup","input":{"session":"stored"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(`{`+test.fields+`}`), &payload))
			payload["model"], payload["max_output_tokens"] = "gpt-4o", 32
			if _, hasInput := payload["input"]; !hasInput {
				payload["input"] = "hello"
			}
			body, marshalErr := json.Marshal(payload)
			require.NoError(t, marshalErr)
			_, _, _, err := ParseFundingTextLimits(body)
			frame := append([]byte(`{"type":"response.create",`), body[1:]...)
			frameErr := validateOpenAIWSStrictClientFrame(coderws.MessageText, frame, true)
			if err == nil || frameErr == nil {
				t.Errorf("non-protocol tool path accepted: parser=%v; strict websocket=%v", err, frameErr)
				return
			}
			require.ErrorIs(t, err, ErrStrictCardEndpointUnsupported)
			require.Error(t, frameErr)
		})
	}
}

func TestFundingTextUpperBoundCoversActualTariffMatrix(t *testing.T) {
	catalog := NewPricingService(&config.Config{}, nil)
	require.NoError(t, catalog.loadPricingDataBytes(modelpricing.DefaultPricing(), "embedded funding test", time.Time{}))
	for _, model := range []string{"claude-sonnet-4", "gpt-4o", "deepseek-chat", "gpt-5.4"} {
		t.Run(model, func(t *testing.T) {
			billing := NewBillingService(&config.Config{}, catalog)
			group := &Group{ID: 7, RateMultiplier: 3, AllowOpenAILongContextBilling: true}
			bound, err := billing.FundingTextUpperBound(context.Background(), nil, group, model, 300_000, 1000, 3, 3)
			require.NoError(t, err)
			pricing, err := billing.GetModelPricing(model)
			require.NoError(t, err)
			for _, tier := range []string{"", "priority", "fast", "flex", OpenAIFastTierUltrafast} {
				for _, input := range []int{1, 271999, 300000} {
					for _, cache := range []UsageTokens{
						{InputTokens: input, OutputTokens: 3000},
						{InputTokens: input, OutputTokens: 3000, CacheReadTokens: input},
						{InputTokens: input, OutputTokens: 3000, CacheCreationTokens: input, CacheCreation5mTokens: input},
						{InputTokens: input, OutputTokens: 3000, CacheCreationTokens: input, CacheCreation1hTokens: input},
					} {
						actual := billing.computeTokenBreakdown(pricing, cache, 3, tier, true).ActualCost
						actual *= math.Max(1, maxReasoningEffortBillingMultiplier(model, "max", pricing))
						require.GreaterOrEqual(t, bound.MicroCents, int64(math.Ceil(actual*100_000_000)), "%s/%s/%d", model, tier, input)
					}
				}
			}
			free, err := billing.FundingTextUpperBound(context.Background(), nil, group, model, 100, 10, 1, 0)
			require.NoError(t, err)
			require.EqualValues(t, 1, free.MicroCents)
			_, err = billing.FundingTextUpperBound(context.Background(), nil, group, model, 100, 10, 1, math.Inf(1))
			require.Error(t, err)
		})
	}
}

// The control stub speaks the real wire contract over HTTP; production clients
// and Redis scripts participate instead of faking the admission result locally.
type fundingDispatchControl struct {
	mu           sync.Mutex
	auth         map[string]gatewaylite.FundingAuthorization
	reservations map[string]gatewaylite.OAuthInvocationReservation
	resizeProof  map[string]any
}

func fundingDispatchFixture(t *testing.T) (*OpenAIGatewayService, context.Context, *gatewaylite.ControlPlaneClientRef, *gatewaylite.RedisQuota, *fundingDispatchControl) {
	t.Helper()
	state := &fundingDispatchControl{auth: map[string]gatewaylite.FundingAuthorization{}, reservations: map[string]gatewaylite.OAuthInvocationReservation{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer test-internal" {
			w.WriteHeader(401)
			return
		}
		write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		switch r.URL.Path {
		case "/internal/billing/authorize":
			var request gatewaylite.AuthorizeFundingRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			a := gatewaylite.FundingAuthorization{AuthorizationID: "fund-" + request.RequestID, RequestID: request.RequestID, KeyID: request.KeyID, UserID: request.UserID,
				BillingSubjectType: "user", BillingSubjectID: request.UserID, GatewayCode: request.GatewayCode, Region: request.Region,
				EntryGatewayNodeID: request.EntryGatewayNodeID, EntryGroupID: request.EntryGroupID, RequestedModel: request.RequestedModel, Endpoint: request.Endpoint,
				ForceAuthorization: request.ForceAuthorization, Version: 2, Revision: 2, Status: "active", Disposition: "authorized", BoundKind: "bounded", BoundEvidence: request.BoundEvidence,
				UpperBoundMicroCents: request.UpperBoundMicroCents, ExecutionDeadlineMillis: request.ExecutionDeadlineMillis,
				Items: []gatewaylite.FundingAuthorizationItem{{ItemID: 1, Ordinal: 1, Source: "time_card", EntitlementID: 11, ReservedMicroCents: request.UpperBoundMicroCents}}}
			state.auth[a.RequestID] = a
			write(gatewaylite.FundingAuthorizationResponse{OK: true, Disposition: a.Disposition, Authorization: &a})
		case "/internal/billing/resize-authorization":
			var request gatewaylite.ResizeFundingAuthorizationRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			a := state.auth[request.RequestID].Clone()
			a.Version++
			a.UpperBoundMicroCents = request.UpperBoundMicroCents
			a.Items[0].ReservedMicroCents = request.UpperBoundMicroCents
			a.BoundEvidence = request.BoundEvidence
			a.LastOperationID = request.OperationID
			state.auth[a.RequestID] = a
			state.resizeProof = request.BoundEvidence
			write(gatewaylite.FundingAuthorizationResponse{OK: true, Authorization: &a})
		case "/internal/oauth/invocations/resolve":
			write(fundingDispatchCredential())
		case "/internal/oauth/invocations/reserve":
			var request gatewaylite.OAuthInvocationReserveRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			reservation := gatewaylite.OAuthInvocationReservation{ReservationID: "reserve-" + request.RequestID, DelegationID: request.DelegationID, RequestID: request.RequestID, KeyID: request.KeyID, GatewayCode: request.GatewayCode,
				EntryGatewayNodeID: request.EntryGatewayNodeID, EntryGroupID: request.EntryGroupID, RequestedModel: request.RequestedModel, Endpoint: request.Endpoint, UpperBoundMicroCents: request.UpperBoundMicroCents,
				ExecutionDeadlineMillis: request.ExecutionDeadlineMillis, Status: "reserved", Version: 1}
			state.reservations[request.RequestID] = reservation
			write(gatewaylite.OAuthInvocationReservationResponse{OK: true, Reservation: &reservation})
		case "/internal/oauth/invocations/bind", "/internal/oauth/invocations/resize":
			var request gatewaylite.OAuthInvocationResizeRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			reservation := state.reservations[request.RequestID]
			reservation.Version++
			reservation.Status = "active"
			reservation.FundingAuthorizationID = request.FundingAuthorizationID
			if request.UpperBoundMicroCents > 0 {
				reservation.UpperBoundMicroCents = request.UpperBoundMicroCents
			}
			state.reservations[request.RequestID] = reservation
			write(gatewaylite.OAuthInvocationReservationResponse{OK: true, Reservation: &reservation})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	client, err := gatewaylite.NewControlPlaneClient(server.URL, "test-internal", time.Second)
	require.NoError(t, err)
	control := gatewaylite.NewControlPlaneClientRef(client)
	mini := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	quota := gatewaylite.NewRedisQuota(redisClient, "dispatch-test")
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	billing := NewBillingService(cfg, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"bound-request":  {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
		"bound-upstream": {InputCostPerToken: 10e-6, OutputCostPerToken: 20e-6},
	}})
	svc := &OpenAIGatewayService{cfg: cfg, billingService: billing, resolver: NewModelPricingResolver(nil, billing), toolCorrector: NewCodexToolCorrector()}
	a := gatewaylite.FundingAuthorization{AuthorizationID: "fund-dispatch", RequestID: "dispatch", KeyID: "key-test", UserID: 42, BillingSubjectType: "user", BillingSubjectID: 42,
		GatewayCode: "gw-test", Region: "sg", EntryGatewayNodeID: 8, EntryGroupID: 80, RequestedModel: "bound-request", Endpoint: "/v1/responses", Status: "selected", Disposition: "authorization_required", Version: 1, ExecutionDeadlineMillis: time.Now().Add(time.Hour).UnixMilli()}
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{RequestID: a.RequestID, KeyID: a.KeyID, UserID: a.UserID, GatewayID: a.GatewayCode, Region: a.Region,
		EntryGatewayNodeID: a.EntryGatewayNodeID, EntryGlobalGroupID: a.EntryGroupID, RateMultiplier: 1, Funding: gatewaylite.NewRequestFunding(a, control, quota)})
	ctx = ContextWithExecutionPricing(ctx)
	ctx = context.WithValue(ctx, ctxkey.Group, &Group{ID: 7, RateMultiplier: 1, Platform: PlatformOpenAI})
	return svc, ctx, control, quota, state
}

func fundingDispatchCredential() *gatewaylite.OAuthInvocationResolveResponse {
	return &gatewaylite.OAuthInvocationResolveResponse{OK: true, DelegationID: "delegate-test", ExpiresAtMillis: time.Now().Add(time.Hour).UnixMilli(),
		Key:        &gatewaylite.KeySnapshot{KeyID: "key-test", UserID: 42, Status: "active", BillingSubjectType: "user", BillingSubjectID: 42, KeyDefaultGatewayNodeID: 8, Group: &gatewaylite.GatewayGroupSnapshot{ID: 80}},
		Invocation: &gatewaylite.OAuthInvocation{GatewayNodeID: 8, GroupID: 80, Models: []string{"bound-request"}, MaxCostCents: 1000, ExpiresIn: 3600, RateLimitRPM: 100, Concurrency: 10, CostConsent: true}}
}

func TestFundingFinalHTTPDispatchRequiresProtocolCapAndDoesNotChangeLegacy(t *testing.T) {
	cases := []struct {
		name, path, body string
		allowed          bool
	}{
		{"responses", "/v1/responses", `{"model":"bound-upstream","max_output_tokens":100,"input":"hello"}`, true},
		{"chat", "/v1/chat/completions", `{"model":"bound-upstream","max_completion_tokens":100,"messages":[]}`, true},
		{"messages", "/v1/messages", `{"model":"bound-upstream","max_tokens":100,"messages":[]}`, true},
		{"removed_limit", "/v1/responses", `{"model":"bound-upstream","input":"hello"}`, false},
		{"wrong_protocol_alias", "/v1/responses", `{"model":"bound-upstream","max_tokens":100}`, false},
		{"codex_internal", "/backend-api/codex/responses", `{"model":"bound-upstream","max_output_tokens":100}`, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			svc, ctx, _, _, _ := fundingDispatchFixture(t)
			var hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				body, _ := io.ReadAll(r.Body)
				require.JSONEq(t, tt.body, string(body))
				w.WriteHeader(200)
			}))
			defer upstream.Close()
			wrapped := newFundingHTTPUpstream(&openAIPriorityLongContextE2EHTTPUpstream{client: upstream.Client()}, svc.billingService, svc.resolver)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL+tt.path, strings.NewReader(tt.body))
			require.NoError(t, err)
			response, err := wrapped.Do(req, "", 1, 1)
			if tt.allowed {
				require.NoError(t, err)
				_ = response.Body.Close()
				require.EqualValues(t, 1, hits.Load())
				meta, _ := gatewaylite.RequestMetaFromContext(ctx)
				require.True(t, meta.Funding.Dispatched())
				a, _ := gatewaylite.FundingFromContext(ctx)
				require.Equal(t, "bound-request", a.RequestedModel)
				require.Equal(t, "text_max_output", a.BoundEvidence["kind"])
			} else {
				require.ErrorIs(t, err, ErrStrictCardEndpointUnsupported)
				require.Zero(t, hits.Load())
			}
			// A genuine no-card balance request retains the original endpoint behavior.
			req, err = http.NewRequestWithContext(context.Background(), http.MethodPost, upstream.URL+tt.path, strings.NewReader(tt.body))
			require.NoError(t, err)
			response, err = wrapped.Do(req, "", 1, 1)
			require.NoError(t, err)
			_ = response.Body.Close()
		})
	}
}

func TestFundingDelegatedFinalBodyProofAndMissingAdmission(t *testing.T) {
	svc, base, control, quota, state := fundingDispatchFixture(t)
	meta, _ := gatewaylite.RequestMetaFromContext(base)
	meta.Funding = nil
	ctx := gatewaylite.ContextWithRequestMeta(base, meta)
	ctx, err := gatewaylite.ContextWithOAuthInvocationCredential(ctx, "tokenav_oa_fixture", "gw-test", "sg", fundingDispatchCredential())
	require.NoError(t, err)
	body := []byte(`{"model":"bound-upstream","max_output_tokens":1000,"input":"expanded by conversion"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/v1/responses", bytes.NewReader(body))
	require.NoError(t, err)
	require.ErrorIs(t, (&fundingHTTPUpstream{}).admit(req), gatewaylite.ErrRequestQuotaUnavailable)
	require.ErrorIs(t, svc.EnsureFundingWebSocketPayload(ctx, &Account{Type: AccountTypeAPIKey, Platform: PlatformOpenAI}, body), gatewaylite.ErrRequestQuotaUnavailable)
	ctx = gatewaylite.ContextWithOAuthInvocationBoundProof(ctx, map[string]any{"kind": "text_max_output", "price_source": "gateway_runtime", "forwarded_body_sha256": "initial-body"})
	ctx, err = gatewaylite.EnsureOAuthInvocationUpperBound(ctx, control, quota, 1, "text:bound-request,output_tokens=1,n=1", "bound-request", "/v1/responses")
	require.NoError(t, err)
	require.NoError(t, ensureFinalFundingText(ctx, svc.billingService, svc.resolver, "/v1/responses", body))
	digest := sha256.Sum256(body)
	state.mu.Lock()
	defer state.mu.Unlock()
	require.Equal(t, hex.EncodeToString(digest[:]), state.resizeProof["forwarded_body_sha256"])
	require.EqualValues(t, 1000, state.resizeProof["max_output_tokens"])
	require.Greater(t, state.reservations["dispatch"].UpperBoundMicroCents, int64(1))
}

func TestFundingForwardHTTPAndWebSocketKeepActualOutputLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct{ ws, delegated bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("websocket_%v_delegated_%v", test.ws, test.delegated), func(t *testing.T) {
			ws := test.ws
			svc, ctx, control, quota, state := fundingDispatchFixture(t)
			if test.delegated {
				previousControl, previousQuota := gatewaylite.DefaultControlPlaneClientRef(), gatewaylite.DefaultRedisQuota()
				gatewaylite.SetDefaultControlPlaneClientRef(control)
				gatewaylite.SetDefaultRedisQuota(quota)
				t.Cleanup(func() {
					gatewaylite.SetDefaultControlPlaneClientRef(previousControl)
					gatewaylite.SetDefaultRedisQuota(previousQuota)
				})
				meta, _ := gatewaylite.RequestMetaFromContext(ctx)
				meta.Funding = nil
				ctx = gatewaylite.ContextWithRequestMeta(ctx, meta)
				var err error
				ctx, err = gatewaylite.ContextWithOAuthInvocationCredential(ctx, "tokenav_oa_fixture", "gw-test", "sg", fundingDispatchCredential())
				require.NoError(t, err)
			}
			observed := make(chan []byte, 1)
			updatePrice := func() {
				// 管理员在真实上游已经收到请求后改价；完成结算只能使用发送时冻结价。
				svc.billingService.pricingService.pricingData["bound-upstream"].InputCostPerToken = 100e-6
				svc.billingService.pricingService.pricingData["bound-upstream"].OutputCostPerToken = 200e-6
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response := map[string]any{"id": "resp-control", "model": "bound-upstream", "usage": map[string]any{"input_tokens": 12, "output_tokens": 7}}
				if ws {
					conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
					if err != nil {
						return
					}
					defer conn.Close()
					_, body, err := conn.ReadMessage()
					if err != nil {
						return
					}
					updatePrice()
					observed <- body
					_ = conn.WriteJSON(map[string]any{"type": "response.completed", "response": response})
					return
				}
				body, _ := io.ReadAll(r.Body)
				updatePrice()
				observed <- body
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer upstream.Close()
			svc.cfg.Gateway.OpenAIWS.Enabled = ws
			svc.cfg.Gateway.OpenAIWS.APIKeyEnabled = ws
			svc.cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = ws
			svc.cfg.Gateway.OpenAIWS.PrewarmGenerateEnabled = true
			svc.cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
			svc.cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
			svc.cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
			svc.openaiWSResolver = NewOpenAIWSProtocolResolver(svc.cfg)
			svc.httpUpstream = newFundingHTTPUpstream(&openAIPriorityLongContextE2EHTTPUpstream{client: upstream.Client()}, svc.billingService, svc.resolver)
			account := &Account{ID: 9, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "test-upstream", "base_url": upstream.URL, "model_mapping": map[string]any{"bound-request": "bound-upstream"}}, Extra: map[string]any{"responses_websockets_v2_enabled": ws}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			c.Set("api_key", &APIKey{ID: 1, User: &User{ID: 42}, Group: &Group{ID: 7, Platform: PlatformOpenAI, RateMultiplier: 1}})
			result, err := svc.Forward(ctx, c, account, []byte(`{"model":"bound-request","max_output_tokens":100,"input":"hello","stream":false}`))
			require.NoError(t, err)
			require.NotNil(t, result)
			// 用上游真实计量和脱离请求的价格副本核费用，不能采用完成后的热更新价。
			usageCtx := ContextWithExecutionPricingFromContext(context.Background(), c.Request.Context())
			cost, err := svc.calculateOpenAIRecordUsageTokenCost(usageCtx, &APIKey{}, "bound-upstream", 99,
				UsageTokens{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens}, "priority", nil)
			require.NoError(t, err)
			require.InDelta(t, 12*10e-6+7*20e-6, cost.ActualCost, 1e-12)
			authorization, ok := gatewaylite.FundingFromContext(c.Request.Context())
			require.True(t, ok)
			require.LessOrEqual(t, int64(math.Ceil(cost.ActualCost*100_000_000)), authorization.UpperBoundMicroCents)
			select {
			case body := <-observed:
				require.EqualValues(t, 100, gjson.GetBytes(body, "max_output_tokens").Int())
				require.Equal(t, "bound-upstream", gjson.GetBytes(body, "model").String())
				require.False(t, gjson.GetBytes(body, "generate").Exists(), "strict funding skips the extra prewarm execution")
				if test.delegated {
					digest := sha256.Sum256(body)
					state.mu.Lock()
					proof := state.resizeProof
					state.mu.Unlock()
					require.Equal(t, hex.EncodeToString(digest[:]), proof["forwarded_body_sha256"], "proof must describe the bytes actually forwarded after account mapping")
					require.EqualValues(t, 100, proof["max_output_tokens"])
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no bounded upstream request observed")
			}
		})
	}
}

func TestFundingInitialMappedHTTPKeepsClientConsentAndCardScope(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			svc, ctx, control, quota, state := fundingDispatchFixture(t)
			previousControl, previousQuota := gatewaylite.DefaultControlPlaneClientRef(), gatewaylite.DefaultRedisQuota()
			gatewaylite.SetDefaultControlPlaneClientRef(control)
			gatewaylite.SetDefaultRedisQuota(quota)
			t.Cleanup(func() {
				gatewaylite.SetDefaultControlPlaneClientRef(previousControl)
				gatewaylite.SetDefaultRedisQuota(previousQuota)
			})
			meta, _ := gatewaylite.RequestMetaFromContext(ctx)
			meta.Funding = nil
			ctx = gatewaylite.ContextWithRequestMeta(ctx, meta)
			ctx = WithClientRequestedResponseModel(ctx, "bound-request")
			var err error
			ctx, err = gatewaylite.ContextWithOAuthInvocationCredential(ctx, "tokenav_oa_fixture", "gw-test", "sg", fundingDispatchCredential())
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, path, nil).WithContext(ctx)
			c.Set("api_key", &APIKey{User: &User{ID: 42}, Group: &Group{ID: 7, RateMultiplier: 1, Platform: PlatformOpenAI}})
			body := []byte(`{"model":"bound-upstream","max_output_tokens":100,"messages":[],"input":"mapped request"}`)
			ctx, err = svc.ensureInitialOAuthTextFunding(ctx, c, &Account{Type: AccountTypeAPIKey, Platform: PlatformOpenAI}, body, "bound-upstream")
			require.NoError(t, err, "A-only consent must authorize an A-to-B mapped request")
			auth, ok := gatewaylite.FundingFromContext(ctx)
			require.True(t, ok)
			require.Equal(t, "bound-request", auth.RequestedModel)
			require.Equal(t, "time_card", auth.Items[0].Source)
			state.mu.Lock()
			require.Equal(t, "bound-request", state.reservations["dispatch"].RequestedModel)
			require.Equal(t, "bound-request", state.auth["dispatch"].RequestedModel)
			state.mu.Unlock()
		})
	}
}

func TestFundingDispatchFreezesPriceBeforeAdminUpdateAndStopsReplay(t *testing.T) {
	svc, ctx, _, _, _ := fundingDispatchFixture(t)
	body := []byte(`{"model":"bound-upstream","max_output_tokens":100,"input":"hello"}`)
	require.NoError(t, ensureFinalFundingText(ctx, svc.billingService, svc.resolver, "/v1/responses", body))
	before, err := svc.calculateOpenAIRecordUsageTokenCost(ctx, &APIKey{}, "bound-upstream", 1, UsageTokens{InputTokens: 100, OutputTokens: 50}, "", nil)
	require.NoError(t, err)
	svc.billingService.pricingService.pricingData["bound-upstream"].InputCostPerToken *= 2
	svc.billingService.pricingService.pricingData["bound-upstream"].OutputCostPerToken *= 2
	after, err := svc.calculateOpenAIRecordUsageTokenCost(ctx, &APIKey{}, "bound-upstream", 99, UsageTokens{InputTokens: 100, OutputTokens: 50}, "priority", nil)
	require.NoError(t, err)
	require.Equal(t, before.ActualCost, after.ActualCost)
	require.NoError(t, gatewaylite.PrepareFundingDispatch(ctx))
	require.ErrorIs(t, gatewaylite.PrepareFundingDispatch(ctx), gatewaylite.ErrRequestQuotaUnavailable)
	require.ErrorIs(t, gatewaylite.EnsureFundingUpperBound(ctx, 1, "text:bound-request"), gatewaylite.ErrRequestQuotaUnavailable)
}

func TestFundingFinalDispatchRejectsImagesWithoutFrozenMediaPrice(t *testing.T) {
	svc, ctx, _, _, _ := fundingDispatchFixture(t)
	a, _ := gatewaylite.FundingFromContext(ctx)
	a.Status, a.Disposition = "active", "authorized"
	a.UpperBoundMicroCents = 100_000_000
	a.BoundEvidence = map[string]any{"kind": "fixed_media"}
	a.Items = []gatewaylite.FundingAuthorizationItem{{ItemID: 1, Ordinal: 1, Source: "time_card", EntitlementID: 11, ReservedMicroCents: a.UpperBoundMicroCents}}
	meta, _ := gatewaylite.RequestMetaFromContext(ctx)
	meta.Funding = gatewaylite.NewRequestFunding(a, nil, nil)
	ctx = gatewaylite.ContextWithRequestMeta(ctx, meta)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/v1/images/generations", strings.NewReader(`{"model":"gpt-image-1","n":1}`))
	require.NoError(t, err)
	require.ErrorIs(t, (&fundingHTTPUpstream{billing: svc.billingService}).admit(req), ErrStrictCardEndpointUnsupported)
	require.False(t, meta.Funding.Dispatched())
}

func TestFundingPricingSnapshotDetachedStoreAndPerTurnIsolation(t *testing.T) {
	svc, parent, _, _, _ := fundingDispatchFixture(t)
	body := []byte(`{"model":"bound-upstream","max_output_tokens":100,"service_tier":"priority","reasoning":{"effort":"max"},"input":"hello"}`)
	require.NoError(t, ensureFinalFundingText(parent, svc.billingService, svc.resolver, "/v1/responses", body))
	original, ok := ExecutionPricingSnapshotFromContext(parent, "bound-upstream")
	require.True(t, ok)
	require.Equal(t, "priority", original.ServiceTier)
	require.Equal(t, "max", original.ReasoningEffort)
	detached := ContextWithExecutionPricingFromContext(context.Background(), parent)
	require.False(t, gatewaylite.HasRequestFunding(detached), "复制执行价不能复制资金执行权")
	parentStore := parent.Value(executionPricingContextKey{}).(*executionPricingStore)
	detachedStore := detached.Value(executionPricingContextKey{}).(*executionPricingStore)
	require.NotSame(t, parentStore, detachedStore)
	require.NotSame(t, &parentStore.mu, &detachedStore.mu)
	original.Resolved.BasePricing.InputPricePerToken = 999
	got, ok := ExecutionPricingSnapshotFromContext(detached, "bound-upstream")
	require.True(t, ok)
	require.NotEqual(t, float64(999), got.Resolved.BasePricing.InputPricePerToken)
	svc.billingService.pricingService.pricingData["bound-upstream"].InputCostPerToken *= 10
	next := ContextWithNewExecutionPricing(parent)
	_, exists := ExecutionPricingSnapshotFromContext(next, "bound-upstream")
	require.False(t, exists, "新轮次必须按本轮派发价准入")
	input := CostInput{Model: "bound-upstream", Resolved: svc.resolver.Resolve(next, PricingInput{Model: "bound-upstream"}), RateMultiplier: 1}
	updated, err := FreezeExecutionPricing(input)
	require.NoError(t, err)
	require.NoError(t, StoreExecutionPricingSnapshot(next, updated))
	require.NoError(t, StoreExecutionPricingSnapshot(parent, updated))
	first, _ := ExecutionPricingSnapshotFromContext(parent, "bound-upstream")
	require.Equal(t, got, first, "同轮首次最终派发价不得覆盖")
	nextPrice, _ := ExecutionPricingSnapshotFromContext(next, "bound-upstream")
	require.Greater(t, nextPrice.Resolved.BasePricing.InputPricePerToken, first.Resolved.BasePricing.InputPricePerToken)
}

func TestFundingResolvedCatalogWithNilChannelKeepsGroupAndSnapshot(t *testing.T) {
	svc, _, _, _, _ := fundingDispatchFixture(t)
	groupID := int64(7)
	var absent *ChannelService
	resolver := NewModelPricingResolver(absent, svc.billingService)
	resolved := resolver.Resolve(context.Background(), PricingInput{Model: "bound-upstream", GroupID: &groupID})
	require.NotNil(t, resolved.BasePricing)
	snapshot, err := FreezeExecutionPricing(CostInput{Model: "bound-upstream", Resolved: resolved, RateMultiplier: 2})
	require.NoError(t, err)
	input, err := snapshot.ApplyToCostInput(CostInput{Model: "bound-upstream", Tokens: UsageTokens{InputTokens: 10, OutputTokens: 20}})
	require.NoError(t, err)
	before, err := svc.billingService.CalculateCostUnified(input)
	require.NoError(t, err)
	svc.billingService.pricingService.pricingData["bound-upstream"].OutputCostPerToken *= 100
	after, err := svc.billingService.CalculateCostUnified(input)
	require.NoError(t, err)
	require.Equal(t, before, after, "Resolved-only 恢复不能回读实时目录")
}

func TestFundingImageUnknownRecoveryKeepsOriginalAuthorization(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{ID: "funded-image-task", UserID: 42, GatewayKeyID: "key-test", QuotaRequestID: "dispatch", State: ImageTaskStateRunning,
		ExecutionPhase: ImageTaskExecutionPhaseDispatched, SettlementState: ImageTaskSettlementPending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
		FundingSnapshot: &gatewaylite.FundingAuthorization{AuthorizationID: "original-funding", RequestID: "dispatch"}}
	billing := &imageTaskBillingStub{}
	executor := &imageTaskExecutorStub{}
	svc, err := NewImageTaskService(&imageTaskMemoryStore{task: task}, billing, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, executor, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)
	svc.process("recovered-1", task)
	svc.process("recovered-2", task)
	require.Zero(t, executor.executeCalls)
	require.Zero(t, billing.releaseCalls)
	require.Equal(t, ImageTaskSettlementPending, task.SettlementState)
	require.False(t, task.FinanciallyFinal())
	require.Equal(t, "execution_outcome_unknown", task.ErrorCode)
	require.Equal(t, "original-funding", task.FundingSnapshot.AuthorizationID)
	require.ErrorIs(t, (GatewayImageTaskBilling{}).Release(context.Background(), task), gatewaylite.ErrRequestQuotaUnavailable)
}
