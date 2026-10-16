package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type GrokMediaEndpoint string

const (
	GrokMediaEndpointImagesGenerations GrokMediaEndpoint = "images_generations"
	GrokMediaEndpointImagesEdits       GrokMediaEndpoint = "images_edits"
	GrokMediaEndpointVideosGenerations GrokMediaEndpoint = "videos_generations"
	GrokMediaEndpointVideosEdits       GrokMediaEndpoint = "videos_edits"
	GrokMediaEndpointVideosExtensions  GrokMediaEndpoint = "videos_extensions"
	GrokMediaEndpointVideoStatus       GrokMediaEndpoint = "video_status"
	GrokMediaEndpointVideoContent      GrokMediaEndpoint = "video_content"

	// 媒体请求会在内容审计、上游转发和计费快照之间重复使用。这里设置独立
	// 上限，避免大量兼容字段或超长 Data URL 在多个阶段重复放大内存占用。
	grokMediaMaxInputImages          = 16
	grokMediaMaxRemoteImageURLBytes  = 8 << 10
	grokMediaMaxDataImageURLBytes    = 28 << 20
	grokMediaMaxTotalImageURLBytes   = 64 << 20
	grokMediaMaxDataVideoURLBytes    = 64 << 20
	grokMediaDNSValidationTimeout    = 2 * time.Second
	grokVideoExtensionDefaultSeconds = 6
	grokVideoExtensionMinSeconds     = 2
	grokVideoExtensionMaxSeconds     = 10
)

func (e GrokMediaEndpoint) RequiresRequestBody() bool {
	return !e.IsVideoLookupRequest()
}

// IsVideoLookupRequest 表示「既有任务的查询/删除」：这类请求不要求正文、不消费 RPM/TPM。
// Seedance（Ark 原生任务）的查询与删除与之同语义，必须一起纳入。
func (e GrokMediaEndpoint) IsVideoLookupRequest() bool {
	return e == GrokMediaEndpointVideoStatus || e == GrokMediaEndpointVideoContent ||
		e == SeedanceEndpointStatus || e == SeedanceEndpointDelete
}

// IsGenerationRequest 表示「一次新的付费提交」：会消费一次 RPM/TPM 并走内容审计。
// 官方的 Seedance 创建同样计入提交；本仓此前漏了它，属于限流旁路。
func (e GrokMediaEndpoint) IsGenerationRequest() bool {
	if e == SeedanceEndpointCreate {
		return true
	}
	switch e {
	case GrokMediaEndpointImagesGenerations, GrokMediaEndpointImagesEdits, GrokMediaEndpointVideosGenerations, GrokMediaEndpointVideosEdits, GrokMediaEndpointVideosExtensions:
		return true
	default:
		return false
	}
}

func (e GrokMediaEndpoint) IsVideoCreationRequest() bool {
	switch e {
	case GrokMediaEndpointVideosGenerations, GrokMediaEndpointVideosEdits, GrokMediaEndpointVideosExtensions:
		return true
	default:
		return false
	}
}

func (e GrokMediaEndpoint) VideoOperation() string {
	switch e {
	case GrokMediaEndpointVideosEdits:
		return GrokVideoOperationEdit
	case GrokMediaEndpointVideosExtensions:
		return GrokVideoOperationExtension
	case GrokMediaEndpointVideosGenerations:
		return GrokVideoOperationGeneration
	default:
		return ""
	}
}

type GrokMediaInputRole string

const (
	GrokMediaInputRoleSource    GrokMediaInputRole = "source"
	GrokMediaInputRoleReference GrokMediaInputRole = "reference"
	GrokMediaInputRoleMask      GrokMediaInputRole = "mask"
	GrokMediaInputRoleVideo     GrokMediaInputRole = "video"
)

// GrokMediaInput 是 JSON 兼容字段和 multipart 上传的统一内部表示。
// SourceField/Order 用于证明审计与上游保留同一顺序；URL 与 Upload 互斥，
// 避免解析、审计和转发各自维护一套容易漂移的图片集合。
type GrokMediaInput struct {
	SourceField string
	URL         string
	Order       int
	MediaType   string
	Uploaded    bool
	Role        GrokMediaInputRole
	Upload      OpenAIImagesUpload
}

func (i GrokMediaInput) moderationURL() string {
	if i.Uploaded {
		return i.Upload.ModerationDataURL()
	}
	return strings.TrimSpace(i.URL)
}

func (i GrokMediaInput) forwardURL() (string, error) {
	if i.Uploaded {
		return openAIImageUploadToDataURL(i.Upload)
	}
	return strings.TrimSpace(i.URL), nil
}

type GrokMediaRequestInfo struct {
	Model            string
	Prompt           string
	N                int
	Size             string
	SizeTier         string
	Resolution       string
	DurationSeconds  int
	DurationProvided bool
	VideoFileID      string
	MediaInputs      []GrokMediaInput
}

func (r GrokMediaRequestInfo) ModerationBody() []byte {
	payload := map[string]any{}
	if prompt := strings.TrimSpace(r.Prompt); prompt != "" {
		payload["prompt"] = prompt
	}

	images := make([]map[string]string, 0, len(r.MediaInputs))
	for _, input := range r.MediaInputs {
		// 当前内容审计的多模态字段只接受图片；视频转换仍审计 prompt，
		// 但不能把 MP4 URL 伪装成 image_url 交给外部审核节点。
		if input.Role == GrokMediaInputRoleVideo {
			continue
		}
		if imageURL := input.moderationURL(); imageURL != "" {
			images = append(images, map[string]string{"image_url": imageURL})
		}
	}
	if len(images) > 0 {
		payload["images"] = images
	}
	if len(payload) == 0 {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return body
}

func (e GrokMediaEndpoint) httpMethod() string {
	if e.IsVideoLookupRequest() {
		return http.MethodGet
	}
	return http.MethodPost
}

func ExtractGrokMediaModel(contentType string, body []byte) string {
	return ParseGrokMediaRequest(contentType, body).Model
}

func ParseGrokMediaRequest(contentType string, body []byte) GrokMediaRequestInfo {
	info := GrokMediaRequestInfo{N: 1}
	if gjson.ValidBytes(body) {
		parseGrokMediaJSONRequest(body, &info)
	} else {
		_ = parseGrokMediaMultipartRequest(contentType, body, &info)
	}
	info.Model = strings.TrimSpace(info.Model)
	info.Prompt = strings.TrimSpace(info.Prompt)
	info.Size = strings.TrimSpace(info.Size)
	info.SizeTier = NormalizeImageBillingTierOrDefault(info.Size)
	info.Resolution = NormalizeVideoBillingResolutionOrDefault(info.Resolution)
	info.DurationSeconds = NormalizeVideoBillingDurationSecondsOrDefault(info.DurationSeconds)
	if info.N <= 0 {
		info.N = 1
	}
	return info
}

// GrokMediaTaskDurationSeconds returns the duration that belongs to the
// generated work. Extension billing uses only the newly generated segment;
// edit duration is unknown until the terminal status reports the inherited
// source duration.
func GrokMediaTaskDurationSeconds(endpoint GrokMediaEndpoint, info GrokMediaRequestInfo) int {
	switch endpoint {
	case GrokMediaEndpointVideosEdits:
		return 0
	case GrokMediaEndpointVideosExtensions:
		if !info.DurationProvided {
			return grokVideoExtensionDefaultSeconds
		}
		return info.DurationSeconds
	default:
		return info.DurationSeconds
	}
}

// GrokMediaReservationDurationSeconds deliberately reserves the maximum
// supported duration for edits because the request contract does not carry the
// source duration. The exact inherited duration is settled from terminal state.
func GrokMediaReservationDurationSeconds(endpoint GrokMediaEndpoint, info GrokMediaRequestInfo) int {
	if endpoint == GrokMediaEndpointVideosEdits {
		return VideoBillingMaxDurationSeconds
	}
	return GrokMediaTaskDurationSeconds(endpoint, info)
}

func GrokMediaBillingResolution(endpoint GrokMediaEndpoint, info GrokMediaRequestInfo) string {
	switch endpoint {
	case GrokMediaEndpointVideosEdits, GrokMediaEndpointVideosExtensions:
		// xAI does not accept a resolution override for transformations. Edit
		// output is explicitly capped at 720p; extension uses the same stable
		// billing tier until terminal metadata provides a more precise value.
		return VideoBillingResolution720P
	default:
		return info.Resolution
	}
}

// ValidateGrokMediaRequest rejects values that could make the upstream work
// differ from the amount reserved for billing. Defaults remain supported, but
// explicitly supplied invalid values are never silently clamped.
func ValidateGrokMediaRequest(endpoint GrokMediaEndpoint, contentType string, body []byte) error {
	return validateGrokMediaRequestShape(endpoint, contentType, body)
}

// ValidateGrokMediaRequestContext performs shape validation plus a fresh DNS
// safety check. The handler calls it before moderation/billing and Forward calls
// it again immediately before upstream construction, so a hostname that rebinds
// from public to private between phases is rejected before leaving the gateway.
func ValidateGrokMediaRequestContext(ctx context.Context, endpoint GrokMediaEndpoint, contentType string, body []byte) error {
	if err := validateGrokMediaRequestShape(endpoint, contentType, body); err != nil {
		return err
	}
	if !endpoint.RequiresRequestBody() {
		return nil
	}
	info := ParseGrokMediaRequest(contentType, body)
	return validateGrokMediaResolvedHosts(ctx, info, net.DefaultResolver.LookupIPAddr)
}

func validateGrokMediaRequestShape(endpoint GrokMediaEndpoint, contentType string, body []byte) error {
	if !endpoint.RequiresRequestBody() {
		return nil
	}
	if !gjson.ValidBytes(body) {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(contentType))
		if endpoint != GrokMediaEndpointImagesEdits || err != nil || !strings.EqualFold(mediaType, "multipart/form-data") || strings.TrimSpace(params["boundary"]) == "" {
			return errors.New("request body must be valid JSON or supported multipart/form-data")
		}
		info := GrokMediaRequestInfo{N: 1}
		if err := parseGrokMediaMultipartRequest(contentType, body, &info); err != nil {
			return err
		}
		return validateGrokMediaParsedInputs(info)
	}

	for _, fieldName := range []string{"model", "prompt", "size", "resolution"} {
		value := gjson.GetBytes(body, fieldName)
		if value.Exists() && value.Type != gjson.String {
			return fmt.Errorf("%s must be a string", fieldName)
		}
	}

	if n := gjson.GetBytes(body, "n"); n.Exists() {
		if n.Type != gjson.Number || math.IsNaN(n.Float()) || math.IsInf(n.Float(), 0) || n.Float() != math.Trunc(n.Float()) || n.Int() <= 0 {
			return errors.New("n must be a positive integer")
		}
		if endpoint.IsVideoCreationRequest() && n.Int() != 1 {
			return errors.New("video requests support exactly one output per request")
		}
	}
	if endpoint == GrokMediaEndpointVideosEdits || endpoint == GrokMediaEndpointVideosExtensions {
		return validateGrokMediaVideoTransformRequest(endpoint, body)
	}
	if err := validateGrokMediaJSONImageFields(endpoint, body); err != nil {
		return err
	}

	if endpoint != GrokMediaEndpointVideosGenerations {
		return nil
	}

	duration := gjson.GetBytes(body, "duration")
	if duration.Exists() {
		value := duration.Float()
		if duration.Type != gjson.Number || math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
			return errors.New("duration must be an integer between 1 and 15 seconds")
		}
		if value < VideoBillingMinDurationSeconds || value > VideoBillingMaxDurationSeconds {
			return errors.New("duration must be between 1 and 15 seconds")
		}
	}

	resolution := gjson.GetBytes(body, "resolution")
	if resolution.Exists() {
		normalizedResolution, ok := NormalizeVideoBillingResolution(resolution.String())
		if !ok {
			return errors.New("resolution must be one of 480p, 720p, or 1080p")
		}
		info := ParseGrokMediaRequest(contentType, body)
		routingModel := ResolveGrokMediaRoutingModel(endpoint, info)
		if normalizedResolution == VideoBillingResolution1080P && routingModel == "grok-imagine-video" {
			return errors.New("1080p requires a video model that supports 1080p pricing")
		}
	}
	return nil
}

func validateGrokMediaVideoTransformRequest(endpoint GrokMediaEndpoint, body []byte) error {
	if strings.TrimSpace(gjson.GetBytes(body, "prompt").String()) == "" {
		return errors.New("prompt is required")
	}
	for _, field := range []string{"image", "images", "reference_images", "mask"} {
		if gjson.GetBytes(body, field).Exists() {
			return fmt.Errorf("%s is not supported for video transformations", field)
		}
	}
	if endpoint == GrokMediaEndpointVideosEdits {
		for _, field := range []string{"duration", "seconds", "resolution", "aspect_ratio"} {
			if gjson.GetBytes(body, field).Exists() {
				return fmt.Errorf("%s is not supported for video edits; output inherits the source video", field)
			}
		}
	}
	if endpoint == GrokMediaEndpointVideosExtensions {
		if gjson.GetBytes(body, "resolution").Exists() || gjson.GetBytes(body, "aspect_ratio").Exists() {
			return errors.New("resolution and aspect_ratio are not supported for video extensions")
		}
		duration := gjson.GetBytes(body, "duration")
		if seconds := gjson.GetBytes(body, "seconds"); seconds.Exists() {
			if duration.Exists() && duration.Raw != seconds.Raw {
				return errors.New("duration and seconds contain conflicting values")
			}
			duration = seconds
		}
		if duration.Exists() {
			value := duration.Float()
			if duration.Type != gjson.Number || math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
				return errors.New("duration must be an integer between 2 and 10 seconds")
			}
			if value < grokVideoExtensionMinSeconds || value > grokVideoExtensionMaxSeconds {
				return errors.New("duration must be between 2 and 10 seconds")
			}
		}
	}
	return validateGrokMediaVideoInput(body)
}

func validateGrokMediaVideoInput(body []byte) error {
	video := gjson.GetBytes(body, "video")
	if !video.Exists() || !video.IsObject() {
		return errors.New("video must be an object")
	}
	official := video.Get("url")
	legacy := video.Get("video_url")
	fileID := video.Get("file_id")
	for name, value := range map[string]gjson.Result{"video.url": official, "video.video_url": legacy, "video.file_id": fileID} {
		if value.Exists() && value.Type != gjson.String {
			return fmt.Errorf("%s must be a string", name)
		}
	}
	officialURL := strings.TrimSpace(official.String())
	legacyURL := strings.TrimSpace(legacy.String())
	fileIDValue := strings.TrimSpace(fileID.String())
	if officialURL != "" && legacyURL != "" && officialURL != legacyURL {
		return errors.New("video contains conflicting url and video_url values")
	}
	videoURL := officialURL
	if videoURL == "" {
		videoURL = legacyURL
	}
	if videoURL != "" && fileIDValue != "" {
		return errors.New("video.url and video.file_id are mutually exclusive")
	}
	if videoURL == "" && fileIDValue == "" {
		return errors.New("video.url or video.file_id is required")
	}
	if len(fileIDValue) > 512 {
		return errors.New("video.file_id exceeds the 512 byte limit")
	}
	if videoURL != "" {
		return validateGrokMediaVideoURL("video", videoURL)
	}
	return nil
}

func validateGrokMediaVideoURL(field, videoURL string) error {
	videoURL = strings.TrimSpace(videoURL)
	lower := strings.ToLower(videoURL)
	if strings.HasPrefix(lower, "data:") {
		if len(videoURL) > grokMediaMaxDataVideoURLBytes {
			return fmt.Errorf("%s data URL exceeds the %d byte limit", field, grokMediaMaxDataVideoURLBytes)
		}
		comma := strings.IndexByte(videoURL, ',')
		if comma <= len("data:") || comma == len(videoURL)-1 {
			return fmt.Errorf("%s must be a non-empty base64 MP4 data URL", field)
		}
		header := strings.ToLower(videoURL[len("data:"):comma])
		if header != "video/mp4;base64" {
			return fmt.Errorf("%s data URL must use base64 video/mp4", field)
		}
		encoded := videoURL[comma+1:]
		if strings.ContainsAny(encoded, " \t\r\n") {
			return fmt.Errorf("%s data URL contains invalid base64 whitespace", field)
		}
		decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded))
		decodedBytes, err := io.Copy(io.Discard, io.LimitReader(decoder, int64(grokMediaMaxDataVideoURLBytes)+1))
		if err != nil || decodedBytes == 0 || decodedBytes > int64(grokMediaMaxDataVideoURLBytes) {
			return fmt.Errorf("%s contains invalid or oversized base64 MP4 data", field)
		}
		return nil
	}
	if len(videoURL) > grokMediaMaxRemoteImageURLBytes {
		return fmt.Errorf("%s URL exceeds the %d byte limit", field, grokMediaMaxRemoteImageURLBytes)
	}
	parsed, err := url.Parse(videoURL)
	if err != nil || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%s must be a public http/https MP4 URL without credentials or fragment", field)
	}
	if !strings.HasSuffix(strings.ToLower(parsed.Path), ".mp4") {
		return fmt.Errorf("%s URL path must end with .mp4", field)
	}
	if _, err := urlvalidator.ValidateHTTPURL(videoURL, true, urlvalidator.ValidationOptions{AllowPrivate: false}); err != nil {
		return fmt.Errorf("%s must be a public http/https MP4 URL", field)
	}
	return nil
}

type grokMediaLookupIPAddrFunc func(context.Context, string) ([]net.IPAddr, error)

func validateGrokMediaResolvedHosts(ctx context.Context, info GrokMediaRequestInfo, lookup grokMediaLookupIPAddrFunc) error {
	if lookup == nil {
		return errors.New("media URL DNS validator is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lookupCtx, cancel := context.WithTimeout(ctx, grokMediaDNSValidationTimeout)
	defer cancel()
	checked := make(map[string]error)
	for _, input := range info.MediaInputs {
		if input.Uploaded || strings.HasPrefix(strings.ToLower(strings.TrimSpace(input.URL)), "data:") {
			continue
		}
		parsed, err := url.Parse(strings.TrimSpace(input.URL))
		if err != nil {
			return fmt.Errorf("%s must be a public http/https URL", grokMediaInputField(input))
		}
		host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
		if previous, ok := checked[host]; ok {
			if previous != nil {
				return fmt.Errorf("%s URL host must resolve only to public IP addresses", grokMediaInputField(input))
			}
			continue
		}
		err = validateGrokMediaPublicHost(lookupCtx, host, lookup)
		checked[host] = err
		if err != nil {
			// 不回显 hostname、签名 URL 或解析到的内网地址。
			return fmt.Errorf("%s URL host must resolve only to public IP addresses", grokMediaInputField(input))
		}
	}
	return nil
}

func validateGrokMediaPublicHost(ctx context.Context, host string, lookup grokMediaLookupIPAddrFunc) error {
	if isBlockedHostname(host) {
		return errors.New("blocked hostname")
	}
	if literal := net.ParseIP(host); literal != nil {
		if isPrivateIP(literal) {
			return errors.New("private IP")
		}
		return nil
	}
	addrs, err := lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		return errors.New("DNS lookup failed")
	}
	for _, addr := range addrs {
		if isPrivateIP(addr.IP) {
			return errors.New("private DNS answer")
		}
	}
	return nil
}

func grokMediaInputField(input GrokMediaInput) string {
	field := strings.TrimSpace(input.SourceField)
	if field == "" {
		return "image"
	}
	return field
}

func validateGrokMediaJSONImageFields(endpoint GrokMediaEndpoint, body []byte) error {
	if endpoint != GrokMediaEndpointImagesEdits && gjson.GetBytes(body, "mask").Exists() {
		return errors.New("mask is only supported for image edits")
	}

	imageCount := 0
	totalURLBytes := 0
	for _, field := range []string{"image", "images", "reference_images"} {
		value := gjson.GetBytes(body, field)
		if !value.Exists() {
			continue
		}
		count, urlBytes, err := validateGrokMediaJSONImageValue(value, field, true)
		if err != nil {
			return err
		}
		imageCount += count
		totalURLBytes += urlBytes
	}
	if mask := gjson.GetBytes(body, "mask"); mask.Exists() {
		_, urlBytes, err := validateGrokMediaJSONImageValue(mask, "mask", false)
		if err != nil {
			return err
		}
		totalURLBytes += urlBytes
	}
	if imageCount > grokMediaMaxInputImages {
		return fmt.Errorf("media request supports at most %d input images", grokMediaMaxInputImages)
	}
	if totalURLBytes > grokMediaMaxTotalImageURLBytes {
		return fmt.Errorf("media image URLs exceed the %d byte total limit", grokMediaMaxTotalImageURLBytes)
	}
	return nil
}

func validateGrokMediaJSONImageValue(value gjson.Result, field string, allowArray bool) (int, int, error) {
	if value.IsArray() {
		if !allowArray {
			return 0, 0, fmt.Errorf("%s must be a string or object", field)
		}
		count := 0
		totalURLBytes := 0
		for index, item := range value.Array() {
			itemField := fmt.Sprintf("%s[%d]", field, index)
			itemCount, urlBytes, err := validateGrokMediaJSONImageValue(item, itemField, false)
			if err != nil {
				return 0, 0, err
			}
			count += itemCount
			totalURLBytes += urlBytes
		}
		return count, totalURLBytes, nil
	}

	imageURL, err := grokMediaJSONImageURLChecked(value, field)
	if err != nil {
		return 0, 0, err
	}
	if err := validateGrokMediaImageURL(field, imageURL); err != nil {
		return 0, 0, err
	}
	return 1, len(imageURL), nil
}

func grokMediaJSONImageURLChecked(value gjson.Result, field string) (string, error) {
	if value.Type == gjson.String {
		imageURL := strings.TrimSpace(value.String())
		if imageURL == "" {
			return "", fmt.Errorf("%s URL is required", field)
		}
		return imageURL, nil
	}
	if !value.IsObject() {
		return "", fmt.Errorf("%s must be a string or object", field)
	}

	official := value.Get("url")
	legacy := value.Get("image_url")
	if official.Exists() && official.Type != gjson.String {
		return "", fmt.Errorf("%s.url must be a string", field)
	}
	if legacy.Exists() && legacy.Type != gjson.String {
		return "", fmt.Errorf("%s.image_url must be a string", field)
	}
	officialURL := strings.TrimSpace(official.String())
	legacyURL := strings.TrimSpace(legacy.String())
	if officialURL != "" && legacyURL != "" && officialURL != legacyURL {
		return "", fmt.Errorf("%s contains conflicting url and image_url values", field)
	}
	imageURL := officialURL
	if imageURL == "" {
		imageURL = legacyURL
	}
	if imageURL == "" {
		return "", fmt.Errorf("%s.url is required", field)
	}
	return imageURL, nil
}

func validateGrokMediaImageURL(field, imageURL string) error {
	imageURL = strings.TrimSpace(imageURL)
	lower := strings.ToLower(imageURL)
	if strings.HasPrefix(lower, "data:") {
		if len(imageURL) > grokMediaMaxDataImageURLBytes {
			return fmt.Errorf("%s data URL exceeds the %d byte limit", field, grokMediaMaxDataImageURLBytes)
		}
		comma := strings.IndexByte(imageURL, ',')
		if comma <= len("data:") || comma == len(imageURL)-1 {
			return fmt.Errorf("%s must be a non-empty base64 image data URL", field)
		}
		header := strings.ToLower(imageURL[len("data:"):comma])
		headerParts := strings.Split(header, ";")
		if len(headerParts) != 2 || headerParts[1] != "base64" || !allowedGrokMediaImageType(headerParts[0]) {
			return fmt.Errorf("%s data URL must use base64 PNG, JPEG, WEBP, or GIF", field)
		}
		encoded := imageURL[comma+1:]
		if strings.ContainsAny(encoded, " \t\r\n") {
			return fmt.Errorf("%s data URL contains invalid base64 whitespace", field)
		}
		decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded))
		decodedBytes, err := io.Copy(io.Discard, io.LimitReader(decoder, openAIImageMaxUploadPartSize+1))
		if err != nil {
			return fmt.Errorf("%s contains invalid base64 image data", field)
		}
		if decodedBytes == 0 || decodedBytes > openAIImageMaxUploadPartSize {
			return fmt.Errorf("%s decoded image must be between 1 and %d bytes", field, openAIImageMaxUploadPartSize)
		}
		return nil
	}

	if len(imageURL) > grokMediaMaxRemoteImageURLBytes {
		return fmt.Errorf("%s URL exceeds the %d byte limit", field, grokMediaMaxRemoteImageURLBytes)
	}
	parsed, err := url.Parse(imageURL)
	if err != nil || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%s must be a public http/https URL without credentials or fragment", field)
	}
	if _, err := urlvalidator.ValidateHTTPURL(imageURL, true, urlvalidator.ValidationOptions{AllowPrivate: false}); err != nil {
		// 不回显原始 URL，避免签名参数进入请求日志。
		return fmt.Errorf("%s must be a public http/https URL", field)
	}
	return nil
}

func allowedGrokMediaImageType(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png", "image/jpeg", "image/jpg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func validateGrokMediaParsedInputs(info GrokMediaRequestInfo) error {
	imageCount := 0
	maskCount := 0
	totalURLBytes := 0
	for _, input := range info.MediaInputs {
		field := strings.TrimSpace(input.SourceField)
		if field == "" {
			field = "image"
		}
		if input.Role == GrokMediaInputRoleMask {
			maskCount++
		} else {
			imageCount++
		}
		if input.Uploaded {
			if len(input.Upload.Data) == 0 || len(input.Upload.Data) > openAIImageMaxUploadPartSize {
				return fmt.Errorf("%s upload must be between 1 and %d bytes", field, openAIImageMaxUploadPartSize)
			}
			mediaType := strings.TrimSpace(input.Upload.ContentType)
			if mediaType == "" {
				mediaType = http.DetectContentType(input.Upload.Data)
			}
			if parsedType, _, err := mime.ParseMediaType(mediaType); err != nil || !allowedGrokMediaImageType(parsedType) {
				return fmt.Errorf("%s upload must be PNG, JPEG, WEBP, or GIF", field)
			}
			continue
		}
		if err := validateGrokMediaImageURL(field, input.URL); err != nil {
			return err
		}
		totalURLBytes += len(input.URL)
	}
	if maskCount > 1 {
		return errors.New("media request supports exactly one mask")
	}
	if imageCount > grokMediaMaxInputImages {
		return fmt.Errorf("media request supports at most %d input images", grokMediaMaxInputImages)
	}
	if totalURLBytes > grokMediaMaxTotalImageURLBytes {
		return fmt.Errorf("media image URLs exceed the %d byte total limit", grokMediaMaxTotalImageURLBytes)
	}
	return nil
}

func parseGrokMediaJSONRequest(body []byte, info *GrokMediaRequestInfo) {
	if info == nil {
		return
	}
	info.Model = strings.TrimSpace(gjson.GetBytes(body, "model").String())
	info.Prompt = strings.TrimSpace(gjson.GetBytes(body, "prompt").String())
	info.Size = strings.TrimSpace(gjson.GetBytes(body, "size").String())
	info.Resolution = strings.TrimSpace(gjson.GetBytes(body, "resolution").String())
	if duration := gjson.GetBytes(body, "duration"); duration.Exists() && duration.Type == gjson.Number {
		info.DurationSeconds = int(duration.Int())
		info.DurationProvided = true
	} else if seconds := gjson.GetBytes(body, "seconds"); seconds.Exists() && seconds.Type == gjson.Number {
		info.DurationSeconds = int(seconds.Int())
		info.DurationProvided = true
	}
	if n := gjson.GetBytes(body, "n"); n.Exists() && n.Type == gjson.Number {
		info.N = int(n.Int())
	}
	appendJSONImageURLs := func(sourceField string, role GrokMediaInputRole, value gjson.Result) {
		if !value.Exists() {
			return
		}
		switch {
		case value.IsArray():
			for index, item := range value.Array() {
				if imageURL := grokMediaJSONImageURL(item); imageURL != "" {
					info.addGrokMediaURLInput(fmt.Sprintf("%s[%d]", sourceField, index), role, imageURL)
				}
			}
		default:
			if imageURL := grokMediaJSONImageURL(value); imageURL != "" {
				info.addGrokMediaURLInput(sourceField, role, imageURL)
			}
		}
	}
	appendJSONImageURLs("image", GrokMediaInputRoleSource, gjson.GetBytes(body, "image"))
	appendJSONImageURLs("images", GrokMediaInputRoleSource, gjson.GetBytes(body, "images"))
	appendJSONImageURLs("reference_images", GrokMediaInputRoleReference, gjson.GetBytes(body, "reference_images"))
	appendJSONImageURLs("mask", GrokMediaInputRoleMask, gjson.GetBytes(body, "mask"))
	if video := gjson.GetBytes(body, "video"); video.IsObject() {
		videoURL := strings.TrimSpace(video.Get("url").String())
		if videoURL == "" {
			videoURL = strings.TrimSpace(video.Get("video_url").String())
		}
		if videoURL != "" {
			info.addGrokMediaURLInput("video", GrokMediaInputRoleVideo, videoURL)
		}
		info.VideoFileID = strings.TrimSpace(video.Get("file_id").String())
	}
}

func grokMediaJSONImageURL(value gjson.Result) string {
	imageURL, _ := grokMediaJSONImageURLChecked(value, "image")
	return imageURL
}

func (r *GrokMediaRequestInfo) addGrokMediaURLInput(sourceField string, role GrokMediaInputRole, imageURL string) {
	if r == nil {
		return
	}
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return
	}
	r.MediaInputs = append(r.MediaInputs, GrokMediaInput{
		SourceField: sourceField,
		URL:         imageURL,
		Order:       len(r.MediaInputs),
		MediaType:   grokMediaInputTypeFromURL(imageURL),
		Role:        role,
	})
}

func (r *GrokMediaRequestInfo) addGrokMediaUploadInput(sourceField string, role GrokMediaInputRole, upload OpenAIImagesUpload) {
	if r == nil {
		return
	}
	mediaType := strings.TrimSpace(upload.ContentType)
	if mediaType == "" && len(upload.Data) > 0 {
		mediaType = http.DetectContentType(upload.Data)
	}
	if parsedType, _, err := mime.ParseMediaType(mediaType); err == nil {
		mediaType = parsedType
	}
	r.MediaInputs = append(r.MediaInputs, GrokMediaInput{
		SourceField: sourceField,
		Order:       len(r.MediaInputs),
		MediaType:   strings.ToLower(strings.TrimSpace(mediaType)),
		Uploaded:    true,
		Role:        role,
		Upload:      upload,
	})
}

func grokMediaInputTypeFromURL(imageURL string) string {
	imageURL = strings.TrimSpace(imageURL)
	if strings.HasPrefix(strings.ToLower(imageURL), "data:") {
		if comma := strings.IndexByte(imageURL, ','); comma > len("data:") {
			header := imageURL[len("data:"):comma]
			if semicolon := strings.IndexByte(header, ';'); semicolon >= 0 {
				header = header[:semicolon]
			}
			return strings.ToLower(strings.TrimSpace(header))
		}
	}
	return "media/remote"
}

func parseGrokMediaMultipartRequest(contentType string, body []byte, info *GrokMediaRequestInfo) error {
	if info == nil {
		return errors.New("grok media request info is required")
	}
	mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
		return errors.New("request body must use multipart/form-data")
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return errors.New("multipart boundary is required")
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read multipart body: %w", err)
		}
		name := strings.TrimSpace(part.FormName())
		if name == "" {
			_ = part.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, openAIImageMaxUploadPartSize+1))
		_ = part.Close()
		if err != nil {
			return fmt.Errorf("read multipart field %s: %w", name, err)
		}
		if len(data) > openAIImageMaxUploadPartSize {
			return fmt.Errorf("multipart field %s exceeds the %d byte limit", name, openAIImageMaxUploadPartSize)
		}
		fileName := strings.TrimSpace(part.FileName())
		partContentType := strings.TrimSpace(part.Header.Get("Content-Type"))
		if fileName != "" {
			upload := OpenAIImagesUpload{
				FieldName:   name,
				FileName:    fileName,
				ContentType: partContentType,
				Data:        data,
			}
			if name == "mask" {
				info.addGrokMediaUploadInput(name, GrokMediaInputRoleMask, upload)
				continue
			}
			if name == "image" || strings.HasPrefix(name, "image[") {
				info.addGrokMediaUploadInput(name, GrokMediaInputRoleSource, upload)
			}
			continue
		}

		value := strings.TrimSpace(string(data))
		switch name {
		case "model":
			info.Model = value
		case "prompt":
			info.Prompt = value
		case "size":
			info.Size = value
		case "resolution":
			info.Resolution = value
		case "duration":
			duration, err := strconv.Atoi(value)
			if err != nil {
				return errors.New("duration must be an integer")
			}
			info.DurationSeconds = duration
		case "n":
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return errors.New("n must be a positive integer")
			}
			info.N = n
		case "image", "image_url":
			if value == "" {
				return fmt.Errorf("%s URL is required", name)
			}
			info.addGrokMediaURLInput(name, GrokMediaInputRoleSource, value)
		case "mask", "mask_image_url":
			if value == "" {
				return fmt.Errorf("%s URL is required", name)
			}
			info.addGrokMediaURLInput(name, GrokMediaInputRoleMask, value)
		}
	}
}

func GrokMediaVideoRequestSessionHash(requestID string) string {
	validatedRequestID, err := xai.ValidateVideoRequestID(requestID)
	if err != nil {
		return ""
	}
	return "grok-video:" + DeriveSessionHashFromSeed(validatedRequestID)
}

func supportsGrokMediaAccount(account *Account) bool {
	return account != nil && account.Platform == PlatformGrok
}

func grokMediaAccountBaseURL(account *Account) string {
	if !supportsGrokMediaAccount(account) {
		return ""
	}
	return account.GetGrokMediaBaseURL()
}

func (s *OpenAIGatewayService) BindGrokMediaVideoRequestAccount(ctx context.Context, groupID *int64, requestID string, accountID int64) error {
	return s.BindStickySession(ctx, groupID, GrokMediaVideoRequestSessionHash(requestID), accountID)
}

func (s *OpenAIGatewayService) ForwardGrokMedia(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	endpoint GrokMediaEndpoint,
	requestID string,
	body []byte,
	contentType string,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()
	if account == nil {
		return nil, fmt.Errorf("grok account is required")
	}
	if !supportsGrokMediaAccount(account) {
		return nil, fmt.Errorf("account platform %s is not supported for grok media", account.Platform)
	}
	if err := ValidateGrokMediaRequestContext(ctx, endpoint, contentType, body); err != nil {
		return nil, err
	}
	beginClientResponseModelPolicy(c, ctx, account, ParseGrokMediaRequest(contentType, body).Model)
	beginUpstreamResponseModelObservation(c)

	token, _, err := s.getRequestCredential(ctx, c, account)
	if err != nil {
		return nil, err
	}
	if endpoint == GrokMediaEndpointVideoContent {
		return s.forwardGrokMediaVideoContent(ctx, c, account, token, requestID, startTime)
	}
	targetURL, err := buildGrokMediaURL(account, s.cfg, endpoint, requestID)
	if err != nil {
		return nil, err
	}

	body, contentType, err = prepareGrokMediaForwardBody(endpoint, body, contentType)
	if err != nil {
		return nil, err
	}
	originalRequestInfo := ParseGrokMediaRequest(contentType, body)
	requestedModel := originalRequestInfo.Model
	body, contentType, err = normalizeGrokMediaForwardBody(endpoint, body, contentType)
	if err != nil {
		return nil, err
	}
	requestInfo := ParseGrokMediaRequest(contentType, body)
	billingModel := strings.TrimSpace(requestInfo.Model)
	upstreamModel := billingModel
	if upstreamModel != "" {
		if mappedModel, matched := ResolveAccountModelForRequest(ctx, nil, account, upstreamModel); matched && strings.TrimSpace(mappedModel) != "" {
			upstreamModel = strings.TrimSpace(mappedModel)
			body, err = sjson.SetBytes(body, "model", upstreamModel)
			if err != nil {
				return nil, fmt.Errorf("apply grok media account model mapping: %w", err)
			}
		}
	}
	SetOpsUpstreamModel(c, upstreamModel)
	body, contentType, err = sanitizeGrokMediaForwardBody(endpoint, body, contentType)
	if err != nil {
		return nil, err
	}

	var bodyReader io.Reader
	if endpoint.RequiresRequestBody() {
		bodyReader = bytes.NewReader(body)
	}
	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	defer releaseUpstreamCtx()
	upstreamReq, err := http.NewRequestWithContext(upstreamCtx, endpoint.httpMethod(), targetURL, bodyReader)
	if err != nil {
		return nil, err
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+token)
	upstreamReq.Header.Set("Accept", "application/json")
	if account.IsGrokOAuth() && isGrokCLIProxyTarget(targetURL) {
		applyGrokCLIHeaders(upstreamReq.Header)
	}
	if endpoint.RequiresRequestBody() {
		contentType = strings.TrimSpace(contentType)
		if contentType == "" {
			contentType = "application/json"
		}
		upstreamReq.Header.Set("Content-Type", contentType)
	}
	account.ApplyHeaderOverrides(upstreamReq.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	upstreamStart := time.Now()
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	defer func() { _ = resp.Body.Close() }()

	requestIDHeader := firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id"))
	requestModel := requestedModel
	if resp.StatusCode >= 400 {
		if shouldRetryGrokUnauthorized(ctx, account, resp.StatusCode) {
			if refreshedToken, refreshErr := s.refreshGrokAfterUnauthorizedForRequest(ctx, c, account); refreshErr == nil {
				_ = resp.Body.Close()
				return s.ForwardGrokMedia(
					contextWithGrokUnauthorizedRetry(ctx, refreshedToken),
					c,
					account,
					endpoint,
					requestID,
					body,
					contentType,
				)
			} else {
				return nil, refreshErr
			}
		}
		return s.handleGrokMediaErrorResponse(ctx, resp, c, account, requestIDHeader)
	}

	s.updateGrokUsageSnapshot(ctx, account, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))
	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}
	if endpoint == GrokMediaEndpointImagesGenerations || endpoint == GrokMediaEndpointImagesEdits {
		if countOpenAIResponseImageOutputsFromJSONBytes(respBody) <= 0 {
			setOpsUpstreamError(c, http.StatusBadGateway, "xAI upstream returned no image output", truncateString(string(respBody), 512))
			return nil, &UpstreamFailoverError{
				StatusCode:      http.StatusBadGateway,
				ResponseBody:    respBody,
				ResponseHeaders: resp.Header.Clone(),
			}
		}
	}
	if endpoint == GrokMediaEndpointVideoStatus {
		respBody = rewriteGrokMediaVideoContentURLs(respBody, requestID, grokMediaContentProxyURL(c, requestID))
		respBody = rewriteGrokVideoTaskFailureMessage(ctx, respBody)
	}
	// Synchronous Grok image responses can contain short-lived URLs hosted on
	// an upstream domain that the customer cannot reach directly.  Copy only
	// image-generation/edit output into the gateway object store before writing
	// the response; video URLs and asynchronous task responses remain untouched.
	if endpoint == GrokMediaEndpointImagesGenerations || endpoint == GrokMediaEndpointImagesEdits {
		respBody = s.RewriteSynchronousImageURLs(
			ctx,
			c,
			respBody,
			firstNonEmpty(requestIDHeader, requestID),
			"",
		)
	}
	// Keep the raw upstream model for the audit record, while honoring the
	// group's client-facing response declaration policy in the JSON payload.
	observeUpstreamOpenAIResponseModel(c, respBody, "")
	respBody = rewriteResponseModelForClient(c, account, requestModel, respBody)
	writeGrokMediaResponse(c, resp, respBody, s.responseHeaderFilter)
	usage := grokMediaUsageFromResponse(endpoint, requestInfo, respBody)
	if endpoint == GrokMediaEndpointVideoStatus {
		status := ParseGrokVideoTaskStatusForAccount(account, respBody)
		usage.MediaTaskState = status.State
		usage.MediaTaskMessage = status.Message
		usage.VideoCount = status.VideoCount
		usage.VideoResolution = status.Resolution
		usage.VideoDurationSeconds = status.DurationSeconds
	}
	if endpoint == GrokMediaEndpointVideoStatus && usage.ResponseID == "" {
		usage.ResponseID = strings.TrimSpace(requestID)
	}
	return &OpenAIForwardResult{
		RequestID:                     requestIDHeader,
		ResponseID:                    usage.ResponseID,
		Usage:                         usage.Usage,
		Model:                         requestModel,
		BillingModel:                  firstNonEmpty(billingModel, requestModel),
		UpstreamModel:                 firstNonEmpty(upstreamModel, requestModel),
		UpstreamResponseModel:         observedUpstreamResponseModel(c),
		UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
		UpstreamModelMismatch:         upstreamModelMismatch(firstNonEmpty(upstreamModel, requestModel), observedUpstreamResponseModel(c)),
		ResponseHeaders:               resp.Header.Clone(),
		Duration:                      time.Since(startTime),
		ImageCount:                    usage.ImageCount,
		ImageSize:                     usage.ImageSize,
		ImageInputSize:                usage.ImageInputSize,
		ImageOutputSizes:              usage.ImageOutputSizes,
		VideoCount:                    usage.VideoCount,
		VideoResolution:               usage.VideoResolution,
		VideoDurationSeconds:          usage.VideoDurationSeconds,
		MediaTaskState:                usage.MediaTaskState,
		MediaTaskMessage:              usage.MediaTaskMessage,
	}, nil
}

func (s *OpenAIGatewayService) forwardGrokMediaVideoContent(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	token, requestID string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	statusURL, err := buildGrokMediaURL(account, s.cfg, GrokMediaEndpointVideoStatus, requestID)
	if err != nil {
		return nil, err
	}
	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	defer releaseUpstreamCtx()
	statusReq, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(upstreamCtx), http.MethodGet, statusURL, nil)
	if err != nil {
		return nil, err
	}
	statusReq.Header.Set("Authorization", "Bearer "+token)
	statusReq.Header.Set("Accept", "application/json")
	if account.IsGrokOAuth() && isGrokCLIProxyTarget(statusURL) {
		applyGrokCLIHeaders(statusReq.Header)
	}
	account.ApplyHeaderOverrides(statusReq.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	upstreamStart := time.Now()
	statusResp, err := s.httpUpstream.Do(statusReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	statusRequestID := firstNonEmpty(statusResp.Header.Get("x-request-id"), statusResp.Header.Get("xai-request-id"))
	if statusResp.StatusCode >= 300 {
		defer func() { _ = statusResp.Body.Close() }()
		SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
		if statusResp.StatusCode < 400 {
			return nil, fmt.Errorf("grok media status redirect is not allowed")
		}
		return s.handleGrokMediaErrorResponse(ctx, statusResp, c, account, statusRequestID)
	}
	statusBody, err := ReadUpstreamResponseBody(statusResp.Body, s.cfg, c, openAITooLargeError)
	_ = statusResp.Body.Close()
	if err != nil {
		SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
		return nil, err
	}

	contentURL, err := grokMediaSignedVideoContentURL(statusBody, requestID)
	if err != nil {
		return nil, err
	}
	signedContent := contentURL != ""
	if !signedContent {
		contentURL, err = buildGrokMediaURL(account, s.cfg, GrokMediaEndpointVideoContent, requestID)
		if err != nil {
			return nil, err
		}
	}
	contentReq, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(upstreamCtx), http.MethodGet, contentURL, nil)
	if err != nil {
		return nil, err
	}
	contentReq.Header.Set("Accept", "*/*")
	if c != nil {
		if rangeHeader := strings.TrimSpace(c.GetHeader("Range")); rangeHeader != "" {
			contentReq.Header.Set("Range", rangeHeader)
		}
	}
	if !signedContent {
		contentReq.Header.Set("Authorization", "Bearer "+token)
		if account.IsGrokOAuth() && isGrokCLIProxyTarget(contentURL) {
			applyGrokCLIHeaders(contentReq.Header)
		}
		account.ApplyHeaderOverrides(contentReq.Header)
	}
	contentResp, err := s.httpUpstream.Do(contentReq, proxyURL, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	defer func() { _ = contentResp.Body.Close() }()
	contentRequestID := firstNonEmpty(contentResp.Header.Get("x-request-id"), contentResp.Header.Get("xai-request-id"), statusRequestID)
	if contentResp.StatusCode >= 300 && contentResp.StatusCode < 400 {
		return nil, fmt.Errorf("grok media signed content redirect is not allowed")
	}
	if contentResp.StatusCode >= 400 && contentResp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		return s.handleGrokMediaErrorResponse(ctx, contentResp, c, account, contentRequestID)
	}
	s.updateGrokUsageSnapshot(ctx, account, xai.ParseQuotaHeaders(contentResp.Header, contentResp.StatusCode))
	if err := writeGrokMediaContentResponse(c, contentResp); err != nil {
		return nil, err
	}
	return &OpenAIForwardResult{
		RequestID:       contentRequestID,
		ResponseHeaders: contentResp.Header.Clone(),
		Duration:        time.Since(startTime),
	}, nil
}

func grokMediaSignedVideoContentURL(body []byte, requestID string) (string, error) {
	rawURL := strings.TrimSpace(gjson.GetBytes(body, "video.url").String())
	if rawURL == "" {
		return "", nil
	}
	// 上游 Sub2API 可能把受保护内容改写为自己的同任务代理路径。
	// 这种情况由调用方按当前账号 base_url 重建并附带上游凭据。
	if isGrokMediaVideoContentURL(rawURL, requestID) {
		return "", nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") ||
		!strings.EqualFold(parsed.Hostname(), "vidgen.x.ai") ||
		(parsed.Port() != "" && parsed.Port() != "443") || parsed.User != nil {
		return "", fmt.Errorf("grok media status returned an unsupported video content URL")
	}
	return parsed.String(), nil
}

func prepareGrokMediaForwardBody(endpoint GrokMediaEndpoint, body []byte, contentType string) ([]byte, string, error) {
	if endpoint != GrokMediaEndpointImagesEdits || gjson.ValidBytes(body) {
		return body, contentType, nil
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
		return body, contentType, nil
	}

	info := ParseGrokMediaRequest(contentType, body)
	payload := make(map[string]any)
	if info.Model != "" {
		payload["model"] = info.Model
	}
	if info.Prompt != "" {
		payload["prompt"] = info.Prompt
	}
	if info.N > 1 {
		payload["n"] = info.N
	}
	if info.Size != "" {
		payload["size"] = info.Size
	}

	images := make([]map[string]string, 0, len(info.MediaInputs))
	maskImageURL := ""
	for _, input := range info.MediaInputs {
		imageURL, err := input.forwardURL()
		if err != nil {
			return nil, "", err
		}
		if imageURL == "" {
			continue
		}
		if input.Role == GrokMediaInputRoleMask {
			maskImageURL = imageURL
			continue
		}
		images = append(images, map[string]string{"url": imageURL})
	}
	if len(images) > 0 {
		payload["image"] = images[0]
		if len(images) > 1 {
			payload["images"] = images
		}
	}

	if maskImageURL != "" {
		payload["mask"] = map[string]string{"url": maskImageURL}
	}

	out, err := marshalOpenAIUpstreamJSON(payload)
	if err != nil {
		return nil, "", err
	}
	return out, "application/json", nil
}

func normalizeGrokMediaForwardBody(endpoint GrokMediaEndpoint, body []byte, contentType string) ([]byte, string, error) {
	if !endpoint.RequiresRequestBody() || !gjson.ValidBytes(body) {
		return body, contentType, nil
	}
	imageFields := []string{"image", "images", "reference_images"}
	if endpoint == GrokMediaEndpointImagesEdits {
		imageFields = append(imageFields, "mask")
	}
	var err error
	body, err = canonicalizeGrokMediaImageURLFields(body, imageFields...)
	if err != nil {
		return nil, "", err
	}
	if endpoint == GrokMediaEndpointVideosEdits || endpoint == GrokMediaEndpointVideosExtensions {
		body, err = canonicalizeGrokMediaVideoInput(body)
		if err != nil {
			return nil, "", err
		}
	}
	info := ParseGrokMediaRequest(contentType, body)
	out := body
	upstreamModel := normalizeGrokMediaModelForEndpoint(endpoint, info.Model, info.HasInputImage())
	if upstreamModel != "" && upstreamModel != info.Model {
		out, err = sjson.SetBytes(out, "model", upstreamModel)
		if err != nil {
			return nil, "", fmt.Errorf("rewrite grok media model: %w", err)
		}
	}
	if endpoint.IsVideoCreationRequest() {
		var err error
		if endpoint == GrokMediaEndpointVideosGenerations {
			out, err = sjson.SetBytes(out, "duration", info.DurationSeconds)
			if err != nil {
				return nil, "", fmt.Errorf("normalize grok video duration: %w", err)
			}
			out, err = sjson.SetBytes(out, "resolution", info.Resolution)
			if err != nil {
				return nil, "", fmt.Errorf("normalize grok video resolution: %w", err)
			}
		} else if endpoint == GrokMediaEndpointVideosExtensions {
			out, err = sjson.SetBytes(out, "duration", GrokMediaTaskDurationSeconds(endpoint, info))
			if err != nil {
				return nil, "", fmt.Errorf("normalize grok video extension duration: %w", err)
			}
			if gjson.GetBytes(out, "seconds").Exists() {
				out, err = sjson.DeleteBytes(out, "seconds")
				if err != nil {
					return nil, "", fmt.Errorf("remove legacy grok video extension seconds: %w", err)
				}
			}
		}
		if gjson.GetBytes(out, "n").Exists() {
			out, err = sjson.DeleteBytes(out, "n")
			if err != nil {
				return nil, "", fmt.Errorf("remove unsupported grok video output count: %w", err)
			}
		}
	}
	return out, contentType, nil
}

func canonicalizeGrokMediaVideoInput(body []byte) ([]byte, error) {
	video := gjson.GetBytes(body, "video")
	if !video.IsObject() {
		return nil, errors.New("video must be an object")
	}
	videoURL := strings.TrimSpace(video.Get("url").String())
	if videoURL == "" {
		videoURL = strings.TrimSpace(video.Get("video_url").String())
	}
	out := body
	var err error
	if videoURL != "" && strings.TrimSpace(video.Get("url").String()) != videoURL {
		out, err = sjson.SetBytes(out, "video.url", videoURL)
		if err != nil {
			return nil, fmt.Errorf("normalize grok video URL: %w", err)
		}
	}
	if gjson.GetBytes(out, "video.video_url").Exists() {
		out, err = sjson.DeleteBytes(out, "video.video_url")
		if err != nil {
			return nil, fmt.Errorf("remove legacy grok video URL: %w", err)
		}
	}
	return out, nil
}

func canonicalizeGrokMediaImageURLFields(body []byte, fields ...string) ([]byte, error) {
	out := body
	for _, field := range fields {
		value := gjson.GetBytes(out, field)
		if !value.Exists() {
			continue
		}
		if value.IsArray() {
			for index := range value.Array() {
				var err error
				out, err = canonicalizeGrokMediaImageURLObject(out, fmt.Sprintf("%s.%d", field, index))
				if err != nil {
					return nil, err
				}
			}
			continue
		}
		var err error
		out, err = canonicalizeGrokMediaImageURLObject(out, field)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func canonicalizeGrokMediaImageURLObject(body []byte, path string) ([]byte, error) {
	value := gjson.GetBytes(body, path)
	imageURL, err := grokMediaJSONImageURLChecked(value, path)
	if err != nil {
		return nil, err
	}
	if value.Type == gjson.String {
		out, err := sjson.SetBytes(body, path, map[string]string{"url": imageURL})
		if err != nil {
			return nil, fmt.Errorf("normalize grok media image URL: %w", err)
		}
		return out, nil
	}

	out := body
	if strings.TrimSpace(value.Get("url").String()) != imageURL {
		out, err = sjson.SetBytes(out, path+".url", imageURL)
		if err != nil {
			return nil, fmt.Errorf("normalize grok media image URL: %w", err)
		}
	}
	if gjson.GetBytes(out, path+".image_url").Exists() {
		out, err = sjson.DeleteBytes(out, path+".image_url")
		if err != nil {
			return nil, fmt.Errorf("remove legacy grok media image URL: %w", err)
		}
	}
	return out, nil
}

func sanitizeGrokMediaForwardBody(endpoint GrokMediaEndpoint, body []byte, contentType string) ([]byte, string, error) {
	if !endpoint.RequiresRequestBody() || !gjson.ValidBytes(body) {
		return body, contentType, nil
	}
	switch endpoint {
	case GrokMediaEndpointImagesGenerations, GrokMediaEndpointImagesEdits:
		if !gjson.GetBytes(body, "size").Exists() {
			return body, contentType, nil
		}
		out, err := sjson.DeleteBytes(body, "size")
		if err != nil {
			return nil, "", fmt.Errorf("sanitize grok media size: %w", err)
		}
		return out, contentType, nil
	default:
		return body, contentType, nil
	}
}

func (r GrokMediaRequestInfo) HasInputImage() bool {
	for _, input := range r.MediaInputs {
		if input.Role != GrokMediaInputRoleMask && input.Role != GrokMediaInputRoleVideo {
			return true
		}
	}
	return false
}

func normalizeGrokMediaModelForEndpoint(endpoint GrokMediaEndpoint, model string, hasInputImage bool) string {
	model = strings.TrimSpace(model)
	switch endpoint {
	case GrokMediaEndpointImagesGenerations, GrokMediaEndpointImagesEdits:
		if strings.EqualFold(model, "grok-imagine") {
			return "grok-imagine-image-quality"
		}
	case GrokMediaEndpointVideosGenerations:
		if xai.CanonicalModelID(model) == "grok-imagine-video-1.5" && !hasInputImage {
			return "grok-imagine-video"
		}
	}
	return model
}

// ResolveGrokMediaRoutingModel returns the exact model that will be sent to
// xAI. Account capability filtering must use this value, otherwise a text-only
// video-1.5 request can reject an account that correctly exposes the base
// grok-imagine-video model used by the actual upstream call.
func ResolveGrokMediaRoutingModel(endpoint GrokMediaEndpoint, info GrokMediaRequestInfo) string {
	return normalizeGrokMediaModelForEndpoint(endpoint, info.Model, info.HasInputImage())
}

type grokMediaUsageMetadata struct {
	ResponseID           string
	Usage                OpenAIUsage
	ImageCount           int
	ImageSize            string
	ImageInputSize       string
	ImageOutputSizes     []string
	VideoCount           int
	VideoResolution      string
	VideoDurationSeconds int
	MediaTaskState       string
	MediaTaskMessage     string
}

func grokMediaUsageFromResponse(endpoint GrokMediaEndpoint, requestInfo GrokMediaRequestInfo, responseBody []byte) grokMediaUsageMetadata {
	usage, _ := extractOpenAIUsageFromJSONBytes(responseBody)
	meta := grokMediaUsageMetadata{Usage: usage}
	switch endpoint {
	case GrokMediaEndpointImagesGenerations, GrokMediaEndpointImagesEdits:
		imageCount := countOpenAIResponseImageOutputsFromJSONBytes(responseBody)
		// A successful HTTP status is not proof that an image was produced.
		// Billing must use actual response outputs; an empty data/output array is
		// recorded as zero-cost instead of falling back to the requested n value.
		meta.ImageCount = imageCount
		meta.ImageSize = requestInfo.SizeTier
		meta.ImageInputSize = requestInfo.Size
		meta.ImageOutputSizes = collectOpenAIResponseImageOutputSizesFromJSONBytes(responseBody)
	case GrokMediaEndpointVideosGenerations, GrokMediaEndpointVideosEdits, GrokMediaEndpointVideosExtensions:
		meta.ResponseID = extractGrokMediaVideoRequestID(responseBody)
		if meta.ResponseID != "" {
			meta.VideoCount = 1
		} else {
			meta.VideoCount = countGrokMediaSynchronousVideoOutputs(responseBody)
		}
		meta.VideoResolution = GrokMediaBillingResolution(endpoint, requestInfo)
		meta.VideoDurationSeconds = GrokMediaTaskDurationSeconds(endpoint, requestInfo)
	case GrokMediaEndpointVideoStatus:
		status := ParseGrokVideoTaskStatus(responseBody)
		meta.MediaTaskState = status.State
		meta.MediaTaskMessage = status.Message
		meta.VideoCount = status.VideoCount
		meta.VideoResolution = status.Resolution
		meta.VideoDurationSeconds = status.DurationSeconds
	}
	return meta
}

func countGrokMediaSynchronousVideoOutputs(body []byte) int {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return 0
	}
	count := 0
	for _, path := range []string{"data", "videos", "output", "data.videos"} {
		value := gjson.GetBytes(body, path)
		if !value.IsArray() {
			continue
		}
		for _, item := range value.Array() {
			if grokVideoOutputExists(item) {
				count++
			}
		}
		if count > 0 {
			return count
		}
	}
	for _, path := range []string{"url", "video_url", "b64_json", "video.url", "video.video_url", "video.b64_json", "data.url", "data.video.url", "output.url"} {
		if strings.TrimSpace(gjson.GetBytes(body, path).String()) != "" {
			return 1
		}
	}
	return 0
}

func grokVideoOutputExists(item gjson.Result) bool {
	for _, path := range []string{"url", "video_url", "b64_json", "video.url", "video.video_url", "video.b64_json"} {
		if strings.TrimSpace(item.Get(path).String()) != "" {
			return true
		}
	}
	return item.Type == gjson.String && strings.TrimSpace(item.String()) != ""
}

func extractGrokMediaVideoRequestID(body []byte) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	for _, path := range []string{"request_id", "id", "data.request_id", "data.id", "video.request_id", "video.id", "task_id", "data.task_id", "video.task_id"} {
		if id := strings.TrimSpace(gjson.GetBytes(body, path).String()); id != "" {
			return id
		}
	}
	return ""
}

func (s *OpenAIGatewayService) handleGrokMediaErrorResponse(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	requestIDHeader string,
) (*OpenAIForwardResult, error) {
	body := s.readUpstreamErrorBody(resp)
	// Reconcile account readiness before any configurable passthrough branch can
	// return; otherwise a Grok 429 could remain schedulable.
	if account.Platform == PlatformGrok {
		s.handleGrokMediaAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, body)
	}
	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	if upstreamMsg == "" {
		upstreamMsg = fmt.Sprintf("xAI upstream returned status %d", resp.StatusCode)
	}

	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(string(body), maxBytes)
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	if isGrokContentPolicyRejection(resp.StatusCode, body) {
		clientMsg := grokContentPolicyClientMessage(body)
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
			UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: requestIDHeader,
			Kind: "http_error", Message: clientMsg, Detail: upstreamDetail,
		})
		MarkResponseCommitted(c)
		writeGrokMediaErrorResponse(c, http.StatusForbidden, "invalid_request_error", clientMsg)
		return nil, fmt.Errorf("grok content policy rejection: %s", clientMsg)
	}

	if status, errType, errMsg, matched := applyErrorPassthroughRule(
		c,
		account.Platform,
		resp.StatusCode,
		body,
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	); matched {
		MarkResponseCommitted(c)
		writeGrokMediaErrorResponse(c, status, errType, errMsg)
		return nil, fmt.Errorf("upstream error: %d (passthrough rule matched) message=%s", resp.StatusCode, upstreamMsg)
	}

	if !account.ShouldHandleErrorCode(resp.StatusCode) {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  requestIDHeader,
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		MarkResponseCommitted(c)
		writeGrokMediaErrorResponse(c, http.StatusInternalServerError, "upstream_error", "Upstream gateway error")
		return nil, fmt.Errorf("upstream error: %d (not in custom error codes) message=%s", resp.StatusCode, upstreamMsg)
	}

	shouldFailover := s.shouldFailoverGrokUpstreamError(resp.StatusCode, body)
	kind := "http_error"
	if shouldFailover {
		kind = "failover"
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  requestIDHeader,
		Kind:               kind,
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})
	if kind == "failover" {
		return nil, &UpstreamFailoverError{
			StatusCode:             resp.StatusCode,
			ResponseBody:           body,
			RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
		}
	}

	MarkResponseCommitted(c)
	writeGrokMediaErrorResponse(c, resp.StatusCode, grokMediaErrorType(resp.StatusCode), upstreamMsg)
	return nil, fmt.Errorf("upstream error: %d %s", resp.StatusCode, upstreamMsg)
}

// handleGrokMediaAccountUpstreamError keeps media entitlement separate from
// text-account health. A non-policy 403 from a media endpoint proves only that
// new media work is unavailable; globally cooling the account would also break
// otherwise healthy Chat/Responses traffic.
func (s *OpenAIGatewayService) handleGrokMediaAccountUpstreamError(ctx context.Context, account *Account, statusCode int, headers http.Header, body []byte) {
	if s == nil || account == nil {
		return
	}
	if statusCode != http.StatusForbidden || isGrokRequestScopedPolicyError(statusCode, body) {
		s.handleGrokAccountUpstreamError(ctx, account, statusCode, headers, body)
		return
	}

	previous, _ := grokMediaEligibilitySnapshotFromExtra(account.Extra)
	snapshot := buildGrokMediaRequestForbiddenSnapshot(previous, time.Now())
	if s.accountRepo == nil || account.ID <= 0 {
		return
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	if err := s.accountRepo.UpdateExtra(stateCtx, account.ID, map[string]any{
		GrokMediaEligibilitySnapshotExtraKey: snapshot,
	}); err != nil {
		slog.Warn("persist_grok_media_forbidden_failed", "account_id", account.ID, "error", err)
	}
}

func grokMediaErrorType(statusCode int) string {
	switch statusCode {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusConflict:
		return "conflict_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "upstream_error"
	}
}

func writeGrokMediaErrorResponse(c *gin.Context, statusCode int, errType, message string) {
	if c == nil || c.Writer == nil || c.Writer.Written() {
		return
	}
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"type":    strings.TrimSpace(errType),
			"message": strings.TrimSpace(message),
		},
	})
}

func writeGrokMediaResponse(c *gin.Context, resp *http.Response, body []byte, filter *responseheaders.CompiledHeaderFilter) {
	if c == nil || resp == nil {
		return
	}
	writeDecodedJSONResponseHeaders(c.Writer.Header(), resp.Header, filter)
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(resp.StatusCode, contentType, body)
}

// rewriteGrokVideoTaskFailureMessage handles asynchronous failures returned
// with HTTP 200. Only existing human-readable string fields are replaced;
// status, state, error codes and all other machine-readable fields stay intact.
func rewriteGrokVideoTaskFailureMessage(ctx context.Context, body []byte) []byte {
	customMessage := GatewayCustomErrorMessageFromContext(ctx)
	if customMessage == "" || ParseGrokVideoTaskStatus(body).State != GrokVideoTaskStateFailed {
		return body
	}
	rewritten := body
	for _, path := range []string{
		"error.message", "message", "data.error.message", "data.message", "video.error.message",
		"error", "data.error", "video.error",
	} {
		value := gjson.GetBytes(rewritten, path)
		if !value.Exists() || value.Type != gjson.String {
			continue
		}
		updated, err := sjson.SetBytes(rewritten, path, customMessage)
		if err != nil {
			continue
		}
		rewritten = updated
	}
	return rewritten
}

func writeGrokMediaContentResponse(c *gin.Context, resp *http.Response) error {
	if c == nil || resp == nil || resp.Body == nil {
		return fmt.Errorf("grok media content response is incomplete")
	}
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Content-Disposition"} {
		if value := strings.TrimSpace(resp.Header.Get(name)); value != "" {
			c.Header(name, value)
		}
	}
	if strings.TrimSpace(c.Writer.Header().Get("Content-Length")) == "" && resp.ContentLength >= 0 {
		c.Header("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	if strings.TrimSpace(c.Writer.Header().Get("Content-Type")) == "" {
		c.Header("Content-Type", "application/octet-stream")
	}
	c.Status(resp.StatusCode)
	MarkResponseCommitted(c)
	_, err := io.Copy(c.Writer, resp.Body)
	return err
}

func rewriteGrokMediaVideoContentURLs(body []byte, requestID, proxyURL string) []byte {
	if len(body) == 0 || strings.TrimSpace(requestID) == "" || strings.TrimSpace(proxyURL) == "" || !gjson.ValidBytes(body) {
		return body
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return body
	}
	changed := rewriteGrokMediaKnownVideoURL(&value, proxyURL)
	if rewriteGrokMediaVideoContentURLValue(&value, requestID, proxyURL) {
		changed = true
	}
	if !changed {
		return body
	}
	rewritten, err := json.Marshal(value)
	if err != nil {
		return body
	}
	return rewritten
}

func rewriteGrokMediaKnownVideoURL(value *any, proxyURL string) bool {
	root, ok := (*value).(map[string]any)
	if !ok {
		return false
	}
	video, ok := root["video"].(map[string]any)
	if !ok {
		return false
	}
	rawURL, ok := video["url"].(string)
	if !ok || strings.TrimSpace(rawURL) == "" {
		return false
	}
	video["url"] = proxyURL
	return true
}

func rewriteGrokMediaVideoContentURLValue(value *any, requestID, proxyURL string) bool {
	if value == nil {
		return false
	}
	switch typed := (*value).(type) {
	case map[string]any:
		changed := false
		for key, child := range typed {
			childValue := child
			if rewriteGrokMediaVideoContentURLValue(&childValue, requestID, proxyURL) {
				typed[key] = childValue
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for index, child := range typed {
			childValue := child
			if rewriteGrokMediaVideoContentURLValue(&childValue, requestID, proxyURL) {
				typed[index] = childValue
				changed = true
			}
		}
		return changed
	case string:
		if isGrokMediaVideoContentURL(typed, requestID) {
			*value = proxyURL
			return true
		}
	}
	return false
}

func isGrokMediaVideoContentURL(rawURL, requestID string) bool {
	validatedRequestID, err := xai.ValidateVideoRequestID(requestID)
	if err != nil {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Path == "" {
		return false
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) < 3 {
		return false
	}
	decodedID, err := url.PathUnescape(segments[len(segments)-2])
	if err != nil {
		return false
	}
	return segments[len(segments)-3] == "videos" && decodedID == validatedRequestID && segments[len(segments)-1] == "content"
}

func grokMediaContentProxyURL(c *gin.Context, requestID string) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return ""
	}
	validatedRequestID, err := xai.ValidateVideoRequestID(requestID)
	if err != nil {
		return ""
	}
	prefix := ""
	if strings.HasPrefix(c.Request.URL.Path, "/v1/") {
		prefix = "/v1"
	}
	return prefix + "/videos/" + url.PathEscape(validatedRequestID) + "/content"
}

// GrokVideoPendingBilling is the create-time snapshot used when status polling
// first observes a completed video URL. Status may omit model/duration; we fall
// back to this snapshot, then defaults.
type GrokVideoPendingBilling struct {
	// Seedance 任务同时冻结归属、原账号与创建请求；远程 Key 的本地 ID 恒为 0，
	// 不能拿本地编号构造归属或把轮询请求当成新的付费生成请求。
	Owner                *MediaTaskIdentity                `json:"owner,omitempty"`
	AccountID            int64                             `json:"account_id,omitempty"`
	GroupID              int64                             `json:"group_id,omitempty"`
	RequestMeta          *gatewaylite.RequestMeta          `json:"request_meta,omitempty"`
	FundingSnapshot      *gatewaylite.FundingAuthorization `json:"funding_snapshot,omitempty"`
	RatePolicyAudit      *gatewaylite.RatePolicyAudit      `json:"rate_policy_audit,omitempty"`
	// Seedance 的最终派发数值价随任务持久，完成结算不能依赖管理员当前配置。
	ExecutionPricing     *ExecutionPricingSnapshot        `json:"execution_pricing,omitempty"`
	TaskID               string                            `json:"task_id,omitempty"`
	Model                string                            `json:"model"`
	BillingModel         string                            `json:"billing_model,omitempty"`
	UpstreamModel        string                            `json:"upstream_model,omitempty"`
	VideoResolution      string                            `json:"video_resolution,omitempty"`
	VideoDurationSeconds int                               `json:"video_duration_seconds,omitempty"`
	OriginalModel        string                            `json:"original_model,omitempty"`
	// CreatedAt is when the gateway accepted the async create (RFC3339Nano UTC).
	// duration_ms for deferred billing is measured from this instant until the
	// first official done+video.url observation (status poll or content download),
	// not the latency of that single discovery request alone.
	CreatedAt string `json:"created_at,omitempty"`
}

func grokVideoPendingBillingKey(requestID string, userID, apiKeyID int64) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || userID <= 0 || apiKeyID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d:%s", userID, apiKeyID, requestID)
}

func grokVideoBilledClaimTTL(cfg *config.Config) time.Duration {
	_ = cfg
	return 48 * time.Hour
}

// LoadGrokVideoPendingBilling returns the create-time snapshot (may be nil on miss).
func (s *OpenAIGatewayService) LoadGrokVideoPendingBilling(
	ctx context.Context,
	requestID string,
	userID, apiKeyID int64,
) (*GrokVideoPendingBilling, error) {
	if s == nil || s.cache == nil {
		return nil, fmt.Errorf("grok video pending billing cache is unavailable")
	}
	key := grokVideoPendingBillingKey(requestID, userID, apiKeyID)
	if key == "" {
		return nil, fmt.Errorf("grok video pending billing key is invalid")
	}
	payload, err := s.cache.GetGrokVideoPendingBilling(ctx, key)
	if err != nil || len(payload) == 0 {
		return nil, err
	}
	var pending GrokVideoPendingBilling
	if err := json.Unmarshal(payload, &pending); err != nil {
		return nil, err
	}
	return &pending, nil
}

func grokVideoPendingBillingTTL(cfg *config.Config) time.Duration {
	// 视频生成可能耗时数分钟；创建时价格要保留足够久，避免状态轮询时查不到价。
	_ = cfg
	return 24 * time.Hour
}

// GrokVideoPendingCreatedAtNow 返回创建受理时刻（RFC3339Nano），用于跨请求计算端到端时长。
func GrokVideoPendingCreatedAtNow() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// StoreGrokVideoPendingBilling 持久化创建时结算快照，供后续状态轮询结算使用。
// 此前本仓只有读取方没有写入方，导致 Seedance 任务成功也拿不到快照、永远不入账。
func (s *OpenAIGatewayService) StoreGrokVideoPendingBilling(
	ctx context.Context,
	requestID string,
	userID, apiKeyID int64,
	pending GrokVideoPendingBilling,
) error {
	if s == nil || s.cache == nil {
		return fmt.Errorf("grok video pending billing cache is unavailable")
	}
	key := grokVideoPendingBillingKey(requestID, userID, apiKeyID)
	if key == "" {
		return fmt.Errorf("grok video pending billing key is invalid")
	}
	pending.Model = strings.TrimSpace(pending.Model)
	pending.BillingModel = strings.TrimSpace(pending.BillingModel)
	pending.UpstreamModel = strings.TrimSpace(pending.UpstreamModel)
	pending.OriginalModel = strings.TrimSpace(pending.OriginalModel)
	if pending.VideoResolution != "" {
		pending.VideoResolution = NormalizeVideoBillingResolutionOrDefault(pending.VideoResolution)
	}
	if pending.VideoDurationSeconds > 0 {
		pending.VideoDurationSeconds = NormalizeVideoBillingDurationSecondsOrDefault(pending.VideoDurationSeconds)
	}
	if strings.TrimSpace(pending.CreatedAt) == "" {
		pending.CreatedAt = GrokVideoPendingCreatedAtNow()
	} else {
		pending.CreatedAt = strings.TrimSpace(pending.CreatedAt)
	}
	payload, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	// 走可选窄接口：待结算快照的写入方是 Redis 缓存实现；测试替身只实现读取侧时
	// 这里返回明确错误，而不是让整个 GatewayCache 接口为新增能力膨胀。
	setter, ok := s.cache.(interface {
		SetGrokVideoPendingBilling(ctx context.Context, key string, payload []byte, ttl time.Duration) error
	})
	if !ok {
		return fmt.Errorf("grok video pending billing store is not supported by this cache")
	}
	return setter.SetGrokVideoPendingBilling(ctx, key, payload, grokVideoPendingBillingTTL(s.cfg))
}

// ResolveGrokMediaVideoRequestAccount 返回创建任务时绑定的账号 ID；没有绑定返回 0。
// 查询/删除必须固定使用原任务账号，不能重新调度到其他账号。
func (s *OpenAIGatewayService) ResolveGrokMediaVideoRequestAccount(ctx context.Context, groupID *int64, requestID string) (int64, error) {
	if s == nil || s.cache == nil {
		return 0, fmt.Errorf("grok video request binding cache is unavailable")
	}
	sessionHash := GrokMediaVideoRequestSessionHash(requestID)
	if sessionHash == "" {
		return 0, fmt.Errorf("grok video request binding is invalid")
	}
	return s.getStickySessionAccountID(ctx, groupID, sessionHash)
}

// ClaimGrokVideoBilling returns true once for a completed video request so status
// polls do not double-bill. Fail-closed: claim errors are treated as already billed.
func (s *OpenAIGatewayService) ClaimGrokVideoBilling(
	ctx context.Context,
	requestID string,
	userID, apiKeyID int64,
) (bool, error) {
	if s == nil || s.cache == nil {
		return false, fmt.Errorf("grok video billing claim cache is unavailable")
	}
	key := grokVideoPendingBillingKey(requestID, userID, apiKeyID)
	if key == "" {
		return false, fmt.Errorf("grok video billing claim key is invalid")
	}
	return s.cache.ClaimGrokVideoBilled(ctx, key, grokVideoBilledClaimTTL(s.cfg))
}

// GrokVideoE2EDuration returns wall time from create accept to discovery of completion.
// Returns 0 when CreatedAt is missing or unparseable (caller keeps poll-only Duration).
func GrokVideoE2EDuration(createdAt string, discoveredAt time.Time) time.Duration {
	createdAt = strings.TrimSpace(createdAt)
	if createdAt == "" {
		return 0
	}
	if discoveredAt.IsZero() {
		discoveredAt = time.Now()
	}
	var created time.Time
	var err error
	if created, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		if created, err = time.Parse(time.RFC3339, createdAt); err != nil {
			return 0
		}
	}
	if created.IsZero() {
		return 0
	}
	d := discoveredAt.Sub(created)
	if d < 0 {
		return 0
	}
	return d
}

// StableGrokVideoBillingRequestID is the durable usage_logs / dedup key for one
// async video task (not the per-poll gateway request id).
func StableGrokVideoBillingRequestID(taskRequestID string) string {
	taskRequestID = strings.TrimSpace(taskRequestID)
	if taskRequestID == "" {
		return ""
	}
	if strings.HasPrefix(taskRequestID, "grok-video:") {
		return taskRequestID
	}
	return "grok-video:" + taskRequestID
}
