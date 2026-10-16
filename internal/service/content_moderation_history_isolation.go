package service

import (
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ContentModerationIsolationScopeID derives a non-secret, process-stable
// namespace for blocked-turn history. Personal requests retain the original
// API-Key scope; enterprise requests add organization/member/session identity
// so a shared Key cannot isolate another employee's conversation.
func ContentModerationIsolationScopeID(apiKeyScope, organizationID, userID int64, sessionID string) int64 {
	if apiKeyScope == 0 || organizationID <= 0 || userID <= 0 {
		return apiKeyScope
	}
	h := sha256.New()
	_, _ = h.Write([]byte("content-moderation-isolation/v2\x00"))
	var ids [24]byte
	binary.BigEndian.PutUint64(ids[:8], uint64(apiKeyScope))
	binary.BigEndian.PutUint64(ids[8:16], uint64(organizationID))
	binary.BigEndian.PutUint64(ids[16:], uint64(userID))
	_, _ = h.Write(ids[:])
	_, _ = h.Write([]byte(strings.TrimSpace(sessionID)))
	sum := h.Sum(nil)
	value := int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
	if value == 0 {
		value = 1
	}
	return -value
}

const (
	blockedTurnFingerprintTTL        = 7 * 24 * time.Hour
	blockedTurnMaxFingerprintsPerKey = 32
	blockedTurnMaxFingerprintsGlobal = 100000
)

type blockedTurnFingerprintCache struct {
	mu        sync.RWMutex
	salt      [sha256.Size]byte
	byAPIKey  map[int64]map[[sha256.Size]byte]blockedTurnFingerprintEntry
	total     int
	lastPrune time.Time
	ttl       time.Duration
	perKeyMax int
	globalMax int
}

type blockedTurnFingerprintEntry struct {
	expiresAt time.Time
	textBytes int
}

type blockedTurnFingerprintSnapshot struct {
	fingerprints map[[sha256.Size]byte]struct{}
	minTextBytes int
	maxTextBytes int
}

func (s blockedTurnFingerprintSnapshot) empty() bool {
	return len(s.fingerprints) == 0
}

func (s blockedTurnFingerprintSnapshot) mayMatchRawBytes(size int) bool {
	if s.empty() || size <= 0 {
		return false
	}
	// 历史轮次通常原样重发。保留 4 倍和 64 字节余量兼容重复空格、转义
	// 等轻微表示差异，同时避免为 20 字节指纹规范化 1MB 的无关历史。
	minAllowed := s.minTextBytes/4 - 64
	if minAllowed < 1 {
		minAllowed = 1
	}
	maxAllowed := s.maxTextBytes*4 + 64
	return size >= minAllowed && size <= maxAllowed
}

func newBlockedTurnFingerprintCache() *blockedTurnFingerprintCache {
	cache := &blockedTurnFingerprintCache{
		byAPIKey:  make(map[int64]map[[sha256.Size]byte]blockedTurnFingerprintEntry),
		ttl:       blockedTurnFingerprintTTL,
		perKeyMax: blockedTurnMaxFingerprintsPerKey,
		globalMax: blockedTurnMaxFingerprintsGlobal,
	}
	if _, err := cryptorand.Read(cache.salt[:]); err != nil {
		// crypto/rand 在正常系统上不会失败；回退值只用于保证极端启动环境仍然
		// 不保存原文，且每次进程启动产生不同的 fingerprint 命名空间。
		binary.LittleEndian.PutUint64(cache.salt[:8], uint64(time.Now().UnixNano()))
	}
	return cache
}

func (c *blockedTurnFingerprintCache) fingerprint(text string) ([sha256.Size]byte, bool) {
	var empty [sha256.Size]byte
	if c == nil {
		return empty, false
	}
	text = normalizeBlockedTurnFingerprintText(text)
	if text == "" {
		return empty, false
	}
	mac := hmac.New(sha256.New, c.salt[:])
	_, _ = mac.Write([]byte(text))
	var out [sha256.Size]byte
	copy(out[:], mac.Sum(nil))
	return out, true
}

func (s *ContentModerationService) contentModerationAuditFingerprint(userID, apiKeyID int64, inputHash string) string {
	return s.contentModerationAuditFingerprintForSubject(0, userID, apiKeyID, "", inputHash)
}

func (s *ContentModerationService) contentModerationAuditFingerprintForSubject(organizationID, userID, apiKeyID int64, controlPlaneKeyID, inputHash string) string {
	// 审计表只需要在同一进程生命周期内聚合同内容重试。对已经计算好的内容
	// hash 再做带随机启动盐的 HMAC，并将 user/API Key 作为固定宽度作用域；
	// 这样同一客户 Key 的重试可以聚合，不同客户或 Key 之间不能关联同一内容。
	// 结果仅保留 128 bit，进程重启后也不会形成永久追踪标识。
	cache := s.blockedTurnCache()
	inputHash = strings.TrimSpace(inputHash)
	if cache == nil || inputHash == "" {
		return ""
	}
	var scope [24]byte
	binary.BigEndian.PutUint64(scope[:8], uint64(organizationID))
	binary.BigEndian.PutUint64(scope[8:16], uint64(userID))
	binary.BigEndian.PutUint64(scope[16:], uint64(apiKeyID))
	mac := hmac.New(sha256.New, cache.salt[:])
	_, _ = mac.Write([]byte("content-moderation-audit/v1\x00"))
	_, _ = mac.Write(scope[:])
	_, _ = mac.Write([]byte(strings.TrimSpace(controlPlaneKeyID)))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(inputHash))
	return "v1:" + hex.EncodeToString(mac.Sum(nil)[:16])
}

func normalizeBlockedTurnFingerprintText(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == text && !strings.ContainsAny(text, "\t\r\n") && !strings.Contains(text, "  ") {
		// ExtractContentModerationInput 产生的 segment（片段）已经是单空格规范形态；
		// 快路径避免为 256KB Skill 再创建一次 strings.Fields 切片。
		return text
	}
	return normalizeContentModerationText(text)
}

func (c *blockedTurnFingerprintCache) record(apiKeyID int64, texts []string, now time.Time) {
	if c == nil || apiKeyID == 0 || len(texts) == 0 {
		return
	}
	type pendingFingerprint struct {
		value     [sha256.Size]byte
		textBytes int
	}
	fingerprints := make([]pendingFingerprint, 0, len(texts))
	for _, text := range texts {
		normalized := normalizeBlockedTurnFingerprintText(text)
		if fingerprint, ok := c.fingerprint(normalized); ok {
			fingerprints = append(fingerprints, pendingFingerprint{value: fingerprint, textBytes: len(normalized)})
		}
	}
	if len(fingerprints) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl <= 0 {
		c.ttl = blockedTurnFingerprintTTL
	}
	if c.perKeyMax <= 0 {
		c.perKeyMax = blockedTurnMaxFingerprintsPerKey
	}
	if c.globalMax <= 0 {
		c.globalMax = blockedTurnMaxFingerprintsGlobal
	}
	if c.lastPrune.IsZero() || now.Sub(c.lastPrune) >= time.Minute || c.total >= c.globalMax {
		c.pruneExpiredLocked(now)
		c.lastPrune = now
	}
	records := c.byAPIKey[apiKeyID]
	if records == nil {
		records = make(map[[sha256.Size]byte]blockedTurnFingerprintEntry)
		c.byAPIKey[apiKeyID] = records
	}
	expiresAt := now.Add(c.ttl)
	for _, pending := range fingerprints {
		fingerprint := pending.value
		if _, exists := records[fingerprint]; exists {
			records[fingerprint] = blockedTurnFingerprintEntry{expiresAt: expiresAt, textBytes: pending.textBytes}
			continue
		}
		for len(records) >= c.perKeyMax {
			c.removeOldestForKeyLocked(apiKeyID, records)
		}
		for c.total >= c.globalMax {
			c.removeOldestGlobalLocked()
		}
		c.byAPIKey[apiKeyID] = records
		records[fingerprint] = blockedTurnFingerprintEntry{expiresAt: expiresAt, textBytes: pending.textBytes}
		c.total++
	}
}

func (c *blockedTurnFingerprintCache) snapshot(apiKeyID int64, now time.Time) blockedTurnFingerprintSnapshot {
	if c == nil || apiKeyID == 0 {
		return blockedTurnFingerprintSnapshot{}
	}
	c.mu.RLock()
	records := c.byAPIKey[apiKeyID]
	if len(records) == 0 {
		c.mu.RUnlock()
		return blockedTurnFingerprintSnapshot{}
	}
	out := make(map[[sha256.Size]byte]struct{}, len(records))
	minTextBytes, maxTextBytes := 0, 0
	for fingerprint, entry := range records {
		if entry.expiresAt.After(now) {
			out[fingerprint] = struct{}{}
			if minTextBytes == 0 || entry.textBytes < minTextBytes {
				minTextBytes = entry.textBytes
			}
			if entry.textBytes > maxTextBytes {
				maxTextBytes = entry.textBytes
			}
		}
	}
	c.mu.RUnlock()
	if len(out) == 0 {
		return blockedTurnFingerprintSnapshot{}
	}
	return blockedTurnFingerprintSnapshot{fingerprints: out, minTextBytes: minTextBytes, maxTextBytes: maxTextBytes}
}

func (c *blockedTurnFingerprintCache) pruneExpiredLocked(now time.Time) {
	for apiKeyID, records := range c.byAPIKey {
		for fingerprint, entry := range records {
			if !entry.expiresAt.After(now) {
				delete(records, fingerprint)
				c.total--
			}
		}
		if len(records) == 0 {
			delete(c.byAPIKey, apiKeyID)
		}
	}
	if c.total < 0 {
		c.total = 0
	}
}

func (c *blockedTurnFingerprintCache) removeOldestForKeyLocked(apiKeyID int64, records map[[sha256.Size]byte]blockedTurnFingerprintEntry) {
	var oldest [sha256.Size]byte
	var oldestAt time.Time
	found := false
	for fingerprint, entry := range records {
		if !found || entry.expiresAt.Before(oldestAt) {
			oldest, oldestAt, found = fingerprint, entry.expiresAt, true
		}
	}
	if !found {
		return
	}
	delete(records, oldest)
	c.total--
	if len(records) == 0 {
		delete(c.byAPIKey, apiKeyID)
	}
}

func (c *blockedTurnFingerprintCache) removeOldestGlobalLocked() {
	var oldestKey int64
	var oldest [sha256.Size]byte
	var oldestAt time.Time
	found := false
	for apiKeyID, records := range c.byAPIKey {
		for fingerprint, entry := range records {
			if !found || entry.expiresAt.Before(oldestAt) {
				oldestKey, oldest, oldestAt, found = apiKeyID, fingerprint, entry.expiresAt, true
			}
		}
	}
	if !found {
		c.total = 0
		return
	}
	records := c.byAPIKey[oldestKey]
	delete(records, oldest)
	c.total--
	if len(records) == 0 {
		delete(c.byAPIKey, oldestKey)
	}
}

func (s *ContentModerationService) blockedTurnCache() *blockedTurnFingerprintCache {
	if s == nil {
		return nil
	}
	s.blockedTurnIsolationOnce.Do(func() {
		s.blockedTurnIsolation = newBlockedTurnFingerprintCache()
	})
	return s.blockedTurnIsolation
}

func (s *ContentModerationService) recordBlockedTurnFingerprints(apiKeyID int64, content ContentModerationInput, matchedKeyword string) {
	if apiKeyID == 0 {
		return
	}
	texts := make([]string, 0, len(content.Segments))
	for _, segment := range content.Segments {
		// 控制指令统一交给 contributor（贡献片段）定位器处理。否则跨
		// system/developer 命中时只能粗暴记录全部 instructions，下一轮
		// 会连无关安全规则一起从转发副本删除。
		if segment.Source == ContentModerationSourceInstructions {
			continue
		}
		if contentModerationSegmentContainsKeyword(segment.Text, matchedKeyword) {
			texts = append(texts, segment.Text)
		}
	}
	texts = append(texts, contentModerationMatchedInstructionContributors(content.Segments, matchedKeyword)...)
	if len(texts) == 0 {
		// 极少数关键词可能跨 content part 边界命中；此时只保存聚合指纹作为
		// 保守回退。常规路径只记录真正命中的 segment，避免顶层指令命中时
		// 把无辜的最新用户消息也加入隔离缓存。
		texts = append(texts, content.Text)
	}
	s.blockedTurnCache().record(apiKeyID, texts, time.Now())
}

// IsolateBlockedHistory 只对当前 API Key 曾被本网关真实拦截的历史轮次生效。
// 普通 Key 在 snapshot 的空 map 快路径直接返回原 slice，不解析 JSON，也不访问外部依赖。
func (s *ContentModerationService) IsolateBlockedHistory(apiKeyID int64, protocol string, body []byte) ([]byte, int) {
	cache := s.blockedTurnCache()
	fingerprintSnapshot := cache.snapshot(apiKeyID, time.Now())
	if fingerprintSnapshot.empty() || len(body) == 0 || !gjson.ValidBytes(body) {
		return body, 0
	}
	if statusCode, _ := validateClientInstructions(protocol, body); statusCode != 0 {
		// 畸形或超限客户端控制指令必须保留原请求交给 Check 稳定返回
		// 400/413，不能先为历史隔离解码、规范化或重建请求体。
		return body, 0
	}
	matcher := func(parts []string) bool {
		if len(parts) == 0 {
			return false
		}
		for _, part := range parts {
			if !fingerprintSnapshot.mayMatchRawBytes(len(part)) {
				continue
			}
			fingerprint, ok := cache.fingerprint(part)
			if !ok {
				continue
			}
			if _, exists := fingerprintSnapshot.fingerprints[fingerprint]; exists {
				return true
			}
		}
		joined := strings.Join(parts, "\n")
		if !fingerprintSnapshot.mayMatchRawBytes(len(joined)) {
			return false
		}
		fingerprint, ok := cache.fingerprint(joined)
		if !ok {
			return false
		}
		_, exists := fingerprintSnapshot.fingerprints[fingerprint]
		return exists
	}

	root := gjson.ParseBytes(body)
	switch protocol {
	case ContentModerationProtocolOpenAIResponses:
		inputField := contentModerationObjectFieldFold(root, "input")
		isolated, count := isolateBlockedHistoryArray(body, inputField.Key, inputField.Value, responsesHistoryItem, responsesCurrentTailStart, matcher)
		instructionsField := contentModerationObjectFieldFold(root, "instructions")
		instructions := instructionsField.Value
		if instructions.Type != gjson.String || !matcher([]string{instructions.String()}) {
			return isolated, count
		}
		// 先重建 input 历史、再删除顶层字段，避免修改 body 后继续使用旧 gjson
		// Index（字节偏移）。删除而不是写占位文案，防止占位文本触发语种门禁。
		withoutInstructions, err := sjson.DeleteBytes(isolated, instructionsField.Key)
		if err != nil {
			return isolated, count
		}
		return withoutInstructions, count + 1
	case ContentModerationProtocolOpenAIChat:
		messagesField := contentModerationObjectFieldFold(root, "messages")
		isolated, count := isolateBlockedHistoryArray(body, messagesField.Key, messagesField.Value, chatHistoryItem, chatCurrentTailStart, matcher)
		instructionsField := contentModerationObjectFieldFold(root, "instructions")
		instructions := instructionsField.Value
		if instructions.Type != gjson.String || !matcher([]string{instructions.String()}) {
			return isolated, count
		}
		withoutInstructions, err := sjson.DeleteBytes(isolated, instructionsField.Key)
		if err != nil {
			return isolated, count
		}
		return withoutInstructions, count + 1
	case ContentModerationProtocolAnthropicMessages:
		messagesField := contentModerationObjectFieldFold(root, "messages")
		isolated, count := isolateBlockedHistoryArray(body, messagesField.Key, messagesField.Value, anthropicHistoryItem, anthropicCurrentTailStart, matcher)
		systemField := contentModerationObjectFieldFold(root, "system")
		isolated, systemIsolated := isolateAnthropicSystemInstruction(isolated, systemField, matcher)
		return isolated, count + systemIsolated
	case ContentModerationProtocolGemini:
		contentsField := contentModerationObjectFieldFold(root, "contents")
		isolated, count := isolateBlockedHistoryArray(body, contentsField.Key, contentsField.Value, geminiHistoryItem, geminiCurrentTailStart, matcher)
		systemField := contentModerationObjectFieldFold(root, "systemInstruction")
		isolated, systemIsolated := isolateGeminiSystemInstruction(isolated, systemField, matcher)
		return isolated, count + systemIsolated
	default:
		return body, 0
	}
}

// isolateAnthropicSystemInstruction 逐 text block 删除已经被当前 Key 拒绝
// 的指令。system 数组里常同时包含计费归因、安全约束和项目规则；其中一个
// block 命中不能让下一轮把整个系统提示词一起丢掉。
func isolateAnthropicSystemInstruction(body []byte, field contentModerationJSONField, match func([]string) bool) ([]byte, int) {
	if field.Key == "" || match == nil {
		return body, 0
	}
	system := field.Value
	if system.Type == gjson.String {
		text := system.String()
		if text == "" || isStrictAnthropicBillingAttributionText(text) || !match([]string{text}) {
			return body, 0
		}
		updated, err := sjson.DeleteBytes(body, field.Key)
		if err != nil {
			return body, 0
		}
		return updated, 1
	}
	if !system.IsArray() {
		return body, 0
	}

	items := system.Array()
	kept := make([]gjson.Result, 0, len(items))
	removed := false
	for _, block := range items {
		typeField := contentModerationObjectFieldFold(block, "type")
		textField := contentModerationObjectFieldFold(block, "text")
		text := textField.Value
		if typeField.Value.String() == "text" && text.Type == gjson.String &&
			!isStrictAnthropicBillingAttributionText(text.String()) && match([]string{text.String()}) {
			removed = true
			continue
		}
		kept = append(kept, block)
	}
	if !removed {
		return body, 0
	}
	if len(kept) == 0 {
		updated, err := sjson.DeleteBytes(body, field.Key)
		if err != nil {
			return body, 0
		}
		return updated, 1
	}
	raw := contentModerationRawJSONArray(kept)
	updated, err := sjson.SetRawBytes(body, field.Key, raw)
	if err != nil {
		return body, 0
	}
	return updated, 1
}

// isolateGeminiSystemInstruction 保留未命中的 part 以及 systemInstruction
// 上的其他结构字段。只有 parts 全部被隔离后才删除整个顶层字段。
func isolateGeminiSystemInstruction(body []byte, field contentModerationJSONField, match func([]string) bool) ([]byte, int) {
	if field.Key == "" || !field.Value.IsObject() || match == nil {
		return body, 0
	}
	partsField := contentModerationObjectFieldFold(field.Value, "parts")
	if partsField.Key == "" || !partsField.Value.IsArray() {
		return body, 0
	}
	parts := partsField.Value.Array()
	kept := make([]gjson.Result, 0, len(parts))
	removed := false
	for _, part := range parts {
		text := contentModerationObjectFieldFold(part, "text").Value
		if text.Type == gjson.String && match([]string{text.String()}) {
			removed = true
			continue
		}
		kept = append(kept, part)
	}
	if !removed {
		return body, 0
	}
	if len(kept) == 0 {
		updated, err := sjson.DeleteBytes(body, field.Key)
		if err != nil {
			return body, 0
		}
		return updated, 1
	}
	raw := contentModerationRawJSONArray(kept)
	updated, err := sjson.SetRawBytes(body, field.Key+"."+partsField.Key, raw)
	if err != nil {
		return body, 0
	}
	return updated, 1
}

func contentModerationRawJSONArray(items []gjson.Result) []byte {
	capacity := 2
	for _, item := range items {
		capacity += len(item.Raw) + 1
	}
	out := make([]byte, 0, capacity)
	out = append(out, '[')
	for index, item := range items {
		if index > 0 {
			out = append(out, ',')
		}
		out = append(out, item.Raw...)
	}
	out = append(out, ']')
	return out
}

type blockedHistoryItem struct {
	parts        []string
	eligible     bool
	userBoundary bool
	toolResult   bool
	exactOnly    bool
}

type blockedHistoryItemParser func(gjson.Result) blockedHistoryItem
type blockedHistoryTailStart func([]gjson.Result) int

func isolateBlockedHistoryArray(body []byte, path string, value gjson.Result, parse blockedHistoryItemParser, tailStart blockedHistoryTailStart, match func([]string) bool) ([]byte, int) {
	if !value.IsArray() {
		return body, 0
	}
	items := value.Array()
	historyEnd := tailStart(items)
	if historyEnd <= 0 {
		return body, 0
	}
	parsed := make([]blockedHistoryItem, historyEnd)
	for index := 0; index < historyEnd; index++ {
		parsed[index] = parse(items[index])
	}
	remove := make([]bool, historyEnd)
	matchedTurns := 0
	for index := 0; index < historyEnd; index++ {
		item := parsed[index]
		if !item.eligible || !match(item.parts) {
			continue
		}
		matchedTurns++
		if item.exactOnly {
			// system/developer 是独立控制消息。只移除真正命中的这一项，不能
			// 连带删除它后面的普通用户历史或工具调用。
			remove[index] = true
			continue
		}
		start := index
		if item.toolResult {
			// 工具结果被拦截时，向前移除关联 assistant/model 工具调用，
			// 但保留此前用户问题，避免制造没有结果的孤立 tool_call。
			for start > 0 && !parsed[start-1].userBoundary {
				start--
			}
		}
		end := index + 1
		for end < historyEnd && !parsed[end].userBoundary {
			end++
		}
		for removeIndex := start; removeIndex < end; removeIndex++ {
			remove[removeIndex] = true
		}
	}
	if matchedTurns == 0 {
		return body, 0
	}
	// gjson 的 Index/Raw 指向原请求中的完整数组。一次性重建该数组，避免
	// sjson 按元素删除时每删一项就复制整份 1MB+ 长上下文。
	arrayStart := value.Index
	arrayEnd := arrayStart + len(value.Raw)
	if arrayStart < 0 || arrayEnd > len(body) || arrayStart >= arrayEnd || body[arrayStart] != '[' {
		return body, 0
	}
	out := make([]byte, 0, len(body))
	out = append(out, body[:arrayStart]...)
	out = append(out, '[')
	wroteItem := false
	for index, item := range items {
		if index < historyEnd && remove[index] {
			continue
		}
		if wroteItem {
			out = append(out, ',')
		}
		out = append(out, item.Raw...)
		wroteItem = true
	}
	out = append(out, ']')
	out = append(out, body[arrayEnd:]...)
	return out, matchedTurns
}

func responsesCurrentTailStart(items []gjson.Result) int {
	if len(items) == 0 {
		return 0
	}
	if isResponsesToolOutput(items[len(items)-1]) {
		start := len(items) - 1
		for start > 0 && isResponsesToolOutput(items[start-1]) {
			start--
		}
		return start
	}
	if isResponsesUserTextItem(items[len(items)-1]) {
		return len(items) - 1
	}
	return len(items)
}

func responsesHistoryItem(item gjson.Result) blockedHistoryItem {
	if text, ok := responsesInputInstructionText(item); ok {
		// 必须与首次预审共用同一提取口径：system/developer 的多个文本
		// content part 会被转换器无分隔拼接，且 output_text 也属于文本。
		// 若历史隔离再次逐 part 提取，首次记录的聚合指纹将永远匹配不到。
		text = normalizeContentModerationText(text)
		return blockedHistoryItem{parts: []string{text}, eligible: text != "", exactOnly: true}
	}
	if isResponsesToolOutput(item) {
		var parts, images []string
		collectContentValue(item.Get("output"), &parts, &images)
		collectContentValue(item.Get("content"), &parts, &images)
		return blockedHistoryItem{parts: parts, eligible: len(parts) > 0, toolResult: true}
	}
	if isResponsesUserTextItem(item) {
		var parts, images []string
		collectContentValue(item.Get("content"), &parts, &images)
		if item.Get("type").String() == "input_text" || item.Get("text").Exists() {
			collectContentValue(item, &parts, &images)
		}
		return blockedHistoryItem{parts: parts, eligible: len(parts) > 0, userBoundary: true}
	}
	return blockedHistoryItem{}
}

func chatCurrentTailStart(items []gjson.Result) int {
	if len(items) == 0 {
		return 0
	}
	if strings.EqualFold(strings.TrimSpace(items[len(items)-1].Get("role").String()), "tool") {
		start := len(items) - 1
		for start > 0 && strings.EqualFold(strings.TrimSpace(items[start-1].Get("role").String()), "tool") {
			start--
		}
		return start
	}
	if strings.EqualFold(strings.TrimSpace(items[len(items)-1].Get("role").String()), "user") {
		return len(items) - 1
	}
	return len(items)
}

func chatHistoryItem(item gjson.Result) blockedHistoryItem {
	if text, _, ok := chatInstructionMessageText(item); ok {
		text = normalizeContentModerationText(text)
		return blockedHistoryItem{parts: []string{text}, eligible: text != "", exactOnly: true}
	}
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if role != "user" && role != "tool" {
		return blockedHistoryItem{}
	}
	var parts, images []string
	collectContentValue(item.Get("content"), &parts, &images)
	return blockedHistoryItem{
		parts: parts, eligible: len(parts) > 0,
		userBoundary: role == "user", toolResult: role == "tool",
	}
}

func anthropicCurrentTailStart(items []gjson.Result) int {
	if len(items) > 0 && strings.EqualFold(strings.TrimSpace(items[len(items)-1].Get("role").String()), "user") {
		return len(items) - 1
	}
	return len(items)
}

func anthropicHistoryItem(item gjson.Result) blockedHistoryItem {
	if !strings.EqualFold(strings.TrimSpace(item.Get("role").String()), "user") {
		return blockedHistoryItem{}
	}
	content := item.Get("content")
	toolResult := false
	if content.IsArray() {
		for _, block := range content.Array() {
			if strings.EqualFold(strings.TrimSpace(block.Get("type").String()), "tool_result") {
				toolResult = true
				break
			}
		}
	}
	var parts, images []string
	if toolResult {
		collectContentValue(content, &parts, &images)
	} else {
		collectAnthropicUserContentValue(content, &parts, &images)
	}
	return blockedHistoryItem{
		parts: parts, eligible: len(parts) > 0,
		userBoundary: !toolResult, toolResult: toolResult,
	}
}

func geminiCurrentTailStart(items []gjson.Result) int {
	if len(items) == 0 {
		return 0
	}
	role := strings.ToLower(strings.TrimSpace(items[len(items)-1].Get("role").String()))
	if role == "" || role == "user" {
		return len(items) - 1
	}
	return len(items)
}

func geminiHistoryItem(item gjson.Result) blockedHistoryItem {
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if role != "" && role != "user" {
		return blockedHistoryItem{}
	}
	partsResult := item.Get("parts")
	toolResult := false
	var parts []string
	for _, part := range partsResult.Array() {
		response := part.Get("functionResponse")
		if !response.Exists() {
			response = part.Get("function_response")
		}
		if response.Exists() {
			toolResult = true
			addModerationText(&parts, moderationFirstNonEmpty(response.Get("response.output").String(), response.Get("response.content").String(), response.Get("response").Raw))
			continue
		}
		addModerationText(&parts, part.Get("text").String())
	}
	return blockedHistoryItem{
		parts: parts, eligible: len(parts) > 0,
		userBoundary: !toolResult, toolResult: toolResult,
	}
}

func (c *blockedTurnFingerprintCache) debugCounts() (keys, fingerprints int) {
	if c == nil {
		return 0, 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byAPIKey), c.total
}

func (c *blockedTurnFingerprintCache) String() string {
	keys, fingerprints := c.debugCounts()
	return fmt.Sprintf("blocked_turn_fingerprints(keys=%d,total=%d)", keys, fingerprints)
}
