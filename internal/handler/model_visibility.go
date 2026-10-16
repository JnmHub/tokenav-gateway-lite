package handler

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func modelDisabledByPricing(pricing *service.PricingService, model string) bool {
	if pricing == nil {
		return false
	}
	return pricing.IsModelDisabled(model)
}

func filterDisabledModelIDs(modelIDs []string, pricing *service.PricingService) []string {
	if pricing == nil || len(modelIDs) == 0 {
		return modelIDs
	}
	filtered := make([]string, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		if strings.TrimSpace(modelID) == "" || pricing.IsModelDisabled(modelID) {
			continue
		}
		filtered = append(filtered, modelID)
	}
	return filtered
}

func filterDisabledGeminiModels(models []geminicli.Model, pricing *service.PricingService) []geminicli.Model {
	if pricing == nil || len(models) == 0 {
		return models
	}
	filtered := make([]geminicli.Model, 0, len(models))
	for _, model := range models {
		if model.ID == "" || pricing.IsModelDisabled(model.ID) {
			continue
		}
		filtered = append(filtered, model)
	}
	return filtered
}

func filterDisabledClaudeModels(models []claude.Model, pricing *service.PricingService) []claude.Model {
	if pricing == nil || len(models) == 0 {
		return models
	}
	filtered := make([]claude.Model, 0, len(models))
	for _, model := range models {
		if model.ID == "" || pricing.IsModelDisabled(model.ID) {
			continue
		}
		filtered = append(filtered, model)
	}
	return filtered
}

func (h *GatewayHandler) isModelDisabled(model string) bool {
	if h == nil {
		return false
	}
	return modelDisabledByPricing(h.pricingService, model)
}

func (h *OpenAIGatewayHandler) isModelDisabled(model string) bool {
	if h == nil {
		return false
	}
	return modelDisabledByPricing(h.pricingService, model)
}
