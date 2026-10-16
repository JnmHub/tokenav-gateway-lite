//go:build unit

package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type channelConfigChangeRepo struct {
	mu      sync.Mutex
	channel *service.Channel
}

func (r *channelConfigChangeRepo) Create(_ context.Context, channel *service.Channel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	channel.ID = 41
	channel.CreatedAt = time.Now().UTC()
	channel.UpdatedAt = channel.CreatedAt
	r.channel = channel.Clone()
	return nil
}

func (r *channelConfigChangeRepo) GetByID(_ context.Context, id int64) (*service.Channel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.channel == nil || r.channel.ID != id {
		return nil, service.ErrChannelNotFound
	}
	return r.channel.Clone(), nil
}

func (r *channelConfigChangeRepo) Update(_ context.Context, channel *service.Channel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	channel.UpdatedAt = time.Now().UTC()
	r.channel = channel.Clone()
	return nil
}

func (r *channelConfigChangeRepo) Delete(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.channel != nil && r.channel.ID == id {
		r.channel = nil
	}
	return nil
}

func (r *channelConfigChangeRepo) List(context.Context, pagination.PaginationParams, string, string) ([]service.Channel, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (r *channelConfigChangeRepo) ListAll(context.Context) ([]service.Channel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.channel == nil {
		return nil, nil
	}
	return []service.Channel{*r.channel.Clone()}, nil
}

func (r *channelConfigChangeRepo) ExistsByName(context.Context, string) (bool, error) {
	return false, nil
}

func (r *channelConfigChangeRepo) ExistsByNameExcluding(context.Context, string, int64) (bool, error) {
	return false, nil
}

func (r *channelConfigChangeRepo) GetGroupIDs(context.Context, int64) ([]int64, error) {
	return nil, nil
}

func (r *channelConfigChangeRepo) SetGroupIDs(context.Context, int64, []int64) error {
	return nil
}

func (r *channelConfigChangeRepo) GetChannelIDByGroupID(context.Context, int64) (int64, error) {
	return 0, nil
}

func (r *channelConfigChangeRepo) GetGroupsInOtherChannels(context.Context, int64, []int64) ([]int64, error) {
	return nil, nil
}

func (r *channelConfigChangeRepo) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return nil, nil
}

func (r *channelConfigChangeRepo) ListModelPricing(context.Context, int64) ([]service.ChannelModelPricing, error) {
	return nil, nil
}

func (r *channelConfigChangeRepo) CreateModelPricing(context.Context, *service.ChannelModelPricing) error {
	return nil
}

func (r *channelConfigChangeRepo) UpdateModelPricing(context.Context, *service.ChannelModelPricing) error {
	return nil
}

func (r *channelConfigChangeRepo) DeleteModelPricing(context.Context, int64) error {
	return nil
}

func (r *channelConfigChangeRepo) ReplaceModelPricing(context.Context, int64, []service.ChannelModelPricing) error {
	return nil
}

func TestChannelImageInputPriceMutationsReportModelPriceConfigChanges(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var requestsMu sync.Mutex
	requests := make([]gatewaylite.GatewayConfigChangedRequest, 0, 3)
	notifier := gatewaylite.NewConfigChangeNotifier(handlerConfigChangedClientFunc(func(_ context.Context, req gatewaylite.GatewayConfigChangedRequest) (*gatewaylite.GatewayConfigChangedResponse, error) {
		requestsMu.Lock()
		defer requestsMu.Unlock()
		requests = append(requests, req)
		return &gatewaylite.GatewayConfigChangedResponse{OK: true}, nil
	}), gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{GatewayCode: "test-gw", Region: "test"}), time.Millisecond, 50*time.Millisecond)
	gatewaylite.SetDefaultConfigChangeNotifier(notifier)

	repo := &channelConfigChangeRepo{}
	handler := NewChannelHandler(service.NewChannelService(repo, nil, nil, nil), nil, nil)
	router := gin.New()
	router.POST("/api/v1/admin/channels", handler.Create)
	router.PUT("/api/v1/admin/channels/:id", handler.Update)
	router.DELETE("/api/v1/admin/channels/:id", handler.Delete)

	invoke := func(method, path, body string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	}
	waitForRequests := func(count int) {
		t.Helper()
		require.Eventually(t, func() bool {
			requestsMu.Lock()
			defer requestsMu.Unlock()
			return len(requests) == count
		}, time.Second, 10*time.Millisecond)
	}

	invoke(http.MethodPost, "/api/v1/admin/channels", `{"name":"image-pricing","model_pricing":[{"platform":"openai","models":["gpt-image-1"],"image_input_price":0.000008}]}`)
	waitForRequests(1)
	invoke(http.MethodPut, "/api/v1/admin/channels/41", `{"model_pricing":[]}`)
	waitForRequests(2)
	invoke(http.MethodDelete, "/api/v1/admin/channels/41", ``)
	waitForRequests(3)

	requestsMu.Lock()
	defer requestsMu.Unlock()
	require.Equal(t, []string{"channel_create", "channel_update", "channel_delete"}, []string{requests[0].Action, requests[1].Action, requests[2].Action})
	for _, req := range requests {
		require.Equal(t, "model_prices", req.Resource)
		require.EqualValues(t, 41, req.ResourceID)
		require.Equal(t, "test-gw", req.GatewayCode)
	}
}
