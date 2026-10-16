package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAIImageOutputCounter struct {
	seen         map[string]struct{}
	seenSizes    map[string]string
	seenOrder    []string
	dataSizes    []string
	count        int
	maxDataCount int
}

func newOpenAIImageOutputCounter() *openAIImageOutputCounter {
	return &openAIImageOutputCounter{
		seen:      make(map[string]struct{}),
		seenSizes: make(map[string]string),
	}
}

func (c *openAIImageOutputCounter) Count() int {
	if c == nil {
		return 0
	}
	if c.maxDataCount > c.count {
		return c.maxDataCount
	}
	return c.count
}

func (c *openAIImageOutputCounter) Sizes() []string {
	if c == nil {
		return nil
	}
	sizes := make([]string, 0, len(c.seenOrder)+len(c.dataSizes))
	for _, key := range c.seenOrder {
		if size := strings.TrimSpace(c.seenSizes[key]); size != "" {
			sizes = append(sizes, size)
		}
	}
	if len(sizes) == 0 && len(c.dataSizes) > 0 {
		sizes = append(sizes, c.dataSizes...)
	}
	if len(sizes) == 0 {
		return nil
	}
	return sizes
}

func (c *openAIImageOutputCounter) AddJSONResponse(body []byte) {
	if c == nil || len(body) == 0 || !gjson.ValidBytes(body) {
		return
	}
	c.addDataArray(gjson.GetBytes(body, "data"))
	c.addOutputArray(gjson.GetBytes(body, "output"))
	c.addOutputArray(gjson.GetBytes(body, "response.output"))
}

func (c *openAIImageOutputCounter) AddSSEData(data []byte) {
	if c == nil || len(data) == 0 || strings.TrimSpace(string(data)) == "[DONE]" || !gjson.ValidBytes(data) {
		return
	}
	root := gjson.ParseBytes(data)
	c.addDataArray(root.Get("data"))
	eventType := strings.TrimSpace(root.Get("type").String())
	switch eventType {
	case "response.output_item.done":
		c.addImageOutputItem(root.Get("item"))
	case "response.completed", "response.done":
		c.addOutputArray(root.Get("response.output"))
	case "image_generation.completed":
		if item := root.Get("item"); item.Exists() {
			c.addImageOutputItem(item)
			return
		}
		if output := root.Get("output"); output.Exists() {
			c.addImageOutputItem(output)
			return
		}
		c.addImageOutputItem(root)
	}
}

func (c *openAIImageOutputCounter) AddSSEBody(body string) {
	if c == nil || strings.TrimSpace(body) == "" {
		return
	}
	forEachOpenAISSEDataPayload(body, c.AddSSEData)
}

func (c *openAIImageOutputCounter) addDataArray(data gjson.Result) {
	if !data.IsArray() {
		return
	}
	items := data.Array()
	imageCount := 0
	sizes := make([]string, 0, len(items))
	for _, item := range items {
		if !item.IsObject() {
			continue
		}
		hasImageOutput := strings.TrimSpace(item.Get("url").String()) != "" ||
			strings.TrimSpace(item.Get("b64_json").String()) != ""
		if !hasImageOutput {
			continue
		}
		imageCount++
		size := strings.TrimSpace(item.Get("size").String())
		if size == "" {
			// Native Images API（原生图片接口）通常只返回 b64_json；最终图片字节比请求尺寸更适合作为日志和分档计费证据。
			size = detectOpenAIImageResultSize(strings.TrimSpace(item.Get("b64_json").String()))
		}
		if size != "" {
			sizes = append(sizes, size)
		}
	}
	if imageCount > c.maxDataCount {
		c.maxDataCount = imageCount
	}
	if len(sizes) > 0 {
		c.dataSizes = sizes
	}
}

func (c *openAIImageOutputCounter) addOutputArray(output gjson.Result) {
	if !output.IsArray() {
		return
	}
	output.ForEach(func(_, item gjson.Result) bool {
		c.addImageOutputItem(item)
		return true
	})
}

func (c *openAIImageOutputCounter) addImageOutputItem(item gjson.Result) {
	if !item.Exists() || !item.IsObject() {
		return
	}
	itemType := strings.TrimSpace(item.Get("type").String())
	if itemType != "" && itemType != "image_generation_call" && itemType != "image_generation.completed" {
		return
	}
	if strings.Contains(strings.ToLower(item.Raw), "partial_image") {
		return
	}
	resultValue := strings.TrimSpace(item.Get("result").String())
	b64JSON := strings.TrimSpace(item.Get("b64_json").String())
	result := resultValue
	if result == "" {
		result = b64JSON
	}
	if result == "" {
		result = strings.TrimSpace(item.Get("url").String())
	}
	if result == "" && itemType != "image_generation.completed" {
		return
	}
	encodedResult := b64JSON
	if encodedResult == "" && !isOpenAIRemoteImageResult(resultValue) {
		encodedResult = resultValue
	}
	key := strings.TrimSpace(item.Get("id").String())
	if key == "" {
		key = strings.TrimSpace(item.Get("call_id").String())
	}
	if key == "" {
		key = hashOpenAIImageOutputResult(result)
	}
	if key == "" {
		return
	}
	size := strings.TrimSpace(item.Get("size").String())
	if size == "" {
		size = detectOpenAIImageResultSize(encodedResult)
	}
	if _, exists := c.seen[key]; exists {
		if size != "" && strings.TrimSpace(c.seenSizes[key]) == "" {
			c.seenSizes[key] = size
		}
		return
	}
	c.seen[key] = struct{}{}
	c.seenOrder = append(c.seenOrder, key)
	if size != "" {
		c.seenSizes[key] = size
	}
	c.count++
}

func isOpenAIRemoteImageResult(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// reconcileOpenAIResponseImageOutputSizes 只检查响应内联的 base64 字节，不会为 URL 发起下载。
func reconcileOpenAIResponseImageOutputSizes(body []byte) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	data := gjson.GetBytes(body, "data")
	if !data.IsArray() {
		return body
	}
	for i, item := range data.Array() {
		if !item.IsObject() || strings.TrimSpace(item.Get("size").String()) != "" {
			continue
		}
		encoded := strings.TrimSpace(item.Get("b64_json").String())
		if encoded == "" {
			continue
		}
		size := detectOpenAIImageResultSize(encoded)
		if size == "" {
			continue
		}
		if updated, err := sjson.SetBytes(body, "data."+strconv.Itoa(i)+".size", size); err == nil {
			body = updated
		}
	}
	return body
}

func hashOpenAIImageOutputResult(result string) string {
	result = strings.TrimSpace(result)
	if result == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(result))
	return hex.EncodeToString(sum[:])
}

func countOpenAIResponseImageOutputsFromJSONBytes(body []byte) int {
	counter := newOpenAIImageOutputCounter()
	counter.AddJSONResponse(body)
	return counter.Count()
}

func collectOpenAIResponseImageOutputSizesFromJSONBytes(body []byte) []string {
	counter := newOpenAIImageOutputCounter()
	counter.AddJSONResponse(body)
	return counter.Sizes()
}

func countOpenAIImageOutputsFromSSEBody(body string) int {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEBody(body)
	return counter.Count()
}

func collectOpenAIImageOutputSizesFromSSEBody(body string) []string {
	counter := newOpenAIImageOutputCounter()
	counter.AddSSEBody(body)
	return counter.Sizes()
}
