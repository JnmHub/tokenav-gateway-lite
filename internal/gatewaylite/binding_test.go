package gatewaylite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type bindingMemoryStore struct {
	mu   sync.Mutex
	r    BindingRecord
	fail bool
}

func (s *bindingMemoryStore) Load(context.Context) (BindingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneBindingRecord(s.r), nil
}
func (s *bindingMemoryStore) Save(_ context.Context, r BindingRecord, expected int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("storage unavailable")
	}
	if s.r.Revision != expected {
		return ErrBindingConflict
	}
	s.r = cloneBindingRecord(r)
	return nil
}

type bindingFakeRemote struct {
	verifyErr, registerErr, handshakeErr, disconnectErr error
	registered                                          []BindingDestination
	disconnected                                        []BindingDestination
	counts                                              map[string]int64
	detached                                            bool
	operation                                           string
	afterDisconnect                                     func()
}

func (r *bindingFakeRemote) Probe(context.Context, string) error          { return nil }
func (r *bindingFakeRemote) Verify(context.Context, string, string) error { return r.verifyErr }
func (r *bindingFakeRemote) Register(_ context.Context, _ string, d BindingDestination) (int64, error) {
	r.registered = append(r.registered, d)
	return 4, r.registerErr
}
func (r *bindingFakeRemote) Handshake(context.Context, BindingDestination) (int64, error) {
	return 4, r.handshakeErr
}
func (r *bindingFakeRemote) Drain(context.Context, BindingDestination) (map[string]int64, error) {
	return r.counts, r.handshakeErr
}
func (r *bindingFakeRemote) Disconnect(_ context.Context, d BindingDestination, operation string) error {
	r.disconnected = append(r.disconnected, d)
	r.detached = true
	r.operation = operation
	if r.afterDisconnect != nil {
		r.afterDisconnect()
	}
	return r.disconnectErr
}
func (r *bindingFakeRemote) DisconnectStatus(_ context.Context, _ BindingDestination, operation string) (bool, error) {
	return r.detached && r.operation == operation, nil
}
func newBindingTestManager(t *testing.T) (*BindingManager, *bindingMemoryStore, *bindingFakeRemote) {
	t.Helper()
	s := &bindingMemoryStore{}
	m, err := NewBindingManager(context.Background(), s, config.GatewayLiteConfig{})
	require.NoError(t, err)
	remote := &bindingFakeRemote{counts: map[string]int64{"funding": 0}}
	m.remote = remote
	m.restart = func() bool { return true }
	m.drain = func(context.Context, BindingRecord) (BindingDrain, error) {
		return BindingDrain{Counts: map[string]int64{}}, nil
	}
	EnableBindingRestart(true)
	t.Cleanup(func() { EnableBindingRestart(false) })
	return m, s, remote
}
func bindForTest(t *testing.T, m *BindingManager) BindingDestination {
	t.Helper()
	status, err := m.Register(context.Background(), BindingInput{URL: "https://one.invalid", Token: "ephemeral", Name: "节点", PublicURL: "https://gateway.invalid", Region: "hk"})
	require.NoError(t, err)
	require.Equal(t, "activating", status.Phase)
	require.NoError(t, m.Activate(context.Background(), func(context.Context) error { return nil }))
	return *m.Record().Current
}
func TestBindingFirstBindingPersistenceAndSensitiveStatus(t *testing.T) {
	m, s, _ := newBindingTestManager(t)
	require.True(t, m.Status().FirstBindingRequired)
	_, ok := m.EnterRequest(context.Background(), false)
	require.False(t, ok)
	status, err := m.Register(context.Background(), BindingInput{URL: "https://one.invalid", Token: "only-this-call", Name: "节点", PublicURL: "https://gateway.invalid", Region: "hk"})
	require.NoError(t, err)
	require.Equal(t, "activating", status.Phase)
	require.Zero(t, status.HandshakeAt)
	r := m.Record()
	raw, err := json.Marshal(r)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "only-this-call")
	wire, _ := json.Marshal(status)
	require.NotContains(t, string(wire), r.Pending.Credential)
	require.NotContains(t, string(wire), r.Pending.AdminSyncKey)
	restored, err := NewBindingManager(context.Background(), s, config.GatewayLiteConfig{ControlPlaneURL: "https://stale-env.invalid", ControlPlaneToken: "stale-secret"})
	require.NoError(t, err)
	cfg := config.GatewayLiteConfig{}
	restored.ApplyStartup(&cfg)
	require.Equal(t, r.Pending.URL, cfg.ControlPlaneURL)
	require.Equal(t, r.Pending.RedisPrefix, cfg.RedisPrefix)
	require.Equal(t, r.Pending.PublicURL, cfg.MediaProxyPublicURL)
	restored.remote = m.remote
	require.NoError(t, restored.Activate(context.Background(), func(context.Context) error { return nil }))
	require.Equal(t, "bound", restored.Status().Phase)
	require.Positive(t, restored.Status().SnapshotAt)
}
func TestBindingFailuresNeverClaimBoundOrChangeOldDestination(t *testing.T) {
	m, _, remote := newBindingTestManager(t)
	remote.verifyErr = errors.New("unauthorized")
	_, err := m.Register(context.Background(), BindingInput{URL: "https://one.invalid", Token: "bad", Name: "n", PublicURL: "https://gateway.invalid", Region: "hk"})
	require.Error(t, err)
	require.Nil(t, m.Record().Pending)
	require.Equal(t, "unbound", m.Status().Phase)
	remote.verifyErr = nil
	remote.registerErr = errors.New("network")
	in := BindingInput{URL: "https://one.invalid", Token: "only-memory", Name: "n", PublicURL: "https://gateway.invalid", Region: "hk"}
	_, err = m.Register(context.Background(), in)
	require.Error(t, err)
	first := m.Record().Pending.BindingID
	require.Nil(t, m.Record().Current)
	remote.registerErr = nil
	_, err = m.Register(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, first, m.Record().Pending.BindingID)
	remote.handshakeErr = errors.New("offline")
	require.Error(t, m.Activate(context.Background(), func(context.Context) error { t.Fatal("snapshot must not run before handshake"); return nil }))
	require.Equal(t, "activating", m.Status().Phase)
	remote.handshakeErr = nil
	require.Error(t, m.Activate(context.Background(), func(context.Context) error { return errors.New("cache failure") }))
	require.Zero(t, m.Status().SnapshotAt)
	require.NoError(t, m.Activate(context.Background(), func(context.Context) error { return nil }))
	old := m.Record().Current
	in.URL = "https://other.invalid"
	_, err = m.Register(context.Background(), in)
	require.ErrorIs(t, err, ErrBindingConflict)
	require.Equal(t, *old, *m.Record().Current)
}
func TestBindingDrainRejectsInflightQueuesUnknownAndOfflineThenCancel(t *testing.T) {
	for _, scenario := range []string{"inflight", "queue", "unknown", "offline", "query_error", "media"} {
		t.Run(scenario, func(t *testing.T) {
			m, _, remote := newBindingTestManager(t)
			old := bindForTest(t, m)
			leave := func() {}
			switch scenario {
			case "inflight":
				var ok bool
				leave, ok = m.EnterRequest(context.Background(), false)
				require.True(t, ok)
			case "queue":
				m.drain = func(context.Context, BindingRecord) (BindingDrain, error) {
					return BindingDrain{Counts: map[string]int64{"dead": 1}}, nil
				}
			case "unknown":
				m.drain = func(context.Context, BindingRecord) (BindingDrain, error) {
					return BindingDrain{Counts: map[string]int64{}, Unknown: []string{"unknown media"}}, nil
				}
			case "offline":
				remote.handshakeErr = errors.New("offline")
			case "query_error":
				m.drain = func(context.Context, BindingRecord) (BindingDrain, error) {
					return BindingDrain{Counts: map[string]int64{}}, errors.New("database failed")
				}
			case "media":
				var ok bool
				leave, ok = m.EnterRequest(context.Background(), true)
				require.True(t, ok)
			}
			_, err := m.Disconnect(context.Background())
			require.Error(t, err)
			require.Equal(t, "blocked", m.Status().Phase)
			require.Equal(t, old, *m.Record().Current)
			require.Empty(t, remote.disconnected)
			_, ok := m.EnterRequest(context.Background(), false)
			require.False(t, ok)
			leave()
			remote.handshakeErr = nil
			require.NoError(t, m.Cancel(context.Background()))
			require.Equal(t, "bound", m.Status().Phase)
		})
	}
}
func TestBindingRebindAfterDrainZeroUsesDistinctNamespaceAndFailureRetry(t *testing.T) {
	m, s, remote := newBindingTestManager(t)
	old := bindForTest(t, m)
	_, err := m.Disconnect(context.Background())
	require.NoError(t, err)
	require.Equal(t, old, remote.disconnected[0])
	require.Nil(t, m.Record().Current)
	restore, err := NewBindingManager(context.Background(), s, config.GatewayLiteConfig{ControlPlaneURL: old.URL, ControlPlaneToken: old.Credential})
	require.NoError(t, err)
	cfg := config.GatewayLiteConfig{}
	restore.ApplyStartup(&cfg)
	require.Empty(t, cfg.ControlPlaneURL)
	remote.registerErr = errors.New("offline")
	in := BindingInput{URL: "https://other.invalid", Token: "new", Name: "n", PublicURL: "https://gateway.invalid", Region: "jp"}
	_, err = m.Register(context.Background(), in)
	require.Error(t, err)
	require.True(t, m.Status().FirstBindingRequired)
	require.Nil(t, m.Record().Current)
	require.NotEqual(t, old.RedisPrefix, m.Record().Pending.RedisPrefix)
	remote.registerErr = nil
	_, err = m.Register(context.Background(), in)
	require.NoError(t, err)
	require.NoError(t, m.Activate(context.Background(), func(context.Context) error { return nil }))
	require.Equal(t, in.URL, m.Record().Current.URL)
	require.NotEqual(t, old.Credential, m.Record().Current.Credential)
}
func TestBindingPersistenceFailureStopsDrainAndLegacyDoesNotClaimHandshake(t *testing.T) {
	m, s, _ := newBindingTestManager(t)
	bindForTest(t, m)
	s.fail = true
	_, err := m.Disconnect(context.Background())
	require.Error(t, err)
	require.Equal(t, "bound", m.Status().Phase)
	legacyStore := &bindingMemoryStore{}
	legacy, err := NewBindingManager(context.Background(), legacyStore, config.GatewayLiteConfig{ControlPlaneURL: "https://legacy.invalid", Region: "hk"})
	require.NoError(t, err)
	require.Equal(t, "activating", legacy.Status().Phase)
	require.Zero(t, legacy.Status().HandshakeAt)
	leave, ok := legacy.EnterRequest(context.Background(), false)
	require.True(t, ok)
	leave()
	require.False(t, m.IdentityAllowed(config.GatewayLiteConfig{ControlPlaneURL: "https://other.invalid"}))
}
func TestBindingWorkerCancelJoinAndRefuseNewGeneration(t *testing.T) {
	var lifetime workerLifetime
	started := make(chan struct{})
	exited := make(chan struct{})
	lifetime.Go(context.Background(), func(ctx context.Context) { close(started); <-ctx.Done(); close(exited) })
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, lifetime.Stop(ctx))
	<-exited
	lifetime.Go(context.Background(), func(context.Context) { t.Error("work started after stop") })
}
func TestBindingLostDisconnectReplyAndLocalSaveFailureRecoverAfterRestart(t *testing.T) {
	for _, scenario := range []string{"lost_reply", "save_failure"} {
		t.Run(scenario, func(t *testing.T) {
			m, s, remote := newBindingTestManager(t)
			old := bindForTest(t, m)
			if scenario == "lost_reply" {
				remote.disconnectErr = errors.New("response lost")
			} else {
				remote.afterDisconnect = func() { s.fail = true }
			}
			_, err := m.Disconnect(context.Background())
			require.Error(t, err)
			require.Equal(t, old, *m.Record().Current)
			require.NotEmpty(t, m.Record().DisconnectOperation)
			require.Error(t, m.Cancel(context.Background()))
			require.NotEqual(t, "bound", m.Status().Phase)
			s.fail = false
			restored, err := NewBindingManager(context.Background(), s, config.GatewayLiteConfig{})
			require.NoError(t, err)
			restored.remote = remote
			restored.restart = func() bool { return true }
			restored.drain = m.drain
			_, err = restored.Disconnect(context.Background())
			require.NoError(t, err)
			require.Equal(t, "unbound", restored.Status().Phase)
			require.Nil(t, restored.Record().Current)
			require.Len(t, remote.disconnected, 1)
		})
	}
}
func TestBindingOldGenerationCannotConfirmNewPending(t *testing.T) {
	m, _, _ := newBindingTestManager(t)
	old := bindForTest(t, m)
	_, err := m.Disconnect(context.Background())
	require.NoError(t, err)
	_, err = m.Register(context.Background(), BindingInput{URL: "https://other.invalid", Token: "new", Name: "n", PublicURL: "https://gateway.invalid", Region: "jp"})
	require.NoError(t, err)
	require.NoError(t, m.activateGeneration(context.Background(), old.BindingID, func(context.Context) error { t.Fatal("old snapshot ran for new binding"); return nil }))
	require.Equal(t, "activating", m.Status().Phase)
	require.Zero(t, m.Status().SnapshotAt)
}
func TestBindingPublicURLUsesMediaStartupValidation(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:18083", "http://192.168.1.43:18083", "http://[::1]:18083", "https://gateway.invalid/base"} {
		in := BindingInput{URL: "http://127.0.0.1:18081", Token: "fixture", Name: "节点", PublicURL: raw, Region: "local"}
		require.NoError(t, validateBindingInput(in))
		require.NoError(t, config.ValidateGatewayLiteMediaProxyPublicURL(in.PublicURL))
	}
	for _, raw := range []string{"http://:18083", "http://127.0.0.1:99999", "http://127.0.0.1:0", "https://gateway.invalid?", "https://user:pass@gateway.invalid", "https://gateway.invalid/#fragment", "https://gateway.invalid/" + strings.Repeat("x", 512)} {
		m, _, remote := newBindingTestManager(t)
		_, err := m.Register(context.Background(), BindingInput{URL: "http://127.0.0.1:18081", Token: "fixture", Name: "节点", PublicURL: raw, Region: "local"})
		require.ErrorContains(t, err, "binding_public_url_invalid", raw)
		require.Empty(t, remote.registered)
		require.Nil(t, m.Record().Pending)
		require.Equal(t, "unbound", m.Status().Phase)
	}
}

func TestBindingURLAndSensitiveWire(t *testing.T) {
	for _, raw := range []string{"ftp://host", "https://user:pass@host", "https://host?q=secret", ""} {
		_, err := bindingURL(raw)
		require.Error(t, err)
	}
	_, err := bindingURL("http://127.0.0.1:1234")
	require.NoError(t, err)
	m, _, _ := newBindingTestManager(t)
	d := bindForTest(t, m)
	data, _ := json.Marshal(m.Status())
	require.False(t, strings.Contains(string(data), d.Credential))
	require.False(t, strings.Contains(string(data), d.AdminSyncKey))
}
