package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func buildOpenAIResponsesURLForPlatform(platform, base string) string {
	if platform == PlatformDeepseek {
		return buildOpenAIEndpointURL(base, "/responses")
	}
	return buildOpenAIResponsesURL(base)
}

// DeepSeek / Kimi 原生 Responses 端点是无状态实现，不能把上一条响应编号传给上游。
// 客户端明确提供的 instructions 与 max_output_tokens 都属于原生字段，必须原样保留；
// 网关自己的默认 Codex 指令由请求构造阶段决定是否注入，不能在这里误删用户输入。
func normalizeDeepSeekResponsesRequestBody(account *Account, body []byte) []byte {
	if account == nil || !account.UsesNativeCNResponses() {
		return body
	}
	normalized, err := sjson.SetBytes(body, "store", false)
	if err != nil {
		return body
	}
	if stripped, err := sjson.DeleteBytes(normalized, "previous_response_id"); err == nil {
		normalized = stripped
	}
	return normalized
}

// readOpenAIUpstreamError 回卷响应体，后续端点错误处理仍可读取。
func (s *OpenAIGatewayService) readOpenAIUpstreamError(resp *http.Response) ([]byte, string) {
	respBody := s.readUpstreamErrorBody(resp)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
	return respBody, message
}

func (s *OpenAIGatewayService) failoverOpenAIUpstreamHTTPError(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	resp *http.Response,
	respBody []byte,
	upstreamMessage string,
	upstreamModel string,
) *UpstreamFailoverError {
	_ = c
	shouldFailover := s.shouldFailoverOpenAIUpstreamResponse(resp.StatusCode, upstreamMessage, respBody)
	if !shouldFailover {
		return nil
	}
	return s.buildOpenAIHTTPFailoverError(ctx, c, account, resp.StatusCode, resp.Header, respBody, upstreamMessage, upstreamModel, shouldFailover)
}

func parseSSEUsagePassthrough(data string, usage *ClaudeUsage) {
	if usage == nil || data == "" || data == "[DONE]" {
		return
	}
	parsed := gjson.Parse(data)
	switch parsed.Get("type").String() {
	case "message_start":
		value := parsed.Get("message.usage")
		if value.Exists() {
			usage.InputTokens = int(value.Get("input_tokens").Int())
			usage.CacheCreationInputTokens = int(value.Get("cache_creation_input_tokens").Int())
			usage.CacheReadInputTokens = int(value.Get("cache_read_input_tokens").Int())
			usage.CacheCreation5mTokens = int(value.Get("cache_creation.ephemeral_5m_input_tokens").Int())
			usage.CacheCreation1hTokens = int(value.Get("cache_creation.ephemeral_1h_input_tokens").Int())
		}
	case "message_delta":
		value := parsed.Get("usage")
		if value.Exists() {
			if tokens := value.Get("input_tokens").Int(); tokens > 0 {
				usage.InputTokens = int(tokens)
			}
			if tokens := value.Get("output_tokens").Int(); tokens > 0 {
				usage.OutputTokens = int(tokens)
			}
			if tokens := value.Get("cache_creation_input_tokens").Int(); tokens > 0 {
				usage.CacheCreationInputTokens = int(tokens)
			}
			if tokens := value.Get("cache_read_input_tokens").Int(); tokens > 0 {
				usage.CacheReadInputTokens = int(tokens)
			}
			if value.Get("cache_creation.ephemeral_5m_input_tokens").Exists() {
				tokens := value.Get("cache_creation.ephemeral_5m_input_tokens").Int()
				usage.CacheCreation5mTokens = int(tokens)
			}
			if value.Get("cache_creation.ephemeral_1h_input_tokens").Exists() {
				tokens := value.Get("cache_creation.ephemeral_1h_input_tokens").Int()
				usage.CacheCreation1hTokens = int(tokens)
			}
		}
	}
	if usage.CacheReadInputTokens == 0 {
		if tokens := parsed.Get("message.usage.cached_tokens").Int(); tokens > 0 {
			usage.CacheReadInputTokens = int(tokens)
		}
		if tokens := parsed.Get("usage.cached_tokens").Int(); usage.CacheReadInputTokens == 0 && tokens > 0 {
			usage.CacheReadInputTokens = int(tokens)
		}
	}
	if usage.CacheCreationInputTokens == 0 {
		usage.CacheCreationInputTokens = usage.CacheCreation5mTokens + usage.CacheCreation1hTokens
	}
}

func invalidNonStreamingJSONFailoverError(
	ctx context.Context,
	rateLimitService *RateLimitService,
	resp *http.Response,
	account *Account,
	body []byte,
	parseErr error,
	requestedModel ...string,
) error {
	const statusCode = http.StatusBadGateway
	logger.LegacyPrintf("service.gateway", "Account %d: CN upstream returned non-JSON 2xx response: %v", account.ID, parseErr)
	if rateLimitService != nil {
		if len(requestedModel) > 0 {
			rateLimitService.HandleUpstreamError(ctx, account, statusCode, resp.Header, body, requestedModel[0])
		} else {
			rateLimitService.HandleUpstreamError(ctx, account, statusCode, resp.Header, body)
		}
	}
	return &UpstreamFailoverError{
		StatusCode:             statusCode,
		ResponseBody:           body,
		ResponseHeaders:        resp.Header,
		RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(statusCode),
	}
}
