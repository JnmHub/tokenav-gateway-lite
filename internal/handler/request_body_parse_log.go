package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

// logRequestBodyParseFailure records enough metadata to diagnose malformed or
// truncated JSON. Request content is deliberately excluded because gateway
// bodies may contain prompts, credentials and other private data.
func logRequestBodyParseFailure(reqLog *zap.Logger, body []byte, err error) {
	if reqLog == nil {
		return
	}
	if err == nil {
		err = service.DescribeInvalidJSON(body)
	}
	reqLog.Warn("parse request body failed",
		zap.Error(err),
		zap.Int("body_len", len(body)),
	)
}
