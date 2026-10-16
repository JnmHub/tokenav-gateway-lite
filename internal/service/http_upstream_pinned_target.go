package service

import (
	"context"
	"net/netip"
	"strings"
)

type httpUpstreamPinnedTargetContextKey struct{}

// HTTPUpstreamPinnedTarget freezes the already validated DNS result for one
// security-sensitive outbound request. The repository transport must dial one
// of Addresses instead of resolving Host again, closing the DNS-rebinding gap
// between URL validation and the actual socket connection.
type HTTPUpstreamPinnedTarget struct {
	Host      string
	Addresses []netip.Addr
}

func WithHTTPUpstreamPinnedTarget(ctx context.Context, host string, addresses []netip.Addr) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || len(addresses) == 0 {
		return ctx
	}
	copyAddresses := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		if address.IsValid() {
			copyAddresses = append(copyAddresses, address.Unmap())
		}
	}
	if len(copyAddresses) == 0 {
		return ctx
	}
	return context.WithValue(ctx, httpUpstreamPinnedTargetContextKey{}, HTTPUpstreamPinnedTarget{
		Host:      host,
		Addresses: copyAddresses,
	})
}

func HTTPUpstreamPinnedTargetFromContext(ctx context.Context) (HTTPUpstreamPinnedTarget, bool) {
	if ctx == nil {
		return HTTPUpstreamPinnedTarget{}, false
	}
	target, ok := ctx.Value(httpUpstreamPinnedTargetContextKey{}).(HTTPUpstreamPinnedTarget)
	if !ok || strings.TrimSpace(target.Host) == "" || len(target.Addresses) == 0 {
		return HTTPUpstreamPinnedTarget{}, false
	}
	return target, true
}
