package handler

import (
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
)

// ErrSeedanceTaskNotFound 表示本节点没有该任务的创建归属记录。
// 上游 task_id 不能当跨用户凭证，缺失归属必须按不存在处理。
var ErrSeedanceTaskNotFound = errors.New("seedance task not found")

// resolveSeedanceTaskAccount 校验 Seedance（火山方舟 Ark）任务的归属并返回创建时的账号。
//
// 为什么不用 Grok 那套：Grok 视频任务在我方是 PostgreSQL 持久任务（`grok_video_tasks`），
// 由后台轮询按每秒视频价结算；Seedance 是 Ark 异步任务、按上游 completion_tokens 计费，
// 创建时 Redis 待结算快照直接保存原账号、本地或远程归属、原请求资金与倍率；
// 仅旧本地任务回退粘性绑定。查询/删除必须校验完整归属并固定原账号，
// 不写 Grok 数据库任务表，也不进入 Grok 的轮询与每秒计价链路。
func (h *OpenAIGatewayHandler) resolveSeedanceTaskAccount(
	c *gin.Context,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	requestID string,
) (*service.Account, error) {
	if h == nil || h.gatewayService == nil || apiKey == nil || c == nil {
		return nil, errors.New("seedance task ownership is unavailable")
	}
	owner, err := service.ResolveMediaTaskIdentity(c.Request.Context(), apiKey, subject.UserID)
	if err != nil {
		// 不完整或冲突的调用身份没有任务访问权，不把跨用户尝试伪装成缓存故障。
		return nil, ErrSeedanceTaskNotFound
	}
	pending, err := h.gatewayService.LoadSeedancePendingBilling(c.Request.Context(), requestID, owner)
	if err != nil {
		return nil, err
	}
	if pending == nil {
		return nil, ErrSeedanceTaskNotFound
	}
	accountID := pending.AccountID
	if pending.GroupID > 0 && (apiKey.GroupID == nil || pending.GroupID != *apiKey.GroupID) {
		return nil, ErrSeedanceTaskNotFound
	}
	if accountID <= 0 {
		// 旧本地任务只有粘性绑定；新任务把原账号与归属快照一起保存，避免绑定TTL先到期。
		accountID, err = h.gatewayService.ResolveSeedanceTaskAccount(c.Request.Context(), apiKey.GroupID, requestID)
		if err != nil {
			return nil, err
		}
	}
	if accountID <= 0 {
		return nil, ErrSeedanceTaskNotFound
	}
	return h.gatewayService.GetMediaTaskAccountByID(c.Request.Context(), accountID)
}
