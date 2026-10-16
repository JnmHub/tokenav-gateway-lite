//go:build unit

package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBackupS3ConnectionFailureResultContainsNoUpstreamDetail(t *testing.T) {
	raw, err := json.Marshal(backupS3ConnectionFailureResult())
	require.NoError(t, err)
	text := string(raw)
	require.Contains(t, text, "S3_CONNECTION_FAILED")
	for _, forbidden := range []string{
		"https://AKID:SECRET@storage.example/bucket?X-Amz-Signature=signed",
		"AKID", "SECRET", "X-Amz-Signature", "signed",
	} {
		require.NotContains(t, text, forbidden)
	}
}
