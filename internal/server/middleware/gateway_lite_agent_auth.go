package middleware

import (
	"context"
	"crypto/subtle"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
)

// GatewayLiteAgentTurnAuthError 标识资金准入前的 Agent 权限拒绝；处理器只对
// 此类错误透传关闭状态，标准委托凭证仍按原恢复与补偿流程执行。
type GatewayLiteAgentTurnAuthError struct{ cause error }

func (e *GatewayLiteAgentTurnAuthError) Error() string { return e.cause.Error() }
func (e *GatewayLiteAgentTurnAuthError) Unwrap() error { return e.cause }

func gatewayLiteAgentTurnClose(status coderws.StatusCode, message string) error {
	return &GatewayLiteAgentTurnAuthError{cause: service.NewOpenAIWSClientCloseError(status, message, nil)}
}

// gatewayLiteAgentTurnAdmission 持有本连接已验证的入口身份；每个新轮次真实
// 回源，不能共享 HTTP 的并发解析或套用 invocation_token（委托凭证）流程。
// 不更新握手 APIKey 或本轮资金对象，避免异步结算读到后续撤销/改绑状态。
func gatewayLiteAgentTurnAdmission(control GatewayLiteControlPlane, localGroups GatewayLiteLocalGroupReader, entry gatewaylite.KeySnapshot, base gatewaylite.RequestMeta, entryGroup *service.Group, clientIP string, admit gatewaylite.TurnAdmission) gatewaylite.TurnAdmission {
	return func(ctx context.Context, requestID, model string) (gatewaylite.RequestMeta, error) {
		deny := func(message string) (gatewaylite.RequestMeta, error) {
			return gatewaylite.RequestMeta{}, gatewayLiteAgentTurnClose(coderws.StatusPolicyViolation, message)
		}
		key, err := gatewayLiteResolveKeyLive(ctx, control, entry.KeyID, base.Region, base.GatewayID, false, false)
		if err != nil {
			if _, rejected := err.(gatewayLiteResolveRejectedError); rejected {
				return deny("应用授权已失效或不允许本轮请求")
			}
			// 上游错误不携带地址或密钥摘要进入长连接日志/关闭消息。
			return gatewaylite.RequestMeta{}, gatewayLiteAgentTurnClose(coderws.StatusTryAgainLater, "应用授权核验暂不可用，请稍后重试")
		}
		if key == nil || key.TokenUse != "agent_api_key" || !key.Active() ||
			(key.ExpiresAt > 0 && time.Now().Unix() >= key.ExpiresAt) ||
			key.KeyID != entry.KeyID || key.LeaseSubject() != entry.LeaseSubject() ||
			subtle.ConstantTimeCompare([]byte(strings.ToLower(key.SecretHash)), []byte(strings.ToLower(entry.SecretHash))) != 1 {
			return deny("应用授权已失效，请重新授权")
		}
		if !gatewayLiteGatewayAllowed(*key, base.Region, base.GatewayID) ||
			key.KeyDefaultGatewayNodeID != base.EntryGatewayNodeID || key.Group.ID != base.EntryGlobalGroupID {
			return deny("应用授权的线路已变更，请重新连接")
		}
		view := gatewayLiteSnapshotToAPIKey("", *key)
		if entryGroup == nil || view.Group == nil || view.Group.ID != entryGroup.ID {
			return deny("应用授权的线路已变更，请重新连接")
		}
		if code, message := GatewayModelPermissionError(view, model); code != "" {
			return deny(message)
		}
		group := entryGroup
		if localGroups != nil {
			group, err = localGroups.GetGroup(ctx, view.Group.ID)
			if err != nil || group == nil {
				return gatewaylite.RequestMeta{}, gatewayLiteAgentTurnClose(coderws.StatusTryAgainLater, "线路配置暂不可用，请稍后重试")
			}
		}
		if !group.IsActive() || group.LocalOnly {
			return deny("应用授权的线路已不可用")
		}
		// 本地配置可能由多个请求共享，只在独立视图上合并主站的线路门槛。
		localView := *group
		if required := service.GroupRequiredTier(view.Group); required > service.GroupRequiredTier(group) {
			localView.EffectiveRequiredTier = required
		}
		view.Group = &localView
		if !service.GroupTierAllowedForUser(view.User, view.Group) {
			return deny("当前用户等级不允许此线路")
		}
		if code, message := GatewayModelPermissionError(view, model); code != "" {
			return deny(message)
		}
		if allowed, _ := ip.CheckIPRestrictionWithCompiledRules(clientIP, view.CompiledIPWhitelist, view.CompiledIPBlacklist); !allowed {
			return deny("应用密钥的 IP 策略已变更")
		}
		if current, ok := gatewaylite.RequestMetaFromContext(ctx); ok && current.GroupID != base.GroupID && key.KeywordRouteGroupAccessEnforced {
			allowed := false
			for _, groupID := range key.AllowedKeywordRouteGroupIDs {
				if groupID == current.GroupID {
					allowed = true
					break
				}
			}
			if !allowed {
				return deny("应用授权不再允许当前转路由线路")
			}
		}
		// 权限检查先于新的资金预留。已准入轮次保留原 reservation/funding，
		// 撤销只阻止下一轮；这里既不撤销旧预留，也不改旧结算主体。
		return admit(ctx, requestID, model)
	}
}
