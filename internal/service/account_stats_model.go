package service

import "strings"

// resolveAccountStatsModel chooses the model that may be used for procurement
// statistics. The final upstream model is authoritative whenever it is known.
// When that evidence is missing, a request model is safe only if the usage row
// carries no mapping evidence; otherwise falling back would charge the
// customer's public-model price for an unmapped upstream request.
func resolveAccountStatsModel(usageLog *UsageLog, upstreamModel, requestedModel string) string {
	if model := strings.TrimSpace(upstreamModel); model != "" {
		return model
	}
	if usageLogHasModelMappingEvidence(usageLog) {
		return ""
	}
	return strings.TrimSpace(requestedModel)
}

func usageLogHasModelMappingEvidence(usageLog *UsageLog) bool {
	if usageLog == nil {
		return false
	}
	if usageLog.ModelMappingChain != nil && strings.TrimSpace(*usageLog.ModelMappingChain) != "" {
		return true
	}
	requested := strings.TrimSpace(usageLog.RequestedModel)
	mapped := strings.TrimSpace(usageLog.ChannelMappedModel)
	return requested != "" && mapped != "" && !strings.EqualFold(requested, mapped)
}
