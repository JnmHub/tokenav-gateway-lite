package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type openAIGroupFailoverRepoStub struct {
	GroupRepository
	groups map[int64]*Group
}

func (r *openAIGroupFailoverRepoStub) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	group := r.groups[id]
	if group == nil {
		return nil, ErrGroupNotFound
	}
	cloned := *group
	return &cloned, nil
}

func openAIGroupFailoverTestService(target *Group) *OpenAIGatewayService {
	repo := &openAIGroupFailoverRepoStub{groups: map[int64]*Group{target.ID: target}}
	return &OpenAIGatewayService{channelService: &ChannelService{groupRepo: repo}}
}

func openAIGroupFailoverTestGroups(strategy string) (*Group, *Group) {
	targetID := int64(20)
	return &Group{
		ID: 10, Name: "A", Platform: PlatformOpenAI, Status: StatusActive,
		FailoverGroupID: &targetID, FailoverStrategy: strategy, FallbackModel: "gpt-fallback",
	}, &Group{ID: targetID, Name: "B", Platform: PlatformOpenAI, Status: StatusActive}
}

func TestResolveOpenAIGroupFailoverPlan_StrategyOrder(t *testing.T) {
	for _, testCase := range []struct {
		name             string
		strategy         string
		crossGroupFirst  bool
		sourceModelFirst bool
	}{
		{name: "default keeps original model", strategy: "", crossGroupFirst: true},
		{name: "explicit keeps original model", strategy: FailoverStrategyOriginalModelFirst, crossGroupFirst: true},
		{name: "current group first", strategy: FailoverStrategyCurrentGroupFirst, sourceModelFirst: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			source, target := openAIGroupFailoverTestGroups(testCase.strategy)
			plan, err := openAIGroupFailoverTestService(target).ResolveOpenAIGroupFailoverPlan(context.Background(), source, OpenAIGroupFailoverRequest{
				Endpoint: OpenAIGroupFailoverEndpointResponses,
			})
			require.NoError(t, err)
			require.True(t, plan.Eligible)
			require.Equal(t, target.ID, plan.TargetGroup.ID)
			require.Equal(t, testCase.crossGroupFirst, plan.TryCrossGroupBeforeSourceFallback)
			require.Equal(t, testCase.sourceModelFirst, plan.TrySourceFallbackBeforeCrossGroup)
		})
	}
}

func TestShouldOpenAIGroupFailover_OnlyRecoverableFailures(t *testing.T) {
	_, target := openAIGroupFailoverTestGroups("")
	plan := OpenAIGroupFailoverPlan{Eligible: true, TargetGroup: target}
	tests := []struct {
		name    string
		failure error
		want    bool
	}{
		{name: "no account", failure: ErrNoAvailableAccounts, want: true},
		{name: "gateway group unavailable", failure: &GatewayGroupUnavailableError{GroupID: 10, Cause: ErrNoAvailableAccounts}, want: true},
		{name: "deadline", failure: context.DeadlineExceeded, want: true},
		{name: "429", failure: &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}, want: true},
		{name: "500", failure: &UpstreamFailoverError{StatusCode: http.StatusInternalServerError}, want: true},
		{name: "503", failure: &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable}, want: true},
		{name: "400", failure: &UpstreamFailoverError{StatusCode: http.StatusBadRequest}},
		{name: "401", failure: &UpstreamFailoverError{StatusCode: http.StatusUnauthorized}},
		{name: "403 content", failure: &UpstreamFailoverError{StatusCode: http.StatusForbidden}},
		{name: "canceled", failure: context.Canceled},
		{name: "compact unavailable", failure: ErrNoAvailableCompactAccounts},
		{name: "explicit stop", failure: &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, NextAccountAction: NextAccountStop}},
		{name: "plain error", failure: errors.New("balance rejected")},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want, ShouldOpenAIGroupFailover(plan, testCase.failure))
		})
	}

}

func TestResolveOpenAIGroupFailoverPlan_RequestSafetyGates(t *testing.T) {
	source, target := openAIGroupFailoverTestGroups("")
	svc := openAIGroupFailoverTestService(target)
	base := OpenAIGroupFailoverRequest{Endpoint: OpenAIGroupFailoverEndpointResponses}
	tests := []struct {
		name   string
		mutate func(*OpenAIGroupFailoverRequest)
		reason string
	}{
		{name: "response started", mutate: func(r *OpenAIGroupFailoverRequest) { r.ResponseStarted = true }, reason: "response_started"},
		{name: "websocket", mutate: func(r *OpenAIGroupFailoverRequest) { r.WebSocket = true }, reason: "websocket_not_supported"},
		{name: "image", mutate: func(r *OpenAIGroupFailoverRequest) { r.Image = true }, reason: "media_not_supported"},
		{name: "video", mutate: func(r *OpenAIGroupFailoverRequest) { r.Video = true }, reason: "media_not_supported"},
		{name: "compact", mutate: func(r *OpenAIGroupFailoverRequest) { r.Compact = true }, reason: "compact_not_supported"},
		{name: "previous response", mutate: func(r *OpenAIGroupFailoverRequest) { r.PreviousResponseID = "resp_1" }, reason: "stateful_request_not_supported"},
		{name: "already failed over", mutate: func(r *OpenAIGroupFailoverRequest) { r.AlreadyFailedOver = true }, reason: "already_failed_over"},
		{name: "auth", mutate: func(r *OpenAIGroupFailoverRequest) { r.LocalRejection = OpenAIGroupFailoverLocalRejectionAuth }, reason: "local_rejection_auth"},
		{name: "balance", mutate: func(r *OpenAIGroupFailoverRequest) { r.LocalRejection = OpenAIGroupFailoverLocalRejectionBalance }, reason: "local_rejection_balance"},
		{name: "concurrency", mutate: func(r *OpenAIGroupFailoverRequest) { r.LocalRejection = OpenAIGroupFailoverLocalRejectionConcurrency }, reason: "local_rejection_concurrency"},
		{name: "content", mutate: func(r *OpenAIGroupFailoverRequest) { r.LocalRejection = OpenAIGroupFailoverLocalRejectionContent }, reason: "local_rejection_content_policy"},
		{name: "unsupported endpoint", mutate: func(r *OpenAIGroupFailoverRequest) { r.Endpoint = "/v1/images/generations" }, reason: "endpoint_not_supported"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := base
			testCase.mutate(&request)
			plan, err := svc.ResolveOpenAIGroupFailoverPlan(context.Background(), source, request)
			require.NoError(t, err)
			require.False(t, plan.Eligible)
			require.Equal(t, testCase.reason, plan.Reason)
		})
	}

	t.Run("keyword account policy", func(t *testing.T) {
		ctx := ContextWithKeywordProtectedAccountIDs(context.Background(), map[int64]struct{}{88: {}})
		plan, err := svc.ResolveOpenAIGroupFailoverPlan(ctx, source, base)
		require.NoError(t, err)
		require.False(t, plan.Eligible)
		require.Equal(t, "content_policy_account_scope", plan.Reason)
	})
	t.Run("keyword routing evidence", func(t *testing.T) {
		ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{RequestID: "req-keyword", UserID: 7, KeywordPolicyMatched: true})
		plan, err := svc.ResolveOpenAIGroupFailoverPlan(ctx, source, base)
		require.NoError(t, err)
		require.False(t, plan.Eligible)
		require.Equal(t, "content_policy_matched", plan.Reason)
	})
}

func TestApplyOpenAIGroupFailover_PreservesOriginalEvidenceAndBilling(t *testing.T) {
	source, target := openAIGroupFailoverTestGroups("")
	source.ModelsListConfig = GroupModelsListConfig{
		ResponseModelMode:   ResponseModelModeCustom,
		ResponseModelCustom: "source-public-model",
	}
	target.ModelsListConfig = GroupModelsListConfig{ResponseModelMode: ResponseModelModeUpstream}
	plan := OpenAIGroupFailoverPlan{
		Eligible: true, Strategy: FailoverStrategyOriginalModelFirst,
		SourceGroup: source, TargetGroup: target,
	}
	startedAt := time.Now().Add(-time.Second)
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "req-1", UserID: 7, GroupID: source.ID, GroupName: source.Name,
		OriginalGroupID: source.ID, OriginalGroupName: source.Name,
	})
	ctx = WithClientRequestedResponseModel(ctx, "client-model")
	ctx, originalPolicy, err := FreezeEffectiveRatePolicy(ctx, source, nil, startedAt)
	require.NoError(t, err)
	ctx = context.WithValue(ctx, ctxkey.Group, source)
	ctx = WithOpenAIGroupModelFallbackOnly(ctx)

	failedOverCtx, groupID, err := ApplyOpenAIGroupFailover(ctx, plan)
	require.NoError(t, err)
	require.Equal(t, target.ID, groupID)
	require.True(t, OpenAIGroupFailoverAttempted(failedOverCtx))
	require.False(t, openAIGroupModelFallbackOnly(failedOverCtx), "target group must start from the requested model")
	responsePolicy, responsePolicyOK := frozenClientResponseModelPolicyFromContext(failedOverCtx)
	require.True(t, responsePolicyOK)
	require.True(t, responsePolicy.rewrite)
	require.Equal(t, "source-public-model", responsePolicy.target, "target routing must not change the client response declaration")
	require.Same(t, target, failedOverCtx.Value(ctxkey.Group))

	meta, ok := gatewaylite.RequestMetaFromContext(failedOverCtx)
	require.True(t, ok)
	require.Equal(t, source.ID, meta.OriginalGroupID)
	require.Equal(t, target.ID, meta.GroupID)
	policy, ok := EffectiveRatePolicyFromContext(failedOverCtx)
	require.True(t, ok)
	require.Equal(t, originalPolicy, policy)

	restoredCtx, restoredID, err := RestoreOpenAIGroupForModelFallback(failedOverCtx, plan)
	require.NoError(t, err)
	require.Equal(t, source.ID, restoredID)
	require.True(t, OpenAIGroupFailoverAttempted(restoredCtx), "restoring A must not reopen the cross-group budget")
	require.True(t, openAIGroupModelFallbackOnly(restoredCtx), "restoring A must skip straight to its fallback model")
	require.Same(t, source, restoredCtx.Value(ctxkey.Group))
	restoredPolicy, ok := EffectiveRatePolicyFromContext(restoredCtx)
	require.True(t, ok)
	require.Equal(t, originalPolicy, restoredPolicy)

	_, _, err = ApplyOpenAIGroupFailover(restoredCtx, plan)
	require.Error(t, err, "one request may cross groups at most once")
}
