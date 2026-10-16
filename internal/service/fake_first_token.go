package service

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)
// SettingKeyFakeFirstTokenSettings 伪造流式首字的总开关配置（JSON 存储）。
const SettingKeyFakeFirstTokenSettings = "fake_stream_first_token_settings"

// FakeFirstTokenSettings 伪造流式首字配置：
// 开启后，流式请求在等待上游首字节期间，按 [MinSeconds, MaxSeconds] 随机延迟
// 先向客户端发送一条 SSE 注释行（: fake-first-token），让客户端在真实上游
// 返回前就收到字节（伪装 TTFT 节奏，反检测行为层）。注释行对所有 SSE 客户端
// 无害（SDK 均忽略），不污染真实事件流。
type FakeFirstTokenSettings struct {
	Enabled    bool    `json:"enabled"`
	MinSeconds float64 `json:"min_seconds"` // 最小延迟（秒，支持小数）
	MaxSeconds float64 `json:"max_seconds"` // 最大延迟（秒，支持小数）
}

// fakeFirstTokenDefaults 默认值：关闭，0.3-1.2 秒。
func fakeFirstTokenDefaults() FakeFirstTokenSettings {
	return FakeFirstTokenSettings{Enabled: false, MinSeconds: 0.3, MaxSeconds: 1.2}
}

// Validate 校验并归一（min<=max、范围 0.01-30 秒；默认值兜底非法输入）。
func (f *FakeFirstTokenSettings) Validate() error {
	if f == nil {
		return fmt.Errorf("settings cannot be nil")
	}
	clamp := func(v float64, def float64) float64 {
		if v <= 0 || v > 30 {
			return def
		}
		return v
	}
	def := fakeFirstTokenDefaults()
	f.MinSeconds = clamp(f.MinSeconds, def.MinSeconds)
	f.MaxSeconds = clamp(f.MaxSeconds, def.MaxSeconds)
	if f.MinSeconds > f.MaxSeconds {
		f.MinSeconds, f.MaxSeconds = f.MaxSeconds, f.MinSeconds
	}
	return nil
}

type cachedFakeFirstToken struct {
	value     FakeFirstTokenSettings
	expiresAt int64
}

const fakeFirstTokenCacheTTL = 30 * time.Second

// GetFakeFirstTokenSettings 返回伪造首字配置（进程内缓存 30s，热路径零 DB 开销）。
func (s *SettingService) GetFakeFirstTokenSettings(ctx context.Context) FakeFirstTokenSettings {
	def := fakeFirstTokenDefaults()
	if s == nil {
		return def
	}
	if cached, ok := s.fakeFirstTokenCache.Load().(*cachedFakeFirstToken); ok && cached != nil && time.Now().UnixNano() < cached.expiresAt {
		return cached.value
	}
	value := def
	if s.settingRepo != nil {
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyFakeFirstTokenSettings)
		cancel()
		if err == nil && strings.TrimSpace(raw) != "" {
			var parsed FakeFirstTokenSettings
			if json.Unmarshal([]byte(raw), &parsed) == nil {
				if p := parsed; p.Validate() == nil {
					value = p
				}
			}
		}
	}
	s.fakeFirstTokenCache.Store(&cachedFakeFirstToken{value: value, expiresAt: time.Now().Add(fakeFirstTokenCacheTTL).UnixNano()})
	return value
}

// SetFakeFirstTokenSettings 保存伪造首字配置（立即刷新缓存，热生效免重启）。
func (s *SettingService) SetFakeFirstTokenSettings(ctx context.Context, settings FakeFirstTokenSettings) error {
	if err := (&settings).Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal fake first token settings: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyFakeFirstTokenSettings, string(data)); err != nil {
		return err
	}
	s.fakeFirstTokenCache.Store(&cachedFakeFirstToken{value: settings, expiresAt: time.Now().Add(fakeFirstTokenCacheTTL).UnixNano()})
	return nil
}

// fakeFirstTokenComment 是发给客户端的 SSE 注释行（含结尾空行）。
// 注释行以冒号开头，EventSource 与各 SDK 均忽略，协议安全。
const fakeFirstTokenComment = ": fake-first-token\n\n"

// FakeFirstTokenMsContextKey 是 gin context 键：handler 层在请求上游之前抢先
// 提交的伪造首字时刻（毫秒）。之所以放在 handler 层，是因为上游可能十几秒都
// 不返回响应头，服务层流式写入函数被调用时已经太晚。
const FakeFirstTokenMsContextKey = "gateway_fake_first_token_ms"

// StoreCommittedFakeFirstTokenMs 记录 handler 层已提交的伪造首字时刻。
func StoreCommittedFakeFirstTokenMs(c *gin.Context, ms int) {
	if c == nil {
		return
	}
	c.Set(FakeFirstTokenMsContextKey, ms)
}

// CommittedFakeFirstTokenMs 返回 handler 层已提交的伪造首字时刻；未提交为 nil。
// 服务层据此跳过重复排程，并把该时刻写入用量日志。
func CommittedFakeFirstTokenMs(c *gin.Context) *int {
	if c == nil {
		return nil
	}
	value, ok := c.Get(FakeFirstTokenMsContextKey)
	if !ok {
		return nil
	}
	ms, ok := value.(int)
	if !ok {
		return nil
	}
	return &ms
}

// noteFakeFirstTokenFromUpstreamLine 在流式扫描中记录链路上游发来的伪造首字
// 注释行到达时刻。中转场景（网关串网关）下注释行可能由上级先发，本节点自己的
// 定时器会被 claim 压制；但客户端体验以实际到达为准，用量审计要如实记录，
// 否则下游节点的用量里伪造首字永远为空。
func noteFakeFirstTokenFromUpstreamLine(line string, current **int, startTime time.Time) {
	if current == nil || *current != nil {
		return
	}
	if !strings.HasPrefix(line, ":") || !strings.Contains(line, "fake-first-token") {
		return
	}
	ms := int(time.Since(startTime).Milliseconds())
	*current = &ms
}

// CommitFakeFirstTokenToClient 在请求上游之前向流式客户端提交响应头并写出
// 伪造首字载荷：
//   - 入站为 chat completions 时写一条真实的 role 空首帧（data: 行）。注释行
//     按 SSE 规范不是数据帧，new-api / sub2api 等对接网关的「首字」统计只认
//     data: 行（new-api stream_scanner 对非 data: 行直接 continue），注释行
//     骗不过它们的报表；role 空首帧没有正文，最终客户端无感（本就是 OpenAI
//     流的正常第一帧形态），但会被对接方记为首字。
//   - 其他入站（/v1/responses、Anthropic /v1/messages）保持注释行：responses
//     协议事件顺序严格（Codex CLI 校验 id 与顺序），伪造协议帧风险高。
//
// 一旦提交，HTTP 状态码即固化为 200 + text/event-stream，后续失败只能以
// 流内事件表达（response.failed / event: error）。
//
// 返回 (是否已提交, 提交时刻毫秒)。
func CommitFakeFirstTokenToClient(c *gin.Context, startTime time.Time) (bool, int) {
	if c == nil || c.Writer == nil {
		return false, 0
	}
	if c.Writer.Written() {
		// 已经有人写过响应体，不再重复提交。
		return false, 0
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return false, 0
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
	frame := fakeFirstTokenComment
	if inboundPathIsChatCompletions(c) {
		frame = fakeFirstTokenChatRoleFrame(c)
	}
	n, err := c.Writer.WriteString(frame)
	if err != nil {
		logger.LegacyPrintf("service.fake_first_token", "[伪造首字] 抢先写出失败 err=%v", err)
		return false, 0
	}
	flusher.Flush()
	// 与流内 keepalive 同口径记账：注释行/role 空首帧都不算真实正文，不能
	// 阻止安全换号（failover 时新流会再发一个 role 帧，对客户端无感）。
	recordOpenAIStreamKeepaliveBytes(c, n)
	ms := int(time.Since(startTime).Milliseconds())
	StoreCommittedFakeFirstTokenMs(c, ms)
	return true, ms
}

// inboundPathIsChatCompletions 判断入站请求是否为 OpenAI chat completions
// 端点。与 handler 层 inboundIsResponses 同款后缀判断，覆盖多组路由变体
// （/v1/chat/completions、顶层 /chat/completions 等）。
func inboundPathIsChatCompletions(c *gin.Context) bool {
	if c == nil {
		return false
	}
	p := strings.TrimRight(c.FullPath(), "/")
	if p == "" && c.Request != nil && c.Request.URL != nil {
		p = strings.TrimRight(c.Request.URL.Path, "/")
	}
	if p == "" {
		return false
	}
	return strings.HasSuffix(p, "/chat/completions") || strings.Contains(p, "/chat/completions/")
}

// fakeFirstTokenChatRoleFrame 构造 chat completions 流式协议的标准空首帧
//（role 帧）。model 取客户请求声明的模型；id 用随机值模仿 OpenAI 形态
//（后续真实流的 id 与它不同——主流客户端与对接网关都按顺序聚合 delta、
// 不校验 chunk 间 id 一致性，因此无感）。
func fakeFirstTokenChatRoleFrame(c *gin.Context) string {
	model := ""
	if c != nil && c.Request != nil {
		model, _ = clientRequestedResponseModel(c.Request.Context())
	}
	payload := fmt.Sprintf(`{"id":%s,"object":"chat.completion.chunk","created":%d,"model":%s,"choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}]}`,
		mustJSONString(fakeChatCompletionID()),
		time.Now().Unix(),
		mustJSONString(model),
	)
	return "data: " + payload + "\n\n"
}

// fakeChatCompletionID 生成伪造 role 帧的 chunk id（模仿 OpenAI 形态）。
func fakeChatCompletionID() string {
	var b [12]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		// 随机源失败时退化为纳秒时间戳，仍然唯一。
		return "chatcmpl-fk" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "chatcmpl-fk" + hex.EncodeToString(b[:])
}

// fakeFirstTokenArmerKey 是 gin context 键：handler 层武装的伪造首字定时器。
const fakeFirstTokenArmerKey = "gateway_fake_first_token_armer"

// FakeFirstTokenArmer 是「请求上游之前先武装」的伪造首字定时器。
//
// 为什么要放在 handler 层：上游经常要十几秒才返回响应头，服务层的流式写入
// 函数被调用时已经晚于真实首字节，用户根本看不到伪造效果。于是 handler 在
// 真正请求上游之前武装定时器，随机延迟到点后若客户端仍未收到任何真实字节，
// 就抢先提交 200 + text/event-stream + 一行 SSE 注释行。
//
// claimed 与服务层共用同一把锁：服务层在开始写真实字节前调用
// ClaimFakeFirstTokenOutput，二者互斥，注释行永远不会插进真实 SSE 帧中间。
type FakeFirstTokenArmer struct {
	mu        sync.Mutex
	stopCh    chan struct{}
	stopped   bool
	claimed   bool
	committed bool
}

// ArmFakeFirstToken 武装一次伪造首字。返回的 stop 必须在请求结束时调用
// （gin 会回收 ResponseWriter，晚到的写入会污染其它响应）。
// 同一请求重复调用只武装一次。
func ArmFakeFirstToken(c *gin.Context, cfg FakeFirstTokenSettings, startTime time.Time) func() {
	noop := func() {}
	if c == nil || c.Writer == nil || !cfg.Enabled {
		return noop
	}
	if _, exists := c.Get(fakeFirstTokenArmerKey); exists {
		return noop
	}
	ch, stopTimer := ScheduleFakeFirstToken(cfg)
	if ch == nil {
		return noop
	}
	armer := &FakeFirstTokenArmer{stopCh: make(chan struct{})}
	c.Set(fakeFirstTokenArmerKey, armer)
	go func() {
		select {
		case <-ch:
			armer.tryCommit(c, startTime)
		case <-armer.stopCh:
		}
	}()
	return func() {
		stopTimer()
		armer.mu.Lock()
		armer.stopped = true
		armer.mu.Unlock()
		close(armer.stopCh)
	}
}

func (a *FakeFirstTokenArmer) tryCommit(c *gin.Context, startTime time.Time) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped || a.claimed || a.committed {
		return
	}
	if c == nil || c.Writer == nil || c.Writer.Written() {
		// 客户端已经收到真实字节（或响应已被提交）。
		return
	}
	if committed, _ := CommitFakeFirstTokenToClient(c, startTime); committed {
		a.committed = true
	} else {
		// 写失败（客户端断开等）后不再重试。
		a.claimed = true
	}
}

// ClaimFakeFirstTokenOutput 由服务层在开始向客户端写真实流式字节之前调用：
// 之后武装的定时器不再补发注释行，两者不会交叉写同一帧。
func ClaimFakeFirstTokenOutput(c *gin.Context) {
	armer := fakeFirstTokenArmerFrom(c)
	if armer == nil {
		return
	}
	armer.mu.Lock()
	armer.claimed = true
	armer.mu.Unlock()
}

// fakeFirstTokenArmerFrom 取出当前请求武装的定时器；未武装为 nil。
func fakeFirstTokenArmerFrom(c *gin.Context) *FakeFirstTokenArmer {
	if c == nil {
		return nil
	}
	value, ok := c.Get(fakeFirstTokenArmerKey)
	if !ok {
		return nil
	}
	armer, _ := value.(*FakeFirstTokenArmer)
	return armer
}

// FakeFirstTokenArmedByHandler 报告 handler 层是否已武装伪造首字。
// 服务层据此避免重复排程（两者同时写入会破坏 SSE 帧）。
func FakeFirstTokenArmedByHandler(c *gin.Context) bool {
	return fakeFirstTokenArmerFrom(c) != nil
}

// fakeFirstTokenSettingsFor 读取伪造首字配置（服务设置器不可用时返回默认关闭）。
// 供 OpenAI/Anthropic 网关服务共用。
func fakeFirstTokenSettingsFor(settingService *SettingService) FakeFirstTokenSettings {
	if settingService == nil {
		return fakeFirstTokenDefaults()
	}
	return settingService.GetFakeFirstTokenSettings(context.Background())
}

// FakeFirstTokenSettings 供 handler 层在上游请求之前读取伪造首字配置
// （handler 需要抢在服务层流式写入之前提交首字）。
func (s *OpenAIGatewayService) FakeFirstTokenSettings() FakeFirstTokenSettings {
	if s == nil {
		return fakeFirstTokenDefaults()
	}
	return fakeFirstTokenSettingsFor(s.settingService)
}

// FakeFirstTokenSettings 供 GatewayService（Anthropic/OAuth 路径）的 handler 读取。
func (s *GatewayService) FakeFirstTokenSettings() FakeFirstTokenSettings {
	if s == nil {
		return fakeFirstTokenDefaults()
	}
	return fakeFirstTokenSettingsFor(s.settingService)
}

// ScheduleFakeFirstToken 为一次流式请求安排伪造首字：
// 返回一次性触发通道（随机延迟后触发）。配置关闭或非流式时返回 nil。
// 触发后由调用方在事件循环内写出注释行并 Flush（同一 goroutine，无并发写风险）。
func ScheduleFakeFirstToken(cfg FakeFirstTokenSettings) (<-chan time.Time, func()) {
	if !cfg.Enabled {
		return nil, func() {}
	}
	delay := cfg.MinSeconds
	if cfg.MaxSeconds > cfg.MinSeconds {
		delay = cfg.MinSeconds + rand.Float64()*(cfg.MaxSeconds-cfg.MinSeconds)
	}
	timer := time.NewTimer(time.Duration(delay * float64(time.Second)))
	return timer.C, func() { timer.Stop() }
}
