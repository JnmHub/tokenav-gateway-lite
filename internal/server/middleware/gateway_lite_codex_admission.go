package middleware

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/codexadmission"
	"github.com/Wei-Shaw/sub2api/internal/ingress"
	"github.com/gin-gonic/gin"
)

const (
	CodexAdmissionClientKey  = "codex_admission_client"
	CodexAdmissionVersionKey = "codex_admission_version"
	CodexAdmissionReasonKey  = "codex_admission_reason"
)

// GatewayLiteCodexClientAdmission executes before API-key resolution and
// lease reservation. Its runtime is an immutable in-memory snapshot, so the
// gateway hot path never performs a settings DB/Redis/remote lookup.
func GatewayLiteCodexClientAdmission(runtime *codexadmission.Runtime) gin.HandlerFunc {
	return gatewayLiteCodexClientAdmission(runtime, ingress.Default())
}

func gatewayLiteCodexClientAdmission(runtime *codexadmission.Runtime, aggregator *ingress.Aggregator) gin.HandlerFunc {
	return func(c *gin.Context) {
		if runtime == nil || c == nil || c.Request == nil || !codexAdmissionGatewayPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		decision := runtime.Evaluate(c.Request)
		if decision.Observed {
			// Only normalized family/version/reason values enter the request
			// context. Raw UA, originator, query strings and bodies are excluded.
			c.Set(CodexAdmissionClientKey, decision.Client)
			c.Set(CodexAdmissionVersionKey, decision.Version)
			c.Set(CodexAdmissionReasonKey, decision.Reason)
		}
		if !decision.ShouldDeny {
			c.Next()
			return
		}

		if aggregator != nil {
			aggregator.Record(ingress.RejectCodexClient)
		}
		AbortWithError(c, http.StatusForbidden, "CODEX_CLIENT_NOT_ALLOWED", codexAdmissionClientMessage(decision))
	}
}

func codexAdmissionGatewayPath(path string) bool {
	switch path {
	case "/responses", "/alpha/search", "/models", "/messages/count_tokens", "/chat/completions", "/embeddings":
		return true
	}
	if len(path) >= len("/v1/") && path[:len("/v1/")] == "/v1/" {
		return true
	}
	return len(path) >= len("/backend-api/codex") && path[:len("/backend-api/codex")] == "/backend-api/codex"
}

func codexAdmissionClientMessage(decision codexadmission.Decision) string {
	switch decision.Reason {
	case "version_too_low":
		return "This Codex client version is below the gateway minimum. Please update Codex."
	case "version_too_high":
		return "This Codex client version is above the gateway maximum."
	default:
		// Do not disclose exact fingerprint/allow-list rules to clients.
		return "This Codex client is not allowed by the gateway policy."
	}
}
