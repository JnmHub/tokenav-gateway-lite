//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUsageUnrestrictedIncludesWeeklyWindowStart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	weeklyWindowStart := time.Date(2026, time.July, 13, 8, 30, 0, 0, time.UTC)
	subscription := &service.UserSubscription{
		WeeklyWindowStart: &weeklyWindowStart,
		ExpiresAt:         weeklyWindowStart.Add(30 * 24 * time.Hour),
	}
	ctx.Set(string(middleware2.ContextKeySubscription), subscription)

	handler := &GatewayHandler{}
	handler.usageUnrestricted(
		ctx,
		context.Background(),
		&service.APIKey{Group: &service.Group{Name: "测试套餐", SubscriptionType: service.SubscriptionTypeSubscription}},
		middleware2.AuthSubject{},
		nil,
		nil,
		nil,
	)

	var response struct {
		Subscription struct {
			WeeklyWindowStart *time.Time `json:"weekly_window_start"`
		} `json:"subscription"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.NotNil(t, response.Subscription.WeeklyWindowStart)
	require.True(t, weeklyWindowStart.Equal(*response.Subscription.WeeklyWindowStart))
}
