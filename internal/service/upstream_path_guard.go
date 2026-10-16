package service

import (
	"fmt"
	"strings"
)

const (
	maxUpstreamPathSegmentLen = 128
	maxUpstreamPathSegments   = 8
)

// 路径片段会被直接拼进上游 URL；这里使用默认拒绝的闭集，避免遗漏新型分隔符。
func isSafeUpstreamPathSegmentByte(value byte) bool {
	switch {
	case value >= 'a' && value <= 'z', value >= 'A' && value <= 'Z', value >= '0' && value <= '9':
		return true
	case value == '_', value == '-', value == '.':
		return true
	default:
		return false
	}
}

func isSafeUpstreamPathSegment(segment string) bool {
	if segment == "" || len(segment) > maxUpstreamPathSegmentLen {
		return false
	}
	dotsOnly := true
	for index := 0; index < len(segment); index++ {
		if !isSafeUpstreamPathSegmentByte(segment[index]) {
			return false
		}
		if segment[index] != '.' {
			dotsOnly = false
		}
	}
	return !dotsOnly
}

func sanitizedUpstreamPathSuffix(raw string) (string, bool) {
	suffix := strings.TrimSpace(raw)
	if suffix == "" {
		return "", true
	}
	if !strings.HasPrefix(suffix, "/") {
		return "", false
	}
	segments := strings.Split(strings.TrimPrefix(suffix, "/"), "/")
	if len(segments) > maxUpstreamPathSegments {
		return "", false
	}
	for _, segment := range segments {
		if !isSafeUpstreamPathSegment(segment) {
			return "", false
		}
	}
	return suffix, true
}

func validateUpstreamPathSegment(kind, segment string) error {
	if isSafeUpstreamPathSegment(strings.TrimSpace(segment)) {
		return nil
	}
	// 不回显客户端原文，避免恶意路径进入日志或错误响应。
	return fmt.Errorf("invalid %s for upstream url path", kind)
}
