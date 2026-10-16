package admin

import (
	"encoding/json"
	"testing"

	"github.com/gin-gonic/gin/binding"
	"github.com/stretchr/testify/require"
)

func TestUpdateGroupRequestOptionalPriceDistinguishesOmittedAndClear(t *testing.T) {
	var omitted UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"grok"}`), &omitted))
	require.Nil(t, omitted.VideoPrice720P.ToServiceInput())

	var cleared UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"video_price_720p":null,"image_price_1k":"","web_search_price_per_call":null}`), &cleared))
	require.NotNil(t, cleared.VideoPrice720P.ToServiceInput())
	require.Equal(t, -1.0, *cleared.VideoPrice720P.ToServiceInput())
	require.Equal(t, -1.0, *cleared.ImagePrice1K.ToServiceInput())
	require.Equal(t, -1.0, *cleared.WebSearchPricePerCall.ToServiceInput())

	var configured UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"video_price_720p":0.14,"web_search_price_per_call":0.006}`), &configured))
	require.NotNil(t, configured.VideoPrice720P.ToServiceInput())
	require.Equal(t, 0.14, *configured.VideoPrice720P.ToServiceInput())
	require.Equal(t, 0.006, *configured.WebSearchPricePerCall.ToServiceInput())
}

func TestUpdateGroupRequestMinimumTierDistinguishesOmittedInheritAndT0(t *testing.T) {
	var omitted UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"openai"}`), &omitted))
	require.False(t, omitted.MinimumTier.set)
	require.Nil(t, omitted.MinimumTier.value)

	var inherited UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"minimum_tier":null}`), &inherited))
	require.True(t, inherited.MinimumTier.set)
	require.Nil(t, inherited.MinimumTier.value)

	var tier0 UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"minimum_tier":0}`), &tier0))
	require.True(t, tier0.MinimumTier.set)
	require.NotNil(t, tier0.MinimumTier.value)
	require.Equal(t, 0, *tier0.MinimumTier.value)
}

func TestGroupRequestsPreserveLocalOnlyTriState(t *testing.T) {
	var create CreateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"local","local_only":true}`), &create))
	require.True(t, create.LocalOnly)

	var omitted UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"local"}`), &omitted))
	require.Nil(t, omitted.LocalOnly)

	var disabled UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"local_only":false}`), &disabled))
	require.NotNil(t, disabled.LocalOnly)
	require.False(t, *disabled.LocalOnly)
}

func TestGroupRequestsAcceptGrokPlatform(t *testing.T) {
	require.NoError(t, binding.Validator.ValidateStruct(CreateGroupRequest{
		Name:     "Grok",
		Platform: "grok",
	}))
	require.NoError(t, binding.Validator.ValidateStruct(UpdateGroupRequest{
		Platform: "grok",
	}))
}

func TestGroupRequestsAcceptChineseProviderPlatforms(t *testing.T) {
	for _, platform := range []string{"kimi", "zhipu", "deepseek"} {
		t.Run(platform, func(t *testing.T) {
			require.NoError(t, binding.Validator.ValidateStruct(CreateGroupRequest{
				Name: "provider-group", Platform: platform,
			}))
			require.NoError(t, binding.Validator.ValidateStruct(UpdateGroupRequest{
				Platform: platform,
			}))
		})
	}
}
