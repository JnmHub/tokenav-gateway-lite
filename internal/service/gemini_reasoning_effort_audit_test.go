package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func TestExtractGeminiReasoningEffortAudit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want *string
	}{
		{name: "direct explicit", body: `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}`, want: reasoningEffortAuditPtr("high")},
		{name: "wrapped explicit", body: `{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"minimal"}}}}`, want: reasoningEffortAuditPtr("minimal")},
		{name: "none preserved", body: `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"none"}}}`, want: reasoningEffortAuditPtr("none")},
		{name: "budget only direct", body: `{"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`, want: nil},
		{name: "budget only wrapped", body: `{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}}`, want: nil},
		{name: "non string ignored", body: `{"generationConfig":{"thinkingConfig":{"thinkingLevel":3}}}`, want: nil},
		{name: "unspecified", body: `{"generationConfig":{}}`, want: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := extractGeminiReasoningEffortAudit([]byte(test.body))
			if test.want == nil {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, *test.want, *got)
		})
	}
}

func reasoningEffortAuditPtr(value string) *string { return &value }

type geminiReasoningAuditSequenceUpstream struct {
	statuses []int
	bodies   [][]byte
}

func (u *geminiReasoningAuditSequenceUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.bodies = append(u.bodies, body)
	status := u.statuses[len(u.bodies)-1]
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"retry"}}`)),
	}, nil
}

func (u *geminiReasoningAuditSequenceUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestAntigravityRetryLoopReturnsLastWireReasoningEffort(t *testing.T) {
	upstream := &geminiReasoningAuditSequenceUpstream{statuses: []int{http.StatusInternalServerError, http.StatusOK}}
	service := &AntigravityGatewayService{}
	body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"minimal"}}}}`)
	result, err := service.antigravityRetryLoop(antigravityRetryLoopParams{
		ctx:          context.Background(),
		prefix:       "reasoning-audit",
		account:      &Account{ID: 1, Platform: PlatformAntigravity, Concurrency: 1},
		accessToken:  "test-token",
		action:       "streamGenerateContent",
		body:         body,
		httpUpstream: upstream,
		handleError: func(context.Context, string, *Account, int, http.Header, []byte, string, int64, string, bool) *handleModelRateLimitResult {
			return nil
		},
		requestedModel:  "gemini-test",
		isStickySession: false,
	})
	require.NoError(t, err)
	require.Len(t, upstream.bodies, 2)
	require.NotNil(t, result.reasoningEffort)
	require.Equal(t, "minimal", *result.reasoningEffort)
}
