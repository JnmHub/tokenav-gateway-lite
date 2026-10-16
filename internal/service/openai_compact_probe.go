package service

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// AccountTestModeDefault drives the standard /responses connection test.
	AccountTestModeDefault = "default"
	// AccountTestModeCompact drives the native remote compaction v2 probe.
	AccountTestModeCompact = "compact"
)

func normalizeAccountTestMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case AccountTestModeCompact:
		return AccountTestModeCompact
	default:
		return AccountTestModeDefault
	}
}

// createOpenAICompactProbePayload builds the native v2 wire shape: a streaming
// /responses request whose final input item is a compaction trigger. The legacy
// unary /responses/compact endpoint is intentionally not used for this probe.
func createOpenAICompactProbePayload(model string, isOAuth bool) map[string]any {
	payload := map[string]any{
		"model":        strings.TrimSpace(model),
		"instructions": "You are a helpful coding assistant.",
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": "Respond with OK.",
			},
			map[string]any{"type": "compaction_trigger"},
		},
		"stream": true,
	}
	// ChatGPT's internal Responses API requires non-persistent Codex requests.
	if isOAuth {
		payload["store"] = false
	}
	return payload
}

// openAICompactProbeFoundCompactionItem verifies the capability itself instead
// of treating any 2xx response as success. It accepts the native SSE item,
// terminal response.output fallback, and JSON response fallback used by some
// compatible gateways.
func openAICompactProbeFoundCompactionItem(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	bodyText := string(body)
	if _, found := findRawCompactionItemFromSSE(bodyText); found {
		return true
	}
	if finalResponse, ok := extractCodexFinalResponse(bodyText); ok && responsesOutputHasCompactionItem(finalResponse) {
		return true
	}
	return responsesOutputHasCompactionItem(body)
}

func shouldMarkOpenAICompactUnsupported(status int, body []byte) bool {
	switch status {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	case http.StatusBadRequest, http.StatusForbidden, http.StatusUnprocessableEntity:
		lower := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body) + " " + string(body)))
		if strings.Contains(lower, "compact") {
			for _, keyword := range []string{
				"unsupported",
				"not support",
				"does not support",
				"not available",
				"disabled",
			} {
				if strings.Contains(lower, keyword) {
					return true
				}
			}
		}
	}
	return false
}

func buildOpenAICompactProbeExtraUpdates(resp *http.Response, body []byte, probeErr error, compactionFound bool, now time.Time) map[string]any {
	updates := map[string]any{
		"openai_compact_checked_at":  now.Format(time.RFC3339),
		"openai_compact_last_status": nil,
	}

	if resp != nil {
		updates["openai_compact_last_status"] = resp.StatusCode
	}

	switch {
	case probeErr != nil:
		updates["openai_compact_last_error"] = truncateString(sanitizeUpstreamErrorMessage(probeErr.Error()), 2048)
	case resp == nil:
		updates["openai_compact_last_error"] = "compact probe failed"
	default:
		errMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
		if errMsg == "" && len(body) > 0 {
			errMsg = strings.TrimSpace(string(body))
		}
		if errMsg == "" && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
			errMsg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		errMsg = truncateString(sanitizeUpstreamErrorMessage(errMsg), 2048)
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300 && compactionFound:
			updates["openai_compact_supported"] = true
			updates["openai_compact_last_error"] = ""
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			// A proxy can return a normal response after silently dropping the
			// trigger. Codex cannot compact that response, so this is unsupported.
			updates["openai_compact_supported"] = false
			updates["openai_compact_last_error"] = "upstream returned 2xx without a compaction output item (native remote compaction v2 unsupported)"
		default:
			if shouldMarkOpenAICompactUnsupported(resp.StatusCode, body) {
				updates["openai_compact_supported"] = false
			}
			updates["openai_compact_last_error"] = errMsg
		}
	}

	return updates
}

func mergeExtraUpdates(base map[string]any, more map[string]any) map[string]any {
	if len(base) == 0 && len(more) == 0 {
		return nil
	}
	out := make(map[string]any, len(base)+len(more))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range more {
		out[key] = value
	}
	return out
}

func compactProbeSessionID(accountID int64) string {
	// 探测请求是独立的临时会话：用真实时间的 UUIDv7（官方 session_id 格式），
	// 不需要跨调用稳定，反而应当像真实的一次性会话。
	return uuid.Must(uuid.NewV7()).String()
}
