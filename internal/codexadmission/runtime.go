package codexadmission

import (
	"errors"
	"math/bits"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const (
	ModeAuditOnly = "audit_only"
	ModeEnforce   = "enforce"

	ClientDesktop = "desktop"
	ClientCLI     = "cli"
	ClientVSCode  = "vscode"
	ClientApp     = "app_server"
)

var knownClients = []string{ClientDesktop, ClientCLI, ClientVSCode, ClientApp}

// Settings is a complete immutable client-admission snapshot. Lists only
// contain fixed client-family IDs; administrators cannot turn this into a
// free-form header matcher by mistake.
type Settings struct {
	Mode             string   `json:"mode"`
	MinVersion       string   `json:"min_version"`
	MaxVersion       string   `json:"max_version"`
	AllowedClients   []string `json:"allowed_clients"`
	DeniedClients    []string `json:"denied_clients"`
	AppServerEnabled bool     `json:"app_server_enabled"`
}

func DefaultSettings() Settings {
	return Settings{
		Mode:             ModeAuditOnly,
		AllowedClients:   append([]string(nil), knownClients...),
		DeniedClients:    []string{},
		AppServerEnabled: false,
	}
}

func KnownClients() []string { return append([]string(nil), knownClients...) }

func NormalizeSettings(input Settings) (Settings, error) {
	result := input
	result.Mode = strings.ToLower(strings.TrimSpace(result.Mode))
	if result.Mode == "" {
		result.Mode = ModeAuditOnly
	}
	if result.Mode != ModeAuditOnly && result.Mode != ModeEnforce {
		return Settings{}, errors.New("mode must be audit_only or enforce")
	}
	result.MinVersion = normalizeVersion(result.MinVersion)
	result.MaxVersion = normalizeVersion(result.MaxVersion)
	if strings.TrimSpace(input.MinVersion) != "" && result.MinVersion == "" {
		return Settings{}, errors.New("min_version must be a numeric semantic version")
	}
	if strings.TrimSpace(input.MaxVersion) != "" && result.MaxVersion == "" {
		return Settings{}, errors.New("max_version must be a numeric semantic version")
	}
	if result.MinVersion != "" && result.MaxVersion != "" && compareVersions(result.MinVersion, result.MaxVersion) > 0 {
		return Settings{}, errors.New("max_version must be greater than or equal to min_version")
	}

	allowed, err := normalizeClientList(result.AllowedClients)
	if err != nil {
		return Settings{}, err
	}
	denied, err := normalizeClientList(result.DeniedClients)
	if err != nil {
		return Settings{}, err
	}
	// deny always wins. Removing overlaps here makes the effective rule obvious
	// in both the API response and the administrator UI.
	denySet := sliceSet(denied)
	filteredAllowed := allowed[:0]
	for _, client := range allowed {
		if _, denied := denySet[client]; !denied {
			filteredAllowed = append(filteredAllowed, client)
		}
	}
	result.AllowedClients = append([]string(nil), filteredAllowed...)
	result.DeniedClients = denied
	return result, nil
}

func normalizeClientList(values []string) ([]string, error) {
	known := sliceSet(knownClients)
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := known[value]; !ok {
			return nil, errors.New("client list contains an unsupported preset")
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

type Decision struct {
	Observed    bool   `json:"observed"`
	Detected    bool   `json:"detected"`
	Client      string `json:"client,omitempty"`
	Version     string `json:"version,omitempty"`
	SignalCount int    `json:"signal_count"`
	Reason      string `json:"reason"`
	Violation   bool   `json:"violation"`
	ShouldDeny  bool   `json:"should_deny"`
	signalMask  uint8
}

type Count struct {
	Key   string `json:"key"`
	Count uint64 `json:"count"`
}

// Profile is a bounded, normalized client fingerprint tuple. It deliberately
// excludes raw User-Agent, originator, query strings and request bodies: the
// administrator only needs the coarse family, engine version and signal
// combination to validate an admission rule without retaining client data.
type Profile struct {
	Client  string   `json:"client"`
	Version string   `json:"version,omitempty"`
	Signals []string `json:"signals"`
	Count   uint64   `json:"count"`
}

type Snapshot struct {
	StartedAt       time.Time `json:"started_at"`
	Inspected       uint64    `json:"inspected"`
	Observed        uint64    `json:"observed"`
	Detected        uint64    `json:"detected"`
	AuditViolations uint64    `json:"audit_violations"`
	Denied          uint64    `json:"denied"`
	Clients         []Count   `json:"clients"`
	Reasons         []Count   `json:"reasons"`
	Signals         []Count   `json:"signals"`
	Versions        []Count   `json:"versions"`
	Profiles        []Profile `json:"profiles"`
}

var reasonKeys = []string{
	"allowed",
	"insufficient_signals",
	"account_policy_client",
	"client_type_unknown",
	"fingerprint_conflict",
	"client_denied",
	"client_not_allowed",
	"app_server_disabled",
	"version_missing",
	"version_signal_conflict",
	"version_too_low",
	"version_too_high",
}

var signalKeys = []string{"user_agent", "originator", "codex_path", "client_version", "codex_header"}

const maxNormalizedVersionBytes = 32

type compiledSettings struct {
	settings    Settings
	allowedMask uint8
	deniedMask  uint8
}

type versionSlot struct {
	mu      sync.Mutex
	version string
	count   uint64
}

type profileSlot struct {
	mu         sync.Mutex
	client     string
	version    string
	signalMask uint8
	count      uint64
}

// Runtime owns the active immutable settings and bounded in-memory telemetry.
// Evaluate never accesses a database, Redis, filesystem, or third-party API.
type Runtime struct {
	settings        atomic.Pointer[compiledSettings]
	started         time.Time
	inspected       atomic.Uint64
	observed        atomic.Uint64
	detected        atomic.Uint64
	auditViolations atomic.Uint64
	denied          atomic.Uint64
	clients         [4]atomic.Uint64
	reasons         [12]atomic.Uint64
	signals         [5]atomic.Uint64
	versions        [16]versionSlot
	profiles        [32]profileSlot
}

func NewRuntime(initial Settings) *Runtime {
	r := &Runtime{started: time.Now().UTC()}
	if err := r.Update(initial); err != nil {
		_ = r.Update(DefaultSettings())
	}
	return r
}

func (r *Runtime) Update(settings Settings) error {
	normalized, err := NormalizeSettings(settings)
	if err != nil {
		return err
	}
	compiled := &compiledSettings{settings: normalized}
	for _, client := range normalized.AllowedClients {
		compiled.allowedMask |= clientMask(client)
	}
	for _, client := range normalized.DeniedClients {
		compiled.deniedMask |= clientMask(client)
	}
	r.settings.Store(compiled)
	return nil
}

func (r *Runtime) Current() Settings {
	if r == nil {
		return DefaultSettings()
	}
	current := r.settings.Load()
	if current == nil {
		return DefaultSettings()
	}
	copy := current.settings
	copy.AllowedClients = append([]string(nil), current.settings.AllowedClients...)
	copy.DeniedClients = append([]string(nil), current.settings.DeniedClients...)
	return copy
}

func (r *Runtime) Evaluate(req *http.Request) Decision {
	if r == nil {
		return Decision{Reason: "allowed"}
	}
	r.inspected.Add(1)
	settings := r.settings.Load()
	if settings == nil {
		return Decision{Reason: "allowed"}
	}
	decision := evaluate(req, settings)
	if decision.Observed {
		r.observed.Add(1)
	}
	if decision.Detected {
		r.detected.Add(1)
	}
	if decision.Violation {
		r.auditViolations.Add(1)
	}
	if decision.ShouldDeny {
		r.denied.Add(1)
	}
	if idx := indexOf(knownClients, decision.Client); idx >= 0 {
		r.clients[idx].Add(1)
	}
	if idx := indexOf(reasonKeys, decision.Reason); idx >= 0 {
		r.reasons[idx].Add(1)
	}
	for idx := range signalKeys {
		if decision.signalMask&(1<<idx) != 0 {
			r.signals[idx].Add(1)
		}
	}
	if decision.Version != "" {
		r.recordVersion(decision.Version)
	}
	if decision.Client != "" {
		r.recordProfile(decision.Client, decision.Version, decision.signalMask)
	}
	return decision
}

func (r *Runtime) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	result := Snapshot{
		StartedAt:       r.started,
		Inspected:       r.inspected.Load(),
		Observed:        r.observed.Load(),
		Detected:        r.detected.Load(),
		AuditViolations: r.auditViolations.Load(),
		Denied:          r.denied.Load(),
		Clients:         atomicCounts(knownClients, r.clients[:]),
		Reasons:         atomicCounts(reasonKeys, r.reasons[:]),
		Signals:         atomicCounts(signalKeys, r.signals[:]),
		Versions:        make([]Count, 0, len(r.versions)),
		Profiles:        make([]Profile, 0, len(r.profiles)),
	}
	versionCounts := make(map[string]uint64, len(r.versions))
	for index := range r.versions {
		slot := &r.versions[index]
		slot.mu.Lock()
		if slot.version != "" {
			versionCounts[slot.version] += slot.count
		}
		slot.mu.Unlock()
	}
	for version, count := range versionCounts {
		result.Versions = append(result.Versions, Count{Key: version, Count: count})
	}
	sortCounts(result.Versions)
	if len(result.Versions) > 10 {
		result.Versions = result.Versions[:10]
	}
	for index := range r.profiles {
		slot := &r.profiles[index]
		slot.mu.Lock()
		if slot.client != "" {
			result.Profiles = append(result.Profiles, Profile{
				Client:  slot.client,
				Version: slot.version,
				Signals: signalNames(slot.signalMask),
				Count:   slot.count,
			})
		}
		slot.mu.Unlock()
	}
	sort.Slice(result.Profiles, func(i, j int) bool {
		if result.Profiles[i].Count != result.Profiles[j].Count {
			return result.Profiles[i].Count > result.Profiles[j].Count
		}
		if result.Profiles[i].Client != result.Profiles[j].Client {
			return result.Profiles[i].Client < result.Profiles[j].Client
		}
		return result.Profiles[i].Version < result.Profiles[j].Version
	})
	if len(result.Profiles) > 16 {
		result.Profiles = result.Profiles[:16]
	}
	return result
}

func signalNames(mask uint8) []string {
	result := make([]string, 0, len(signalKeys))
	for index, name := range signalKeys {
		if mask&(1<<index) != 0 {
			result = append(result, name)
		}
	}
	return result
}

func atomicCounts(keys []string, values []atomic.Uint64) []Count {
	result := make([]Count, 0, len(keys))
	for index, key := range keys {
		if count := values[index].Load(); count > 0 {
			result = append(result, Count{Key: key, Count: count})
		}
	}
	sortCounts(result)
	return result
}

func sortCounts(values []Count) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Count == values[j].Count {
			return values[i].Key < values[j].Key
		}
		return values[i].Count > values[j].Count
	})
}

func evaluate(req *http.Request, settings *compiledSettings) Decision {
	decision := Decision{Reason: "allowed"}
	if req == nil {
		return decision
	}
	ua := strings.TrimSpace(req.Header.Get("User-Agent"))
	originator := strings.TrimSpace(req.Header.Get("Originator"))
	uaClient := ""
	originClient := ""
	uaObserved := containsASCIIFold(ua, "codex")
	originObserved := containsASCIIFold(originator, "codex")
	if uaObserved {
		uaClient = openai.CodexOfficialClientFamilyFromUserAgent(ua)
	}
	if originObserved {
		originClient = openai.CodexOfficialClientFamilyFromOriginator(originator)
	}
	uaVersion := ""
	if uaClient != "" {
		uaVersion = extractVersion(ua)
	}
	rawQueryVersion := rawQueryValue(req.URL.RawQuery, "client_version")
	queryVersion := ""
	if rawQueryVersion != "" {
		queryVersion = normalizeVersion(rawQueryVersion)
	}

	if uaObserved {
		decision.signalMask |= 1 << 0
	}
	if originObserved {
		decision.signalMask |= 1 << 1
	}
	if isCodexPath(req.URL.Path) {
		decision.signalMask |= 1 << 2
	}
	if rawQueryVersion != "" {
		decision.signalMask |= 1 << 3
	}
	hasEngineFingerprint := hasCodexHeader(req.Header)
	if hasEngineFingerprint {
		decision.signalMask |= 1 << 4
	}
	decision.SignalCount = bits.OnesCount8(decision.signalMask)
	decision.Observed = decision.SignalCount > 0
	if !decision.Observed {
		return decision
	}
	// Claude Code is a built-in account-level named client, not an unlisted
	// Codex App Server. Even a partial Claude identity accompanied by an
	// x-codex engine header must stay delegated to codex_cli_only; otherwise
	// enabling App Server globally would accidentally bypass the stricter
	// account rule.
	if openai.IsClaudeCodeClientClaim(ua, originator) {
		decision.Reason = "account_policy_client"
		return decision
	}
	if decision.SignalCount < 2 {
		decision.Reason = "insufficient_signals"
		return decision
	}
	decision.Detected = true
	decision.Client = uaClient
	if decision.Client == "" {
		decision.Client = originClient
	}
	// client_version is the Codex engine version advertised by model-manifest
	// requests. Desktop/VS Code wrapper UA versions may use a different product
	// number, so the explicit engine signal takes precedence rather than being
	// treated as a conflicting identity.
	decision.Version = queryVersion
	if decision.Version == "" {
		decision.Version = uaVersion
	}

	if uaClient != "" && originClient != "" && uaClient != originClient {
		return violationDecision(decision, settings, "fingerprint_conflict")
	}
	// A real Codex app-server still uses the outer "Codex Desktop" UA and
	// originator. Its initialized clientInfo is carried in the final wrapper,
	// for example "(... Baseline App Server; 1.2.3)". Classifying only the
	// outer UA therefore silently turns every third-party app-server into the
	// first-party desktop client. Require the paired desktop identity, a valid
	// non-desktop wrapper and an independent x-codex engine signal before
	// selecting the separate app_server gate. This remains an admission signal,
	// not authentication: audit-only is still the safe default.
	if uaClient == ClientDesktop && originClient == ClientDesktop && hasEngineFingerprint &&
		isThirdPartyAppServerWrapper(ua) {
		decision.Client = ClientApp
	}
	if decision.Client == "" && hasEngineFingerprint {
		// App Server is not Claude Code and has no stable named UA in the
		// upstream policy. Treat only an otherwise-unlisted request carrying a
		// Codex engine header plus another independent signal as an App Server
		// candidate. The signal-count gate above prevents one spoofable header
		// from selecting this family.
		decision.Client = ClientApp
	}
	if decision.Client == "" {
		return violationDecision(decision, settings, "client_type_unknown")
	}
	if decision.Client == ClientApp && !settings.settings.AppServerEnabled {
		return violationDecision(decision, settings, "app_server_disabled")
	}
	mask := clientMask(decision.Client)
	if settings.deniedMask&mask != 0 {
		return violationDecision(decision, settings, "client_denied")
	}
	if settings.allowedMask&mask == 0 {
		return violationDecision(decision, settings, "client_not_allowed")
	}
	// Official min/max bounds describe named Codex clients. Unlisted App
	// Server candidates may not expose a parseable wrapper version and are
	// instead admitted by the independent engine-fingerprint gate above.
	if decision.Client != ClientApp && (settings.settings.MinVersion != "" || settings.settings.MaxVersion != "") && decision.Version == "" {
		return violationDecision(decision, settings, "version_missing")
	}
	if decision.Client != ClientApp && settings.settings.MinVersion != "" && compareVersions(decision.Version, settings.settings.MinVersion) < 0 {
		return violationDecision(decision, settings, "version_too_low")
	}
	if decision.Client != ClientApp && settings.settings.MaxVersion != "" && compareVersions(decision.Version, settings.settings.MaxVersion) > 0 {
		return violationDecision(decision, settings, "version_too_high")
	}
	return decision
}

func isThirdPartyAppServerWrapper(userAgent string) bool {
	userAgent = strings.TrimSpace(userAgent)
	if len(userAgent) < 5 || userAgent[len(userAgent)-1] != ')' {
		return false
	}
	open := strings.LastIndexByte(userAgent, '(')
	if open < 0 || open+1 >= len(userAgent)-1 {
		return false
	}
	inner := userAgent[open+1 : len(userAgent)-1]
	separator := strings.LastIndexByte(inner, ';')
	if separator <= 0 || separator+1 >= len(inner) {
		return false
	}
	wrapper := strings.TrimSpace(inner[:separator])
	wrapperVersion := strings.TrimSpace(inner[separator+1:])
	if wrapper == "" || normalizeVersion(wrapperVersion) == "" {
		return false
	}
	// Production Desktop samples consistently use clientInfo.name="Codex
	// Desktop". Only a different validated wrapper is a direct app-server
	// integration; VS Code and CLI have distinct outer identities and never
	// reach this branch.
	return !strings.EqualFold(wrapper, "Codex Desktop")
}

func hasCodexHeader(header http.Header) bool {
	if values := header["X-Openai-Internal-Codex-Responses-Lite"]; len(values) > 0 &&
		strings.EqualFold(strings.TrimSpace(values[0]), "true") {
		return true
	}
	for key := range header {
		if hasPrefixASCIIFold(key, "x-codex-") {
			return true
		}
	}
	return false
}

func isCodexPath(path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	switch path {
	case "/responses", "/v1/responses", "/models", "/v1/models", "/alpha/search", "/v1/alpha/search", "/backend-api/codex":
		return true
	}
	return strings.HasPrefix(path, "/responses/") ||
		strings.HasPrefix(path, "/v1/responses/") ||
		strings.HasPrefix(path, "/backend-api/codex/")
}

func normalizeVersion(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 0 && (value[0] == 'v' || value[0] == 'V') {
		value = value[1:]
	}
	// 版本会进入固定槽位遥测。限长后再 Clone，避免一个很长 User-Agent 或
	// RawQuery 的小型子串继续引用整块原始请求缓冲区。
	if len(value) == 0 || len(value) > maxNormalizedVersionBytes {
		return ""
	}
	if !validVersion(value) {
		return ""
	}
	return strings.Clone(value)
}

func validVersion(value string) bool {
	if value == "" {
		return false
	}
	components := 1
	digits := 0
	for index := 0; index < len(value); index++ {
		char := value[index]
		if char >= '0' && char <= '9' {
			digits++
			continue
		}
		if char != '.' || digits == 0 || components >= 4 {
			return false
		}
		components++
		digits = 0
	}
	return digits > 0 && (components == 3 || components == 4)
}

func compareVersions(left, right string) int {
	li, ri := 0, 0
	for li < len(left) || ri < len(right) {
		l, nextLeft := nextVersionPart(left, li)
		r, nextRight := nextVersionPart(right, ri)
		if l < r {
			return -1
		}
		if l > r {
			return 1
		}
		li, ri = nextLeft, nextRight
	}
	return 0
}

func nextVersionPart(value string, index int) (uint64, int) {
	var number uint64
	for index < len(value) && value[index] != '.' {
		number = number*10 + uint64(value[index]-'0')
		index++
	}
	if index < len(value) {
		index++
	}
	return number, index
}

func sliceSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func indexOf(values []string, value string) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return -1
}

func containsASCIIFold(value, needle string) bool {
	if needle == "" {
		return true
	}
	for start := 0; start+len(needle) <= len(value); start++ {
		matched := true
		for offset := 0; offset < len(needle); offset++ {
			left := value[start+offset]
			right := needle[offset]
			if left >= 'A' && left <= 'Z' {
				left += 'a' - 'A'
			}
			if right >= 'A' && right <= 'Z' {
				right += 'a' - 'A'
			}
			if left != right {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func hasPrefixASCIIFold(value, prefix string) bool {
	if len(value) < len(prefix) {
		return false
	}
	return containsASCIIFold(value[:len(prefix)], prefix)
}

func violationDecision(decision Decision, settings *compiledSettings, reason string) Decision {
	decision.Reason = reason
	decision.Violation = true
	decision.ShouldDeny = settings != nil && settings.settings.Mode == ModeEnforce
	return decision
}

func clientMask(client string) uint8 {
	if index := indexOf(knownClients, client); index >= 0 {
		return 1 << index
	}
	return 0
}

func extractVersion(value string) string {
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			continue
		}
		start := index
		dots := 0
		for index < len(value) && ((value[index] >= '0' && value[index] <= '9') || value[index] == '.') {
			if value[index] == '.' {
				dots++
			}
			index++
		}
		if dots == 2 || dots == 3 {
			if version := normalizeVersion(value[start:index]); version != "" {
				return version
			}
		}
	}
	return ""
}

func rawQueryValue(rawQuery, key string) string {
	for len(rawQuery) > 0 {
		part := rawQuery
		if index := strings.IndexByte(rawQuery, '&'); index >= 0 {
			part, rawQuery = rawQuery[:index], rawQuery[index+1:]
		} else {
			rawQuery = ""
		}
		if len(part) > len(key) && part[:len(key)] == key && part[len(key)] == '=' {
			// Client versions are numeric semver values; percent-encoded or
			// otherwise complex values deliberately fail closed as unparseable.
			return part[len(key)+1:]
		}
	}
	return ""
}

func (r *Runtime) recordVersion(version string) {
	hash := uint32(2166136261)
	for index := 0; index < len(version); index++ {
		hash ^= uint32(version[index])
		hash *= 16777619
	}
	slot := &r.versions[hash%uint32(len(r.versions))]
	slot.mu.Lock()
	if slot.version == "" {
		slot.version = strings.Clone(version)
		slot.count++
	} else if slot.version == version {
		slot.count++
	} else if slot.count <= 1 {
		// Bounded heavy-hitter approximation: a rare colliding version may be
		// replaced, but no attacker can grow memory cardinality.
		slot.version = strings.Clone(version)
		slot.count = 1
	} else {
		slot.count--
	}
	slot.mu.Unlock()
}

func (r *Runtime) recordProfile(client, version string, signalMask uint8) {
	hash := uint32(2166136261)
	for _, value := range []string{client, version} {
		for index := 0; index < len(value); index++ {
			hash ^= uint32(value[index])
			hash *= 16777619
		}
		hash ^= 0xff
		hash *= 16777619
	}
	hash ^= uint32(signalMask)
	hash *= 16777619
	start := int(hash % uint32(len(r.profiles)))
	for probe := range r.profiles {
		slot := &r.profiles[(start+probe)%len(r.profiles)]
		slot.mu.Lock()
		if slot.client == "" {
			slot.client = client
			slot.version = strings.Clone(version)
			slot.signalMask = signalMask
			slot.count++
			slot.mu.Unlock()
			return
		}
		if slot.client == client && slot.version == version && slot.signalMask == signalMask {
			slot.signalMask = signalMask
			slot.count++
			slot.mu.Unlock()
			return
		}
		slot.mu.Unlock()
	}
	// The table is full. Keep memory bounded and apply the same conservative
	// heavy-hitter replacement used for version telemetry.
	slot := &r.profiles[start]
	slot.mu.Lock()
	if slot.count <= 1 {
		slot.client = client
		slot.version = strings.Clone(version)
		slot.signalMask = signalMask
		slot.count = 1
	} else {
		slot.count--
	}
	slot.mu.Unlock()
}
