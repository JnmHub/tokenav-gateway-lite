package handler

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/ingress"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
)

func extractMaxBytesError(err error) (*http.MaxBytesError, bool) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		// 未经过统一入口中间件的直接调用仍在这里计数；已经由受限 reader
		// 聚合过的请求只返回协议兼容 413，不重复加一。
		if !ingress.BodyTooLargeObserved(err) {
			ingress.Record(ingress.RejectBodyTooLarge)
		}
		return maxErr, true
	}
	// 所有调用点都位于读取或解析客户请求体失败的分支。这里只增加匿名聚合
	// 计数，不保存错误原文，避免 JSON 内容或字段值进入观测数据。
	if err != nil {
		ingress.Record(ingress.RejectMalformed)
	}
	return nil, false
}

func formatBodyLimit(limit int64) string {
	const mb = 1024 * 1024
	if limit >= mb {
		return fmt.Sprintf("%dMB", limit/mb)
	}
	return fmt.Sprintf("%dB", limit)
}

func buildBodyTooLargeMessage(limit int64) string {
	return fmt.Sprintf("Request body too large, limit is %s", formatBodyLimit(limit))
}

func readLenientJSONRequestBodyWithPrealloc(req *http.Request, cfg *config.Config) ([]byte, error) {
	return pkghttputil.ReadLenientJSONRequestBodyWithPrealloc(req, gatewayMaxBodySize(cfg))
}

func readLenientJSONTextRequestBodyWithPrealloc(req *http.Request, cfg *config.Config) ([]byte, error) {
	return pkghttputil.ReadLenientJSONRequestBodyWithPrealloc(req, gatewayTextMaxBodySize(cfg))
}

func gatewayMaxBodySize(cfg *config.Config) int64 {
	if cfg == nil {
		return 0
	}
	return cfg.Gateway.MaxBodySize
}

func gatewayTextMaxBodySize(cfg *config.Config) int64 {
	if cfg == nil {
		return 0
	}
	if cfg.Gateway.TextMaxBodySize > 0 {
		return cfg.Gateway.TextMaxBodySize
	}
	// 直接构造 Config 的旧测试或嵌入调用可能未经过 Load/Validate；
	// 此时沿用原 max_body_size，避免兼容调用被意外变成 0 字节上限。
	return cfg.Gateway.MaxBodySize
}

func decodedRequestBodyTooLarge(body []byte, maxBytes int64) error {
	if maxBytes > 0 && int64(len(body)) > maxBytes {
		return &http.MaxBytesError{Limit: maxBytes}
	}
	return nil
}
