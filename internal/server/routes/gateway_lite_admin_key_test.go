package routes

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayLiteAdminKeyAccountProxyPathAllowed(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{
			name:   "list accounts",
			method: http.MethodGet,
			path:   "/api/v1/admin/accounts",
			want:   true,
		},
		{
			name:   "create account",
			method: http.MethodPost,
			path:   "/api/v1/admin/accounts",
			want:   true,
		},
		{
			name:   "update account",
			method: http.MethodPut,
			path:   "/api/v1/admin/accounts/12",
			want:   true,
		},
		{
			name:   "delete account",
			method: http.MethodDelete,
			path:   "/api/v1/admin/accounts/12",
			want:   true,
		},
		{
			name:   "test account",
			method: http.MethodPost,
			path:   "/api/v1/admin/accounts/12/test",
			want:   true,
		},
		{
			name:   "sync upstream models",
			method: http.MethodPost,
			path:   "/api/v1/admin/accounts/12/models/sync-upstream",
			want:   true,
		},
		{
			name:   "duplicate account must not be available to sync key",
			method: http.MethodPost,
			path:   "/api/v1/admin/accounts/12/duplicate",
			want:   false,
		},
		{
			name:   "groups must not be available to sync key",
			method: http.MethodGet,
			path:   "/api/v1/admin/groups",
			want:   false,
		},
		{
			name:   "model prices must not be available to sync key",
			method: http.MethodGet,
			path:   "/api/v1/admin/model-prices",
			want:   false,
		},
		{
			name:   "gateway-lite config must not be available to sync key",
			method: http.MethodGet,
			path:   "/api/v1/admin/gateway-lite/config",
			want:   false,
		},
		{
			name:   "usage must not be available to sync key",
			method: http.MethodGet,
			path:   "/api/v1/admin/usage",
			want:   false,
		},
		{
			name:   "account stats must not be available to sync key",
			method: http.MethodGet,
			path:   "/api/v1/admin/accounts/12/stats",
			want:   false,
		},
		{
			name:   "account data export must not be available to sync key",
			method: http.MethodGet,
			path:   "/api/v1/admin/accounts/data",
			want:   false,
		},
		{
			name:   "list one users image tasks",
			method: http.MethodGet,
			path:   "/api/v1/admin/image-tasks",
			want:   true,
		},
		{
			name:   "get one users image task",
			method: http.MethodGet,
			path:   "/api/v1/admin/image-tasks/95885982-c5bc-4499-850f-f6c3ef65cb32",
			want:   true,
		},
		{
			name:   "read image task capability",
			method: http.MethodGet,
			path:   "/api/v1/admin/image-tasks/capabilities",
			want:   true,
		},
		{
			name:   "cancel one users image task",
			method: http.MethodPost,
			path:   "/api/v1/admin/image-tasks/95885982-c5bc-4499-850f-f6c3ef65cb32/cancel",
			want:   true,
		},
		{
			name:   "download one users image task artifact",
			method: http.MethodGet,
			path:   "/api/v1/admin/image-tasks/95885982-c5bc-4499-850f-f6c3ef65cb32/artifacts/0",
			want:   true,
		},
		{
			name:   "image task replay must remain local admin only",
			method: http.MethodPost,
			path:   "/api/v1/admin/image-tasks/95885982-c5bc-4499-850f-f6c3ef65cb32/replay",
			want:   false,
		},
		{
			name:   "image task cleanup must remain local admin only",
			method: http.MethodPost,
			path:   "/api/v1/admin/image-tasks/cleanup",
			want:   false,
		},
		{
			name:   "image task metrics must remain local admin only",
			method: http.MethodGet,
			path:   "/api/v1/admin/image-tasks/metrics",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gatewayLiteAdminKeyAccountProxyPathAllowed(tt.method, tt.path)
			require.Equal(t, tt.want, got)
		})
	}
}
