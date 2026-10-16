package service

import (
	"time"
)

// [tokenav 定制] 保留本地实现
//
// 本文件是 v0.2.5 合并时的**适配桥**。
//
// 背景：官方 v0.2.5 的异步图片任务实现位于 `image_task.go`，其构造入口为
// `NewImageTaskServiceWithResolver(store, resolve, ttl, executionTimeout)`，
// 其中 `ImageStorageResolver` 让后台设置的开关变更可以热生效。
//
// 我们仓库的异步图片任务实现是**本地定制版** `image_async_task.go`：
// 存储 / 计费 / 对象存储 / 执行器 / 加密器 / 配置全部作为显式构造参数注入。
// 两边对 `ImageTask` / `ImageTaskStore` / `ImageTaskService` 的定义互斥，
// 因此官方 `image_task.go` 在合并时被判为重复定义并**跳过**（保留我们的）。
//
// 但合并后的 `wire.go`（`ProvideImageTaskService`）已按官方签名调用
// `settings.Resolver()` 与 `NewImageTaskServiceWithResolver(...)`，
// 而 `ImageStorageSettingService.Resolver()` 的返回类型 `ImageStorageResolver`
// 原本只定义在被跳过的官方文件里 —— 所以这里补上类型与构造器，
// 把官方的「resolver 热生效」语义映射到我们的实现上。
//
// 设计取舍：本地 `image_async_task.go` 的启用条件是
// `cfg.Enabled && store/billing/objects/executor/encryptor 齐全`，
// 与本桥的 resolver 并不冲突。resolver 仅作为**额外**的运行时可判定来源保留，
// 不改变本地既有的启用与调度语义。

// ImageStorageResolver 返回当前生效的对象存储上传器与启用状态。
//
// 返回 enabled=false 表示「异步图片任务功能当前不可用」（总开关关闭，
// 或对象存储凭证不齐全），调用方应直接返回 404，不创建任务、不写 Redis。
// 签名与官方 v0.2.5 保持一致，供 `ImageStorageSettingService.Resolver()` 使用。
type ImageStorageResolver func() (uploader *ImageResultUploader, enabled bool)

// 本地实现使用的默认构造时长常量。
//
// 说明：`wire.go` 的 `ProvideImageTaskService` 只传 ttl 与 executionTimeout，
// 而本地构造器需要完整的 `ImageTaskConfig`；这里以 `DefaultImageTaskConfig()`
// 为基底，仅用传入的两个时长覆盖对应字段，避免与后台配置页的单位解释冲突。
const (
	// defaultImageTaskTTL 任务结果在存储中的保留时长（官方 wire 传参缺省值）。
	defaultImageTaskTTL = 24 * time.Hour
	// defaultImageTaskExecutionTimeout 单次上游生图执行的超时（官方 wire 传参缺省值）。
	defaultImageTaskExecutionTimeout = 10 * time.Minute
)

// NewImageTaskServiceWithResolver 以官方 v0.2.5 的签名构造异步图片任务服务。
//
// 与官方实现的差异（本地定制，见 image_async_task.go）：
// 本地服务需要 store / billing / objects / executor / encryptor / cfg 六项依赖。
// 本适配器只用 store 构造一个「可查询」的实例（其余依赖留空），
// 因此在 cfg.Enabled 为 false 时不会因为依赖缺失而构造失败。
//
// 若后续要完整启用异步图片任务，应在 wire 中改为直接调用
// `NewImageTaskService(...)` 并显式传入全部依赖，而不是依赖本适配器。
func NewImageTaskServiceWithResolver(store ImageTaskStore, resolve ImageStorageResolver, ttl, executionTimeout time.Duration) *ImageTaskService {
	cfg := DefaultImageTaskConfig()

	// 覆盖 wire 传入的两个时长；越界时回落到默认值，避免 Validate() 直接失败。
	if ttl >= time.Hour && ttl <= 90*24*time.Hour {
		cfg.ResultRetention = ttl
	}
	if executionTimeout >= 10*time.Second && executionTimeout <= time.Hour {
		cfg.ExecutionTimeout = executionTimeout
		// Validate() 要求 claim_ttl 至少比 execution_timeout 大 3 分钟。
		if want := cfg.ExecutionTimeout + 5*time.Minute; want > cfg.ClaimTTL {
			cfg.ClaimTTL = want
		}
	}

	// 保持与官方相同的「不返回 error」签名：构造失败时返回 nil，交由调用方判定。
	svc, err := NewImageTaskService(store, nil, nil, nil, nil, cfg)
	if err != nil || svc == nil {
		return nil
	}
	svc.storageResolver = resolve
	return svc
}

// StorageEnabled 返回对象存储解析器上报的启用状态。
//
// 未注入 resolver 时返回 true（不干扰本地既有的 cfg.Enabled 判定路径）。
func (s *ImageTaskService) StorageEnabled() bool {
	if s == nil || s.storageResolver == nil {
		return true
	}
	_, enabled := s.storageResolver()
	return enabled
}
