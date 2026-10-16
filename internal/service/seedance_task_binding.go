package service

import (
	"context"
	"fmt"
	"strings"
)

// SeedanceTaskKeyPrefix 隔离 Seedance 任务的归属与计费键，避免与其他视频供应商串键。
const SeedanceTaskKeyPrefix = "seedance:"

// SeedanceTaskSessionHash 用 Ark 原始任务 ID 派生粘性绑定键。
//
// 关键坑：不能复用 GrokMediaVideoRequestSessionHash——它按 xAI 视频 request_id 校验，
// 而 SeedanceTaskKey 带 "seedance:" 前缀，校验必然失败并返回空串；
// 结果是账号绑定被静默跳过（BindStickySession 对空 hash 直接返回 nil），
// 查询/删除时永远解析不到创建时的账号。
func SeedanceTaskSessionHash(taskKey string) string {
	raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(taskKey), SeedanceTaskKeyPrefix))
	if raw == "" {
		return ""
	}
	return "seedance-video:" + DeriveSessionHashFromSeed(raw)
}

// BindSeedanceTaskAccount 记录 Seedance 任务与创建账号的绑定，查询/删除固定使用该账号。
func (s *OpenAIGatewayService) BindSeedanceTaskAccount(ctx context.Context, groupID *int64, taskKey string, accountID int64) error {
	return s.BindStickySession(ctx, groupID, SeedanceTaskSessionHash(taskKey), accountID)
}

// ResolveSeedanceTaskAccount 返回创建 Seedance 任务时绑定的账号 ID；未绑定返回 0。
func (s *OpenAIGatewayService) ResolveSeedanceTaskAccount(ctx context.Context, groupID *int64, taskKey string) (int64, error) {
	if s == nil || s.cache == nil {
		return 0, fmt.Errorf("seedance task binding cache is unavailable")
	}
	sessionHash := SeedanceTaskSessionHash(taskKey)
	if sessionHash == "" {
		return 0, fmt.Errorf("seedance task binding is invalid")
	}
	return s.getStickySessionAccountID(ctx, groupID, sessionHash)
}
