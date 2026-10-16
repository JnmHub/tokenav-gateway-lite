package service

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"golang.org/x/net/http/httpguts"
)

const openAICodexRoutingHintHeader = "x-codex-routing-hint"

// setOpenAICodexRoutingHint implements the Codex backend's advisory routing
// contract. model and serviceTier must be the final values after local mapping
// and priority policy, so the hint never disagrees with the actual request.
func setOpenAICodexRoutingHint(headers http.Header, account *Account, model string, serviceTier string) {
	if headers == nil {
		return
	}

	// 该头归网关所有：先清理所有大小写形式，避免调用方或 API Key 账号伪造提示。
	deleteOpenAIHeaderEqualFold(headers, openAICodexRoutingHintHeader)
	if account == nil || !account.IsOpenAIOAuth() {
		return
	}

	model = strings.TrimSpace(model)
	if model == "" || strings.ContainsAny(model, ";=") {
		return
	}

	canonicalTier := normalizedOpenAIServiceTierValue(serviceTier)
	switch canonicalTier {
	case OpenAIFastTierPriority, OpenAIFastTierFlex:
	default:
		canonicalTier = ""
	}

	hint := "model=" + model
	if canonicalTier != "" {
		hint += ";tier=" + canonicalTier
	}
	if !httpguts.ValidHeaderFieldValue(hint) {
		return
	}
	headers.Set(openAICodexRoutingHintHeader, hint)
}

func deleteOpenAIHeaderEqualFold(headers http.Header, name string) {
	if headers == nil {
		return
	}
	name = strings.TrimSpace(name)
	for key := range headers {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			delete(headers, key)
		}
	}
}

func setOpenAICodexRoutingHintFromBody(headers http.Header, account *Account, body []byte) {
	fields := gjson.GetManyBytes(body, "model", "service_tier")
	setOpenAICodexRoutingHint(headers, account, fields[0].String(), fields[1].String())
}
