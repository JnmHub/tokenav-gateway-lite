package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// decodeOpenAIJSONUseNumber decodes request payloads without converting large
// JSON numbers to float64. Responses WebSocket frames may contain sequence
// identifiers larger than JavaScript's safe integer range; preserving them is
// required when a compatibility pass rewrites the surrounding payload.
func decodeOpenAIJSONUseNumber(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
