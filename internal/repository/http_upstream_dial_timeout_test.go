package repository

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildUpstreamTransportSetsDialTimeout(t *testing.T) {
	transport, err := buildUpstreamTransport(defaultPoolSettings(nil), nil, upstreamProtocolModeDefault)
	require.NoError(t, err)
	require.NotNil(t, transport.DialContext)
	require.Equal(t, 10*time.Second, transport.TLSHandshakeTimeout)
}

func TestNewUpstreamDialerHasBoundedTimeout(t *testing.T) {
	dialer := newUpstreamDialer()
	require.Equal(t, 10*time.Second, dialer.Timeout)
	require.Equal(t, 30*time.Second, dialer.KeepAlive)
}
