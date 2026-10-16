package service

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const imageTaskUsageSchemaVersion = 1

// ImageTaskUsageSnapshot contains the minimum non-secret facts needed to retry
// the normal RecordUsage path after the upstream image already succeeded. It
// intentionally excludes account credentials, OAuth tokens, proxies and the
// customer's API key. The account row is reloaded when possible; the embedded
// billing facts are only a deletion-safe fallback.
type ImageTaskUsageSnapshot struct {
	SchemaVersion      int                           `json:"schema_version"`
	Account            ImageTaskAccountUsageSnapshot `json:"account"`
	Result             ImageTaskForwardUsageSnapshot `json:"result"`
	InboundEndpoint    string                        `json:"inbound_endpoint,omitempty"`
	UpstreamEndpoint   string                        `json:"upstream_endpoint,omitempty"`
	UserAgent          string                        `json:"user_agent,omitempty"`
	IPAddress          string                        `json:"ip_address,omitempty"`
	SessionID          string                        `json:"session_id,omitempty"`
	RequestPayloadHash string                        `json:"request_payload_hash,omitempty"`
	QuotaPlatform      string                        `json:"quota_platform,omitempty"`
	Channel            ImageTaskChannelUsageSnapshot `json:"channel,omitempty"`
}

type ImageTaskAccountUsageSnapshot struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name,omitempty"`
	Platform       string   `json:"platform"`
	Type           string   `json:"type"`
	RateMultiplier *float64 `json:"rate_multiplier,omitempty"`
}

type ImageTaskForwardUsageSnapshot struct {
	RequestID                string         `json:"request_id,omitempty"`
	ResponseID               string         `json:"response_id,omitempty"`
	Usage                    OpenAIUsage    `json:"usage"`
	Model                    string         `json:"model"`
	BillingModel             string         `json:"billing_model,omitempty"`
	UpstreamModel            string         `json:"upstream_model,omitempty"`
	UpstreamEndpoint         string         `json:"upstream_endpoint,omitempty"`
	ServiceTier              *string        `json:"service_tier,omitempty"`
	SentServiceTier          *string        `json:"sent_service_tier,omitempty"`
	ResponseServiceTier      *string        `json:"response_service_tier,omitempty"`
	RequestedServiceTier     *string        `json:"requested_service_tier,omitempty"`
	PriorityPolicyMode       string         `json:"priority_policy_mode,omitempty"`
	PriorityDetectionSource  string         `json:"priority_detection_source,omitempty"`
	PriorityPolicyAction     string         `json:"priority_policy_action,omitempty"`
	PriorityPolicyRuleID     string         `json:"priority_policy_rule_id,omitempty"`
	PriorityPolicyFallback   bool           `json:"priority_policy_fallback,omitempty"`
	RequestedReasoningEffort *string        `json:"requested_reasoning_effort,omitempty"`
	ReasoningEffort          *string        `json:"reasoning_effort,omitempty"`
	DurationMillis           int64          `json:"duration_millis,omitempty"`
	FirstTokenMs             *int           `json:"first_token_ms,omitempty"`
	ImageCount               int            `json:"image_count"`
	ImageSize                string         `json:"image_size,omitempty"`
	ImageInputSize           string         `json:"image_input_size,omitempty"`
	ImageOutputSize          string         `json:"image_output_size,omitempty"`
	ImageOutputSizes         []string       `json:"image_output_sizes,omitempty"`
	ImageSizeSource          string         `json:"image_size_source,omitempty"`
	ImageSizeBreakdown       map[string]int `json:"image_size_breakdown,omitempty"`
}

type ImageTaskChannelUsageSnapshot struct {
	ChannelID          int64  `json:"channel_id,omitempty"`
	OriginalModel      string `json:"original_model,omitempty"`
	ChannelMappedModel string `json:"channel_mapped_model,omitempty"`
	BillingModelSource string `json:"billing_model_source,omitempty"`
	ModelMappingChain  string `json:"model_mapping_chain,omitempty"`
}

func NewImageTaskUsageSnapshot(result *OpenAIForwardResult, account *Account, inboundEndpoint, upstreamEndpoint, userAgent, ipAddress, sessionID, requestPayloadHash, quotaPlatform string, channel ChannelUsageFields) *ImageTaskUsageSnapshot {
	if result == nil || account == nil {
		return nil
	}
	return &ImageTaskUsageSnapshot{
		SchemaVersion: imageTaskUsageSchemaVersion,
		Account: ImageTaskAccountUsageSnapshot{
			ID: account.ID, Name: strings.TrimSpace(account.Name), Platform: strings.TrimSpace(account.Platform),
			Type: strings.TrimSpace(account.Type), RateMultiplier: cloneFloat64Ptr(account.RateMultiplier),
		},
		Result: ImageTaskForwardUsageSnapshot{
			RequestID: result.RequestID, ResponseID: result.ResponseID, Usage: result.Usage,
			Model: result.Model, BillingModel: result.BillingModel, UpstreamModel: result.UpstreamModel,
			UpstreamEndpoint: result.UpstreamEndpoint, ServiceTier: cloneStringPtr(result.ServiceTier),
			SentServiceTier: cloneStringPtr(result.SentServiceTier), ResponseServiceTier: cloneStringPtr(result.ResponseServiceTier),
			RequestedServiceTier: cloneStringPtr(result.RequestedServiceTier), PriorityPolicyMode: result.PriorityPolicyMode,
			PriorityDetectionSource: result.PriorityDetectionSource, PriorityPolicyAction: result.PriorityPolicyAction,
			PriorityPolicyRuleID: result.PriorityPolicyRuleID, PriorityPolicyFallback: result.PriorityPolicyFallback,
			RequestedReasoningEffort: cloneStringPtr(result.RequestedReasoningEffort),
			ReasoningEffort:          cloneStringPtr(result.ReasoningEffort), DurationMillis: result.Duration.Milliseconds(),
			FirstTokenMs: cloneIntPtr(result.FirstTokenMs), ImageCount: result.ImageCount, ImageSize: result.ImageSize,
			ImageInputSize: result.ImageInputSize, ImageOutputSize: result.ImageOutputSize,
			ImageOutputSizes: append([]string(nil), result.ImageOutputSizes...), ImageSizeSource: result.ImageSizeSource,
			ImageSizeBreakdown: cloneStringIntMap(result.ImageSizeBreakdown),
		},
		InboundEndpoint: strings.TrimSpace(inboundEndpoint), UpstreamEndpoint: strings.TrimSpace(upstreamEndpoint),
		UserAgent: strings.TrimSpace(userAgent), IPAddress: strings.TrimSpace(ipAddress), SessionID: sanitizeSessionID(sessionID),
		RequestPayloadHash: strings.TrimSpace(requestPayloadHash), QuotaPlatform: strings.TrimSpace(quotaPlatform),
		Channel: ImageTaskChannelUsageSnapshot{
			ChannelID: channel.ChannelID, OriginalModel: channel.OriginalModel, ChannelMappedModel: channel.ChannelMappedModel,
			BillingModelSource: channel.BillingModelSource, ModelMappingChain: channel.ModelMappingChain,
		},
	}
}

func (s *ImageTaskUsageSnapshot) Validate() error {
	if s == nil || s.SchemaVersion != imageTaskUsageSchemaVersion || s.Account.ID <= 0 || strings.TrimSpace(s.Account.Platform) == "" || strings.TrimSpace(s.Result.Model) == "" || s.Result.ImageCount <= 0 {
		return errors.New("image task usage snapshot is invalid")
	}
	return nil
}

func (s *ImageTaskUsageSnapshot) Marshal() (json.RawMessage, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(s)
	return json.RawMessage(payload), err
}

func DecodeImageTaskUsage(payload json.RawMessage) (*ImageTaskUsageSnapshot, error) {
	var snapshot ImageTaskUsageSnapshot
	if len(payload) == 0 || json.Unmarshal(payload, &snapshot) != nil {
		return nil, errors.New("image task usage snapshot cannot be decoded")
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *ImageTaskUsageSnapshot) Clone() *ImageTaskUsageSnapshot {
	if s == nil {
		return nil
	}
	cloned := *s
	cloned.Account.RateMultiplier = cloneFloat64Ptr(s.Account.RateMultiplier)
	cloned.Result.ServiceTier = cloneStringPtr(s.Result.ServiceTier)
	cloned.Result.SentServiceTier = cloneStringPtr(s.Result.SentServiceTier)
	cloned.Result.ResponseServiceTier = cloneStringPtr(s.Result.ResponseServiceTier)
	cloned.Result.RequestedServiceTier = cloneStringPtr(s.Result.RequestedServiceTier)
	cloned.Result.RequestedReasoningEffort = cloneStringPtr(s.Result.RequestedReasoningEffort)
	cloned.Result.ReasoningEffort = cloneStringPtr(s.Result.ReasoningEffort)
	cloned.Result.FirstTokenMs = cloneIntPtr(s.Result.FirstTokenMs)
	cloned.Result.ImageOutputSizes = append([]string(nil), s.Result.ImageOutputSizes...)
	cloned.Result.ImageSizeBreakdown = cloneStringIntMap(s.Result.ImageSizeBreakdown)
	return &cloned
}

func (s *ImageTaskUsageSnapshot) ResultValue() *OpenAIForwardResult {
	if s == nil {
		return nil
	}
	v := s.Result
	return &OpenAIForwardResult{
		RequestID: v.RequestID, ResponseID: v.ResponseID, Usage: v.Usage, Model: v.Model, BillingModel: v.BillingModel,
		UpstreamModel: v.UpstreamModel, UpstreamEndpoint: v.UpstreamEndpoint, ServiceTier: cloneStringPtr(v.ServiceTier),
		SentServiceTier: cloneStringPtr(v.SentServiceTier), ResponseServiceTier: cloneStringPtr(v.ResponseServiceTier),
		RequestedServiceTier: cloneStringPtr(v.RequestedServiceTier), PriorityPolicyMode: v.PriorityPolicyMode,
		PriorityDetectionSource: v.PriorityDetectionSource, PriorityPolicyAction: v.PriorityPolicyAction,
		PriorityPolicyRuleID: v.PriorityPolicyRuleID, PriorityPolicyFallback: v.PriorityPolicyFallback,
		RequestedReasoningEffort: cloneStringPtr(v.RequestedReasoningEffort),
		ReasoningEffort:          cloneStringPtr(v.ReasoningEffort), Duration: time.Duration(v.DurationMillis) * time.Millisecond,
		FirstTokenMs: cloneIntPtr(v.FirstTokenMs), ImageCount: v.ImageCount, ImageSize: v.ImageSize,
		ImageInputSize: v.ImageInputSize, ImageOutputSize: v.ImageOutputSize,
		ImageOutputSizes: append([]string(nil), v.ImageOutputSizes...), ImageSizeSource: v.ImageSizeSource,
		ImageSizeBreakdown: cloneStringIntMap(v.ImageSizeBreakdown),
	}
}

func (s *ImageTaskUsageSnapshot) AccountValue() *Account {
	if s == nil {
		return nil
	}
	return &Account{ID: s.Account.ID, Name: s.Account.Name, Platform: s.Account.Platform, Type: s.Account.Type, RateMultiplier: cloneFloat64Ptr(s.Account.RateMultiplier)}
}

func (s *ImageTaskUsageSnapshot) ChannelValue() ChannelUsageFields {
	if s == nil {
		return ChannelUsageFields{}
	}
	v := s.Channel
	return ChannelUsageFields{ChannelID: v.ChannelID, OriginalModel: v.OriginalModel, ChannelMappedModel: v.ChannelMappedModel, BillingModelSource: v.BillingModelSource, ModelMappingChain: v.ModelMappingChain}
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneStringIntMap(input map[string]int) map[string]int {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]int, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
