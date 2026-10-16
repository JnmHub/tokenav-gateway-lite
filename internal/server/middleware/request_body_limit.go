package middleware

import (
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/ingress"
	"github.com/gin-gonic/gin"
)

const requestBodyLimitRecordedContextKey = "gateway.request_body_limit_recorded"

// RequestBodyLimit 使用 MaxBytesReader 限制请求体大小，并只在本地 reader
// 确认超限时记录聚合指标。上游返回的 413 不经过该错误路径，因此不会被
// 误记为客户入口正文过大。
func RequestBodyLimit(maxBytes int64) gin.HandlerFunc {
	return requestBodyLimit(maxBytes, ingress.Default())
}

func requestBodyLimit(maxBytes int64, aggregator *ingress.Aggregator) gin.HandlerFunc {
	if aggregator == nil {
		aggregator = ingress.Default()
	}
	return func(c *gin.Context) {
		if c.Request == nil || c.Request.Body == nil {
			c.Next()
			return
		}
		var recordOnce sync.Once
		recordRejected := func() {
			recordOnce.Do(func() {
				// 纯文本路由会在通用多媒体限额内再套一层更小的 reader。
				// 两层都命中 Content-Length 时仍只能算一次入口拒绝，否则拒绝率
				// 可能超过 100%。
				if _, recorded := c.Get(requestBodyLimitRecordedContextKey); recorded {
					return
				}
				c.Set(requestBodyLimitRecordedContextKey, struct{}{})
				aggregator.Record(ingress.RejectBodyTooLarge)
			})
		}
		// 已知 Content-Length 可提前进入聚合，但仍让现有协议 handler 读取
		// MaxBytesReader 并返回各自兼容格式的 413，不在通用中间件猜协议。
		if maxBytes >= 0 && c.Request.ContentLength > maxBytes {
			recordRejected()
		}
		limited := http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Request.Body = &ingressObservedBody{
			ReadCloser: limited,
			onTooLarge: recordRejected,
		}
		c.Next()
	}
}

type ingressObservedBody struct {
	io.ReadCloser
	onTooLarge func()
}

func (b *ingressObservedBody) Read(buffer []byte) (int, error) {
	n, err := b.ReadCloser.Read(buffer)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) && b.onTooLarge != nil {
			b.onTooLarge()
			// handler 仍能 errors.As 到 MaxBytesError，但可识别本次拒绝已聚合，
			// 避免同一超限请求被算两次。
			err = ingress.MarkBodyTooLargeObserved(err)
		}
	}
	return n, err
}
