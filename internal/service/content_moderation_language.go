package service

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	// 本轮用户、Skill（技能）和工具输出继续共享 256K 字符预算；顶层
	// instructions（系统指令）独立保留 512K，避免任一来源用超长安全文本
	// 挤掉另一来源的真实风险内容。两者只在本地审查副本中合并。
	maxContentPolicyTextRunes             = 256 * 1024
	maxContentPolicyRequestTextRunes      = maxContentPolicyTextRunes
	maxContentPolicyInstructionsTextRunes = 512 * 1024
)

var (
	contentPolicyUnicodeEscape = regexp.MustCompile(`\\u([0-9a-fA-F]{4})`)
	contentPolicySpacedLetters = regexp.MustCompile(`(?i)(?:\b[\p{L}\p{N}]\s+){3,}[\p{L}\p{N}]\b`)
)

type contentPolicyLatinLanguageProfile struct {
	code        string
	words       map[string]struct{}
	markerRunes string
}

var contentPolicyLatinLanguageProfiles = []contentPolicyLatinLanguageProfile{
	{
		code: "en",
		words: contentPolicyWordSet(
			"the", "and", "this", "that", "with", "please", "from", "for", "as", "is", "are", "to", "of", "in", "on", "by", "or", "not", "can", "you",
			"your", "we", "they", "what", "why", "how", "when", "where", "which", "help", "explain", "answer", "problem", "details", "summarize", "write", "make", "without",
		),
	},
	{
		code: "es",
		words: contentPolicyWordSet(
			"el", "la", "los", "las", "que", "con", "por", "para", "este", "esta", "esto", "como", "cómo", "también", "muy", "gracias", "favor", "usted", "ustedes", "puede", "puedes", "respuesta", "explica", "explicar", "problema", "detalle", "detalles", "ayuda", "ayudar", "quiero", "hacer", "sin", "donde", "cuando",
		),
		markerRunes: "ñ¿¡",
	},
	{
		code: "fr",
		words: contentPolicyWordSet(
			"le", "la", "les", "des", "avec", "pour", "que", "une", "vous", "cette", "ce", "comment", "merci", "français", "réponse", "expliquer", "pouvez", "peut", "faire", "sans", "dans", "ça", "problème", "détails", "aide", "pourquoi", "quand", "où",
		),
		markerRunes: "œ",
	},
	{
		code: "de",
		words: contentPolicyWordSet(
			"der", "die", "das", "und", "mit", "für", "ist", "ein", "nicht", "bitte", "diese", "dieser", "antwort", "erklären", "kann", "ohne", "problem", "details", "hilfe", "warum", "wie", "wann", "machen",
		),
		markerRunes: "ß",
	},
	{
		// `a/as/o/os` 同时是 SQL 关键字或高频表别名，不能作为葡萄牙语证据。
		code: "pt",
		words: contentPolicyWordSet(
			"que", "com", "para", "uma", "um", "por", "não", "você", "vocês", "isso", "isto", "também", "muito", "como", "está", "são", "pode", "favor", "obrigado", "obrigada", "resposta", "explicar", "problema", "detalhes", "ajuda", "quero", "fazer", "sem", "onde", "quando",
		),
		markerRunes: "ãõ",
	},
	{
		code: "it",
		words: contentPolicyWordSet(
			"il", "la", "gli", "che", "con", "per", "una", "questo", "questa", "come", "grazie", "italiano", "risposta", "spiega", "spiegare", "puoi", "fare", "senza", "problema", "dettagli", "aiuto", "voglio", "dove", "quando", "perché",
		),
	},
	{
		code: "id",
		words: contentPolicyWordSet(
			"yang", "dan", "dengan", "untuk", "ini", "dari", "pada", "adalah", "tidak", "tolong", "bagaimana", "jawaban", "bisa", "tanpa", "masalah", "detail", "bantu", "mengapa", "kapan", "dimana", "buat",
		),
	},
	{
		code: "tr",
		words: contentPolicyWordSet(
			"ve", "bir", "bu", "için", "ile", "değil", "nasıl", "lütfen", "olan", "olarak", "cevap", "açıkla", "sorun", "ayrıntı", "yardım", "neden", "nerede", "zaman", "yapmak",
		),
		markerRunes: "ğış",
	},
	{
		code: "vi",
		words: contentPolicyWordSet(
			"và", "là", "của", "cho", "với", "không", "một", "này", "hãy", "trong", "trả", "lời", "vấn", "đề", "chi", "tiết", "giúp", "tại", "sao", "khi", "đâu",
		),
		markerRunes: "ăđơư",
	},
}

var contentPolicyLatinEvidenceWords = func() map[string]struct{} {
	out := make(map[string]struct{})
	for _, profile := range contentPolicyLatinLanguageProfiles {
		for word := range profile.words {
			out[word] = struct{}{}
		}
	}
	return out
}()

// contentPolicyLatinWordProfileCounts 在进程启动时预计算证据词被多少个语种共享。
// 请求热路径据此降低 `la/que/para` 等跨语种高频词权重，避免仅凭含糊词猜语种。
var contentPolicyLatinWordProfileCounts = func() map[string]int {
	out := make(map[string]int, len(contentPolicyLatinEvidenceWords))
	for _, profile := range contentPolicyLatinLanguageProfiles {
		for word := range profile.words {
			out[word]++
		}
	}
	return out
}()

var (
	contentPolicySQLLeadWords = contentPolicyWordSet(
		"select", "insert", "update", "delete", "create", "alter", "drop", "merge", "replace", "explain", "describe", "show", "truncate", "grant", "revoke", "with",
	)
	contentPolicySQLStructureWords = contentPolicyWordSet(
		"from", "into", "values", "set", "where", "join", "on", "group", "order", "having", "limit", "offset", "union", "returning", "table", "view", "index", "procedure",
	)
	contentPolicyCodeWords = contentPolicyWordSet(
		"package", "import", "func", "function", "class", "interface", "struct", "const", "let", "var", "def", "lambda", "return", "if", "else", "switch", "case", "for", "while", "try", "catch", "finally", "public", "private", "protected", "async", "await",
	)
	contentPolicyInlineSQLStartWords = contentPolicyWordSet(
		"select", "insert", "update", "delete", "create", "alter", "drop", "merge", "replace", "truncate", "grant", "revoke",
	)
	contentPolicyInlineCodeStartWords = contentPolicyWordSet(
		"package", "import", "func", "function", "class", "interface", "struct", "const", "let", "var", "def", "public", "private", "protected", "async",
	)
)

func contentPolicyWordSet(words ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(words))
	for _, word := range words {
		out[word] = struct{}{}
	}
	return out
}

var supportedContentLanguages = map[string]struct{}{
	"zh": {}, "en": {}, "ja": {}, "ko": {}, "es": {}, "fr": {}, "de": {},
	"pt": {}, "it": {}, "ru": {}, "ar": {}, "hi": {}, "th": {}, "tr": {},
	"vi": {}, "id": {}, "he": {}, "el": {},
}

func normalizeAllowedLanguages(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		code := strings.ToLower(strings.TrimSpace(raw))
		if _, ok := supportedContentLanguages[code]; !ok {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	if len(out) == 0 {
		return []string{"zh", "en"}
	}
	sort.Strings(out)
	return out
}

// normalizeContentPolicyText 只做有界、一次性的本地标准化。这里禁止递归解码，
// 避免大上下文或恶意编码让关键词热路径出现不可控 CPU/内存开销。
func normalizeContentPolicyText(text string) string {
	return normalizeContentPolicyTextWithLimit(text, maxContentPolicyTextRunes)
}

func normalizeContentPolicyTextWithLimit(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	text = strings.TrimSpace(text)
	if strings.Contains(text, `\u`) {
		text = contentPolicyUnicodeEscape.ReplaceAllStringFunc(text, func(raw string) string {
			value, err := strconv.ParseInt(raw[2:], 16, 32)
			if err != nil {
				return raw
			}
			return string(rune(value))
		})
	}
	if strings.Contains(text, "%") && len(text) <= maxRunes {
		if decoded, err := url.QueryUnescape(text); err == nil && utf8.ValidString(decoded) {
			text = decoded
		}
	}
	trimmed := strings.TrimSpace(text)
	if len(trimmed) >= 16 && len(trimmed) <= 64*1024 && !strings.ContainsAny(trimmed, " \t\r\n") {
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			decoded, err := encoding.DecodeString(trimmed)
			if err == nil && utf8.Valid(decoded) && contentPolicyReadableRatio(string(decoded)) >= 0.9 {
				text = string(decoded)
				break
			}
		}
	}
	// 解码后再统一做一次 NFKC 和不可见字符清理。若先清理再解码，
	// `\u200b`、URL 编码或 Base64 中藏入的零宽字符会绕过关键词匹配。
	text = norm.NFKC.String(text)
	text = strings.Map(func(r rune) rune {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff':
			return -1
		default:
			return r
		}
	}, text)
	text = contentPolicySpacedLetters.ReplaceAllStringFunc(text, func(value string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, value)
	})
	// 复用与审查摘要完全相同的 Unicode 空白折叠，并让 trimRunes 只在
	// 真正超限时分配/截断，避免大请求在每个策略阶段复制 rune 切片。
	return trimRunes(collapseContentModerationWhitespace(text), maxRunes)
}

func contentPolicyReadableRatio(text string) float64 {
	var readable, total int
	for _, r := range text {
		total++
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSpace(r) || unicode.IsPunct(r) {
			readable++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(readable) / float64(total)
}

func detectContentLanguages(text string) []string {
	return detectContentLanguagesNormalized(normalizeContentPolicyText(text))
}

// detectContentLanguagesNormalized 复用请求已经完成的策略标准化结果，避免大型
// Skill 在语种识别和关键词匹配前各复制一次完整文本。
func detectContentLanguagesNormalized(text string) []string {
	return detectContentLanguagesDetailedNormalized(text).Languages
}

const (
	contentPolicyLanguageDetectorVersion = "v2"
	// 64K 只是一条防止无标点大段把不同语种永久粘在一起的上限。自然句仍会在
	// 标点处分块；上限不能过小，否则 256KB 的无标点 Skill/Tool 输出会为每个
	// 小块重复分配统计对象，对内存和 CPU 没有实际判定收益。
	contentPolicyLanguageChunkLetters = 64 * 1024
	contentPolicyMinLanguageRunes     = 4
	contentPolicyMinLanguageRatioBPS  = 1000
)

type contentPolicyLanguageDetection struct {
	Languages []string
	Evidence  ContentModerationLanguageEvidence
}

type contentPolicyScript uint8

const (
	contentPolicyScriptHiragana contentPolicyScript = iota
	contentPolicyScriptKatakana
	contentPolicyScriptHangul
	contentPolicyScriptHan
	contentPolicyScriptBopomofo
	contentPolicyScriptCyrillic
	contentPolicyScriptArabic
	contentPolicyScriptDevanagari
	contentPolicyScriptThai
	contentPolicyScriptHebrew
	contentPolicyScriptGreek
	contentPolicyScriptLatin
	contentPolicyScriptCount
)

var contentPolicyScriptNames = [...]string{
	"hiragana", "katakana", "hangul", "han", "bopomofo", "cyrillic",
	"arabic", "devanagari", "thai", "hebrew", "greek", "latin",
}

type contentPolicyLanguageChunkStats struct {
	totalLetters int
	letters      [contentPolicyScriptCount]int
	words        [contentPolicyScriptCount]int
}

type contentPolicyLatinDetection struct {
	code          string
	score         int
	evidenceCount int
	totalWords    int
	ok            bool
}

// detectContentLanguagesDetailedNormalized 只把有足够自然语言证据的语种写入
// Languages。弱证据仍保留在 Evidence 中供管理员排查，但不会用于 403 门禁。
// 这条边界很重要：文件名、数学变量或一个日文假名不能让整次请求被拒绝。
func detectContentLanguagesDetailedNormalized(text string) contentPolicyLanguageDetection {
	evidence := ContentModerationLanguageEvidence{
		DetectorVersion:     contentPolicyLanguageDetectorVersion,
		ContentRunes:        utf8.RuneCountInString(text),
		MinLanguageRunes:    contentPolicyMinLanguageRunes,
		MinLanguageRatioBPS: contentPolicyMinLanguageRatioBPS,
		ScriptCounts:        make(map[string]int),
		LanguageCounts:      make(map[string]int),
		LanguageRatioBPS:    make(map[string]int),
		LatinEvidenceCounts: make(map[string]int),
	}
	languageText := contentPolicyTextForLanguageDetection(text)
	if languageText == "" || contentPolicyLooksLikeCodeURLOrModel(languageText) {
		// 代码、URL 和模型名不是自然语言。把它们标记为 unknown 可避免管理员
		// 只允许中文时误伤 `gpt-5.6-sol` 或一段代码；关键词匹配仍照常执行。
		return contentPolicyLanguageDetection{Languages: []string{"unknown"}, Evidence: evidence}
	}
	// 大多数普通请求无需清理，languageText 会直接复用 text。此时沿用已经
	// 统计过的字符数，避免大型 Skill 为证据字段再完整扫描一遍。
	if languageText == text {
		evidence.SampledRunes = evidence.ContentRunes
	} else {
		evidence.SampledRunes = utf8.RuneCountInString(languageText)
	}
	detected := make(map[string]struct{}, 4)
	contentPolicyForEachLanguageChunk(languageText, func(chunk string) {
		stats := contentPolicyCollectLanguageChunkStats(chunk)
		if stats.totalLetters == 0 {
			return
		}
		evidence.TotalLetterRunes += stats.totalLetters
		for script, count := range stats.letters {
			if count > 0 {
				evidence.ScriptCounts[contentPolicyScriptNames[script]] += count
			}
		}

		// Han 默认属于中文证据。日语只有在当前自然语言片段具有足够假名/语法
		// 证据时，才可以有限认领同片段的 Han，绝不能再全局吞并中文字符。
		hanLetters := stats.letters[contentPolicyScriptHan] + stats.letters[contentPolicyScriptBopomofo]
		kanaLetters := stats.letters[contentPolicyScriptHiragana] + stats.letters[contentPolicyScriptKatakana]
		if kanaLetters >= 4 {
			grammarEvidence := contentPolicyJapaneseGrammarEvidence(chunk)
			kanaShareBPS := contentPolicyRatioBPS(kanaLetters, kanaLetters+hanLetters)
			japaneseConfirmed := kanaLetters >= 8 || grammarEvidence >= 2 || (kanaLetters >= 4 && kanaShareBPS >= 6000)
			if japaneseConfirmed {
				japaneseLetters := kanaLetters
				// 纯片假名串更常见于文件名、产品名或短引用。只有当前片段
				// 同时具有足量假名和日语语法证据时才认领 Han（汉字），避免
				// “中文说明 + 日文名称”整段被改判为日语。
				if grammarEvidence >= 2 {
					claimedHan := hanLetters
					if maximum := kanaLetters * 3; claimedHan > maximum {
						claimedHan = maximum
					}
					japaneseLetters += claimedHan
					hanLetters -= claimedHan
				}
				contentPolicyAddLanguageCandidate(detected, &evidence, "ja", japaneseLetters, stats.words[contentPolicyScriptHiragana]+stats.words[contentPolicyScriptKatakana], stats.totalLetters, false)
			}
		}

		// 少量 Hanja 可能出现在韩语说明中；只有 Hangul 已经很明确且 Han 很少
		// 时才归入韩语，避免中文加一个韩文名称被错误判成混合语种。
		hangulLetters := stats.letters[contentPolicyScriptHangul]
		if hangulLetters >= 4 && hanLetters > 0 && hanLetters <= hangulLetters/3 {
			hangulLetters += hanLetters
			hanLetters = 0
		}
		contentPolicyAddLanguageCandidate(detected, &evidence, "ko", hangulLetters, stats.words[contentPolicyScriptHangul], stats.totalLetters, false)
		contentPolicyAddLanguageCandidate(detected, &evidence, "zh", hanLetters, stats.words[contentPolicyScriptHan]+stats.words[contentPolicyScriptBopomofo], stats.totalLetters, contentPolicyHasChineseLanguageMarker(chunk))

		for _, candidate := range []struct {
			language string
			script   contentPolicyScript
		}{
			{language: "ru", script: contentPolicyScriptCyrillic},
			{language: "ar", script: contentPolicyScriptArabic},
			{language: "hi", script: contentPolicyScriptDevanagari},
			{language: "th", script: contentPolicyScriptThai},
			{language: "he", script: contentPolicyScriptHebrew},
			{language: "el", script: contentPolicyScriptGreek},
		} {
			letters := stats.letters[candidate.script]
			// α/β/Δ 等经常只是公式变量；含数学运算符且不足一个真实短语时不把
			// 它们当希腊语。其他独立文字同样由显著片段门槛过滤专名噪声。
			if candidate.language == "el" && letters < 8 && strings.ContainsAny(chunk, "=+*/^_<>") {
				continue
			}
			contentPolicyAddLanguageCandidate(detected, &evidence, candidate.language, letters, stats.words[candidate.script], stats.totalLetters, false)
		}

		if stats.letters[contentPolicyScriptLatin] >= 4 {
			latin := detectLatinLanguageDetailed(chunk)
			if latin.code != "" {
				evidence.LatinEvidenceCounts[latin.code] += latin.evidenceCount
			}
			if latin.ok {
				contentPolicyAddLanguageCandidate(detected, &evidence, latin.code, stats.letters[contentPolicyScriptLatin], latin.totalWords, stats.totalLetters, false)
			}
		}
	})

	for language, count := range evidence.LanguageCounts {
		evidence.LanguageRatioBPS[language] = contentPolicyRatioBPS(count, evidence.TotalLetterRunes)
	}
	out := make([]string, 0, len(detected)+1)
	for language := range detected {
		out = append(out, language)
	}
	if len(out) == 0 {
		return contentPolicyLanguageDetection{Languages: []string{"unknown"}, Evidence: evidence}
	}
	sort.Strings(out)
	if len(out) > 1 {
		out = append(out, "mixed")
	}
	return contentPolicyLanguageDetection{Languages: out, Evidence: evidence}
}

func contentPolicyForEachLanguageChunk(text string, visit func(string)) {
	start := 0
	letters := 0
	for index, r := range text {
		if unicode.IsLetter(r) {
			letters++
		}
		end := index + utf8.RuneLen(r)
		if contentPolicyLanguageChunkBoundary(r) || letters >= contentPolicyLanguageChunkLetters {
			if chunk := strings.TrimSpace(text[start:end]); chunk != "" {
				visit(chunk)
			}
			start = end
			letters = 0
		}
	}
	if start < len(text) {
		if chunk := strings.TrimSpace(text[start:]); chunk != "" {
			visit(chunk)
		}
	}
}

func contentPolicyLanguageChunkBoundary(r rune) bool {
	switch r {
	case '\n', '\r', '.', '!', '?', ';', '\u3002', '\uff01', '\uff1f', '\uff1b':
		return true
	default:
		return false
	}
}

func contentPolicyCollectLanguageChunkStats(text string) contentPolicyLanguageChunkStats {
	var stats contentPolicyLanguageChunkStats
	previousScript := contentPolicyScriptCount
	for _, r := range text {
		if !unicode.IsLetter(r) {
			previousScript = contentPolicyScriptCount
			continue
		}
		script, ok := contentPolicyRuneScript(r)
		if !ok {
			previousScript = contentPolicyScriptCount
			continue
		}
		stats.totalLetters++
		stats.letters[script]++
		if previousScript != script {
			stats.words[script]++
		}
		previousScript = script
	}
	return stats
}

func contentPolicyRuneScript(r rune) (contentPolicyScript, bool) {
	// 英文及多数拉丁语正文以 ASCII 为主，不能让每个普通字母依次查询
	// 十二张 Unicode RangeTable（范围表）。调用方已确认它是字母；这里
	// 仍保留独立校验，避免该 helper 以后被直接调用时误收数字或符号。
	if r <= unicode.MaxASCII {
		if contentPolicyIsASCIIAlpha(byte(r)) {
			return contentPolicyScriptLatin, true
		}
		return 0, false
	}
	switch {
	case unicode.In(r, unicode.Hiragana):
		return contentPolicyScriptHiragana, true
	case unicode.In(r, unicode.Katakana):
		return contentPolicyScriptKatakana, true
	case unicode.In(r, unicode.Hangul):
		return contentPolicyScriptHangul, true
	case unicode.In(r, unicode.Han):
		return contentPolicyScriptHan, true
	case unicode.In(r, unicode.Bopomofo):
		return contentPolicyScriptBopomofo, true
	case unicode.In(r, unicode.Cyrillic):
		return contentPolicyScriptCyrillic, true
	case unicode.In(r, unicode.Arabic):
		return contentPolicyScriptArabic, true
	case unicode.In(r, unicode.Devanagari):
		return contentPolicyScriptDevanagari, true
	case unicode.In(r, unicode.Thai):
		return contentPolicyScriptThai, true
	case unicode.In(r, unicode.Hebrew):
		return contentPolicyScriptHebrew, true
	case unicode.In(r, unicode.Greek):
		return contentPolicyScriptGreek, true
	case unicode.In(r, unicode.Latin):
		return contentPolicyScriptLatin, true
	default:
		return 0, false
	}
}

func contentPolicyAddLanguageCandidate(detected map[string]struct{}, evidence *ContentModerationLanguageEvidence, language string, letters, words, total int, naturalMarker bool) {
	if letters <= 0 || total <= 0 {
		return
	}
	evidence.LanguageCounts[language] += letters
	ratioBPS := contentPolicyRatioBPS(letters, total)
	// 中文、泰文正常可以不使用空格；其他独立文字系统要求至少
	// 两个自然词块，避免单个人名、产品名或标识符在一个短分块内
	// 因占比 100% 就拦截整次请求。
	dominant := ratioBPS >= 6000 && ((language == "zh" && letters >= contentPolicyMinLanguageRunes) ||
		(language == "th" && letters >= 8) ||
		(language == "ja" && letters >= 8) ||
		(language != "zh" && language != "th" && language != "ja" && letters >= 8 && words >= 2))
	longPhrase := letters >= 16 && (language == "zh" || language == "th" || words >= 2)
	wordPhrase := letters >= 8 && words >= 3
	visibleSecondary := letters >= 8 && words >= 2 && ratioBPS >= contentPolicyMinLanguageRatioBPS
	// 中文和英文混排时，四个 Han 已足以构成真实中文短语；但必须同时存在
	// 中文功能字，避免四字人名或产品名让纯英文请求被判成中文。
	markedChinesePhrase := language == "zh" && naturalMarker && letters >= 3 && ratioBPS >= 800
	if dominant || longPhrase || wordPhrase || visibleSecondary || markedChinesePhrase {
		detected[language] = struct{}{}
	}
}

func contentPolicyRatioBPS(value, total int) int {
	if value <= 0 || total <= 0 {
		return 0
	}
	return value * 10_000 / total
}

func contentPolicyJapaneseGrammarEvidence(text string) int {
	evidence := 0
	for _, marker := range []string{"の", "を", "は", "が", "に", "で", "へ", "も", "です", "ます", "ない", "する", "した", "ください"} {
		if strings.Contains(text, marker) {
			evidence++
		}
	}
	return evidence
}

func contentPolicyHasChineseLanguageMarker(text string) bool {
	for _, marker := range []string{"请", "帮", "我", "你", "您", "这", "那", "的", "了", "吗", "呢", "怎么", "为什么", "解释", "问题", "需要", "可以", "請", "幫", "這", "為什麼", "問題", "需要", "可以"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// contentPolicyTextForLanguageDetection 只从语种识别视图中移除能够确认是
// 结构化代码的 fenced body（围栏正文）。围栏外说明仍参与语种门禁；把普通
// 葡萄牙语文字套进 ``` 也不会被豁免。原始 policyText 不会改变，关键词
// 自动机仍会扫描围栏内外的完整内容。
func contentPolicyTextForLanguageDetection(text string) string {
	languageText := text
	// Tool/Skill 常把结果包装成 JSON。JSON 的 key、type、call_id 等结构字段
	// 不是客户自然语言；只抽取 string value，可以同时避免英文 schema 误判，
	// 又不会漏掉 value 中真正用于引导模型的外语说明。
	if values, ok := contentPolicyJSONValuesForLanguageDetection(languageText); ok {
		languageText = values
	}
	if strings.Contains(text, "```") {
		parts := strings.Split(languageText, "```")
		if len(parts) >= 3 {
			kept := make([]string, 0, len(parts))
			for index, part := range parts {
				trimmed := strings.TrimSpace(part)
				if trimmed == "" {
					continue
				}
				isPairedFenceBody := index%2 == 1 && index < len(parts)-1
				if isPairedFenceBody {
					if contentPolicyHasStructuredFenceTag(trimmed) || contentPolicyLooksLikeCodeURLOrModel(trimmed) {
						continue
					}
				}
				kept = append(kept, trimmed)
			}
			languageText = strings.Join(kept, " ")
		}
	}
	// 围栏外也可能直接粘贴 SQL/代码。只移除确认的结构化片段，保留片段
	// 前后的自然语言说明，避免“葡萄牙语说明 + SQL”整体变成 unknown。
	languageText = contentPolicyStripInlineStructuredFragments(languageText)
	// 这些清理只用于 language view（语种识别视图）。调用者仍使用
	// 原始 policyText 执行关键词自动机，所以 URL、路径或代码里的关键词
	// 不会因语种降噪而绕过拦截。
	languageText = contentPolicyStripInlineCodeSpans(languageText)
	return strings.TrimSpace(contentPolicyStripLanguageArtifactTokens(languageText))
}

// contentPolicyHasStructuredFenceTag 只接受显式的代码/数据围栏标签。
// 无标签的 ``` 仍要通过内容启发式判断，不能让普通外语说明
// 只靠套一层围栏就躲过语种限制。
func contentPolicyHasStructuredFenceTag(text string) bool {
	firstLine := text
	// normalizeContentPolicyText 会把换行折叠为空格，所以这里必须
	// 取首个空白 token，不能只查 '\n'。
	if index := strings.IndexFunc(firstLine, unicode.IsSpace); index >= 0 {
		firstLine = firstLine[:index]
	}
	tag := strings.ToLower(strings.TrimSpace(firstLine))
	switch tag {
	case "bash", "c", "c++", "cpp", "csharp", "cs", "css", "csv", "diff", "dockerfile", "dotenv", "env",
		"go", "golang", "graphql", "html", "ini", "java", "javascript", "js", "json", "jsonc", "jsx",
		"kotlin", "less", "lua", "makefile", "php", "powershell", "ps1", "py", "python", "rb", "ruby",
		"rust", "scss", "sh", "shell", "sql", "swift", "toml", "ts", "tsx", "typescript", "xml", "yaml", "yml":
		return true
	default:
		return false
	}
}

// contentPolicyStripInlineCodeSpans 仅剥离可确认的 Markdown 行内代码。
// 若反引号中是普通自然语言，仍保留参与语种判断。
func contentPolicyStripInlineCodeSpans(text string) string {
	if !strings.Contains(text, "`") {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	for start := 0; start < len(text); {
		open := strings.IndexByte(text[start:], '`')
		if open < 0 {
			out.WriteString(text[start:])
			break
		}
		open += start
		out.WriteString(text[start:open])
		closeOffset := strings.IndexByte(text[open+1:], '`')
		if closeOffset < 0 {
			out.WriteString(text[open:])
			break
		}
		close := open + 1 + closeOffset
		inner := strings.TrimSpace(text[open+1 : close])
		if contentPolicyLooksLikeLanguageArtifactToken(inner) || contentPolicyLooksLikeCodeURLOrModel(inner) {
			out.WriteByte(' ')
		} else {
			out.WriteString(inner)
		}
		start = close + 1
	}
	return out.String()
}

// contentPolicyStripLanguageArtifactTokens 是单次线性扫描：只忽略明确的
// URL、路径、文件名、标识符和 key=value 日志项。普通文章不做
// 逐词分词，避免大 Skill 为语种降噪产生无界临时对象。
func contentPolicyStripLanguageArtifactTokens(text string) string {
	if text == "" {
		return text
	}
	// 常见大 Skill 可能是没有任何结构符号的长段落；先做一次不分配
	// 的快速判断，避免为“根本无需清理”的 256KB 文本再复制一份。
	if !contentPolicyMayContainEmbeddedArtifact(text) {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	start := 0
	for index, r := range text {
		if !unicode.IsSpace(r) {
			continue
		}
		if start < index {
			token := text[start:index]
			out.WriteString(contentPolicyStripLanguageArtifactToken(token))
		}
		out.WriteRune(r)
		start = index + utf8.RuneLen(r)
	}
	if start < len(text) {
		token := text[start:]
		out.WriteString(contentPolicyStripLanguageArtifactToken(token))
	}
	return out.String()
}

func contentPolicyStripLanguageArtifactToken(token string) string {
	if contentPolicyLooksLikeLanguageArtifactToken(token) {
		return " "
	}
	// 中文通常不加空格，所以文件名或日志项可能直接粘在汉字中间。
	// 只对包含结构符号或明显混合标识符的 token 做二次局部扫描，
	// 普通大段中文不会为此分配 rune slice（字符切片）。
	if !contentPolicyMayContainEmbeddedArtifact(token) {
		return token
	}
	runes := []rune(token)
	var out strings.Builder
	out.Grow(len(token))
	changed := false
	start := 0
	for index := 0; index <= len(runes); index++ {
		boundary := index == len(runes) || unicode.In(runes[index], unicode.Han, unicode.Bopomofo)
		if !boundary {
			continue
		}
		if start < index {
			part := string(runes[start:index])
			if contentPolicyLooksLikeLanguageArtifactToken(part) {
				out.WriteByte(' ')
				changed = true
			} else {
				out.WriteString(part)
			}
		}
		if index < len(runes) {
			out.WriteRune(runes[index])
		}
		start = index + 1
	}
	if !changed {
		return token
	}
	return out.String()
}

func contentPolicyMayContainEmbeddedArtifact(token string) bool {
	if strings.ContainsAny(token, ".=_/@\\") {
		return true
	}
	latinLetters := 0
	independentLetters := 0
	for _, r := range token {
		if unicode.In(r, unicode.Latin) {
			latinLetters++
		} else if unicode.IsLetter(r) && !unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Bopomofo) {
			independentLetters++
		}
	}
	return latinLetters >= 2 && independentLetters >= 8
}

func contentPolicyLooksLikeLanguageArtifactToken(raw string) bool {
	token := strings.Trim(raw, "\"'()[]{}<>,;!?\uff0c\u3002\uff01\uff1f\uff1b\uff1a\u3001\u201c\u201d\u2018\u2019\uff08\uff09\u3010\u3011\u300a\u300b")
	if token == "" {
		return false
	}
	for _, r := range token {
		if unicode.In(r, unicode.Han, unicode.Bopomofo) {
			return false
		}
	}
	lower := strings.ToLower(token)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "www.") {
		return true
	}
	if strings.ContainsAny(token, "=\\") || strings.Contains(token, "_") || strings.Contains(token, "@") {
		return true
	}
	if slashCount := strings.Count(token, "/"); slashCount >= 2 || (slashCount == 1 && strings.ContainsAny(token, ".:")) {
		return true
	}
	// 域名与文件名的末尾只接受 1~10 个 ASCII 字母，不会把句号、
	// 小数或 gpt-5.6-sol 这类模型名当成文件。
	if dot := strings.LastIndexByte(token, '.'); dot > 0 && dot+1 < len(token) {
		extension := token[dot+1:]
		if len(extension) <= 10 && contentPolicyASCIIOnlyLetters(extension) {
			return true
		}
	}
	// Latin+非 Han 的无分隔长 token 通常是粘连标识符，例如
	// abcПользовательскийXYZ。中文夹 OpenAI 等正常混写保留。
	latinLetters := 0
	independentLetters := 0
	for _, r := range token {
		if !unicode.IsLetter(r) {
			continue
		}
		if unicode.In(r, unicode.Latin) {
			latinLetters++
			continue
		}
		if !unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Bopomofo) {
			independentLetters++
		}
	}
	return latinLetters >= 2 && independentLetters >= 8
}

func contentPolicyASCIIOnlyLetters(text string) bool {
	if text == "" {
		return false
	}
	for index := 0; index < len(text); index++ {
		if (text[index] < 'a' || text[index] > 'z') && (text[index] < 'A' || text[index] > 'Z') {
			return false
		}
	}
	return true
}

// contentPolicyJSONValuesForLanguageDetection 是有界的轻量 tokenizer，不做整棵
// JSON 反序列化，因此大型 tool output 不会产生与正文同尺寸的对象树。它只区分
// object key 与 string value；数字、布尔值和结构符号本身没有语种意义。
func contentPolicyJSONValuesForLanguageDetection(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) < 2 || !((trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}') || (trimmed[0] == '[' && trimmed[len(trimmed)-1] == ']')) {
		return text, false
	}
	var values strings.Builder
	values.Grow(len(trimmed) / 4)
	foundString := false
	for index := 0; index < len(trimmed); {
		if trimmed[index] != '"' {
			index++
			continue
		}
		start := index + 1
		index = start
		escaped := false
		for index < len(trimmed) {
			current := trimmed[index]
			if escaped {
				escaped = false
				index++
				continue
			}
			if current == '\\' {
				escaped = true
				index++
				continue
			}
			if current == '"' {
				break
			}
			index++
		}
		if index >= len(trimmed) {
			return text, false
		}
		end := index
		index++
		next := index
		for next < len(trimmed) && unicode.IsSpace(rune(trimmed[next])) {
			next++
		}
		if next < len(trimmed) && trimmed[next] == ':' {
			continue
		}
		value := strings.TrimSpace(trimmed[start:end])
		if value == "" {
			continue
		}
		if values.Len() > 0 {
			values.WriteByte(' ')
		}
		values.WriteString(value)
		foundString = true
	}
	if !foundString {
		return "", true
	}
	return values.String(), true
}

type contentPolicyTextRange struct {
	start int
	end   int
}

func contentPolicyStripInlineStructuredFragments(text string) string {
	remainingStart := 0
	kept := make([]string, 0, 3)
	for removed := 0; removed < 16 && remainingStart < len(text); removed++ {
		fragment, ok := contentPolicyFindInlineStructuredFragment(text[remainingStart:])
		if !ok {
			if removed == 0 {
				return text
			}
			break
		}
		fragment.start += remainingStart
		fragment.end += remainingStart
		if prefix := strings.TrimSpace(text[remainingStart:fragment.start]); prefix != "" {
			kept = append(kept, prefix)
		}
		remainingStart = fragment.end
	}
	if suffix := strings.TrimSpace(text[remainingStart:]); suffix != "" {
		kept = append(kept, suffix)
	}
	return strings.Join(kept, " ")
}

func contentPolicyFindInlineStructuredFragment(text string) (contentPolicyTextRange, bool) {
	// 是否成立只取决于候选位置之后的结构证据。对 SQL 和代码分别记录
	// 最早候选即可：若更早候选的完整后缀都不成立，任何更晚、证据更少的
	// 同类候选也不可能成立。这样长文章出现常见单词 function 时，仍只保留
	// 两个整数位置，不再为全文构造 []string 和 []range。
	firstSQLStart := -1
	firstCodeStart := -1
	for index := 0; index < len(text); {
		if !contentPolicyIsASCIIAlpha(text[index]) {
			index++
			continue
		}
		start := index
		for index < len(text) && contentPolicyIsASCIIAlpha(text[index]) {
			index++
		}
		// 所有起始词最长 9 字节；更长 token 不可能成为候选。
		if index-start > 9 {
			continue
		}
		word := text[start:index]
		if firstSQLStart < 0 && contentPolicyASCIIWordInSetFold(word, contentPolicyInlineSQLStartWords) {
			firstSQLStart = start
		}
		if firstCodeStart < 0 && contentPolicyASCIIWordInSetFold(word, contentPolicyInlineCodeStartWords) {
			firstCodeStart = start
		}
		if firstSQLStart >= 0 && firstCodeStart >= 0 {
			break
		}
	}

	var sqlRange contentPolicyTextRange
	sqlOK := false
	if firstSQLStart >= 0 {
		fragment := text[firstSQLStart:]
		summary := contentPolicySummarizeASCIIWords(fragment)
		if contentPolicyLooksLikeStructuredSQLSummary(fragment, summary) {
			sqlRange = contentPolicyTextRange{start: firstSQLStart, end: contentPolicySQLFragmentEnd(text, firstSQLStart)}
			sqlOK = true
		}
	}

	var codeRange contentPolicyTextRange
	codeOK := false
	if firstCodeStart >= 0 {
		fragment := text[firstCodeStart:]
		summary := contentPolicySummarizeASCIIWords(fragment)
		if contentPolicyLooksLikeStructuredCodeSummary(fragment, summary) {
			codeRange = contentPolicyTextRange{start: firstCodeStart, end: contentPolicyCodeFragmentEnd(text, firstCodeStart)}
			codeOK = true
		}
	}

	switch {
	case sqlOK && (!codeOK || sqlRange.start <= codeRange.start):
		return sqlRange, true
	case codeOK:
		return codeRange, true
	default:
		return contentPolicyTextRange{}, false
	}
}

func contentPolicyASCIIWordInSetFold(word string, candidates map[string]struct{}) bool {
	for candidate := range candidates {
		if len(candidate) == len(word) && strings.EqualFold(candidate, word) {
			return true
		}
	}
	return false
}

func contentPolicyNormalizeASCIIWord(word string) string {
	// 结构识别词都很短。超长单词只需保留“这里有一个词”的占位，
	// 绝不能为 220KB 无分隔 Skill 复制同尺寸的小写字符串。
	if len(word) > 32 {
		return ""
	}
	for index := 0; index < len(word); index++ {
		if word[index] >= 'A' && word[index] <= 'Z' {
			return strings.ToLower(word)
		}
	}
	return word
}

func contentPolicyIsASCIIAlpha(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}

func contentPolicySQLFragmentEnd(text string, start int) int {
	if offset := strings.IndexByte(text[start:], ';'); offset >= 0 {
		return start + offset + 1
	}
	return len(text)
}

func contentPolicyCodeFragmentEnd(text string, start int) int {
	fragment := text[start:]
	if open := strings.IndexByte(fragment, '{'); open >= 0 {
		depth := 0
		for index := open; index < len(fragment); index++ {
			switch fragment[index] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return start + index + 1
				}
			}
		}
	}
	if offset := strings.IndexByte(fragment, ';'); offset >= 0 {
		return start + offset + 1
	}
	return len(text)
}

func contentPolicyLooksLikeCodeURLOrModel(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if (contentPolicyHasPrefixFold(trimmed, "http://") || contentPolicyHasPrefixFold(trimmed, "https://")) && !strings.ContainsAny(trimmed, " \t\r\n") {
		return true
	}
	summary := contentPolicySummarizeASCIIWords(trimmed)
	if contentPolicyLooksLikeStructuredSQLSummary(trimmed, summary) || contentPolicyLooksLikeStructuredCodeSummary(trimmed, summary) {
		return true
	}
	if summary.totalWords <= 4 && strings.ContainsAny(trimmed, "{}[]();=<>`") && !contentPolicyHasNaturalNonLatinLetters(trimmed, 3) {
		return true
	}
	if !strings.ContainsAny(trimmed, " \t\r\n") && strings.IndexFunc(trimmed, unicode.IsDigit) >= 0 && strings.ContainsAny(trimmed, "-_/.:+") {
		return true
	}
	return false
}

func contentPolicyHasPrefixFold(text, prefix string) bool {
	return len(text) >= len(prefix) && strings.EqualFold(text[:len(prefix)], prefix)
}

type contentPolicyASCIIWordSummary struct {
	totalWords     int
	firstWord      string
	firstLead      int
	leadWords      [3]string
	leadCount      int
	structureWords [3]string
	structureCount int
	codeWords      [2]string
	codeCount      int
}

// contentPolicySummarizeASCIIWords 只保留结构判定需要的有限状态。
// totalWords 在 5 封顶，因为调用方只关心“是否不超过 4”；即使输入是
// 220KB 多词文章，也不会创建与单词数量成正比的 []string。
func contentPolicySummarizeASCIIWords(text string) contentPolicyASCIIWordSummary {
	summary := contentPolicyASCIIWordSummary{firstLead: -1}
	wordIndex := 0
	for index := 0; index < len(text); {
		if !contentPolicyIsASCIIAlpha(text[index]) {
			index++
			continue
		}
		start := index
		for index < len(text) && contentPolicyIsASCIIAlpha(text[index]) {
			index++
		}
		if summary.totalWords < 5 {
			summary.totalWords++
		}
		word := contentPolicyNormalizeASCIIWord(text[start:index])
		if wordIndex == 0 {
			summary.firstWord = word
		}
		if _, ok := contentPolicySQLLeadWords[word]; ok {
			if summary.firstLead < 0 {
				summary.firstLead = wordIndex
			}
			contentPolicyAddUniqueSummaryWord(summary.leadWords[:], &summary.leadCount, word)
		}
		if _, ok := contentPolicySQLStructureWords[word]; ok {
			contentPolicyAddUniqueSummaryWord(summary.structureWords[:], &summary.structureCount, word)
		}
		if _, ok := contentPolicyCodeWords[word]; ok {
			contentPolicyAddUniqueSummaryWord(summary.codeWords[:], &summary.codeCount, word)
		}
		wordIndex++
	}
	return summary
}

func contentPolicyAddUniqueSummaryWord(words []string, count *int, word string) {
	for index := 0; index < *count; index++ {
		if words[index] == word {
			return
		}
	}
	if *count < len(words) {
		words[*count] = word
		*count++
	}
}

func contentPolicyLooksLikeStructuredSQLSummary(text string, summary contentPolicyASCIIWordSummary) bool {
	if summary.totalWords < 2 || summary.leadCount == 0 || summary.structureCount == 0 {
		return false
	}
	if summary.firstLead == 0 || (summary.firstLead <= 2 && (summary.firstWord == "sql" || summary.firstWord == "query")) {
		return true
	}
	return summary.leadCount+summary.structureCount >= 3 && strings.ContainsAny(text, "(),;=*.`\"")
}

func contentPolicyLooksLikeStructuredCodeSummary(text string, summary contentPolicyASCIIWordSummary) bool {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) >= 2 {
		if ((strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
			(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"))) && strings.Contains(trimmed, ":") {
			return true
		}
		if strings.HasPrefix(trimmed, "<") && (strings.Contains(trimmed, "</") || strings.Contains(trimmed, "/>")) {
			return true
		}
	}
	return strings.ContainsAny(trimmed, "{}[]();=<>:`") && summary.codeCount >= 2
}

func contentPolicyHasNaturalNonLatinLetters(text string, minimum int) bool {
	count := 0
	for _, r := range text {
		if unicode.IsLetter(r) && !unicode.In(r, unicode.Latin) {
			count++
			if count >= minimum {
				return true
			}
		}
	}
	return false
}

func contentPolicyLooksLikeStructuredSQL(text string, words []string) bool {
	if len(words) < 2 {
		return false
	}
	leadCount := 0
	structureCount := 0
	firstLead := -1
	seenLead := make(map[string]struct{}, 2)
	seenStructure := make(map[string]struct{}, 4)
	for index, word := range words {
		if _, ok := contentPolicySQLLeadWords[word]; ok {
			if firstLead < 0 {
				firstLead = index
			}
			if _, seen := seenLead[word]; !seen {
				seenLead[word] = struct{}{}
				leadCount++
			}
		}
		if _, ok := contentPolicySQLStructureWords[word]; ok {
			if _, seen := seenStructure[word]; !seen {
				seenStructure[word] = struct{}{}
				structureCount++
			}
		}
	}
	if leadCount == 0 || structureCount == 0 {
		return false
	}
	// 纯 SQL 通常由动作词开头；带中文说明时，第一个拉丁词常是 SQL/query，
	// 动作词紧随其后。这样简单 `select ... from ...` 也能识别，不要求大查询。
	if firstLead == 0 || (firstLead <= 2 && (words[0] == "sql" || words[0] == "query")) {
		return true
	}
	// 混在说明文字中的长 SQL 必须同时具备更多结构词和真实语法符号，
	// 避免把自然语言里的 “select ... from ...” 当成代码绕过语种限制。
	return leadCount+structureCount >= 3 && strings.ContainsAny(text, "(),;=*.`\"")
}

func contentPolicyLooksLikeStructuredCode(text string, words []string) bool {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) >= 2 {
		if ((strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
			(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"))) && strings.Contains(trimmed, ":") {
			return true
		}
		if strings.HasPrefix(trimmed, "<") && (strings.Contains(trimmed, "</") || strings.Contains(trimmed, "/>")) {
			return true
		}
	}
	if !strings.ContainsAny(trimmed, "{}[]();=<>:`") {
		return false
	}
	seen := make(map[string]struct{}, 3)
	for _, word := range words {
		if _, ok := contentPolicyCodeWords[word]; ok {
			seen[word] = struct{}{}
		}
	}
	return len(seen) >= 2
}

func detectLatinLanguage(text string) string {
	detected := detectLatinLanguageDetailed(text)
	if detected.ok {
		return detected.code
	}
	// 保留旧 helper 的回退语义，避免包内历史调用受到影响；真正语种门禁使用
	// detailed 结果的 ok 位，证据不足时会返回 unknown 而不是猜成英语。
	return "en"
}

func detectLatinLanguageDetailed(text string) contentPolicyLatinDetection {
	observedWords, totalWords, markerBits := contentPolicyCollectLatinEvidence(text)

	best := contentPolicyLatinDetection{totalWords: totalWords}
	secondScore := 0
	for profileIndex, profile := range contentPolicyLatinLanguageProfiles {
		evidenceCount := 0
		score := 0
		for word := range profile.words {
			if _, ok := observedWords[word]; ok {
				evidenceCount++
				if contentPolicyLatinWordProfileCounts[word] == 1 {
					score += 2
				} else {
					score++
				}
			}
		}
		independentMarkerEvidence := false
		for _, marker := range profile.markerRunes {
			if markerBits&contentPolicyLatinMarkerBit(marker) != 0 {
				// 语种专用字符比通用停用词更可靠，但仍必须再有一份独立证据。
				score += 3
				coveredByMatchedWord := false
				for word := range profile.words {
					if _, matched := observedWords[word]; matched && strings.ContainsRune(word, marker) {
						coveredByMatchedWord = true
						break
					}
				}
				if !coveredByMatchedWord {
					// 多个专用字符可能来自同一个专名，最多只算一份
					// 独立来源；已在命中词内的 ã/ğ/đ 等只加置信分，
					// 不能把同一个词重复算成“两份证据”。
					independentMarkerEvidence = true
				}
			}
		}
		if independentMarkerEvidence {
			evidenceCount++
		}
		if score > best.score {
			secondScore = best.score
			best.code = profile.code
			best.score = score
			best.evidenceCount = evidenceCount
		} else if score > secondScore {
			secondScore = score
		}
		// 相同分数时保持 profiles 的稳定顺序；下面的 margin 门槛会把无法
		// 区分的 `la/que/con` 判为 unknown，而不是依赖遍历顺序猜测。
		_ = profileIndex
	}
	minimumScore := 5
	if best.code == "en" {
		minimumScore = 4
	}
	best.ok = totalWords >= 3 && best.evidenceCount >= 2 && best.score >= minimumScore && best.score-secondScore >= 2
	return best
}

// contentPolicyCollectLatinEvidence 用一次 rune（字符）扫描同时完成分词、
// 证据词收集和专用字符统计。它只会给不超过 32 字节的候选词做局部小写，
// 220KB 无分隔 Skill 只计作一个普通词，不再复制正文或触发 regexp bitState。
func contentPolicyCollectLatinEvidence(text string) (map[string]struct{}, int, uint32) {
	var observedWords map[string]struct{}
	totalWords := 0
	var markerBits uint32
	wordStart := -1
	wordRunes := 0
	latinOnly := true

	flushWord := func(end int) {
		if wordStart < 0 {
			return
		}
		if latinOnly && wordRunes >= 2 {
			totalWords++
			if end-wordStart <= 32 {
				word := strings.ToLower(text[wordStart:end])
				if _, relevant := contentPolicyLatinEvidenceWords[word]; relevant {
					if observedWords == nil {
						observedWords = make(map[string]struct{}, 8)
					}
					observedWords[word] = struct{}{}
				}
			}
		}
		wordStart = -1
		wordRunes = 0
		latinOnly = true
	}

	for index, r := range text {
		// 所有语种专用 marker（标记字符）都是非 ASCII；英文大段正文
		// 无需为每个字母执行 Unicode 小写和 14 路 switch（分支判断）。
		if r >= utf8.RuneSelf {
			markerBits |= contentPolicyLatinMarkerBit(unicode.ToLower(r))
		}
		if r == '\'' || unicode.IsLetter(r) {
			if wordStart < 0 {
				wordStart = index
			}
			wordRunes++
			if r != '\'' && !unicode.In(r, unicode.Latin) {
				latinOnly = false
			}
			continue
		}
		flushWord(index)
	}
	flushWord(len(text))
	return observedWords, totalWords, markerBits
}

func contentPolicyLatinMarkerBit(r rune) uint32 {
	switch r {
	case 'ñ':
		return 1 << 0
	case '¿':
		return 1 << 1
	case '¡':
		return 1 << 2
	case 'œ':
		return 1 << 3
	case 'ß':
		return 1 << 4
	case 'ã':
		return 1 << 5
	case 'õ':
		return 1 << 6
	case 'ğ':
		return 1 << 7
	case 'ı':
		return 1 << 8
	case 'ş':
		return 1 << 9
	case 'ă':
		return 1 << 10
	case 'đ':
		return 1 << 11
	case 'ơ':
		return 1 << 12
	case 'ư':
		return 1 << 13
	default:
		return 0
	}
}

func contentPolicyIsLatinWord(word string) bool {
	for _, r := range word {
		if r == '\'' {
			continue
		}
		if !unicode.IsLetter(r) || !unicode.In(r, unicode.Latin) {
			return false
		}
	}
	return true
}

func unsupportedDetectedLanguages(detected, allowed []string) []string {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, language := range normalizeAllowedLanguages(allowed) {
		allowedSet[language] = struct{}{}
	}
	var unsupported []string
	for _, language := range detected {
		if language == "mixed" || language == "unknown" {
			continue
		}
		if _, ok := allowedSet[language]; !ok {
			unsupported = append(unsupported, language)
		}
	}
	return unsupported
}

func filterContentModerationInputForPolicy(input ContentModerationInput, cfg *ContentModerationConfig) ContentModerationInput {
	if cfg == nil || len(input.Segments) == 0 {
		return input
	}
	segments := make([]ContentModerationSegment, 0, len(input.Segments))
	parts := make([]string, 0, len(input.Segments))
	requestRunesLeft := maxContentPolicyRequestTextRunes
	instructionsRunesLeft := maxContentPolicyInstructionsTextRunes
	for _, segment := range input.Segments {
		allowed := false
		switch segment.Source {
		case ContentModerationSourceUser:
			allowed = cfg.ScanLatestUserInput
		case ContentModerationSourceSkill:
			allowed = cfg.ScanNewSkillContent
		case ContentModerationSourceTool:
			allowed = cfg.ScanNewToolOutput
		case ContentModerationSourceInstructions:
			allowed = cfg.ScanTopLevelInstructions
		}
		if !allowed {
			continue
		}
		budget := &requestRunesLeft
		if segment.Source == ContentModerationSourceInstructions {
			budget = &instructionsRunesLeft
		}
		if *budget <= 0 {
			continue
		}
		segment.Text = trimRunes(segment.Text, *budget)
		used := len([]rune(segment.Text))
		*budget -= used
		if strings.TrimSpace(segment.Text) == "" {
			continue
		}
		segments = append(segments, segment)
		parts = append(parts, segment.Text)
	}
	input.Segments = segments
	// Check 会在过滤完成后统一 Normalize；这里保留原始分隔，避免大型
	// instructions 在同一次请求里重复创建 Fields（字段切片）和文本副本。
	input.Text = strings.Join(parts, "\n")
	return input
}

// contentModerationPolicyText 在本地语种/基础匹配副本中用 NUL 分隔普通
// 内容和控制指令，防止两个无关来源拼成假命中；控制片段按结构边界以空格
// 合并。关键词专用的无分隔多块变体由 contentModerationKeywordPolicyText
// 追加，不能复制到语种样本中改变各语种字符比例。
func contentModerationPolicyText(input ContentModerationInput) string {
	if len(input.Segments) == 0 {
		return normalizeContentPolicyText(input.Text)
	}
	requestParts := make([]string, 0, len(input.Segments))
	instructionParts := make([]string, 0, len(input.Segments))
	for _, segment := range input.Segments {
		limit := maxContentPolicyRequestTextRunes
		if segment.Source == ContentModerationSourceInstructions {
			limit = maxContentPolicyInstructionsTextRunes
		}
		if normalized := normalizeContentPolicyTextWithLimit(segment.Text, limit); normalized != "" {
			if segment.Source == ContentModerationSourceInstructions {
				instructionParts = append(instructionParts, normalized)
			} else {
				requestParts = append(requestParts, normalized)
			}
		}
	}
	parts := make([]string, 0, 2)
	if len(requestParts) > 0 {
		parts = append(parts, strings.Join(requestParts, "\x00"))
	}
	if len(instructionParts) > 0 {
		// 每个片段已经完成 Fields（空白折叠）标准化；上游双换行在相同
		// 标准化语义下等价为一个空格，自动机才能识别跨边界完整短语。
		parts = append(parts, strings.Join(instructionParts, " "))
	}
	return strings.Join(parts, "\x00")
}

// contentModerationKeywordPolicyText 额外加入 Anthropic/Gemini 相邻系统文本
// 块的无分隔表示。上游可能连续解释两个块，因此既要识别
// "forbidden " + "directive"，也要识别 "forbid" + "den directive"。
// 两个表示之间使用 NUL，绝不会与普通用户内容或彼此首尾再组成新关键词。
func contentModerationKeywordPolicyText(input ContentModerationInput, base string) string {
	// Responses/Chat 的指令块只需要上面的空格连接表示；base 已经包含它。
	// 仅 Anthropic/Gemini 的相邻系统块需要额外“无分隔”表示，先做零分配
	// 扫描可避免普通 256 KiB instructions 再完整规范化、复制一次。
	if !contentModerationHasNoSeparatorInstructionRun(input.Segments) {
		return base
	}
	representations := contentModerationInstructionRepresentations(input.Segments)
	if len(representations) <= 1 {
		return base
	}
	var out strings.Builder
	out.Grow(len(base) + len(representations)*16)
	_, _ = out.WriteString(base)
	for _, representation := range representations[1:] {
		if representation.text == "" {
			continue
		}
		if out.Len() > 0 {
			_ = out.WriteByte('\x00')
		}
		_, _ = out.WriteString(representation.text)
	}
	return out.String()
}

func contentModerationHasNoSeparatorInstructionRun(segments []ContentModerationSegment) bool {
	for index := 1; index < len(segments); index++ {
		if contentModerationSameNoSeparatorInstructionRun(segments[index-1], segments[index]) {
			return true
		}
	}
	return false
}

func contentModerationSegmentContainsKeyword(text, keyword string) bool {
	normalizedText := strings.ToLower(normalizeContentPolicyTextWithLimit(text, maxContentPolicyInstructionsTextRunes))
	normalizedKeyword := strings.ToLower(normalizeContentPolicyText(keyword))
	if normalizedText == "" || normalizedKeyword == "" {
		return false
	}
	if contentModerationKeywordNeedsASCIIWordBoundary(normalizedKeyword) {
		return contentModerationContainsShortASCIIWord(normalizedText, normalizedKeyword)
	}
	return strings.Contains(normalizedText, normalizedKeyword)
}

func contentModerationMatchedSource(segments []ContentModerationSegment, keyword string) string {
	matchedSource := ""
	for _, segment := range segments {
		if segment.Source == ContentModerationSourceInstructions {
			continue
		}
		if !contentModerationSegmentContainsKeyword(segment.Text, keyword) {
			continue
		}
		if matchedSource == "" {
			matchedSource = segment.Source
			continue
		}
		if matchedSource != segment.Source {
			return "mixed"
		}
	}
	if len(contentModerationMatchedInstructionContributors(segments, keyword)) > 0 {
		if matchedSource != "" && matchedSource != ContentModerationSourceInstructions {
			return "mixed"
		}
		matchedSource = ContentModerationSourceInstructions
	}
	if matchedSource != "" {
		return matchedSource
	}
	return ContentModerationSourceUser
}

func contentModerationMatchedExcerpt(content ContentModerationInput, keyword string) string {
	normalizedKeyword := strings.ToLower(normalizeContentPolicyText(keyword))
	if normalizedKeyword == "" {
		return ""
	}
	for _, segment := range content.Segments {
		if segment.Source == ContentModerationSourceInstructions {
			continue
		}
		normalizedText := strings.ToLower(normalizeContentPolicyTextWithLimit(segment.Text, maxContentPolicyRequestTextRunes))
		if start, end, ok := contentModerationNormalizedKeywordOccurrence(normalizedText, normalizedKeyword); ok {
			return contentModerationExcerptAroundNormalizedMatch(normalizedText, start, end, maxModerationExcerptRunes)
		}
	}
	for _, representation := range contentModerationInstructionRepresentations(content.Segments) {
		if start, end, ok := contentModerationNormalizedKeywordOccurrence(representation.text, normalizedKeyword); ok {
			return contentModerationExcerptAroundNormalizedMatch(representation.text, start, end, maxModerationExcerptRunes)
		}
	}
	// 关键词匹配器和证据定位必须使用同一套标准化语义；若未来两者出现
	// 偏差，宁可只展示独立的 matched_keyword，也不能回退到最新用户消息，
	// 否则管理员会把无关正文误认为触发证据。
	return ""
}

func contentModerationNormalizedKeywordOccurrence(text, keyword string) (int, int, bool) {
	if text == "" || keyword == "" || len(text) < len(keyword) {
		return 0, 0, false
	}
	for searchFrom := 0; searchFrom <= len(text)-len(keyword); {
		relative := strings.Index(text[searchFrom:], keyword)
		if relative < 0 {
			return 0, 0, false
		}
		start := searchFrom + relative
		end := start + len(keyword)
		if !contentModerationKeywordNeedsASCIIWordBoundary(keyword) ||
			((start == 0 || !isContentModerationASCIIAlphaNumeric(text[start-1])) &&
				(end == len(text) || !isContentModerationASCIIAlphaNumeric(text[end]))) {
			return start, end, true
		}
		searchFrom = start + 1
	}
	return 0, 0, false
}

func contentModerationExcerptAroundNormalizedMatch(text string, start, end, maxRunes int) string {
	if text == "" || maxRunes <= 0 || start < 0 || end <= start || end > len(text) {
		return ""
	}
	totalRunes := utf8.RuneCountInString(text)
	if totalRunes <= maxRunes {
		return text
	}
	startRune := utf8.RuneCountInString(text[:start])
	endRune := startRune + utf8.RuneCountInString(text[start:end])
	matchRunes := endRune - startRune
	// 两个省略号各占一个字符；内置和自定义关键词上限为 200 字符，
	// 因此正常配置下命中词本身始终能完整保留在 240 字符证据窗口中。
	contentBudget := maxRunes - 2
	if contentBudget <= 0 || matchRunes >= contentBudget {
		windowEnd := startRune + maxRunes
		if windowEnd > totalRunes {
			windowEnd = totalRunes
		}
		windowStartByte, windowEndByte := contentModerationRuneWindowBytes(text, startRune, windowEnd)
		return text[windowStartByte:windowEndByte]
	}
	contextBudget := contentBudget - matchRunes
	leftBudget := contextBudget / 2
	rightBudget := contextBudget - leftBudget
	windowStart := startRune - leftBudget
	if windowStart < 0 {
		rightBudget += -windowStart
		windowStart = 0
	}
	windowEnd := endRune + rightBudget
	if windowEnd > totalRunes {
		windowStart -= windowEnd - totalRunes
		if windowStart < 0 {
			windowStart = 0
		}
		windowEnd = totalRunes
	}
	windowStartByte, windowEndByte := contentModerationRuneWindowBytes(text, windowStart, windowEnd)
	var excerpt strings.Builder
	if windowStart > 0 {
		excerpt.WriteRune('…')
	}
	excerpt.WriteString(text[windowStartByte:windowEndByte])
	if windowEnd < totalRunes {
		excerpt.WriteRune('…')
	}
	return excerpt.String()
}

func contentModerationRuneWindowBytes(text string, startRune, endRune int) (int, int) {
	if startRune <= 0 && endRune >= utf8.RuneCountInString(text) {
		return 0, len(text)
	}
	startByte, endByte := 0, len(text)
	runeIndex := 0
	for byteIndex := range text {
		if runeIndex == startRune {
			startByte = byteIndex
		}
		if runeIndex == endRune {
			endByte = byteIndex
			break
		}
		runeIndex++
	}
	return startByte, endByte
}

func keywordBlockRouteReason(input ContentModerationCheckInput) string {
	if input.KeywordRouteRequiresReconnect {
		return "websocket_reconnect_required"
	}
	return "no_candidate"
}
