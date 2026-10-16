package service

import "time"

type AccountGroup struct {
	AccountID int64
	GroupID   int64
	Priority  int
	// ModelMapping 是当前账号在当前分组内的专属模型映射。它不会修改账号
	// Credentials 中的全局 model_mapping；未命中时才回退全局配置。
	ModelMapping map[string]string
	CreatedAt    time.Time

	Account *Account
	Group   *Group
}
