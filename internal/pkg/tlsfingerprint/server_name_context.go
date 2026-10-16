package tlsfingerprint

import (
	"context"
	"strings"
)

type serverNameContextKey struct{}

// WithServerName pins the TLS SNI name when a security-sensitive caller dials
// a pre-validated IP address. Keeping this value in the request context avoids
// changing the shared TLS fingerprint profile or its connection-pool policy.
func WithServerName(ctx context.Context, serverName string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	serverName = strings.ToLower(strings.TrimSpace(serverName))
	if serverName == "" {
		return ctx
	}
	return context.WithValue(ctx, serverNameContextKey{}, serverName)
}

func serverNameFromContext(ctx context.Context, fallback string) string {
	if ctx != nil {
		if serverName, ok := ctx.Value(serverNameContextKey{}).(string); ok && strings.TrimSpace(serverName) != "" {
			return strings.TrimSpace(serverName)
		}
	}
	return fallback
}
