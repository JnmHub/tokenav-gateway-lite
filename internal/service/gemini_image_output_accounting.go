package service

import (
	"crypto/sha256"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const geminiImageOutputCounterKey = "gemini_image_output_counter"

// geminiImageOutputCounter 记录一次转发里 Gemini 上游真正回吐的内联图片数量。
// 同一图片可能在累积式 SSE 的后续分片里再次出现，因此按内容指纹去重；
// 这样既能累计不同增量分片里的图片，也不会重复计算累积快照。
type geminiImageOutputCounter struct {
	seenFingerprints map[[sha256.Size]byte]struct{}
}

// beginGeminiImageOutputObservation 每次转发都重置计数器，防止 failover 复用
// gin.Context 时把失败账号已经返回的图片带入成功账号账单。
func beginGeminiImageOutputObservation(c *gin.Context) *geminiImageOutputCounter {
	if c == nil {
		return nil
	}
	counter := &geminiImageOutputCounter{
		seenFingerprints: make(map[[sha256.Size]byte]struct{}),
	}
	c.Set(geminiImageOutputCounterKey, counter)
	return counter
}

func geminiImageOutputCounterFromContext(c *gin.Context) *geminiImageOutputCounter {
	if c == nil {
		return nil
	}
	value, ok := c.Get(geminiImageOutputCounterKey)
	if !ok {
		return nil
	}
	counter, _ := value.(*geminiImageOutputCounter)
	return counter
}

func observeGeminiImageOutputs(c *gin.Context, payload []byte) {
	counter := geminiImageOutputCounterFromContext(c)
	if counter == nil {
		return
	}
	if counter.seenFingerprints == nil {
		counter.seenFingerprints = make(map[[sha256.Size]byte]struct{})
	}
	forEachGeminiInlineImage(payload, func(fingerprint [sha256.Size]byte) {
		counter.seenFingerprints[fingerprint] = struct{}{}
	})
}

func observedGeminiImageOutputs(c *gin.Context) int {
	counter := geminiImageOutputCounterFromContext(c)
	if counter == nil {
		return 0
	}
	return len(counter.seenFingerprints)
}

// resolveGeminiImageCount 优先按上游实际返回的图片张数计费；响应没有可计数的
// 内联图片时才退回模型名判断，同时兼顾请求模型名和映射后的上游模型名。
func resolveGeminiImageCount(c *gin.Context, originalModel, mappedModel string) int {
	if observed := observedGeminiImageOutputs(c); observed > 0 {
		return observed
	}
	if isImageGenerationModel(originalModel) || isImageGenerationModel(mappedModel) {
		return 1
	}
	return 0
}

func countGeminiInlineImageOutputs(payload []byte) int {
	count := 0
	forEachGeminiInlineImage(payload, func(_ [sha256.Size]byte) {
		count++
	})
	return count
}

func forEachGeminiInlineImage(payload []byte, visit func([sha256.Size]byte)) {
	if len(payload) == 0 || visit == nil || !gjson.ValidBytes(payload) {
		return
	}
	gjson.GetBytes(payload, "candidates").ForEach(func(_, candidate gjson.Result) bool {
		candidate.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
			if fingerprint, ok := geminiInlineImageFingerprint(part); ok {
				visit(fingerprint)
			}
			return true
		})
		return true
	})
}

func geminiInlineImageFingerprint(part gjson.Result) ([sha256.Size]byte, bool) {
	var empty [sha256.Size]byte
	inline := part.Get("inlineData")
	if !inline.Exists() {
		inline = part.Get("inline_data")
	}
	if !inline.Exists() {
		return empty, false
	}

	mimeType := inline.Get("mimeType")
	if !mimeType.Exists() {
		mimeType = inline.Get("mime_type")
	}
	if !isGeminiInlineImageMIMEType(strings.ToLower(strings.TrimSpace(mimeType.String()))) {
		return empty, false
	}

	// 空壳 part 没有真正返回图片，不进入按张计费。
	data := strings.TrimSpace(inline.Get("data").String())
	if data == "" {
		return empty, false
	}

	// 图片通常可达数 MB，流式写入哈希可避免把 base64 字符串再复制一份。
	hasher := sha256.New()
	_, _ = io.Copy(hasher, strings.NewReader(data))
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], hasher.Sum(nil))
	return fingerprint, true
}
