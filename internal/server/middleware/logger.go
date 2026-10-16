package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const successfulModelAccessLogSampleEvery uint64 = 100

var successfulModelAccessLogCounter atomic.Uint64

// Logger 请求日志中间件
func Logger(configs ...*config.Config) gin.HandlerFunc {
	var cfg *config.Config
	if len(configs) > 0 {
		cfg = configs[0]
	}
	return func(c *gin.Context) {
		// 开始时间
		startTime := time.Now()
		perfDiagnostics := strings.TrimSpace(c.GetHeader("X-Gateway-Perf-Diagnostics")) == "1"
		if perfDiagnostics {
			// Trailer（响应尾字段）必须在业务处理写出响应头前声明；只在隔离压测显式请求时启用，
			// 避免把内部性能数据暴露给普通客户请求。
			c.Writer.Header().Add("Trailer", "X-Gateway-Access-Log-Ms")
			// 客户端用 httptrace（HTTP 分段追踪）的 WroteRequest 时间减去这个入口时间，
			// 可区分“请求已写入本机 TCP，但还没进入 Gin”与网关内部处理；普通请求不计算或返回。
			c.Writer.Header().Set("X-Gateway-Handler-Started-At-Unix-Nano", strconv.FormatInt(startTime.UnixNano(), 10))
			c.Writer = &perfDiagnosticResponseWriter{ResponseWriter: c.Writer}
		}

		// 请求路径
		path := c.Request.URL.Path

		// 处理请求
		c.Next()

		// 跳过健康检查等高频探针路径的日志
		if path == "/health" || path == "/ready" || path == "/setup/status" {
			return
		}

		endTime := time.Now()
		latency := endTime.Sub(startTime)

		method := c.Request.Method
		statusCode := c.Writer.Status()
		// 与 API Key ACL、hybrid auth、后台会话和操作审计使用同一
		// trusted-proxy（可信代理）开关，避免日志记录原始转发头中的伪造地址。
		clientIP := ip.GetSecurityClientIP(c, cfg != nil && cfg.TrustForwardedIPForAPIKeyACL())
		protocol := c.Request.Proto
		clientRequestID, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
		accountID, hasAccountID := c.Request.Context().Value(ctxkey.AccountID).(int64)
		platform, _ := c.Request.Context().Value(ctxkey.Platform).(string)
		model, _ := c.Request.Context().Value(ctxkey.Model).(string)

		// 入口拒绝（ingress reject）处理：先向审计记录一次拒绝事实，再对高频
		// 认证失败类请求做进程级访问日志采样限幅，防止打无效 key 的流量刷爆日志。
		// 被采样丢弃的请求不再写 "http request completed"，改为每周期输出一条
		// 汇总日志（标记 ops_system_log_skip，避免进入运维系统日志通道）。
		reason, rejected := GetIngressRejectReason(c)
		if rejected {
			recordIngressReject(c, reason)
			allowed, droppedSummary := globalIngressRejectAccessSampler.allow(endTime)
			if droppedSummary > 0 {
				logger.FromContext(c.Request.Context()).Info("ingress rejection access logs dropped",
					zap.String("component", "http.access"),
					zap.Uint64("dropped_count", droppedSummary),
					zap.Bool(logger.OpsSystemLogSkipField, true),
				)
			}
			if !allowed {
				return
			}
		}

		fields := []zap.Field{
			zap.String("component", "http.access"),
			zap.Int("status_code", statusCode),
			zap.Int64("latency_ms", latency.Milliseconds()),
			zap.String("client_ip", clientIP),
			zap.String("protocol", protocol),
			zap.String("method", method),
			zap.String("path", path),
			zap.String("user_agent", boundedHeaderValue(c.GetHeader("User-Agent"), 512)),
			zap.Int64("content_length", c.Request.ContentLength),
			zap.String("host", boundedHeaderValue(c.Request.Host, 255)),
			zap.String("referer", boundedHeaderValue(c.GetHeader("Referer"), 512)),
			zap.String("x_request_id_header", boundedHeaderValue(c.GetHeader(requestIDHeader), 128)),
			zap.String("x_client_request_id_header", boundedHeaderValue(c.GetHeader(clientRequestIDHeader), 128)),
			zap.String("effective_client_request_id", strings.TrimSpace(clientRequestID)),
		}
		if rejected {
			// 入口拒绝的访问日志附带拒绝原因，并跳过运维系统日志通道，
			// 与官方 v0.2.5 对齐（拒绝日志保留在标准访问日志里便于排查）。
			fields = append(fields,
				zap.String("ingress_reject_reason", string(reason)),
				zap.Bool(logger.OpsSystemLogSkipField, true),
			)
		}
		if hasAccountID && accountID > 0 {
			fields = append(fields, zap.Int64("account_id", accountID))
		}
		if platform != "" {
			fields = append(fields, zap.String("platform", platform))
		}
		if model != "" {
			fields = append(fields, zap.String("model", model))
		}

		isSuccessfulModelRequest := statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices &&
			len(c.Errors) == 0 && platform != "" && model != ""
		if isSuccessfulModelRequest {
			// 成功模型请求已经有完整 usage（用量）、request log（请求日志）和账本证据；
			// access log 只保留 1% 运行样本，避免千并发同步争抢 stdout/file 锁。
			// 失败响应与 Gin error 始终完整记录，不能因性能优化损失故障审计。
			sequence := successfulModelAccessLogCounter.Add(1)
			if (sequence-1)%successfulModelAccessLogSampleEvery != 0 {
				if perfDiagnostics {
					c.Writer.Header().Set("X-Gateway-Access-Log-Ms", "0")
				}
				return
			}
			fields = append(fields, zap.Uint64("sample_every", successfulModelAccessLogSampleEvery))
		}

		l := logger.FromContext(c.Request.Context()).With(fields...)
		logStartedAt := time.Now()
		l.Info("http request completed", zap.Time("completed_at", endTime))
		if perfDiagnostics {
			logLatencyMS := float64(time.Since(logStartedAt).Microseconds()) / 1000
			c.Writer.Header().Set("X-Gateway-Access-Log-Ms", strconv.FormatFloat(logLatencyMS, 'f', 3, 64))
		}

		if len(c.Errors) > 0 {
			l.Warn("http request contains gin errors", zap.String("errors", c.Errors.String()))
		}
	}
}

type perfDiagnosticResponseWriter struct {
	gin.ResponseWriter
}

func (writer *perfDiagnosticResponseWriter) markResponseWrite() {
	if writer.Written() {
		return
	}
	writer.Header().Set("X-Gateway-Response-Write-At-Unix-Nano", strconv.FormatInt(time.Now().UnixNano(), 10))
}

func (writer *perfDiagnosticResponseWriter) WriteHeader(code int) {
	writer.markResponseWrite()
	writer.ResponseWriter.WriteHeader(code)
}

func (writer *perfDiagnosticResponseWriter) Write(data []byte) (int, error) {
	writer.markResponseWrite()
	return writer.ResponseWriter.Write(data)
}

func (writer *perfDiagnosticResponseWriter) WriteString(value string) (int, error) {
	writer.markResponseWrite()
	return writer.ResponseWriter.WriteString(value)
}

func boundedHeaderValue(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max]
}
