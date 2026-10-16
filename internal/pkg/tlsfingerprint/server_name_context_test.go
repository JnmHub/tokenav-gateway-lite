package tlsfingerprint

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServerNameFromContextUsesPinnedOriginalHostname(t *testing.T) {
	ctx := WithServerName(context.Background(), " API.EXAMPLE.TEST ")
	require.Equal(t, "api.example.test", serverNameFromContext(ctx, "203.0.113.10"))
	require.Equal(t, "203.0.113.10", serverNameFromContext(context.Background(), "203.0.113.10"))
}
