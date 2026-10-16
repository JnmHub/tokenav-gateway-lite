package service

import (
	"context"
	"net/netip"
	"testing"
)

func TestHTTPUpstreamPinnedTargetContextCopiesValidatedAddresses(t *testing.T) {
	addresses := []netip.Addr{netip.MustParseAddr("203.0.113.10")}
	ctx := WithHTTPUpstreamPinnedTarget(context.Background(), " API.EXAMPLE.TEST ", addresses)
	addresses[0] = netip.MustParseAddr("127.0.0.1")
	target, ok := HTTPUpstreamPinnedTargetFromContext(ctx)
	if !ok || target.Host != "api.example.test" || len(target.Addresses) != 1 || target.Addresses[0].String() != "203.0.113.10" {
		t.Fatalf("unexpected pinned target: %+v ok=%v", target, ok)
	}
}
