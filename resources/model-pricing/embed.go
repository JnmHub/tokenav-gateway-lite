package modelpricing

import (
	"bytes"
	_ "embed"
)

//go:embed model_prices_and_context_window.json
var defaultPricing []byte

// DefaultPricing returns an isolated copy because the pricing parser may
// retain or transform the supplied bytes during startup.
func DefaultPricing() []byte {
	return bytes.Clone(defaultPricing)
}
