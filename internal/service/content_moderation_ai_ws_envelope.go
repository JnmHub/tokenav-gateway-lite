package service

import "strings"

const contentModerationAIResponsesWSDomain = "responses-ws-create-v1"

// The caller must opt in from the server's WebSocket ingress. Validate the
// complete parsed envelope before exempting only its two transport fields from
// the ordinary HTTP field allowlist; never mutate the tree used for review/hash.
func contentModerationAIWSBusinessFields(protocol string, root map[string]any) (map[string]any, error) {
	if protocol != ContentModerationProtocolOpenAIResponses {
		return nil, aiError("ai_unsupported_protocol", 503)
	}
	if typ, ok := root["type"].(string); !ok || typ != "response.create" {
		return nil, aiError("ai_invalid_ws_envelope", 503)
	}
	if value, exists := root["event_id"]; exists {
		eventID, ok := value.(string)
		if !ok || len(eventID) > 512 {
			return nil, aiError("ai_invalid_ws_envelope", 503)
		}
	}
	business := make(map[string]any, len(root))
	for key, value := range root {
		if strings.EqualFold(key, "type") || strings.EqualFold(key, "event_id") {
			// Case aliases must not acquire transport privileges through the
			// case-insensitive lookup used by ordinary business input parsing.
			if key != "type" && key != "event_id" {
				return nil, aiError("ai_invalid_ws_envelope", 503)
			}
			continue
		}
		business[key] = value
	}
	return business, nil
}
