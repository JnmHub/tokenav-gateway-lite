package domain

// GroupModelsListConfig controls the optional custom /v1/models response list.
type GroupModelsListConfig struct {
	Enabled             bool     `json:"enabled"`
	Models              []string `json:"models,omitempty"`
	ResponseModelMode   string   `json:"response_model_mode,omitempty"`
	ResponseModelCustom string   `json:"response_model_custom,omitempty"`
}
