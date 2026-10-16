package service

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

var responseModelSSEBenchmarkResult string

func TestResolveClientResponseModelPrecedence(t *testing.T) {
	t.Parallel()
	group := &Group{ModelsListConfig: GroupModelsListConfig{
		ResponseModelMode: ResponseModelModeCustom, ResponseModelCustom: "group-public-model",
	}}
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)

	target, rewrite := resolveClientResponseModel(ctx, &Account{}, "client-model")
	require.True(t, rewrite)
	require.Equal(t, "group-public-model", target)

	account := &Account{Extra: map[string]any{ResponseModelModeExtraKey: ResponseModelModeRequest}}
	target, rewrite = resolveClientResponseModel(ctx, account, "client-model")
	require.True(t, rewrite)
	require.Equal(t, "client-model", target)

	account.Extra[ResponseModelModeExtraKey] = ResponseModelModeUpstream
	target, rewrite = resolveClientResponseModel(ctx, account, "client-model")
	require.False(t, rewrite)
	require.Empty(t, target)
}

func TestNormalizeResponseModelPolicyRejectsInvalidCustom(t *testing.T) {
	t.Parallel()
	_, err := normalizeAccountResponseModelPolicyExtra(map[string]any{
		ResponseModelModeExtraKey: ResponseModelModeCustom,
	})
	require.Error(t, err)
	require.Error(t, validateGroupModelsListConfig(GroupModelsListConfig{ResponseModelMode: ResponseModelModeCustom}))
}

func TestRewriteResponseModelJSONCoversProtocolShapes(t *testing.T) {
	t.Parallel()
	payload := []byte("{\"model\":\"one\",\"response\":{\"model\":\"two\",\"modelVersion\":\"three\",\"response\":{\"modelVersion\":\"four\"}},\"message\":{\"model\":\"five\"},\"session\":{\"model\":\"session-upstream\",\"modelVersion\":\"session-version\"},\"modelVersion\":\"six\"}")
	rewritten := rewriteResponseModelJSON(payload, "public-model")
	require.JSONEq(t, "{\"model\":\"public-model\",\"response\":{\"model\":\"public-model\",\"modelVersion\":\"public-model\",\"response\":{\"modelVersion\":\"public-model\"}},\"message\":{\"model\":\"public-model\"},\"session\":{\"model\":\"public-model\",\"modelVersion\":\"public-model\"},\"modelVersion\":\"public-model\"}", string(rewritten))
}

func TestRewriteResponseModelJSONCoversUnicodeEscapedFieldName(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"\u006dodel":"upstream-model"}`)
	rewritten := rewriteResponseModelJSON(payload, "public-model")
	require.JSONEq(t, `{"model":"public-model"}`, string(rewritten))
	require.NotContains(t, string(rewritten), "upstream-model")
}

func TestRewriteResponseModelSSEBodyForClientUsesResolvedPolicy(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	beginClientResponseModelPolicy(c, context.Background(), &Account{Extra: map[string]any{
		ResponseModelModeExtraKey:   ResponseModelModeCustom,
		ResponseModelCustomExtraKey: "public-model",
	}}, "requested-model")

	body := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"upstream-model\"}}\n\ndata: [DONE]\n"
	rewritten := rewriteResponseModelSSEBodyForClient(c, nil, "requested-model", body)
	require.Contains(t, rewritten, `"model":"public-model"`)
	require.Contains(t, rewritten, "data: [DONE]")
}

func TestRewriteResponseModelSSELineForClientHandlesNoModelAndEscapedModel(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	beginClientResponseModelPolicy(c, context.Background(), &Account{Extra: map[string]any{
		ResponseModelModeExtraKey:   ResponseModelModeCustom,
		ResponseModelCustomExtraKey: "public-model",
	}}, "requested-model")

	noModel := `data: {"type":"response.output_text.delta","delta":"hello"}`
	require.Equal(t, noModel, rewriteResponseModelSSELineForClient(c, nil, "requested-model", noModel))

	escapedModel := `data: {"\u006dodel":"upstream-model"}`
	rewritten := rewriteResponseModelSSELineForClient(c, nil, "requested-model", escapedModel)
	require.JSONEq(t, `{"model":"public-model"}`, strings.TrimPrefix(rewritten, "data: "))
}

func TestClientRequestedResponseModelSurvivesMappedForwardModel(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	group := &Group{ModelsListConfig: GroupModelsListConfig{ResponseModelMode: ResponseModelModeRequest}}
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)
	ctx = WithClientRequestedResponseModel(ctx, "client-model")

	// 响应策略初始化时，请求体已经被映射为上游模型；声明目标仍必须保留客户原始模型。
	beginClientResponseModelPolicy(c, ctx, &Account{}, "upstream-model")
	body := "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"upstream-model\"}}\n\n"
	rewritten := rewriteResponseModelSSEBodyForClient(c, nil, "upstream-model", body)
	require.Contains(t, rewritten, `"model":"client-model"`)
}

func TestWithClientRequestedResponseModelPrefersCompositePublicModel(t *testing.T) {
	t.Parallel()
	ctx := WithCompositeRouteDecision(context.Background(), CompositeRouteDecision{
		Matched:        true,
		PublicModel:    "gpt-5.6-sol",
		TargetPlatform: PlatformOpenAI,
		UpstreamModel:  "gpt-5.6-luna",
		GroupID:        42,
	})

	// The composite middleware rewrites body.model before the handler sees it.
	// A later handler write must not replace the public model with that upstream ID.
	ctx = WithClientRequestedResponseModel(ctx, "gpt-5.6-luna")
	group := &Group{ModelsListConfig: GroupModelsListConfig{ResponseModelMode: ResponseModelModeRequest}}
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)

	beginClientResponseModelPolicy(c, ctx, &Account{}, "gpt-5.6-luna")
	target, rewrite := responseModelRewriteTarget(c, nil, "gpt-5.6-luna")
	require.True(t, rewrite)
	require.Equal(t, "gpt-5.6-sol", target)
}

func TestCompositePublicModelOverridesEarlierClientRequestedResponseModel(t *testing.T) {
	t.Parallel()
	ctx := WithClientRequestedResponseModel(context.Background(), "gpt-5.6-luna")
	ctx = WithCompositeRouteDecision(ctx, CompositeRouteDecision{
		Matched:        true,
		PublicModel:    "gpt-5.6-sol",
		TargetPlatform: PlatformOpenAI,
		UpstreamModel:  "gpt-5.6-luna",
		GroupID:        42,
	})

	requested, ok := clientRequestedResponseModel(ctx)
	require.True(t, ok)
	require.Equal(t, "gpt-5.6-sol", requested)
}

func TestClientRequestedResponseModelDoesNotOverrideAccountPolicy(t *testing.T) {
	t.Parallel()
	group := &Group{ModelsListConfig: GroupModelsListConfig{ResponseModelMode: ResponseModelModeRequest}}
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)
	ctx = WithClientRequestedResponseModel(ctx, "client-model")
	account := &Account{Extra: map[string]any{
		ResponseModelModeExtraKey:   ResponseModelModeCustom,
		ResponseModelCustomExtraKey: "public-model",
	}}

	target, rewrite := resolveClientResponseModel(ctx, account, "upstream-model")
	require.True(t, rewrite)
	require.Equal(t, "public-model", target)
}

func TestClientResponseModelForSyntheticOutputHonorsPolicy(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	newContext := func(ctx context.Context) *gin.Context {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		beginClientResponseModelPolicy(c, ctx, &Account{}, "mapped-upstream-model")
		return c
	}

	requestGroup := &Group{ModelsListConfig: GroupModelsListConfig{ResponseModelMode: ResponseModelModeRequest}}
	requestCtx := context.WithValue(context.Background(), ctxkey.Group, requestGroup)
	requestCtx = WithClientRequestedResponseModel(requestCtx, "client-request-model")
	require.Equal(t, "client-request-model", clientResponseModelForSyntheticOutput(newContext(requestCtx), nil, "mapped-upstream-model", "actual-upstream-model"))

	upstreamGroup := &Group{ModelsListConfig: GroupModelsListConfig{ResponseModelMode: ResponseModelModeUpstream}}
	upstreamCtx := context.WithValue(context.Background(), ctxkey.Group, upstreamGroup)
	require.Equal(t, "actual-upstream-model", clientResponseModelForSyntheticOutput(newContext(upstreamCtx), nil, "client-request-model", "actual-upstream-model"))

	customGroup := &Group{ModelsListConfig: GroupModelsListConfig{
		ResponseModelMode: ResponseModelModeCustom, ResponseModelCustom: "public-model",
	}}
	customCtx := context.WithValue(context.Background(), ctxkey.Group, customGroup)
	require.Equal(t, "public-model", clientResponseModelForSyntheticOutput(newContext(customCtx), nil, "client-request-model", "actual-upstream-model"))
}

func BenchmarkRewriteResponseModelSSELineForClient(b *testing.B) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	beginClientResponseModelPolicy(c, context.Background(), &Account{Extra: map[string]any{
		ResponseModelModeExtraKey:   ResponseModelModeCustom,
		ResponseModelCustomExtraKey: "public-model",
	}}, "requested-model")

	benchmarks := []struct {
		name string
		line string
	}{
		{
			name: "no_model_delta",
			line: `data: {"type":"response.output_text.delta","delta":"hello"}`,
		},
		{
			name: "nested_model",
			line: `data: {"type":"response.completed","response":{"id":"resp_1","model":"upstream-model"}}`,
		},
	}

	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				responseModelSSEBenchmarkResult = rewriteResponseModelSSELineForClient(c, nil, "requested-model", benchmark.line)
			}
		})
	}
}
