package gatewaylite

import "testing"

func TestOAuthInvocationEndpointSupportedExactWebSocketAliases(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{name: "v1_exact", endpoint: "/v1/responses", want: true},
		{name: "root_exact", endpoint: "/responses", want: true},
		{name: "codex_exact", endpoint: "/backend-api/codex/responses", want: true},
		// Adding an existing ingress alias must not grant delegation to adjacent
		// protocols, subpaths or lookalike routes through a broad prefix match.
		{name: "codex_compact", endpoint: "/backend-api/codex/responses/compact"},
		{name: "codex_input_tokens", endpoint: "/backend-api/codex/responses/input_tokens"},
		{name: "codex_ws_turn_subpath", endpoint: "/backend-api/codex/responses/ws-turn"},
		{name: "codex_suffix", endpoint: "/backend-api/codex/responses-extra"},
		{name: "codex_other_prefix", endpoint: "/other/backend-api/codex/responses"},
		{name: "codex_case_mismatch", endpoint: "/backend-api/Codex/responses"},
		{name: "codex_realtime", endpoint: "/backend-api/codex/realtime/calls"},
		{name: "v1_compact", endpoint: "/v1/responses/compact"},
		{name: "root_compact", endpoint: "/responses/compact"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OAuthInvocationEndpointSupported(tc.endpoint); got != tc.want {
				t.Errorf("OAuthInvocationEndpointSupported(%q) = %t, want %t", tc.endpoint, got, tc.want)
			}
		})
	}
}
