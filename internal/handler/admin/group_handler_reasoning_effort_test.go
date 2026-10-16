package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateGroupRequestReasoningEffortMappingsTriState(t *testing.T) {
	var omitted UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{}`), &omitted))
	require.Nil(t, omitted.ReasoningEffortMappings)

	var cleared UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"reasoning_effort_mappings":[]}`), &cleared))
	require.NotNil(t, cleared.ReasoningEffortMappings)
	require.Empty(t, *cleared.ReasoningEffortMappings)

	var replaced UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"reasoning_effort_mappings":[{"from":"max","to":"xhigh"}]}`), &replaced))
	require.NotNil(t, replaced.ReasoningEffortMappings)
	require.Equal(t, "max", (*replaced.ReasoningEffortMappings)[0].From)
}
