package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestImageTaskArtifactExtractionAndLimits(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\ncontent")
	payload, err := json.Marshal(map[string]any{"data": []any{
		map[string]any{"b64_json": base64.StdEncoding.EncodeToString(png)},
		map[string]any{"image": map[string]any{"b64_json": base64.StdEncoding.EncodeToString(png)}},
	}})
	require.NoError(t, err)
	values := collectImageTaskOutputValues(payload)
	require.Len(t, values, 2)

	decoded, contentType, err := decodeImageTaskBase64(values[0].value, int64(len(png)))
	require.NoError(t, err)
	require.Equal(t, png, decoded)
	require.Empty(t, contentType)
	_, _, err = decodeImageTaskBase64(values[0].value, int64(len(png)-1))
	require.Error(t, err)
}

func TestImageTaskDownloadBlocksPrivateAndSpecialNetworks(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "224.0.0.1", "0.0.0.0", "100.64.0.1", "::1", "fc00::1"}
	for _, raw := range blocked {
		require.True(t, imageTaskIPBlocked(net.ParseIP(raw)), raw)
	}
	require.False(t, imageTaskIPBlocked(net.ParseIP("1.1.1.1")))
	client := newImageTaskDownloadClient(1)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://127.0.0.1/private.png", nil)
	require.NoError(t, err)
	_, err = client.Do(req)
	require.ErrorContains(t, err, "blocked addresses")
}

func TestImageTaskDownloadRedirectPolicyRejectsUnsafeTargets(t *testing.T) {
	client := newImageTaskDownloadClient(1)
	unsafe, err := http.NewRequest(http.MethodGet, "http://example.com/image.png", nil)
	require.NoError(t, err)
	require.Error(t, client.CheckRedirect(unsafe, nil))
	withCredentials := &http.Request{URL: &url.URL{Scheme: "https", Host: "example.com", User: url.UserPassword("user", "secret")}}
	require.Error(t, client.CheckRedirect(withCredentials, nil))
	safe, err := http.NewRequest(http.MethodGet, "https://example.com/image.png", nil)
	require.NoError(t, err)
	via := []*http.Request{safe, safe, safe}
	require.ErrorContains(t, client.CheckRedirect(safe, via), "too many")
}

func TestImageTaskResponseWriterIsBounded(t *testing.T) {
	w := newBoundedImageTaskWriter(4)
	n, err := w.Write([]byte("1234"))
	require.NoError(t, err)
	require.Equal(t, 4, n)
	_, err = w.Write([]byte("5"))
	require.ErrorIs(t, err, errImageTaskResponseTooLarge)
	require.True(t, w.overflow)
	require.Equal(t, "1234", w.body.String())
}

func TestImageTaskPublicDTOHidesPrivateSnapshotsAndObjectKeys(t *testing.T) {
	now := time.Now().UTC()
	task := &service.ImageTask{
		ID: "task", State: service.ImageTaskStateFailed, SettlementState: service.ImageTaskSettlementReleased,
		Model: "gpt-image-1", ImageCount: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
		EncryptedRequest: "ciphertext-secret", PolicySnapshot: []byte(`{"oauth_token":"secret"}`),
		BillingSnapshot: []byte(`{"private":true}`), ExecutionSnapshot: []byte(`{"account":99}`),
		Artifacts: []service.ImageTaskArtifact{{ObjectKey: "private/object/key", FileName: "image.png", ContentType: "image/png", SizeBytes: 10, SHA256: "abc"}},
	}
	dto, err := imageTaskToDTO(nil, nil, task, "admin")
	require.NoError(t, err)
	payload, err := json.Marshal(dto)
	require.NoError(t, err)
	text := string(payload)
	for _, forbidden := range []string{"ciphertext-secret", "oauth_token", "private/object/key", "PolicySnapshot", "ExecutionSnapshot"} {
		require.False(t, strings.Contains(text, forbidden), text)
	}
	require.Contains(t, text, "image.png")
}

func TestImageTaskAllowedModelPatterns(t *testing.T) {
	require.True(t, imageTaskModelAllowed("gpt-image-1", nil))
	require.True(t, imageTaskModelAllowed("gpt-image-1", []string{"gpt-image-*"}))
	require.False(t, imageTaskModelAllowed("gpt-image-1", []string{"dall-e-3"}))
}

func TestImageTaskGatewayTierRevalidationUsesCurrentRequirement(t *testing.T) {
	minimum := 1
	require.False(t, imageTaskGatewayTierAllowed(0, 0, &minimum, 0))
	require.False(t, imageTaskGatewayTierAllowed(0, 1, &minimum, 2), "effective snapshot tier must win")
	require.True(t, imageTaskGatewayTierAllowed(0, 2, &minimum, 2))
	require.True(t, imageTaskGatewayTierAllowed(88, 0, &minimum, 2), "enterprise authorization must not inherit employee tier")
}

func TestImageTaskLocalRevalidationRejectsCurrentTierAndModelChanges(t *testing.T) {
	groupID := int64(701)
	group := &service.Group{
		ID: groupID, Status: service.StatusActive, Platform: service.PlatformOpenAI,
		AllowImageGeneration: true, EffectiveRequiredTier: 1,
	}
	key := &service.APIKey{
		ID: 700, UserID: 699, Status: service.StatusAPIKeyActive, GroupID: &groupID,
		User: &service.User{ID: 699, Tier: 0, Status: service.StatusActive}, Group: group,
		AllowedModels: []string{"gpt-image-1"},
	}
	repo := &imageTaskAPIKeyRepoStub{key: key}
	executor := &GatewayImageTaskExecutor{apiKeys: service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, nil)}
	task := &service.ImageTask{ID: "task", UserID: key.User.ID, APIKeyID: key.ID, Model: "gpt-image-1"}
	policy := &service.ImageTaskPolicySnapshot{
		User:  service.ImageTaskUserSnapshot{ID: key.User.ID},
		Group: service.ImageTaskGroupSnapshot{ID: group.ID},
	}

	_, err := executor.revalidateKey(context.Background(), task, policy)
	require.ErrorContains(t, err, "tier")

	key.User.Tier = 2
	key.AllowedModels = []string{"dall-e-3"}
	_, err = executor.revalidateKey(context.Background(), task, policy)
	require.ErrorContains(t, err, "model")

	key.AllowedModels = []string{"gpt-image-*"}
	_, err = executor.revalidateKey(context.Background(), task, policy)
	require.NoError(t, err)
}

type imageTaskAPIKeyRepoStub struct {
	service.APIKeyRepository
	key *service.APIKey
}

func (r *imageTaskAPIKeyRepoStub) GetByID(context.Context, int64) (*service.APIKey, error) {
	return r.key, nil
}
