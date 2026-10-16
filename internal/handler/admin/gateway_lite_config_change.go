package admin

import "github.com/Wei-Shaw/sub2api/internal/gatewaylite"

func reportGatewayLiteConfigChange(resource, action string, resourceID int64) {
	gatewaylite.NotifyDefaultConfigChanged(resource, action, resourceID)
}
