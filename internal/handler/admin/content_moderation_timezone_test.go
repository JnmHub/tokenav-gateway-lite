package admin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseContentModerationDateUsesShanghaiForDateOnly(t *testing.T) {
	parsed, dateOnly, err := parseContentModerationDate("2026-08-04")
	require.NoError(t, err)
	require.True(t, dateOnly)
	require.Equal(t, "Asia/Shanghai", parsed.Location().String())
	require.Equal(t, time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC), parsed.UTC())

	end := contentModerationEndOfDay(parsed)
	require.Equal(t, time.Date(2026, 8, 4, 15, 59, 59, 999999999, time.UTC), end.UTC())
}

func TestParseContentModerationDateKeepsRFC3339AbsoluteTime(t *testing.T) {
	parsed, dateOnly, err := parseContentModerationDate("2026-08-04T00:30:00-07:00")
	require.NoError(t, err)
	require.False(t, dateOnly)
	require.Equal(t, time.Date(2026, 8, 4, 7, 30, 0, 0, time.UTC), parsed.UTC())
}
