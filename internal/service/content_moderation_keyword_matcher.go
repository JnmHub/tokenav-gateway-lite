package service

import (
	"sort"
	"strings"
	"unicode/utf8"
)

type contentModerationKeywordRuleMatcher struct {
	matcher                *contentModerationKeywordMatcher
	matches                []contentModerationKeywordMatch
	matchPriorities        []int
	matchLengths           []int
	asciiWordKeywords      map[string]contentModerationKeywordCandidate
	asciiWordKeywordMaxLen int
}

type contentModerationKeywordCandidate struct {
	match            contentModerationKeywordMatch
	priority         int
	normalizedLength int
}

type contentModerationKeywordEntry struct {
	normalized string
	candidate  contentModerationKeywordCandidate
}

func newContentModerationKeywordRuleMatcher(cfg *ContentModerationConfig) *contentModerationKeywordRuleMatcher {
	if cfg == nil {
		return nil
	}
	entries := make([]contentModerationKeywordEntry, 0, len(cfg.BlockedKeywords))
	asciiWordKeywords := make(map[string]contentModerationKeywordCandidate)
	asciiWordKeywordMaxLen := 0
	priority := 0
	addKeyword := func(keyword, normalizedKeyword string, match contentModerationKeywordMatch) {
		candidate := contentModerationKeywordCandidate{
			match:            match,
			priority:         priority,
			normalizedLength: utf8.RuneCountInString(normalizedKeyword),
		}
		priority++
		if contentModerationKeywordNeedsASCIIWordBoundary(normalizedKeyword) {
			key := strings.ToLower(normalizedKeyword)
			if current, exists := asciiWordKeywords[key]; !exists || contentModerationKeywordCandidateBetter(candidate, current) {
				asciiWordKeywords[key] = candidate
			}
			if len(key) > asciiWordKeywordMaxLen {
				asciiWordKeywordMaxLen = len(key)
			}
			return
		}
		entries = append(entries, contentModerationKeywordEntry{
			normalized: normalizedKeyword,
			candidate:  candidate,
		})
	}
	for _, keyword := range cfg.BlockedKeywords {
		normalizedKeyword := normalizeContentPolicyText(keyword)
		if normalizedKeyword == "" {
			continue
		}
		addKeyword(keyword, normalizedKeyword, contentModerationKeywordMatch{
			Keyword:  keyword,
			PackID:   "custom",
			PackName: "自定义关键词",
			Action:   ContentModerationActionKeywordBlock,
		})
	}
	for _, pack := range cfg.KeywordRulePacks {
		if !pack.Enabled {
			continue
		}
		action := normalizeContentModerationKeywordRuleAction(pack.Action)
		if action == "" {
			continue
		}
		for _, keyword := range pack.Keywords {
			normalizedKeyword := normalizeContentPolicyText(keyword)
			if normalizedKeyword == "" {
				continue
			}
			addKeyword(keyword, normalizedKeyword, contentModerationKeywordMatch{
				Keyword:  keyword,
				PackID:   pack.ID,
				PackName: pack.Name,
				Action:   action,
			})
		}
	}
	// 自动机只保存每个状态的最佳下标，因此在构建前固定业务优先级：强拦截
	// 永远高于只审计；动作相同时优先更长、更具体的短语；最后才沿用配置顺序。
	sort.SliceStable(entries, func(left, right int) bool {
		return contentModerationKeywordCandidateBetter(entries[left].candidate, entries[right].candidate)
	})
	keywords := make([]string, 0, len(entries))
	matches := make([]contentModerationKeywordMatch, 0, len(entries))
	matchPriorities := make([]int, 0, len(entries))
	matchLengths := make([]int, 0, len(entries))
	for _, entry := range entries {
		keywords = append(keywords, entry.normalized)
		matches = append(matches, entry.candidate.match)
		matchPriorities = append(matchPriorities, entry.candidate.priority)
		matchLengths = append(matchLengths, entry.candidate.normalizedLength)
	}
	matcher := newContentModerationKeywordMatcher(keywords)
	if matcher == nil && len(asciiWordKeywords) == 0 {
		return nil
	}
	return &contentModerationKeywordRuleMatcher{
		matcher:                matcher,
		matches:                matches,
		matchPriorities:        matchPriorities,
		matchLengths:           matchLengths,
		asciiWordKeywords:      asciiWordKeywords,
		asciiWordKeywordMaxLen: asciiWordKeywordMaxLen,
	}
}

func contentModerationKeywordCandidateBetter(left, right contentModerationKeywordCandidate) bool {
	leftAction := contentModerationKeywordActionPriority(left.match.Action)
	rightAction := contentModerationKeywordActionPriority(right.match.Action)
	if leftAction != rightAction {
		return leftAction < rightAction
	}
	if left.normalizedLength != right.normalizedLength {
		return left.normalizedLength > right.normalizedLength
	}
	return left.priority < right.priority
}

func contentModerationKeywordActionPriority(action string) int {
	if action == ContentModerationActionKeywordBlock {
		return 0
	}
	if action == ContentModerationActionKeywordAudit {
		return 1
	}
	return 2
}

func contentModerationKeywordCoveredByShorterPhrase(keyword string, accepted []string) bool {
	lower := strings.ToLower(keyword)
	for _, existing := range accepted {
		if len(existing) > len(lower) {
			continue
		}
		if contentModerationKeywordNeedsASCIIWordBoundary(existing) {
			if contentModerationContainsShortASCIIWord(lower, existing) {
				return true
			}
			continue
		}
		if strings.Contains(lower, existing) {
			return true
		}
	}
	return false
}

// 纯 ASCII 单词必须按完整 token（词元）匹配。否则 exp 会误撞 example/export，
// rce 会误撞 source，ida 会误撞 validation，carding 也会误撞 discarding。
// 中文和包含空格/标点的短语仍交给 Aho-Corasick（多关键词自动机）做原有子串匹配。
func contentModerationKeywordNeedsASCIIWordBoundary(keyword string) bool {
	if len(keyword) == 0 {
		return false
	}
	for index := 0; index < len(keyword); index++ {
		if !isContentModerationASCIIAlphaNumeric(keyword[index]) {
			return false
		}
	}
	return true
}

func isContentModerationASCIIAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func contentModerationContainsShortASCIIWord(text, keyword string) bool {
	for searchFrom := 0; searchFrom <= len(text)-len(keyword); {
		relative := strings.Index(text[searchFrom:], keyword)
		if relative < 0 {
			return false
		}
		start := searchFrom + relative
		end := start + len(keyword)
		leftBoundary := start == 0 || !isContentModerationASCIIAlphaNumeric(text[start-1])
		rightBoundary := end == len(text) || !isContentModerationASCIIAlphaNumeric(text[end])
		if leftBoundary && rightBoundary {
			return true
		}
		searchFrom = start + 1
	}
	return false
}

func (m *contentModerationKeywordRuleMatcher) Match(text string) (*contentModerationKeywordMatch, bool) {
	return m.MatchNormalized(normalizeContentPolicyText(text))
}

func (m *contentModerationKeywordRuleMatcher) MatchNormalized(text string) (*contentModerationKeywordMatch, bool) {
	if m == nil || text == "" {
		return nil, false
	}
	lower := strings.ToLower(text)
	best := contentModerationKeywordCandidate{priority: int(^uint(0) >> 1)}
	found := false
	if m.matcher != nil {
		index, ok := m.matcher.matchLowerIndex(lower)
		if ok && index >= 0 && index < len(m.matches) && index < len(m.matchPriorities) && index < len(m.matchLengths) {
			best = contentModerationKeywordCandidate{
				match:            m.matches[index],
				priority:         m.matchPriorities[index],
				normalizedLength: m.matchLengths[index],
			}
			found = true
		}
	}
	if candidate, ok := matchContentModerationASCIIWordKeyword(lower, m.asciiWordKeywords, m.asciiWordKeywordMaxLen); ok && (!found || contentModerationKeywordCandidateBetter(candidate, best)) {
		best = candidate
		found = true
	}
	if !found {
		return nil, false
	}
	match := best.match
	return &match, true
}

func matchContentModerationASCIIWordKeyword(text string, keywords map[string]contentModerationKeywordCandidate, maxKeywordLen int) (contentModerationKeywordCandidate, bool) {
	best := contentModerationKeywordCandidate{priority: int(^uint(0) >> 1)}
	if text == "" || len(keywords) == 0 || maxKeywordLen <= 0 {
		return best, false
	}
	found := false
	for start, index := -1, 0; index <= len(text); index++ {
		if index < len(text) && isContentModerationASCIIAlphaNumeric(text[index]) {
			if start < 0 {
				start = index
			}
			continue
		}
		if start >= 0 && index-start <= maxKeywordLen {
			if candidate, ok := keywords[text[start:index]]; ok && (!found || contentModerationKeywordCandidateBetter(candidate, best)) {
				best = candidate
				found = true
			}
		}
		start = -1
	}
	return best, found
}

type contentModerationKeywordMatcher struct {
	nodes           []contentModerationKeywordNode
	edges           []contentModerationKeywordEdge
	rootTransitions [256]int32
	keywords        []string
}

type contentModerationKeywordNode struct {
	failure     int32
	bestKeyword int32
	edgeStart   uint32
	edgeCount   uint16
}

type contentModerationKeywordEdge struct {
	target int32
	label  byte
}

type contentModerationKeywordBuildEdge struct {
	target      int32
	nextSibling int32
	label       byte
}

func newContentModerationKeywordMatcher(keywords []string) *contentModerationKeywordMatcher {
	if len(keywords) == 0 {
		return nil
	}

	buildNodes := []contentModerationKeywordNode{newContentModerationKeywordNode()}
	buildEdges := make([]contentModerationKeywordBuildEdge, 0)
	originalKeywords := append([]string(nil), keywords...)

	for keywordIndex, keyword := range keywords {
		if keyword == "" {
			continue
		}
		state := int32(0)
		for _, label := range []byte(strings.ToLower(keyword)) {
			next := contentModerationKeywordBuildTransition(buildNodes, buildEdges, state, label)
			if next < 0 {
				next = int32(len(buildNodes))
				buildNodes = append(buildNodes, newContentModerationKeywordNode())
				buildEdges = append(buildEdges, contentModerationKeywordBuildEdge{
					target:      next,
					nextSibling: contentModerationKeywordBuildFirstEdge(buildNodes[state]),
					label:       label,
				})
				buildNodes[state].edgeStart = uint32(len(buildEdges))
			}
			state = next
		}
		if current := buildNodes[state].bestKeyword; current < 0 || int32(keywordIndex) < current {
			buildNodes[state].bestKeyword = int32(keywordIndex)
		}
	}

	if len(buildNodes) == 1 {
		return nil
	}

	queue := make([]int32, 0, len(buildNodes)-1)
	var rootTransitions [256]int32
	for edgeIndex := contentModerationKeywordBuildFirstEdge(buildNodes[0]); edgeIndex >= 0; edgeIndex = buildEdges[edgeIndex].nextSibling {
		edge := buildEdges[edgeIndex]
		rootTransitions[edge.label] = edge.target
		queue = append(queue, edge.target)
	}

	for queueIndex := 0; queueIndex < len(queue); queueIndex++ {
		state := queue[queueIndex]
		for edgeIndex := contentModerationKeywordBuildFirstEdge(buildNodes[state]); edgeIndex >= 0; edgeIndex = buildEdges[edgeIndex].nextSibling {
			edge := buildEdges[edgeIndex]
			failure := buildNodes[state].failure
			fallback := contentModerationKeywordBuildTransition(buildNodes, buildEdges, failure, edge.label)
			for fallback < 0 && failure != 0 {
				failure = buildNodes[failure].failure
				fallback = contentModerationKeywordBuildTransition(buildNodes, buildEdges, failure, edge.label)
			}
			if fallback >= 0 {
				buildNodes[edge.target].failure = fallback
			}
			buildNodes[edge.target].bestKeyword = minKeywordIndex(
				buildNodes[edge.target].bestKeyword,
				buildNodes[buildNodes[edge.target].failure].bestKeyword,
			)
			queue = append(queue, edge.target)
		}
	}

	edges := make([]contentModerationKeywordEdge, 0, len(buildEdges))
	var outgoing [256]contentModerationKeywordEdge
	for nodeIndex := range buildNodes {
		count := 0
		for edgeIndex := contentModerationKeywordBuildFirstEdge(buildNodes[nodeIndex]); edgeIndex >= 0; edgeIndex = buildEdges[edgeIndex].nextSibling {
			edge := buildEdges[edgeIndex]
			outgoing[count] = contentModerationKeywordEdge{target: edge.target, label: edge.label}
			count++
		}
		for index := 1; index < count; index++ {
			current := outgoing[index]
			insertAt := index
			for insertAt > 0 && current.label < outgoing[insertAt-1].label {
				outgoing[insertAt] = outgoing[insertAt-1]
				insertAt--
			}
			outgoing[insertAt] = current
		}
		buildNodes[nodeIndex].edgeStart = uint32(len(edges))
		buildNodes[nodeIndex].edgeCount = uint16(count)
		edges = append(edges, outgoing[:count]...)
	}

	return &contentModerationKeywordMatcher{
		nodes:           buildNodes,
		edges:           edges,
		rootTransitions: rootTransitions,
		keywords:        originalKeywords,
	}
}

func newContentModerationKeywordNode() contentModerationKeywordNode {
	return contentModerationKeywordNode{bestKeyword: -1}
}

func contentModerationKeywordBuildFirstEdge(node contentModerationKeywordNode) int32 {
	if node.edgeStart == 0 {
		return -1
	}
	return int32(node.edgeStart - 1)
}

func contentModerationKeywordBuildTransition(
	nodes []contentModerationKeywordNode,
	edges []contentModerationKeywordBuildEdge,
	state int32,
	label byte,
) int32 {
	if state < 0 || int(state) >= len(nodes) {
		return -1
	}
	for edgeIndex := contentModerationKeywordBuildFirstEdge(nodes[state]); edgeIndex >= 0; edgeIndex = edges[edgeIndex].nextSibling {
		if edges[edgeIndex].label == label {
			return edges[edgeIndex].target
		}
	}
	return -1
}

func minKeywordIndex(left, right int32) int32 {
	if left < 0 {
		return right
	}
	if right < 0 || left < right {
		return left
	}
	return right
}

func (m *contentModerationKeywordMatcher) Match(text string) (string, bool) {
	index, ok := m.MatchIndex(text)
	if !ok {
		return "", false
	}
	return m.keywords[index], true
}

func (m *contentModerationKeywordMatcher) MatchIndex(text string) (int, bool) {
	if m == nil || text == "" || len(m.nodes) == 0 || len(m.keywords) == 0 {
		return 0, false
	}
	return m.matchLowerIndex(strings.ToLower(text))
}

func (m *contentModerationKeywordMatcher) matchLowerIndex(lower string) (int, bool) {
	if m == nil || lower == "" || len(m.nodes) == 0 || len(m.keywords) == 0 {
		return 0, false
	}
	state := int32(0)
	bestKeyword := int32(-1)
	for index := 0; index < len(lower); index++ {
		label := lower[index]
		for {
			next := m.next(state, label)
			if next != 0 {
				state = next
				break
			}
			if state == 0 {
				break
			}
			state = m.nodes[state].failure
		}
		bestKeyword = minKeywordIndex(bestKeyword, m.nodes[state].bestKeyword)
		if bestKeyword == 0 {
			return 0, true
		}
	}
	if bestKeyword < 0 || int(bestKeyword) >= len(m.keywords) {
		return 0, false
	}
	return int(bestKeyword), true
}

func (m *contentModerationKeywordMatcher) next(state int32, label byte) int32 {
	if state == 0 {
		return m.rootTransitions[label]
	}
	if state < 0 || int(state) >= len(m.nodes) {
		return 0
	}
	node := m.nodes[state]
	left := int(node.edgeStart)
	right := left + int(node.edgeCount)
	for left < right {
		middle := left + (right-left)/2
		edge := m.edges[middle]
		if edge.label < label {
			left = middle + 1
			continue
		}
		right = middle
	}
	end := int(node.edgeStart) + int(node.edgeCount)
	if left < end && m.edges[left].label == label {
		return m.edges[left].target
	}
	return 0
}
