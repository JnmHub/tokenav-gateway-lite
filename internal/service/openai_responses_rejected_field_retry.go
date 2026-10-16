package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const maxOpenAIResponsesRejectedFieldRetries = 1

var (
	openAIResponsesRejectedNamespaceParamPattern = regexp.MustCompile(`(?i)^input\[(\d+)\]\.namespace$`)
	openAIResponsesRejectedStatusParamPattern    = regexp.MustCompile(`(?i)^input\[(\d+)\]\.status$`)
	openAIResponsesRejectedMessageParamPattern   = regexp.MustCompile(`(?i)(?:unknown|unsupported)[ _-]+parameter\s*(?::|=|is)?\s*["']?(max_output_tokens|input\[\d+\]\.(?:namespace|status))(?:["']|\b)`)
)

type openAIResponsesRejectedFieldRetryDecision struct {
	Body  []byte
	Field string
	Rule  string
}

type openAIResponsesRejectedFieldRetryState struct {
	attempts       int
	seenBodyHashes map[[sha256.Size]byte]struct{}
	field          string
	rule           string
}

type openAIResponsesRejectedFieldRetryContextKey struct{}

func openAIResponsesRejectedFieldRetryStateForContext(ctx context.Context, initialBody []byte) (context.Context, *openAIResponsesRejectedFieldRetryState) {
	if ctx == nil {
		ctx = context.Background()
	}
	if state, ok := ctx.Value(openAIResponsesRejectedFieldRetryContextKey{}).(*openAIResponsesRejectedFieldRetryState); ok && state != nil {
		return ctx, state
	}
	state := newOpenAIResponsesRejectedFieldRetryState(initialBody)
	return context.WithValue(ctx, openAIResponsesRejectedFieldRetryContextKey{}, state), state
}

func newOpenAIResponsesRejectedFieldRetryState(initialBody []byte) *openAIResponsesRejectedFieldRetryState {
	state := &openAIResponsesRejectedFieldRetryState{
		seenBodyHashes: make(map[[sha256.Size]byte]struct{}, maxOpenAIResponsesRejectedFieldRetries+1),
	}
	state.remember(initialBody)
	return state
}

func (s *openAIResponsesRejectedFieldRetryState) Allow(decision openAIResponsesRejectedFieldRetryDecision) bool {
	if s == nil || len(decision.Body) == 0 || strings.TrimSpace(decision.Field) == "" ||
		s.attempts >= maxOpenAIResponsesRejectedFieldRetries {
		return false
	}
	bodyHash := sha256.Sum256(decision.Body)
	if _, seen := s.seenBodyHashes[bodyHash]; seen {
		return false
	}
	s.seenBodyHashes[bodyHash] = struct{}{}
	s.attempts++
	s.field = strings.TrimSpace(decision.Field)
	s.rule = strings.TrimSpace(decision.Rule)
	return true
}

func (s *openAIResponsesRejectedFieldRetryState) remember(body []byte) {
	if s == nil || len(body) == 0 {
		return
	}
	if s.seenBodyHashes == nil {
		s.seenBodyHashes = make(map[[sha256.Size]byte]struct{}, maxOpenAIResponsesRejectedFieldRetries+1)
	}
	s.seenBodyHashes[sha256.Sum256(body)] = struct{}{}
}

func (s *openAIResponsesRejectedFieldRetryState) attempted() bool {
	return s != nil && s.attempts > 0
}

func normalizeOpenAIResponsesRejectedFieldRetryBody(statusCode int, body, responseBody []byte) (openAIResponsesRejectedFieldRetryDecision, bool, error) {
	if statusCode != http.StatusBadRequest || len(body) == 0 || len(responseBody) == 0 || !gjson.ValidBytes(responseBody) {
		return openAIResponsesRejectedFieldRetryDecision{}, false, nil
	}

	// Only the official structured rejection codes are accepted. An upstream
	// message merely containing "unknown parameter" is untrusted text and must
	// never be allowed to delete customer data.
	code := strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(responseBody)))
	if code != "unknown_parameter" && code != "unsupported_parameter" {
		return openAIResponsesRejectedFieldRetryDecision{}, false, nil
	}

	param := strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, "error.param").String()))
	if param == "" {
		message := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(responseBody)))
		param = openAIResponsesRejectedParamFromMessage(message)
	}
	if index, ok := openAIResponsesRejectedNamespaceIndex(param); ok {
		return removeOpenAIResponsesRejectedNamespaceAtIndex(body, index)
	}
	if index, ok := openAIResponsesRejectedStatusIndex(param); ok {
		return removeOpenAIResponsesRejectedStatusAtIndex(body, index)
	}
	if param == "max_output_tokens" && gjson.GetBytes(body, "max_output_tokens").Exists() {
		retryBody, err := sjson.DeleteBytes(body, "max_output_tokens")
		if err != nil {
			return openAIResponsesRejectedFieldRetryDecision{}, false, fmt.Errorf("delete rejected max_output_tokens: %w", err)
		}
		return openAIResponsesRejectedFieldRetryDecision{
			Body:  retryBody,
			Field: "max_output_tokens",
			Rule:  "allowlist_top_level_optional",
		}, true, nil
	}
	return openAIResponsesRejectedFieldRetryDecision{}, false, nil
}

func openAIResponsesRejectedParamFromMessage(message string) string {
	match := openAIResponsesRejectedMessageParamPattern.FindStringSubmatch(strings.TrimSpace(message))
	if len(match) != 2 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(match[1]))
}

func openAIResponsesRejectedNamespaceIndex(param string) (int, bool) {
	match := openAIResponsesRejectedNamespaceParamPattern.FindStringSubmatch(strings.TrimSpace(param))
	if len(match) != 2 {
		return 0, false
	}
	index, err := strconv.Atoi(match[1])
	if err == nil && index >= 0 {
		return index, true
	}
	return 0, false
}

func openAIResponsesRejectedStatusIndex(param string) (int, bool) {
	match := openAIResponsesRejectedStatusParamPattern.FindStringSubmatch(strings.TrimSpace(param))
	if len(match) != 2 {
		return 0, false
	}
	index, err := strconv.Atoi(match[1])
	if err == nil && index >= 0 {
		return index, true
	}
	return 0, false
}

// removeOpenAIResponsesRejectedStatusAtIndex drops the status field the
// upstream rejected, and the status of every other input item sharing the
// rejected item's type.
//
// A replayed conversation can contain many items of the same type. Clearing
// only the index named by the upstream would require another upstream round
// trip for every item and exceed the single retry budget. Items with other
// types retain their status because the rejection does not apply to them.
func removeOpenAIResponsesRejectedStatusAtIndex(body []byte, index int) (openAIResponsesRejectedFieldRetryDecision, bool, error) {
	itemPath := fmt.Sprintf("input.%d", index)
	rejected := gjson.GetBytes(body, itemPath)
	if !rejected.IsObject() {
		return openAIResponsesRejectedFieldRetryDecision{}, false, nil
	}
	if !gjson.GetBytes(body, itemPath+".status").Exists() {
		return openAIResponsesRejectedFieldRetryDecision{}, false, nil
	}

	retryBody := body
	cleared := 0
	rejectedType := strings.TrimSpace(rejected.Get("type").String())
	if input := gjson.GetBytes(body, "input"); rejectedType != "" && input.IsArray() {
		// Removing object fields does not shift array indexes, so indexes from
		// the original request remain valid while applying each deletion.
		for itemIndex, item := range input.Array() {
			if !item.IsObject() || strings.TrimSpace(item.Get("type").String()) != rejectedType {
				continue
			}
			statusPath := fmt.Sprintf("input.%d.status", itemIndex)
			if !gjson.GetBytes(retryBody, statusPath).Exists() {
				continue
			}
			next, err := sjson.DeleteBytes(retryBody, statusPath)
			if err != nil {
				return openAIResponsesRejectedFieldRetryDecision{}, false, fmt.Errorf("delete rejected status at input[%d]: %w", itemIndex, err)
			}
			retryBody = next
			cleared++
		}
	}
	if cleared == 0 {
		// If the rejected item has no type, fall back to the exact index named
		// by the upstream rather than broadening the deletion scope.
		next, err := sjson.DeleteBytes(retryBody, itemPath+".status")
		if err != nil {
			return openAIResponsesRejectedFieldRetryDecision{}, false, fmt.Errorf("delete rejected status at input[%d]: %w", index, err)
		}
		retryBody = next
	}
	return openAIResponsesRejectedFieldRetryDecision{
		Body:  retryBody,
		Field: fmt.Sprintf("input[%d].status", index),
		Rule:  "allowlist_input_status_by_type",
	}, true, nil
}

func removeOpenAIResponsesRejectedNamespaceAtIndex(body []byte, index int) (openAIResponsesRejectedFieldRetryDecision, bool, error) {
	itemPath := fmt.Sprintf("input.%d", index)
	itemType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, itemPath+".type").String()))
	switch itemType {
	case "function_call", "tool_call", "custom_tool_call", "mcp_tool_call":
	default:
		return openAIResponsesRejectedFieldRetryDecision{}, false, nil
	}

	namespacePath := itemPath + ".namespace"
	if !gjson.GetBytes(body, namespacePath).Exists() {
		return openAIResponsesRejectedFieldRetryDecision{}, false, nil
	}
	retryBody, err := sjson.DeleteBytes(body, namespacePath)
	if err != nil {
		return openAIResponsesRejectedFieldRetryDecision{}, false, fmt.Errorf("delete rejected namespace at input[%d]: %w", index, err)
	}
	return openAIResponsesRejectedFieldRetryDecision{
		Body:  retryBody,
		Field: fmt.Sprintf("input[%d].namespace", index),
		Rule:  "allowlist_tool_call_namespace",
	}, true, nil
}

func appendOpenAIResponsesRejectedFieldRetryEvent(c *gin.Context, account *Account, state *openAIResponsesRejectedFieldRetryState, kind string, statusCode int, requestID, result string) {
	if c == nil || account == nil || state == nil || !state.attempted() {
		return
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		ProxyHost:          proxyHostForAccount(account),
		UpstreamStatusCode: statusCode,
		UpstreamRequestID:  requestID,
		Kind:               kind,
		Stage:              "responses_rejected_field_retry",
		Scope:              "same_account_once",
		Reason:             state.rule,
		Message:            "OpenAI allowlisted optional field retry",
		Detail:             fmt.Sprintf("removed_field=%s result=%s", state.field, strings.TrimSpace(result)),
	})
}
