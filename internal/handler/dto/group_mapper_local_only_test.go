package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupFromServiceAdminExposesLocalOnlyToGatewayAdmin(t *testing.T) {
	group := GroupFromServiceAdmin(&service.Group{
		ID:        7,
		Name:      "gateway-local",
		Platform:  service.PlatformOpenAI,
		Status:    service.StatusActive,
		LocalOnly: true,
	})

	require.NotNil(t, group)
	require.True(t, group.LocalOnly)
}
