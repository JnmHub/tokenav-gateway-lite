package xai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeModelMappingOptions(t *testing.T) {
	previous := RuntimeModelMappingOptions()
	t.Cleanup(func() { SetRuntimeModelMappingOptions(previous) })

	version := RuntimeModelMappingVersion()
	SetRuntimeModelMappingOptions(ModelMappingOptions{DefaultText: "grok-4.3", EnableCrossClientMap: true})

	require.Greater(t, RuntimeModelMappingVersion(), version)
	mapping := DefaultModelMapping()
	require.Equal(t, "grok-4.3", mapping["grok"])
	require.Equal(t, "grok-4.3", mapping["gpt-*"])
	require.Equal(t, "grok-4.3", mapping["claude-*"])
}

func TestResolveGrokTextResponsesModelIDUsesRuntimeFallbackForDefaultAliases(t *testing.T) {
	t.Parallel()

	// [tokenav 定制] 官方 v0.2.5 起 `grok` / `grok-latest` 这类「默认文本别名」不再
	// 固定解析为编译期 DefaultTextModel，而是回落到调用方通过 defaultText 传入的
	// 运行时默认模型（运行时改写见 SetRuntimeModelMappingOptions）。因此传入
	// "grok-4.3" 作为运行时默认时，默认别名必须原样回落到 "grok-4.3"。
	for _, alias := range []string{"", "grok", "grok-latest", "xai/grok"} {
		require.Equal(t, "grok-4.3", ResolveGrokTextResponsesModelID(alias, "grok-4.3"), alias)
	}
	// 显式版本别名不受运行时默认影响，按别名表解析为固定 canonical ID。
	require.Equal(t, "grok-build-0.1", ResolveGrokTextResponsesModelID("grok-build-latest", "grok-4.3"))
	require.Equal(t, "grok-4.5", ResolveGrokTextResponsesModelID("grok-4.5-latest", "grok-4.3"))
	require.Equal(t, "grok-4.6", ResolveGrokTextResponsesModelID("x-ai/grok-4.6-latest", "grok-4.3"))
	require.Equal(t, "Private-Model", ResolveGrokTextResponsesModelID("grok/Private-Model", "grok-4.3"))
}
