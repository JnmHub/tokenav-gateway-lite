package service

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateAnthropicMsgIDUsesNativeFormat(t *testing.T) {
	pattern := regexp.MustCompile(`^msg_01[0-9A-Za-z]{22}$`)
	seen := make(map[string]struct{}, 128)
	for range 128 {
		id := generateAnthropicMsgID()
		require.Regexp(t, pattern, id)
		_, duplicated := seen[id]
		require.False(t, duplicated)
		seen[id] = struct{}{}
	}
}
