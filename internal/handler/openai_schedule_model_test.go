package handler

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMappedOpenAIAccountScheduleModelUsesForwardedChannelModel(t *testing.T) {
	groupID := int64(18)
	ctx := context.WithValue(context.Background(), ctxkey.Group, &service.Group{
		ID:       groupID,
		Platform: service.PlatformOpenAI,
		Status:   service.StatusActive,
		Hydrated: true,
	})
	account := &service.Account{
		Platform: service.PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-luna": "gpt-5.6-sol"},
		},
		AccountGroups: []service.AccountGroup{{
			GroupID:      groupID,
			ModelMapping: map[string]string{"gpt-5.6-luna": "gpt-5.6-terra"},
		}},
	}

	model := mappedOpenAIAccountScheduleModel(ctx, account, service.ChannelMappingResult{
		Mapped:      true,
		MappedModel: "gpt-5.6-luna",
	}, "public-alias")

	require.Equal(t, "gpt-5.6-terra", model)
}
