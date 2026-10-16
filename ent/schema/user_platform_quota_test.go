package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserPlatformQuotaValidatorAllowsCNProviders(t *testing.T) {
	t.Parallel()

	platformField := UserPlatformQuota{}.Fields()[1].Descriptor()
	require.Len(t, platformField.Validators, 3)
	platformValidator := platformField.Validators[2].(func(string) error)

	for _, platform := range []string{"kimi", "zhipu", "deepseek"} {
		require.NoError(t, platformValidator(platform), platform)
	}
	require.Error(t, platformValidator("unknown"))
}
