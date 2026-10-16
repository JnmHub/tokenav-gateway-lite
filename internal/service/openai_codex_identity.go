package service

import (
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
)

const (
	codexUpstreamMinVersion  = "0.144.0"
	codexClientVersionMaxLen = 64
)

var codexClientVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}(-[0-9A-Za-z.]+)?$`)

func NormalizeCodexClientVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || len(version) > codexClientVersionMaxLen || !codexClientVersionPattern.MatchString(version) {
		return ""
	}
	return version
}

func buildCodexCLIUserAgent(version string) string {
	if version = NormalizeCodexClientVersion(version); version == "" {
		return codexCLIUserAgent
	}
	return openai.CodexDefaultOriginator + "/" + version + codexCLIUserAgentSuffix
}

var codexIdentityEnforcement = func() *atomic.Bool {
	value := &atomic.Bool{}
	value.Store(true)
	return value
}()

func SetCodexIdentityEnforcementEnabled(enabled bool) {
	codexIdentityEnforcement.Store(enabled)
}

var (
	codexCanonicalUAMu       sync.RWMutex
	codexCanonicalUAResolver func() string
)

func SetCodexCanonicalUserAgentResolver(resolver func() string) {
	codexCanonicalUAMu.Lock()
	defer codexCanonicalUAMu.Unlock()
	codexCanonicalUAResolver = resolver
}

func codexCanonicalUserAgent() string {
	codexCanonicalUAMu.RLock()
	resolver := codexCanonicalUAResolver
	codexCanonicalUAMu.RUnlock()
	if resolver != nil {
		if userAgent := strings.TrimSpace(resolver()); userAgent != "" {
			return userAgent
		}
	}
	return codexCLIUserAgent
}

func codexCanonicalClientVersion() string {
	return codexClientVersionFromUA(codexCanonicalUserAgent())
}

type codexOutboundIdentity struct {
	userAgent  string
	originator string
	version    string
}

func resolveCodexOutboundIdentity(candidateUserAgent string) codexOutboundIdentity {
	canonical := codexCanonicalUserAgent()
	userAgent := strings.TrimSpace(candidateUserAgent)
	if userAgent == "" {
		userAgent = canonical
	}
	originator, pairedUserAgent, ok := openai.PairCodexClientIdentity(userAgent)
	if !ok {
		originator, pairedUserAgent, ok = openai.PairCodexClientIdentity(canonical)
	}
	if !ok {
		originator, pairedUserAgent = openai.CodexDefaultOriginator, codexCLIUserAgent
	}
	version := codexClientVersionFromUA(canonical)
	if rebuilt := openai.SetCodexUserAgentVersion(pairedUserAgent, version); rebuilt != "" {
		pairedUserAgent = rebuilt
	}
	return codexOutboundIdentity{userAgent: pairedUserAgent, originator: originator, version: version}
}

func codexOutboundIdentityHeaders(candidateUserAgent string) http.Header {
	identity := resolveCodexOutboundIdentity(candidateUserAgent)
	headers := make(http.Header, 3)
	headers.Set("user-agent", identity.userAgent)
	headers.Set("originator", identity.originator)
	headers.Set("version", identity.version)
	return headers
}

func codexClientVersionFromUA(userAgent string) string {
	version := NormalizeCodexClientVersion(openai.CodexUserAgentVersion(userAgent))
	if version == "" || CompareVersions(version, codexUpstreamMinVersion) < 0 {
		return codexCLIVersion
	}
	return version
}

func ensureCodexIdentityHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	identity := resolveCodexOutboundIdentity("")
	if strings.TrimSpace(headers.Get("user-agent")) == "" {
		headers.Set("user-agent", identity.userAgent)
	}
	if strings.TrimSpace(headers.Get("originator")) == "" {
		headers.Set("originator", identity.originator)
	}
	if strings.TrimSpace(headers.Get("version")) == "" {
		headers.Set("version", identity.version)
	}
	stripOpenAILegacyResponsesBeta(headers)
}

func applyOpenAICodexProbeHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	ensureCodexIdentityHeaders(headers)
	headers.Set("X-Codex-Window-ID", uuid.NewString())
}

func enforceCodexIdentityHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	enforceCodexIdentityHeadersWithUA(headers, headers.Get("user-agent"))
}

// enforceCodexIdentityHeadersWithUA 必须在账号 Header override 后调用，确保所有
// OAuth 出站路径最终使用同源的 User-Agent、originator 与 version。
func enforceCodexIdentityHeadersWithUA(headers http.Header, overrideUserAgent string) {
	if headers == nil || strings.TrimSpace(headers.Get("originator")) == "" {
		return
	}
	if !codexIdentityEnforcement.Load() {
		pairCodexIdentityHeaders(headers)
		return
	}
	identity := resolveCodexOutboundIdentity(overrideUserAgent)
	headers.Set("user-agent", identity.userAgent)
	headers.Set("originator", identity.originator)
	headers.Set("version", identity.version)
}

func pairCodexIdentityHeaders(headers http.Header) {
	originator, userAgent, ok := openai.PairCodexClientIdentity(headers.Get("user-agent"))
	if !ok {
		identity := resolveCodexOutboundIdentity("")
		originator, userAgent = identity.originator, identity.userAgent
		headers.Set("version", identity.version)
	}
	headers.Set("originator", originator)
	headers.Set("user-agent", userAgent)
	if version := strings.TrimSpace(headers.Get("version")); version != "" && CompareVersions(version, codexUpstreamMinVersion) < 0 {
		headers.Set("version", codexCLIVersion)
	}
}

// [v0.2.5 新增]
func CodexCanonicalClientVersion() string {
	return resolveCodexOutboundIdentity("").version
}
