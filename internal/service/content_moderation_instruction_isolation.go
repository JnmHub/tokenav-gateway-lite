package service

import (
	"sort"
	"strings"
)

type contentModerationInstructionSpan struct {
	segmentIndex int
	start        int
	end          int
}

type contentModerationInstructionRepresentation struct {
	text  string
	spans []contentModerationInstructionSpan
}

// contentModerationInstructionRepresentations 是关键词预审与命中贡献者定位
// 的唯一连接口径。所有协议先按真实控制片段顺序生成空格连接版本；Anthropic
// system blocks 与 Gemini parts 还生成无分隔版本，因为两类上游都可能把相邻
// 文本块连续解释。两种版本一起覆盖“词间拆块”和“词内拆块”，但不会把普通
// 用户内容或 Responses 顶层/消息级指令错误地无分隔拼接。
func contentModerationInstructionRepresentations(segments []ContentModerationSegment) []contentModerationInstructionRepresentation {
	allInstructionIndexes := make([]int, 0, len(segments))
	for index, segment := range segments {
		if segment.Source == ContentModerationSourceInstructions {
			allInstructionIndexes = append(allInstructionIndexes, index)
		}
	}
	representations := make([]contentModerationInstructionRepresentation, 0, 3)
	if representation := buildContentModerationInstructionRepresentation(segments, allInstructionIndexes, " "); representation.text != "" {
		representations = append(representations, representation)
	}

	for start := 0; start < len(segments); {
		if !contentModerationInstructionNeedsNoSeparatorVariant(segments[start]) {
			start++
			continue
		}
		end := start + 1
		for end < len(segments) && contentModerationSameNoSeparatorInstructionRun(segments[start], segments[end]) {
			end++
		}
		if end-start > 1 {
			indexes := make([]int, 0, end-start)
			for index := start; index < end; index++ {
				indexes = append(indexes, index)
			}
			if representation := buildContentModerationInstructionRepresentation(segments, indexes, ""); representation.text != "" {
				representations = append(representations, representation)
			}
		}
		start = end
	}
	return representations
}

func contentModerationInstructionNeedsNoSeparatorVariant(segment ContentModerationSegment) bool {
	if segment.Source != ContentModerationSourceInstructions {
		return false
	}
	return segment.Protocol == ContentModerationProtocolAnthropicMessages || segment.Protocol == ContentModerationProtocolGemini
}

func contentModerationSameNoSeparatorInstructionRun(first, next ContentModerationSegment) bool {
	return contentModerationInstructionNeedsNoSeparatorVariant(next) &&
		first.Protocol == next.Protocol && first.InstructionOrigin == next.InstructionOrigin
}

func buildContentModerationInstructionRepresentation(segments []ContentModerationSegment, indexes []int, separator string) contentModerationInstructionRepresentation {
	var joined strings.Builder
	spans := make([]contentModerationInstructionSpan, 0, len(indexes))
	for _, segmentIndex := range indexes {
		if segmentIndex < 0 || segmentIndex >= len(segments) {
			continue
		}
		normalized := strings.ToLower(normalizeContentPolicyTextWithLimit(segments[segmentIndex].Text, maxContentPolicyInstructionsTextRunes))
		if normalized == "" {
			continue
		}
		if joined.Len() > 0 {
			_, _ = joined.WriteString(separator)
		}
		start := joined.Len()
		_, _ = joined.WriteString(normalized)
		spans = append(spans, contentModerationInstructionSpan{segmentIndex: segmentIndex, start: start, end: joined.Len()})
	}
	return contentModerationInstructionRepresentation{text: joined.String(), spans: spans}
}

// contentModerationMatchedInstructionContributors returns only the control
// segments that actually overlap a keyword occurrence, whether it is contained
// in one segment or spans several. Recording every system/developer instruction
// would make one split phrase erase unrelated safe project instructions later.
func contentModerationMatchedInstructionContributors(segments []ContentModerationSegment, keyword string) []string {
	normalizedKeyword := strings.ToLower(normalizeContentPolicyText(keyword))
	if normalizedKeyword == "" {
		return nil
	}
	selected := make(map[int]struct{})
	for _, representation := range contentModerationInstructionRepresentations(segments) {
		text, spans := representation.text, representation.spans
		for searchFrom := 0; searchFrom <= len(text)-len(normalizedKeyword); {
			relative := strings.Index(text[searchFrom:], normalizedKeyword)
			if relative < 0 {
				break
			}
			start := searchFrom + relative
			end := start + len(normalizedKeyword)
			matched := true
			if contentModerationKeywordNeedsASCIIWordBoundary(normalizedKeyword) {
				matched = (start == 0 || !isContentModerationASCIIAlphaNumeric(text[start-1])) &&
					(end == len(text) || !isContentModerationASCIIAlphaNumeric(text[end]))
			}
			if matched {
				first := sort.Search(len(spans), func(index int) bool { return spans[index].end > start })
				for index := first; index < len(spans) && spans[index].start < end; index++ {
					selected[spans[index].segmentIndex] = struct{}{}
				}
			}
			searchFrom = start + 1
		}
	}
	if len(selected) == 0 {
		return nil
	}

	out := make([]string, 0, len(selected))
	for index, segment := range segments {
		if _, ok := selected[index]; ok {
			out = append(out, segment.Text)
		}
	}
	return out
}
