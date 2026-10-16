package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

type gatewayCustomErrorMessageContextKey struct{}

// WithGatewayCustomErrorMessage freezes the setting for one client request.
// Internal errors and logs deliberately keep the original value; only the
// final protocol writer reads this context value.
func WithGatewayCustomErrorMessage(ctx context.Context, message string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return ctx
	}
	return context.WithValue(ctx, gatewayCustomErrorMessageContextKey{}, message)
}

func GatewayCustomErrorMessageFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	message, _ := ctx.Value(gatewayCustomErrorMessageContextKey{}).(string)
	return strings.TrimSpace(message)
}

func GatewayClientErrorMessage(ctx context.Context, original string) string {
	if message := GatewayCustomErrorMessageFromContext(ctx); message != "" {
		return message
	}
	return original
}

// RewriteGatewayClientErrorJSON replaces only human-readable message fields
// of a payload that is recognisably an error. Status/type/code and all other
// fields remain unchanged, so SDK retry and authentication behaviour is stable.
func RewriteGatewayClientErrorJSON(payload []byte, customMessage string) ([]byte, bool) {
	customMessage = strings.TrimSpace(customMessage)
	if customMessage == "" || len(payload) == 0 {
		return payload, false
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return payload, false
	}
	root, ok := value.(map[string]any)
	if !ok || !isGatewayErrorPayload(root) {
		return payload, false
	}
	if !replaceGatewayErrorMessages(root, customMessage) {
		return payload, false
	}
	rewritten, err := json.Marshal(root)
	if err != nil {
		return payload, false
	}
	return rewritten, true
}

func isGatewayErrorPayload(root map[string]any) bool {
	if rawError, ok := root["error"]; ok {
		switch typed := rawError.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return true
			}
		case map[string]any:
			if typed["message"] != nil || typed["code"] != nil || typed["type"] != nil || typed["status"] != nil {
				return true
			}
		}
	}
	typeName := strings.ToLower(strings.TrimSpace(gatewayErrorStringValue(root["type"])))
	if typeName == "error" || typeName == "response.failed" || strings.HasSuffix(typeName, ".error") {
		return true
	}
	status := strings.ToLower(strings.TrimSpace(gatewayErrorStringValue(root["status"])))
	if status == "failed" || status == "error" {
		return true
	}
	if response, ok := root["response"].(map[string]any); ok {
		responseStatus := strings.ToLower(strings.TrimSpace(gatewayErrorStringValue(response["status"])))
		if responseStatus == "failed" || responseStatus == "error" || gatewayNestedErrorPresent(response["error"]) {
			return true
		}
	}
	// Several middleware errors use the compact {code,message} layout.
	return root["code"] != nil && root["message"] != nil
}

func gatewayNestedErrorPresent(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case map[string]any:
		return typed["message"] != nil || typed["code"] != nil || typed["type"] != nil || typed["status"] != nil
	default:
		return false
	}
}

func gatewayErrorStringValue(value any) string {
	text, _ := value.(string)
	return text
}

func replaceGatewayErrorMessages(value any, customMessage string) bool {
	changed := false
	root, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if _, ok := root["message"].(string); ok {
		root["message"] = customMessage
		changed = true
	}
	if rawError, exists := root["error"]; exists {
		switch typed := rawError.(type) {
		case string:
			root["error"] = customMessage
			changed = true
		case map[string]any:
			// Some protocol errors expose only code/type. Add the human-readable
			// field so the configured client notice is still returned, while the
			// machine fields remain untouched for SDK retry decisions.
			typed["message"] = customMessage
			changed = true
		}
	}
	if response, ok := root["response"].(map[string]any); ok {
		if responseError, ok := response["error"].(map[string]any); ok {
			responseError["message"] = customMessage
			changed = true
		}
	}
	if !changed {
		// Type/status-only error envelopes have no dedicated error object. A
		// top-level message is the least invasive protocol-compatible fallback.
		root["message"] = customMessage
		changed = true
	}
	return changed
}

// RewriteGatewayClientErrorSSE rewrites complete SSE frames only. Normal token
// frames and incomplete chunks pass through byte-for-byte.
func RewriteGatewayClientErrorSSE(payload []byte, customMessage string) ([]byte, bool) {
	customMessage = strings.TrimSpace(customMessage)
	if customMessage == "" || len(payload) == 0 {
		return payload, false
	}
	parts := bytes.SplitAfter(payload, []byte("\n\n"))
	changed := false
	for index, part := range parts {
		if len(part) == 0 || (!bytes.HasSuffix(part, []byte("\n\n")) && index == len(parts)-1) {
			continue
		}
		lines := bytes.Split(part, []byte("\n"))
		for lineIndex, line := range lines {
			trimmed := bytes.TrimSpace(line)
			if !bytes.HasPrefix(trimmed, []byte("data:")) {
				continue
			}
			data := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
			rewritten, ok := RewriteGatewayClientErrorJSON(data, customMessage)
			if !ok {
				continue
			}
			prefixLen := bytes.Index(line, []byte("data:"))
			prefix := line[:prefixLen]
			lines[lineIndex] = append(append(append([]byte{}, prefix...), []byte("data: ")...), rewritten...)
			changed = true
		}
		parts[index] = bytes.Join(lines, []byte("\n"))
	}
	if !changed {
		return payload, false
	}
	return bytes.Join(parts, nil), true
}

// TruncateGatewayClientErrorReason keeps WebSocket close reasons valid UTF-8
// and within the protocol's byte limit.
func TruncateGatewayClientErrorReason(message string, maxBytes int) string {
	message = strings.TrimSpace(message)
	if maxBytes <= 0 || len(message) <= maxBytes {
		return message
	}
	message = message[:maxBytes]
	for !utf8.ValidString(message) && len(message) > 0 {
		message = message[:len(message)-1]
	}
	return message
}
