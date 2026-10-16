package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type failingOpenAIModelAvailabilityRepo struct {
	AccountRepository
}

func (f failingOpenAIModelAvailabilityRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	return nil, errors.New("repo down")
}

func (f failingOpenAIModelAvailabilityRepo) ListSchedulableUngroupedByPlatform(context.Context, string) ([]Account, error) {
	return nil, errors.New("repo down")
}

// ListModelAvailabilityCandidates 补齐专用查询接口：DiagnoseModelAvailabilityForPlatform
// 会通过类型断言优先调用该方法，嵌入的 nil AccountRepository 不能替它兜底，必须显式
// 返回错误以触发保守回退分支。
func (f failingOpenAIModelAvailabilityRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]Account, error) {
	return nil, errors.New("repo down")
}

func TestOpenAIGatewayDiagnoseModelAvailability_ModelNotSupported(t *testing.T) {
	groupID := int64(7)
	svc := &OpenAIGatewayService{
		accountRepo: groupAwareStubOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: []Account{
			{
				ID:       1,
				Platform: PlatformOpenAI,
				GroupIDs: []int64{groupID},
				Credentials: map[string]any{
					"model_mapping": map[string]any{"gpt-5.4-mini": "gpt-5.4-mini"},
				},
			},
		}}},
	}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "gpt-5.9", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool)
	require.False(t, diag.HasModelSupport)
}

func TestOpenAIGatewayDiagnoseModelAvailability_ModelSupported(t *testing.T) {
	groupID := int64(7)
	svc := &OpenAIGatewayService{
		accountRepo: groupAwareStubOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: []Account{
			{
				ID:       1,
				Platform: PlatformOpenAI,
				GroupIDs: []int64{groupID},
				Credentials: map[string]any{
					"model_mapping": map[string]any{"gpt-5.*": "gpt-5.4-mini"},
				},
			},
		}}},
	}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "gpt-5.9", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool)
	require.True(t, diag.HasModelSupport)
}

func TestOpenAIGatewayDiagnoseModelAvailability_ResidualGrokMappingIsUnsupported(t *testing.T) {
	groupID := int64(7)
	svc := &OpenAIGatewayService{
		accountRepo: groupAwareStubOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: []Account{
			{
				ID:       1,
				Platform: PlatformOpenAI,
				GroupIDs: []int64{groupID},
				Credentials: map[string]any{
					"model_mapping": map[string]any{"grok-4.5": "grok-4.5"},
				},
			},
		}}},
	}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "grok-4.5", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool)
	require.False(t, diag.HasModelSupport)
}

func TestOpenAIGatewayDiagnoseModelAvailability_NoAccounts(t *testing.T) {
	groupID := int64(7)
	svc := &OpenAIGatewayService{
		accountRepo: groupAwareStubOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: nil}},
	}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "gpt-5.9", PlatformOpenAI)

	require.False(t, diag.HasAccountsInPool)
	require.False(t, diag.HasModelSupport)
}

func TestOpenAIGatewayDiagnoseModelAvailability_QueryFailureIsConservative(t *testing.T) {
	groupID := int64(7)
	svc := &OpenAIGatewayService{
		accountRepo: failingOpenAIModelAvailabilityRepo{},
	}

	diag := svc.DiagnoseModelAvailabilityForPlatform(context.Background(), &groupID, "gpt-5.9", PlatformOpenAI)

	require.True(t, diag.HasAccountsInPool)
	require.True(t, diag.HasModelSupport)
}
