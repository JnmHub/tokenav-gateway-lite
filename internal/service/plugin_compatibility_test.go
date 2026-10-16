package service

import (
	"testing"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
)

// officialLikeManifest 构造与官方发行插件清单同构的 requires 块，
// 版本范围取自官方 CLIProxyAPI 传输插件的对齐结论（空格分隔写法）。
func officialLikeManifest() PluginManifest {
	return PluginManifest{
		Requires: PluginRequirements{
			Sub2API:               ">= 0.2.5",
			TestedSub2APIVersions: []string{"0.2.6", "0.2.7"},
			PluginProtocol:        pluginv1.ProtocolVersion,
			TransportAPI:          pluginv1.TransportAPIVersion,
			UIBridge:              pluginv1.UIBridgeVersion,
		},
	}
}

// 守卫：网关版本线对齐官方 0.2.7 后，官方发行插件必须落在
// compatible/tested，安装后即可直接启用。
func TestEvaluatePluginCompatibility_OfficialAlignmentVersion(t *testing.T) {
	result := EvaluatePluginCompatibility(officialLikeManifest(), PluginHostInfo{Version: "0.2.7"})
	if !result.Compatible || !result.Tested || result.Status != "compatible" {
		t.Fatalf("0.2.7 应判 compatible/tested，得到 %+v", result)
	}
	if result.CurrentSub2API != "0.2.7" {
		t.Fatalf("兼容结论应申报宿主版本 0.2.7，得到 %s", result.CurrentSub2API)
	}
}

// 守卫：范围解析必须同时支持官方清单的两种写法。
// 旧实现遇到 ">= 0.2.5"（空格分隔）直接判 false，官方插件永远 incompatible。
func TestMatchesSemverRange_ExpressionForms(t *testing.T) {
	cases := []struct {
		version    string
		expression string
		want       bool
	}{
		{"0.2.7", ">= 0.2.5", true},
		{"0.2.7", ">=0.2.5", true},
		{"0.1.179", ">= 0.2.5", false},
		{"0.2.7", ">= 0.2.5, < 0.3.0", true},
		{"0.3.0", ">= 0.2.5, < 0.3.0", false},
		{"0.2.7", "= 0.2.7", true},
		{"0.2.6", "= 0.2.7", false},
		{"v0.2.7", ">= 0.2.5", true},
	}
	for _, tc := range cases {
		if got := matchesSemverRange(tc.version, tc.expression); got != tc.want {
			t.Fatalf("matchesSemverRange(%q, %q) = %v, 期望 %v", tc.version, tc.expression, got, tc.want)
		}
	}
}

// 守卫：版本范围满足但清单未声明测试当前版本时，保持官方的
// compatible+untested 语义，启用仍需管理员 accept_untested 确认。
func TestEvaluatePluginCompatibility_UntestedRequiresConfirmation(t *testing.T) {
	manifest := officialLikeManifest()
	manifest.Requires.TestedSub2APIVersions = []string{"0.2.6"}
	result := EvaluatePluginCompatibility(manifest, PluginHostInfo{Version: "0.2.7"})
	if !result.Compatible || result.Tested || result.Status != "untested" {
		t.Fatalf("范围内未测试版本应判 compatible+untested，得到 %+v", result)
	}
}

// 守卫：协议版本不一致仍然 incompatible，版本对齐不放宽协议门禁。
func TestEvaluatePluginCompatibility_ProtocolMismatchStillIncompatible(t *testing.T) {
	manifest := officialLikeManifest()
	manifest.Requires.PluginProtocol = pluginv1.ProtocolVersion + 1
	result := EvaluatePluginCompatibility(manifest, PluginHostInfo{Version: "0.2.7"})
	if result.Compatible || result.Status != "incompatible" {
		t.Fatalf("协议版本不一致应判 incompatible，得到 %+v", result)
	}
}
