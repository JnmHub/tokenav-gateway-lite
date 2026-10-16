package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

type reasoningContentCacheTestStore struct {
	contents map[string]string
	ttls     map[string]time.Duration
}

func (c *reasoningContentCacheTestStore) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, nil
}

func (c *reasoningContentCacheTestStore) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}

func (c *reasoningContentCacheTestStore) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (c *reasoningContentCacheTestStore) DeleteSessionAccountID(context.Context, int64, string) error {
	return nil
}

func (c *reasoningContentCacheTestStore) GetGrokVideoPendingBilling(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (c *reasoningContentCacheTestStore) ClaimGrokVideoBilled(context.Context, string, time.Duration) (bool, error) {
	return false, nil
}

func (c *reasoningContentCacheTestStore) SetReasoningContent(_ context.Context, itemID, content string, ttl time.Duration) error {
	if c.contents == nil {
		c.contents = make(map[string]string)
		c.ttls = make(map[string]time.Duration)
	}
	c.contents[itemID] = content
	c.ttls[itemID] = ttl
	return nil
}

func (c *reasoningContentCacheTestStore) GetReasoningContent(_ context.Context, itemID string) (string, error) {
	content, ok := c.contents[itemID]
	if !ok {
		return "", ErrReasoningContentNotFound
	}
	return content, nil
}

func TestOpenAIReasoningContentCacheFlow(t *testing.T) {
	store := &reasoningContentCacheTestStore{}
	service := &OpenAIGatewayService{cache: store}

	service.cacheReasoningItemsFromOutput([]apicompat.ResponsesOutput{{
		Type: "reasoning",
		ID:   "item_output",
		Summary: []apicompat.ResponsesSummary{{
			Type: "summary_text",
			Text: "output thinking",
		}},
	}})
	service.cacheReasoningItemsFromEvents([]apicompat.ResponsesStreamEvent{{
		Type: "response.output_item.done",
		Item: &apicompat.ResponsesOutput{
			Type:    "reasoning",
			ID:      "item_stream",
			Summary: []apicompat.ResponsesSummary{{Type: "summary_text", Text: "stream thinking"}},
		},
	}})
	service.recacheReasoningItemsFromInput(json.RawMessage(`[
		{"type":"reasoning","id":"item_input","summary":[{"type":"summary_text","text":"input thinking"}]},
		{"type":"reasoning","id":"item_encrypted","summary":[],"encrypted_content":"opaque"}
	]`))

	require.Equal(t, "output thinking", store.contents["item_output"])
	require.Equal(t, "stream thinking", store.contents["item_stream"])
	require.Equal(t, "input thinking", store.contents["item_input"])
	require.NotContains(t, store.contents, "item_encrypted")
	for _, itemID := range []string{"item_output", "item_stream", "item_input"} {
		require.Equal(t, responsesReasoningCacheTTL, store.ttls[itemID])
	}
	require.Equal(t, "stream thinking", service.reasoningContentByID("item_stream"))
	require.Empty(t, service.reasoningContentByID("item_missing"))
}
