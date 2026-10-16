package server

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProvideHTTPServerAppliesConfiguredHeaderLimit(t *testing.T) {
	gateway := gin.New()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:               "127.0.0.1",
			Port:               8080,
			MaxHeaderBytes:     8 * 1024,
			MaxRequestBodySize: 1024,
		},
		Gateway: config.GatewayConfig{MaxBodySize: 1024, TextMaxBodySize: 512},
	}

	server := ProvideHTTPServer(cfg, gateway)
	require.Equal(t, 8*1024, server.MaxHeaderBytes)
}
