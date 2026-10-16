package service

import (
	"encoding/json"
	"math"
)

// EstimateRequestTokensForLimit returns a conservative, dependency-free token estimate.
// It is used only for TPM admission control; final billing still uses upstream usage.
func EstimateRequestTokensForLimit(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return estimateCharsAsTokens(len(body), 4)
	}
	textChars := countLikelyPromptChars(payload)
	completionBudget := extractCompletionBudget(payload)
	estimate := estimateCharsAsTokens(textChars, 4) + completionBudget
	if estimate <= 0 {
		estimate = estimateCharsAsTokens(len(body), 4)
	}
	if estimate < 1 {
		return 1
	}
	return estimate
}

func estimateCharsAsTokens(chars int, charsPerToken int) int {
	if chars <= 0 {
		return 0
	}
	if charsPerToken <= 0 {
		charsPerToken = 4
	}
	return int(math.Ceil(float64(chars) / float64(charsPerToken)))
}

func countLikelyPromptChars(v any) int {
	switch x := v.(type) {
	case string:
		return len([]rune(x))
	case []any:
		total := 0
		for _, item := range x {
			total += countLikelyPromptChars(item)
		}
		return total
	case map[string]any:
		total := 0
		for key, val := range x {
			switch key {
			case "content", "text", "input", "prompt", "messages", "parts":
				total += countLikelyPromptChars(val)
			case "metadata", "tools", "tool_choice", "response_format", "stream_options":
				continue
			default:
				total += countLikelyPromptChars(val)
			}
		}
		return total
	default:
		return 0
	}
}

func extractCompletionBudget(v any) int {
	obj, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if n := numericInt(obj[key]); n > 0 {
			return n
		}
	}
	return 0
}

func numericInt(v any) int {
	switch x := v.(type) {
	case float64:
		if x > 0 {
			return int(math.Ceil(x))
		}
	case json.Number:
		n, _ := x.Int64()
		if n > 0 {
			return int(n)
		}
	}
	return 0
}
