package service

import (
	"fmt"
	"strings"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"golang.org/x/mod/semver"
)

type PluginHostInfo struct {
	Version   string
	BuildType string
}

func EvaluatePluginCompatibility(manifest PluginManifest, host PluginHostInfo) PluginCompatibility {
	result := PluginCompatibility{
		CurrentSub2API:     host.Version,
		RequiredSub2API:    manifest.Requires.Sub2API,
		RecommendedSub2API: manifest.Requires.RecommendedSub2APIVersion,
		PluginProtocol:     manifest.Requires.PluginProtocol,
		TransportAPI:       manifest.Requires.TransportAPI,
		UIBridge:           manifest.Requires.UIBridge,
	}
	if manifest.Requires.PluginProtocol != pluginv1.ProtocolVersion ||
		manifest.Requires.TransportAPI != pluginv1.TransportAPIVersion ||
		manifest.Requires.UIBridge != pluginv1.UIBridgeVersion {
		result.Status = "incompatible"
		result.Message = "插件协议版本与当前 Sub2API 不兼容"
		return result
	}
	if !matchesSemverRange(host.Version, manifest.Requires.Sub2API) {
		result.Status = "incompatible"
		result.Message = fmt.Sprintf("当前 Sub2API %s 不满足插件要求 %s", host.Version, manifest.Requires.Sub2API)
		return result
	}
	result.Compatible = true
	for _, tested := range manifest.Requires.TestedSub2APIVersions {
		if normalizeSemver(tested) == normalizeSemver(host.Version) {
			result.Tested = true
			break
		}
	}
	if result.Tested {
		result.Status = "compatible"
		result.Message = "当前 Sub2API 版本已由插件声明测试"
	} else {
		result.Status = "untested"
		result.Message = "版本范围兼容，但插件未声明已测试当前 Sub2API 版本"
	}
	return result
}

func normalizeSemver(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}

// matchesSemverRange 判断 version 是否满足官方清单的 semver 范围表达式。
// 官方清单存在两种写法：">=0.2.5"（操作符紧贴版本）与 ">= 0.2.5"（空格分隔），
// 旧实现把空格分隔的操作符当成空操作数直接返回 false，导致官方插件永远
// incompatible；这里先归并「操作符 + 版本号」成对，再按 AND 语义逐条比较。
func matchesSemverRange(version, expression string) bool {
	v := normalizeSemver(version)
	if v == "" {
		return false
	}
	tokens := strings.Fields(strings.ReplaceAll(expression, ",", " "))
	if len(tokens) == 0 {
		return false
	}
	type semverConstraint struct {
		op    string
		bound string
	}
	var constraints []semverConstraint
	pendingOp := ""
	for _, token := range tokens {
		op := "="
		raw := token
		for _, candidate := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(token, candidate) {
				op = candidate
				raw = strings.TrimSpace(strings.TrimPrefix(token, candidate))
				break
			}
		}
		if raw == "" {
			// ">= 0.2.5"：操作符独立成 token，版本号在下一个 token。
			pendingOp = op
			continue
		}
		activeOp := op
		if pendingOp != "" {
			activeOp = pendingOp
			pendingOp = ""
		}
		bound := normalizeSemver(raw)
		if bound == "" {
			return false
		}
		constraints = append(constraints, semverConstraint{op: activeOp, bound: bound})
	}
	if len(constraints) == 0 {
		return false
	}
	for _, constraint := range constraints {
		comparison := semver.Compare(v, constraint.bound)
		matched := map[string]bool{
			">=": comparison >= 0,
			"<=": comparison <= 0,
			">":  comparison > 0,
			"<":  comparison < 0,
			"=":  comparison == 0,
		}[constraint.op]
		if !matched {
			return false
		}
	}
	return true
}
