package repository

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorV2ErrorAggregationResolvesCompositePlatform(t *testing.T) {
	query := strings.ToLower(channelMonitorV2ErrorAggregationSQL)

	require.Contains(t, query, "g.platform = 'composite'")
	require.Contains(t, query, "left join groups g on g.id = current_error.group_id")
	require.Contains(t, query, "left join accounts a on a.id = current_error.account_id")
	require.Contains(t, query, "nullif(trim(a.platform), '')")
}
