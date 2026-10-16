package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func resolveGrokImagePrompt(prompt string) string {
	if prompt = strings.TrimSpace(prompt); prompt != "" {
		return prompt
	}
	return defaultGrokImageTestPrompt
}

func resolveGrokVideoPrompt(prompt string) string {
	if prompt = strings.TrimSpace(prompt); prompt != "" {
		return prompt
	}
	return defaultGrokVideoTestPrompt
}

func resolveGrokImageTestModel(account *Account, modelID string) string {
	if modelID = strings.TrimSpace(modelID); modelID == "" {
		modelID = "grok-imagine-image"
	}
	if mapped := strings.TrimSpace(account.GetMappedModel(modelID)); mapped != "" {
		return mapped
	}
	return modelID
}

func resolveGrokVideoTestModel(account *Account, modelID string) string {
	if modelID = strings.TrimSpace(modelID); modelID == "" {
		modelID = "grok-imagine-video"
	}
	if mapped := strings.TrimSpace(account.GetMappedModel(modelID)); mapped != "" {
		return mapped
	}
	return modelID
}

func (s *AccountTestService) prepareGrokTestSSE(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()
}

func (s *AccountTestService) grokTestProxyURL(account *Account) string {
	if account.ProxyID != nil && account.Proxy != nil {
		return account.Proxy.URL()
	}
	return ""
}

func (s *AccountTestService) applyGrokTestRequestHeaders(req *http.Request, account *Account, token, accept string) {
	req.Header.Set("Authorization", "Bearer "+token)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if account.IsGrokOAuth() && req.URL != nil && isGrokCLIProxyTarget(req.URL.String()) {
		applyGrokCLIHeaders(req.Header)
	}
	account.ApplyHeaderOverrides(req.Header)
}

func (s *AccountTestService) observeGrokTestResponse(ctx context.Context, account *Account, resp *http.Response) {
	if resp == nil || account == nil || s.accountRepo == nil {
		return
	}
	now := time.Now()
	snapshot := parseGrokQuotaSnapshot(resp.Header, resp.StatusCode, now)
	if snapshot == nil {
		return
	}
	resetAt, limited := grokRateLimitResetAt(snapshot, now)
	if limited {
		normalizeGrokExhaustedWindowResets(snapshot, resetAt, now)
	}
	_ = s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{grokQuotaSnapshotExtraKey: snapshot})
	if limited {
		persistGrokRateLimit(ctx, s.accountRepo, account, resetAt)
	}
}

func (s *AccountTestService) testGrokImageGeneration(c *gin.Context, ctx context.Context, account *Account, token, modelID, prompt, imageDataURL string) error {
	endpoint := GrokMediaEndpointImagesGenerations
	imageDataURL = strings.TrimSpace(imageDataURL)
	if imageDataURL != "" {
		endpoint = GrokMediaEndpointImagesEdits
	}
	modelID = normalizeGrokMediaModelForEndpoint(endpoint, modelID, imageDataURL != "")
	if modelID == "" {
		modelID = "grok-imagine-image-quality"
	}
	targetURL, err := buildGrokMediaURL(account, s.cfg, endpoint, "")
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid Grok media base URL: "+err.Error())
	}
	s.prepareGrokTestSSE(c)
	s.sendEvent(c, TestEvent{Type: "test_start", Model: modelID})

	payload := map[string]any{
		"model": modelID, "prompt": prompt, "n": 1, "response_format": "b64_json",
	}
	if imageDataURL != "" {
		normalized, normalizeErr := normalizeAccountTestImageDataURL(imageDataURL)
		if normalizeErr != nil {
			return s.sendErrorAndEnd(c, normalizeErr.Error())
		}
		payload["image"] = map[string]string{"url": normalized, "type": "image_url"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to build Grok image request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create Grok image request")
	}
	req.Header.Set("Content-Type", "application/json")
	s.applyGrokTestRequestHeaders(req, account, token, "application/json")
	resp, err := s.httpUpstream.Do(req, s.grokTestProxyURL(account), account.ID, maxInt(account.Concurrency, 1))
	if err != nil {
		return s.sendErrorAndEnd(c, "Grok image request failed: "+err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	s.observeGrokTestResponse(ctx, account, resp)
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Grok images API returned %d: %s", resp.StatusCode, truncateString(string(responseBody), 800)))
	}
	items := gjson.GetBytes(responseBody, "data").Array()
	if len(items) == 0 {
		return s.sendErrorAndEnd(c, "No images returned from Grok API")
	}
	for _, item := range items {
		mimeType := firstNonEmpty(item.Get("mime_type").String(), "image/jpeg")
		if revised := strings.TrimSpace(item.Get("revised_prompt").String()); revised != "" {
			s.sendEvent(c, TestEvent{Type: "content", Text: revised + "\n"})
		}
		if encoded := strings.TrimSpace(item.Get("b64_json").String()); encoded != "" {
			s.sendEvent(c, TestEvent{Type: "image", ImageURL: "data:" + mimeType + ";base64," + encoded, MimeType: mimeType})
		} else if imageURL := strings.TrimSpace(item.Get("url").String()); imageURL != "" {
			s.sendEvent(c, TestEvent{Type: "image", ImageURL: imageURL, MimeType: mimeType})
		}
	}
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

func (s *AccountTestService) testGrokVideoGeneration(c *gin.Context, ctx context.Context, account *Account, token, modelID, prompt, imageDataURL string) error {
	targetURL, err := buildGrokMediaURL(account, s.cfg, GrokMediaEndpointVideosGenerations, "")
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid Grok media base URL: "+err.Error())
	}
	s.prepareGrokTestSSE(c)
	s.sendEvent(c, TestEvent{Type: "test_start", Model: modelID})
	payload := map[string]any{
		"model": modelID, "prompt": prompt, "duration": 6, "aspect_ratio": "16:9", "resolution": "480p",
	}
	if imageDataURL = strings.TrimSpace(imageDataURL); imageDataURL != "" {
		normalized, normalizeErr := normalizeAccountTestImageDataURL(imageDataURL)
		if normalizeErr != nil {
			return s.sendErrorAndEnd(c, normalizeErr.Error())
		}
		payload["image"] = map[string]string{"url": normalized, "type": "image_url"}
	}
	body, _ := json.Marshal(payload)
	responseBody, status, requestErr := s.doGrokTestJSON(ctx, account, token, http.MethodPost, targetURL, body)
	if requestErr != nil {
		return s.sendErrorAndEnd(c, "Grok video request failed: "+requestErr.Error())
	}
	if status != http.StatusOK && status != http.StatusAccepted && status != http.StatusCreated {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Grok videos API returned %d: %s", status, truncateString(string(responseBody), 800)))
	}
	if videoURL := accountTestVideoURL(responseBody); videoURL != "" {
		s.sendEvent(c, TestEvent{Type: "video", VideoURL: videoURL, MimeType: "video/mp4"})
		s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
		return nil
	}
	requestID := firstNonEmpty(gjson.GetBytes(responseBody, "request_id").String(), gjson.GetBytes(responseBody, "id").String())
	if requestID == "" {
		return s.sendErrorAndEnd(c, "Grok video response did not contain request_id")
	}
	s.sendEvent(c, TestEvent{Type: "status", Text: "视频任务已提交，正在等待生成完成…"})
	statusURL, err := buildGrokMediaURL(account, s.cfg, GrokMediaEndpointVideoStatus, requestID)
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid Grok video status URL: "+err.Error())
	}
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		responseBody, status, requestErr = s.doGrokTestJSON(ctx, account, token, http.MethodGet, statusURL, nil)
		if requestErr != nil {
			return s.sendErrorAndEnd(c, "Grok video status failed: "+requestErr.Error())
		}
		if status != http.StatusOK && status != http.StatusAccepted {
			return s.sendErrorAndEnd(c, fmt.Sprintf("Grok video status returned %d: %s", status, truncateString(string(responseBody), 800)))
		}
		state := strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, "status").String()))
		s.sendEvent(c, TestEvent{Type: "status", Text: "视频状态：" + firstNonEmpty(state, "processing")})
		switch state {
		case "done", "completed", "succeeded", "success":
			return s.emitGrokVideoResult(c, ctx, account, token, requestID, responseBody)
		case "failed", "error", "canceled", "cancelled":
			return s.sendErrorAndEnd(c, "Grok video generation failed: "+truncateString(string(responseBody), 800))
		}
		select {
		case <-ctx.Done():
			return s.sendErrorAndEnd(c, "Grok video poll canceled")
		case <-time.After(3 * time.Second):
		}
	}
	return s.sendErrorAndEnd(c, "Grok video is still processing after 60 seconds (request_id="+requestID+")")
}

func accountTestVideoURL(body []byte) string {
	for _, path := range []string{"video.url", "url", "video_url", "download_url", "data.0.url", "data.0.video_url"} {
		if value := strings.TrimSpace(gjson.GetBytes(body, path).String()); strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "data:") {
			return value
		}
	}
	return ""
}

func (s *AccountTestService) emitGrokVideoResult(c *gin.Context, ctx context.Context, account *Account, token, requestID string, statusBody []byte) error {
	if videoURL := accountTestVideoURL(statusBody); videoURL != "" {
		s.sendEvent(c, TestEvent{Type: "video", VideoURL: videoURL, MimeType: "video/mp4"})
		s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
		return nil
	}
	contentURL, err := buildGrokMediaURL(account, s.cfg, GrokMediaEndpointVideoContent, requestID)
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid Grok video content URL: "+err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, contentURL, nil)
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create Grok video content request")
	}
	s.applyGrokTestRequestHeaders(req, account, token, "video/*, application/octet-stream")
	resp, err := s.httpUpstream.Do(req, s.grokTestProxyURL(account), account.ID, maxInt(account.Concurrency, 1))
	if err != nil {
		return s.sendErrorAndEnd(c, "Grok video content download failed: "+err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Grok video content returned %d", resp.StatusCode))
	}
	mimeType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = "video/mp4"
	}
	s.sendEvent(c, TestEvent{Type: "video", VideoURL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), MimeType: mimeType})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

func (s *AccountTestService) doGrokTestJSON(ctx context.Context, account *Account, token, method, targetURL string, body []byte) ([]byte, int, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, reader)
	if err != nil {
		return nil, 0, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	s.applyGrokTestRequestHeaders(req, account, token, "application/json")
	resp, err := s.httpUpstream.Do(req, s.grokTestProxyURL(account), account.ID, maxInt(account.Concurrency, 1))
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	s.observeGrokTestResponse(ctx, account, resp)
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return responseBody, resp.StatusCode, readErr
}

func (s *AccountTestService) testGrokWebSearch(c *gin.Context, ctx context.Context, account *Account, token, query string) error {
	if query = strings.TrimSpace(query); query == "" {
		query = defaultGrokSearchTestQuery
	}
	targetURL, err := buildGrokResponsesURL(account, s.cfg)
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid Grok base URL: "+err.Error())
	}
	s.prepareGrokTestSSE(c)
	s.sendEvent(c, TestEvent{Type: "test_start", Model: "grok-web-search"})
	payload, _ := json.Marshal(map[string]any{
		"model": "grok-4.5", "input": query, "tools": []map[string]any{{"type": "web_search"}},
		"include": []string{"web_search_call.action.sources"}, "store": false, "stream": false,
	})
	body, status, err := s.doGrokTestJSON(ctx, account, token, http.MethodPost, targetURL, payload)
	if err != nil {
		return s.sendErrorAndEnd(c, "Grok web_search request failed: "+err.Error())
	}
	if status != http.StatusOK {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Grok web_search returned %d: %s", status, truncateString(string(body), 800)))
	}
	searchCalls := countGrokNativeSearchCallsFromJSONBytes(body)
	sources := 0
	gjson.GetBytes(body, "output").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "web_search_call" {
			sources += len(item.Get("action.sources").Array())
		}
		return true
	})
	if searchCalls == 0 && sources == 0 {
		return s.sendErrorAndEnd(c, "Grok web_search completed but no search result was observed")
	}
	s.sendEvent(c, TestEvent{Type: "content", Text: fmt.Sprintf("网页搜索成功：调用 %d 次，返回 %d 个来源\n", searchCalls, sources)})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

func (s *AccountTestService) testGrokTTS(c *gin.Context, ctx context.Context, account *Account, token, text string) error {
	if text = strings.TrimSpace(text); text == "" {
		text = defaultGrokTTSTestText
	}
	targetURL, err := buildGrokVoiceURL(account, s.cfg, "tts")
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid Grok TTS URL: "+err.Error())
	}
	s.prepareGrokTestSSE(c)
	s.sendEvent(c, TestEvent{Type: "test_start", Model: "grok-voice-tts"})
	payload, _ := json.Marshal(map[string]any{"text": text, "language": "en", "voice_id": "Ara"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create Grok TTS request")
	}
	req.Header.Set("Content-Type", "application/json")
	s.applyGrokTestRequestHeaders(req, account, token, "audio/*, application/json")
	resp, err := s.httpUpstream.Do(req, s.grokTestProxyURL(account), account.ID, maxInt(account.Concurrency, 1))
	if err != nil {
		return s.sendErrorAndEnd(c, "Grok TTS request failed: "+err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	s.observeGrokTestResponse(ctx, account, resp)
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Grok TTS returned %d: %s", resp.StatusCode, truncateString(string(body), 800)))
	}
	mimeType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if mimeType == "" {
		mimeType = "audio/mpeg"
	}
	s.sendEvent(c, TestEvent{Type: "audio", AudioURL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(body), MimeType: mimeType})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

func (s *AccountTestService) testGrokSTT(c *gin.Context, ctx context.Context, account *Account, token, audioDataURL string) error {
	targetURL, err := buildGrokVoiceURL(account, s.cfg, "stt")
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid Grok STT URL: "+err.Error())
	}
	s.prepareGrokTestSSE(c)
	s.sendEvent(c, TestEvent{Type: "test_start", Model: "grok-voice-stt"})
	audio := minimalSilentWAV()
	filename := "probe.wav"
	if audioDataURL = strings.TrimSpace(audioDataURL); audioDataURL != "" {
		var mimeType string
		audio, mimeType, err = decodeAccountTestDataURL(audioDataURL)
		if err != nil || !strings.HasPrefix(strings.ToLower(mimeType), "audio/") {
			return s.sendErrorAndEnd(c, "Invalid audio data URL")
		}
		filename = sttFilenameForMIME(mimeType)
	}
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to build Grok STT request")
	}
	_, _ = part.Write(audio)
	_ = writer.WriteField("model", "grok-stt")
	_ = writer.WriteField("language", "en")
	_ = writer.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, &requestBody)
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create Grok STT request")
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	s.applyGrokTestRequestHeaders(req, account, token, "application/json")
	resp, err := s.httpUpstream.Do(req, s.grokTestProxyURL(account), account.ID, maxInt(account.Concurrency, 1))
	if err != nil {
		return s.sendErrorAndEnd(c, "Grok STT request failed: "+err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	s.observeGrokTestResponse(ctx, account, resp)
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Grok STT returned %d: %s", resp.StatusCode, truncateString(string(body), 800)))
	}
	result := firstNonEmpty(gjson.GetBytes(body, "text").String(), strings.TrimSpace(string(body)))
	s.sendEvent(c, TestEvent{Type: "content", Text: "语音识别成功：" + truncateString(result, 300) + "\n"})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

func (s *AccountTestService) testGrokRealtime(c *gin.Context, ctx context.Context, account *Account, token, modelID string) error {
	if modelID = strings.TrimSpace(modelID); modelID == "" {
		modelID = defaultGrokRealtimeTestModel
	}
	if mapped := strings.TrimSpace(account.GetMappedModel(modelID)); mapped != "" {
		modelID = mapped
	}
	baseURL, err := buildGrokVoiceURL(account, s.cfg, "realtime")
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid Grok Realtime URL: "+err.Error())
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return s.sendErrorAndEnd(c, "Invalid Grok Realtime URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "ws", "wss":
	default:
		return s.sendErrorAndEnd(c, "Invalid Grok Realtime URL scheme")
	}
	query := u.Query()
	query.Set("model", modelID)
	u.RawQuery = query.Encode()
	s.prepareGrokTestSSE(c)
	s.sendEvent(c, TestEvent{Type: "test_start", Model: modelID})
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	if account.IsGrokOAuth() && isGrokCLIProxyTarget(u.String()) {
		applyGrokCLIHeaders(headers)
	}
	account.ApplyHeaderOverrides(headers)
	dialer := s.grokWSDialer
	if dialer == nil {
		dialer = newDefaultOpenAIWSClientDialer()
	}
	dialCtx, cancel := context.WithTimeout(ctx, grokRealtimeProbeTimeout)
	defer cancel()
	conn, status, _, err := dialer.Dial(dialCtx, u.String(), headers, s.grokTestProxyURL(account))
	if err != nil {
		var handshakeErr *openAIWSHandshakeError
		if errors.As(err, &handshakeErr) && len(handshakeErr.Body) > 0 {
			return s.sendErrorAndEnd(c, fmt.Sprintf("Grok Realtime handshake failed (HTTP %d): %s", status, truncateString(string(handshakeErr.Body), 300)))
		}
		return s.sendErrorAndEnd(c, "Grok Realtime connection failed: "+err.Error())
	}
	defer func() { _ = conn.Close() }()
	s.sendEvent(c, TestEvent{Type: "content", Text: "Realtime WebSocket 握手成功\n"})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

func normalizeAccountTestImageDataURL(raw string) (string, error) {
	data, mimeType, err := decodeAccountTestDataURL(raw)
	if err != nil || !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return "", fmt.Errorf("invalid image data URL")
	}
	if len(data) > 4<<20 {
		return "", fmt.Errorf("source image must be smaller than 4 MB")
	}
	if dimensions, _, decodeErr := image.DecodeConfig(bytes.NewReader(data)); decodeErr == nil && (dimensions.Width < 8 || dimensions.Height < 8) {
		return "", fmt.Errorf("source image width and height must both be at least 8 pixels")
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func decodeAccountTestDataURL(raw string) ([]byte, string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "data:") || len(raw) > maxAccountTestMediaBytes*2 {
		return nil, "", fmt.Errorf("invalid or oversized media data URL")
	}
	comma := strings.Index(raw, ",")
	if comma < 0 || !strings.Contains(strings.ToLower(raw[:comma]), ";base64") {
		return nil, "", fmt.Errorf("media must be a base64 data URL")
	}
	meta := strings.TrimPrefix(raw[:comma], "data:")
	mimeType := strings.TrimSpace(strings.Split(meta, ";")[0])
	data, err := base64.StdEncoding.DecodeString(raw[comma+1:])
	if err != nil || len(data) == 0 || len(data) > maxAccountTestMediaBytes {
		return nil, "", fmt.Errorf("invalid or oversized base64 media")
	}
	return data, firstNonEmpty(mimeType, "application/octet-stream"), nil
}

func sttFilenameForMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "audio/mpeg", "audio/mp3":
		return "upload.mp3"
	case "audio/webm":
		return "upload.webm"
	case "audio/ogg", "audio/opus":
		return "upload.ogg"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "upload.m4a"
	default:
		return "upload.wav"
	}
}

func minimalSilentWAV() []byte {
	const sampleRate = 8000
	const samples = 400
	dataSize := samples * 2
	buffer := make([]byte, 44+dataSize)
	copy(buffer[0:], "RIFF")
	binary.LittleEndian.PutUint32(buffer[4:], uint32(36+dataSize))
	copy(buffer[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(buffer[16:], 16)
	binary.LittleEndian.PutUint16(buffer[20:], 1)
	binary.LittleEndian.PutUint16(buffer[22:], 1)
	binary.LittleEndian.PutUint32(buffer[24:], sampleRate)
	binary.LittleEndian.PutUint32(buffer[28:], sampleRate*2)
	binary.LittleEndian.PutUint16(buffer[32:], 2)
	binary.LittleEndian.PutUint16(buffer[34:], 16)
	copy(buffer[36:], "data")
	binary.LittleEndian.PutUint32(buffer[40:], uint32(dataSize))
	return buffer
}
