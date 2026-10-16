package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func delegationFundingContext(t *testing.T, base context.Context) context.Context {
	t.Helper()
	meta, ok := gatewaylite.RequestMetaFromContext(base)
	require.True(t, ok)
	meta.Funding = nil
	ctx := gatewaylite.ContextWithRequestMeta(base, meta)
	ctx, err := gatewaylite.ContextWithOAuthInvocationCredential(ctx, "tokenav_oa_fixture", "gw-test", "sg", fundingDispatchCredential())
	require.NoError(t, err)
	return ctx
}

// This checks the delegated service's real price resolver and persisted initial
// admission. Dispatch and recovery are separately exercised by the gateway SQL tests.
func TestOAuthInvocationTextFundingPersistsActualCardBoundAndBodyProof(t *testing.T) {
	svc, base, control, quota, state := fundingDispatchFixture(t)
	previousControl, previousQuota := gatewaylite.DefaultControlPlaneClientRef(), gatewaylite.DefaultRedisQuota()
	previousRecovery := gatewaylite.DefaultOAuthInvocationRecoveryOutbox()
	gatewaylite.SetDefaultControlPlaneClientRef(control)
	gatewaylite.SetDefaultRedisQuota(quota)
	t.Cleanup(func() {
		gatewaylite.SetDefaultControlPlaneClientRef(previousControl)
		gatewaylite.SetDefaultRedisQuota(previousQuota)
		gatewaylite.SetDefaultOAuthInvocationRecoveryOutbox(previousRecovery)
	})
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "delegation-funding.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	migration, err := migrations.FS.ReadFile("241_gateway_lite_oauth_invocation_recovery.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	gatewaylite.SetDefaultOAuthInvocationRecoveryOutbox(gatewaylite.NewOAuthInvocationRecoveryOutbox(db, control))
	ctx := delegationFundingContext(t, base)
	key := &APIKey{User: &User{ID: 42}, Group: &Group{ID: 7, RateMultiplier: 1, Platform: PlatformOpenAI}}
	body := []byte(`{"model":"bound-upstream","max_output_tokens":100,"n":2,"input":"non-secret-user-body-not-to-persist"}`)
	cardBound, err := svc.FundingOpenAITextUpperBound(ctx, key, body, "bound-request", "bound-upstream")
	require.NoError(t, err)
	ctx, err = svc.EnsureOAuthInvocationTextFunding(ctx, key, body, "bound-request", "/v1/responses", "bound-request", "bound-upstream")
	require.NoError(t, err)
	funding, ok := gatewaylite.FundingFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, cardBound.MicroCents, funding.UpperBoundMicroCents, "delegation and card must use one price authority")
	digest := sha256.Sum256(body)
	require.Equal(t, hex.EncodeToString(digest[:]), funding.BoundEvidence["forwarded_body_sha256"])
	require.EqualValues(t, 100, funding.BoundEvidence["max_output_tokens"])
	require.EqualValues(t, 2, funding.BoundEvidence["completions"])
	var payload string
	var mayDispatch bool
	require.NoError(t, db.QueryRow(`SELECT snapshot_payload,may_dispatch FROM gateway_lite_oauth_invocation_recovery`).Scan(&payload, &mayDispatch))
	require.False(t, mayDispatch)
	require.NotContains(t, payload, "tokenav_oa_fixture")
	require.NotContains(t, payload, "non-secret-user-body-not-to-persist")
	var snapshot gatewaylite.OAuthInvocationRequestSnapshot
	require.NoError(t, json.Unmarshal([]byte(payload), &snapshot))
	require.True(t, snapshot.FundingRequest.ForceAuthorization)
	require.Equal(t, cardBound.MicroCents, snapshot.ReserveRequest.UpperBoundMicroCents)
	require.Equal(t, cardBound.MicroCents, snapshot.FundingRequest.UpperBoundMicroCents)
	state.mu.Lock()
	require.Equal(t, "active", state.reservations["dispatch"].Status)
	require.Equal(t, funding.AuthorizationID, state.reservations["dispatch"].FundingAuthorizationID)
	state.mu.Unlock()
	require.NoError(t, gatewaylite.FinishOAuthInvocationRecovery(ctx))
}

func TestOAuthInvocationTextFundingSQLFailureKeepsOriginalRecoveryContext(t *testing.T) {
	svc, base, control, quota, state := fundingDispatchFixture(t)
	previousControl, previousQuota := gatewaylite.DefaultControlPlaneClientRef(), gatewaylite.DefaultRedisQuota()
	previousRecovery := gatewaylite.DefaultOAuthInvocationRecoveryOutbox()
	gatewaylite.SetDefaultControlPlaneClientRef(control)
	gatewaylite.SetDefaultRedisQuota(quota)
	// Match production's fail-closed initialization: a nonnil outbox with missing
	// SQL cannot let delegated admission silently skip the durable financial fence.
	gatewaylite.SetDefaultOAuthInvocationRecoveryOutbox(gatewaylite.NewOAuthInvocationRecoveryOutbox(nil, control))
	t.Cleanup(func() {
		gatewaylite.SetDefaultControlPlaneClientRef(previousControl)
		gatewaylite.SetDefaultRedisQuota(previousQuota)
		gatewaylite.SetDefaultOAuthInvocationRecoveryOutbox(previousRecovery)
	})
	ctx := delegationFundingContext(t, base)
	body := []byte(`{"model":"bound-request","max_output_tokens":100,"input":"hello"}`)
	ctx, err := svc.EnsureOAuthInvocationTextFunding(ctx, &APIKey{User: &User{ID: 42}, Group: &Group{ID: 7, RateMultiplier: 1, Platform: PlatformOpenAI}}, body, "bound-request", "/v1/responses")
	require.ErrorIs(t, err, gatewaylite.ErrOAuthInvocationUnavailable)
	_, ok := gatewaylite.OAuthInvocationRequestFromContext(ctx)
	require.True(t, ok, "failure must return the original request for its finalizer")
	require.Error(t, gatewaylite.MarkOAuthInvocationExecutionStarted(ctx))
	state.mu.Lock()
	defer state.mu.Unlock()
	require.Empty(t, state.reservations)
	require.Empty(t, state.auth)
}
