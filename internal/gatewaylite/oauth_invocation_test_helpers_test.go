package gatewaylite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const oauthInvocationTestToken = "tokenav_oa_0123456789012345678901234567890123456789012"

type oauthInvocationTestServer struct {
	mu                  sync.Mutex
	resolved            OAuthInvocationResolveResponse
	reservations        map[string]OAuthInvocationReservation
	funding             map[string]FundingAuthorization
	operations          map[string]string
	fundingRevisions    map[string]map[int64]int64
	delegationRevisions map[string]map[int64]int64
	calls               []string
	failBefore          map[string]int
	failAfter           map[string]int
	deny                map[string]string
	currentPath         string
	revoked             bool
	maximum             int64
	spent               int64
}

func newOAuthInvocationTestServer(t *testing.T) (*oauthInvocationTestServer, *ControlPlaneClientRef) {
	t.Helper()
	s := &oauthInvocationTestServer{reservations: map[string]OAuthInvocationReservation{}, funding: map[string]FundingAuthorization{},
		operations: map[string]string{}, fundingRevisions: map[string]map[int64]int64{}, delegationRevisions: map[string]map[int64]int64{},
		failBefore: map[string]int{}, failAfter: map[string]int{}, deny: map[string]string{}, maximum: 1_000_000_000}
	s.resolved = OAuthInvocationResolveResponse{OK: true, DelegationID: "delegation-test", ExpiresAtMillis: time.Now().Add(time.Hour).UnixMilli(),
		Key: &KeySnapshot{KeyID: "od_test", UserID: 42, Status: "active", BillingSubjectType: "user", BillingSubjectID: 42,
			KeyDefaultGatewayNodeID: 5, GroupID: 3, Group: &GatewayGroupSnapshot{ID: 50}, AllowedModels: []string{"model-test"}},
		Invocation: &OAuthInvocation{GatewayNodeID: 5, GroupID: 50, Models: []string{"model-test"}, MaxCostCents: 1000,
			ExpiresIn: 3600, RateLimitRPM: 1000, Concurrency: 100, CostConsent: true}}
	server := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(server.Close)
	client, err := NewControlPlaneClient(server.URL, "test-internal", 2*time.Second)
	require.NoError(t, err)
	return s, NewControlPlaneClientRef(client)
}

func (s *oauthInvocationTestServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer test-internal" {
		w.WriteHeader(401)
		return
	}
	path := r.URL.Path
	s.currentPath = path
	s.calls = append(s.calls, path)
	if code := s.deny[path]; code != "" {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": code})
		return
	}
	if s.failBefore[path] > 0 {
		s.failBefore[path]--
		w.WriteHeader(503)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	out, code, err := s.apply(path, body)
	if err != "" {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err})
		return
	}
	if s.failAfter[path] > 0 {
		s.failAfter[path]--
		w.WriteHeader(503)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (s *oauthInvocationTestServer) operation(id, requestID string, body []byte) (bool, bool) {
	if id == "" {
		return false, false
	}
	hash := sha256.Sum256(body)
	value := hex.EncodeToString(hash[:])
	key := s.currentPath + ":" + requestID + ":" + id
	if existing, ok := s.operations[key]; ok {
		return existing == value, existing == value
	}
	return false, true
}
func (s *oauthInvocationTestServer) saveOperation(id, requestID string, body []byte) {
	hash := sha256.Sum256(body)
	s.operations[s.currentPath+":"+requestID+":"+id] = hex.EncodeToString(hash[:])
}

func (s *oauthInvocationTestServer) apply(path string, body []byte) (any, int, string) {
	denied := func(code string) (any, int, string) { return nil, 403, code }
	reservationResult := func(a OAuthInvocationReservation) (any, int, string) {
		return OAuthInvocationReservationResponse{OK: true, Reservation: &a}, 200, ""
	}
	fundingResult := func(a FundingAuthorization) (any, int, string) {
		return FundingAuthorizationResponse{OK: true, Disposition: a.Disposition, Authorization: &a}, 200, ""
	}
	switch path {
	case "/internal/oauth/invocations/resolve":
		var in OAuthInvocationResolveRequest
		_ = json.Unmarshal(body, &in)
		if in.InvocationToken != oauthInvocationTestToken || in.GatewayCode != "node-test" || s.revoked || s.resolved.ExpiresAtMillis <= time.Now().UnixMilli() {
			return denied("invalid_token")
		}
		return cloneOAuthInvocationResolve(&s.resolved), 200, ""
	case "/internal/oauth/invocations/status":
		var in OAuthInvocationStatusRequest
		_ = json.Unmarshal(body, &in)
		a, ok := s.reservations[in.RequestID]
		if !ok || a.GatewayCode != in.GatewayCode || (in.DelegationID != "" && a.DelegationID != in.DelegationID) || (in.ReservationID != "" && a.ReservationID != in.ReservationID) {
			return nil, 404, "not_found"
		}
		return reservationResult(a)
	case "/internal/oauth/invocations/reserve":
		var in OAuthInvocationReserveRequest
		_ = json.Unmarshal(body, &in)
		if a, ok := s.reservations[in.RequestID]; ok {
			if !sameOAuthInvocationReserve(in, a) || in.UpperBoundMicroCents != a.UpperBoundMicroCents {
				return denied("request_conflict")
			}
			return reservationResult(a)
		}
		if s.revoked || s.resolved.ExpiresAtMillis <= time.Now().UnixMilli() {
			return denied("invalid_token")
		}
		used, count := s.spent, 0
		for _, a := range s.reservations {
			if a.Status == "active" || a.Status == "reserved" {
				used += a.UpperBoundMicroCents - a.ActualMicroCents
				count++
			}
		}
		if used+in.UpperBoundMicroCents > s.maximum {
			return denied("insufficient_scope")
		}
		if count >= s.resolved.Invocation.Concurrency || len(s.reservations) >= s.resolved.Invocation.RateLimitRPM {
			return denied("slow_down")
		}
		if in.DelegationID != s.resolved.DelegationID || in.KeyID != s.resolved.Key.KeyID || in.EntryGatewayNodeID != 5 || in.EntryGroupID != 50 || in.RequestedModel != "model-test" || in.GatewayCode != "node-test" || in.ExecutionDeadlineMillis > s.resolved.ExpiresAtMillis {
			return denied("scope_conflict")
		}
		a := OAuthInvocationReservation{ReservationID: "r-" + in.RequestID, DelegationID: in.DelegationID, RequestID: in.RequestID, GatewayCode: in.GatewayCode, KeyID: in.KeyID,
			EntryGatewayNodeID: in.EntryGatewayNodeID, EntryGroupID: in.EntryGroupID, RequestedModel: in.RequestedModel, Endpoint: in.Endpoint,
			UpperBoundMicroCents: in.UpperBoundMicroCents, ExecutionDeadlineMillis: in.ExecutionDeadlineMillis, Status: "reserved", Version: 1}
		s.reservations[in.RequestID] = a
		return reservationResult(a)
	case "/internal/billing/authorize":
		var in AuthorizeFundingRequest
		_ = json.Unmarshal(body, &in)
		if a, ok := s.funding[in.RequestID]; ok {
			return fundingResult(a)
		}
		if !in.ForceAuthorization || in.BoundKind != "bounded" || in.BoundEvidence["kind"] == nil || in.BoundEvidence["price_source"] == nil {
			return denied("funding_bound_required")
		}
		a := FundingAuthorization{AuthorizationID: "f-" + in.RequestID, RequestID: in.RequestID, KeyID: in.KeyID, UserID: in.UserID,
			BillingSubjectType: "user", BillingSubjectID: in.UserID, GatewayCode: in.GatewayCode, Region: in.Region,
			EntryGatewayNodeID: in.EntryGatewayNodeID, EntryGroupID: in.EntryGroupID, RequestedModel: in.RequestedModel, Endpoint: in.Endpoint,
			Version: 2, Status: "active", Disposition: "authorized", UpperBoundMicroCents: in.UpperBoundMicroCents, ExecutionDeadlineMillis: in.ExecutionDeadlineMillis,
			BoundKind: "bounded", BoundEvidence: in.BoundEvidence, Items: []FundingAuthorizationItem{{ItemID: 1, Ordinal: 1, Source: "time_card", EntitlementID: 9, CardVersion: 1, ReservedMicroCents: in.UpperBoundMicroCents}}}
		s.funding[in.RequestID] = a
		return fundingResult(a)
	case "/internal/oauth/invocations/bind":
		var in OAuthInvocationBindRequest
		_ = json.Unmarshal(body, &in)
		a, ok := s.reservations[in.RequestID]
		if !ok {
			return denied("not_found")
		}
		duplicate, valid := s.operation(in.OperationID, in.RequestID, body)
		if !valid {
			return denied("operation_conflict")
		}
		if duplicate {
			return reservationResult(a)
		}
		f := s.funding[in.RequestID]
		if a.Version != in.ExpectedVersion || a.Status != "reserved" || f.AuthorizationID != in.FundingAuthorizationID || f.UpperBoundMicroCents != a.UpperBoundMicroCents {
			return denied("bind_conflict")
		}
		a.Status = "active"
		a.Version++
		a.FundingAuthorizationID = in.FundingAuthorizationID
		s.reservations[in.RequestID] = a
		s.saveOperation(in.OperationID, in.RequestID, body)
		return reservationResult(a)
	case "/internal/billing/resize-authorization":
		var in ResizeFundingAuthorizationRequest
		_ = json.Unmarshal(body, &in)
		a := s.funding[in.RequestID]
		duplicate, valid := s.operation(in.OperationID, in.RequestID, body)
		if !valid {
			return denied("operation_conflict")
		}
		if duplicate {
			return fundingResult(a)
		}
		if a.Version != in.ExpectedVersion || a.AuthorizationID != in.AuthorizationID || in.UpperBoundMicroCents < a.UpperBoundMicroCents {
			return denied("funding_version_conflict")
		}
		a.UpperBoundMicroCents = in.UpperBoundMicroCents
		a.Items[0].ReservedMicroCents = in.UpperBoundMicroCents
		a.Version++
		a.BoundEvidence = in.BoundEvidence
		s.funding[in.RequestID] = a
		s.saveOperation(in.OperationID, in.RequestID, body)
		return fundingResult(a)
	case "/internal/oauth/invocations/resize":
		var in OAuthInvocationResizeRequest
		_ = json.Unmarshal(body, &in)
		a := s.reservations[in.RequestID]
		duplicate, valid := s.operation(in.OperationID, in.RequestID, body)
		if !valid {
			return denied("operation_conflict")
		}
		if duplicate {
			return reservationResult(a)
		}
		if s.revoked || a.Version != in.ExpectedVersion || a.Status != "active" || s.funding[in.RequestID].UpperBoundMicroCents != in.UpperBoundMicroCents {
			return denied("resize_conflict")
		}
		used := s.spent
		for id, other := range s.reservations {
			if id != in.RequestID && (other.Status == "active" || other.Status == "reserved") {
				used += other.UpperBoundMicroCents - other.ActualMicroCents
			}
		}
		if used+in.UpperBoundMicroCents > s.maximum {
			return denied("insufficient_scope")
		}
		a.UpperBoundMicroCents = in.UpperBoundMicroCents
		a.Version++
		s.reservations[in.RequestID] = a
		s.saveOperation(in.OperationID, in.RequestID, body)
		return reservationResult(a)
	case "/internal/billing/release-authorization":
		var in ReleaseFundingAuthorizationRequest
		_ = json.Unmarshal(body, &in)
		a := s.funding[in.RequestID]
		duplicate, valid := s.operation(in.OperationID, in.RequestID, body)
		if !valid {
			return denied("operation_conflict")
		}
		if duplicate {
			return fundingResult(a)
		}
		if !in.TerminalConfirmed || a.Version != in.ExpectedVersion {
			return denied("terminal_required")
		}
		a.Status = "released"
		a.Version++
		s.funding[in.RequestID] = a
		s.saveOperation(in.OperationID, in.RequestID, body)
		return fundingResult(a)
	case "/internal/oauth/invocations/settle", "/internal/oauth/invocations/release":
		var in OAuthInvocationFinalizeRequest
		_ = json.Unmarshal(body, &in)
		a := s.reservations[in.RequestID]
		duplicate, valid := s.operation(in.OperationID, in.RequestID, body)
		if !valid {
			return denied("operation_conflict")
		}
		if duplicate {
			return reservationResult(a)
		}
		if !in.TerminalConfirmed || in.ReservationID != a.ReservationID || in.GatewayCode != a.GatewayCode ||
			in.FundingAuthorizationID != a.FundingAuthorizationID || in.ActualMicroCents < 0 || in.ActualMicroCents > a.UpperBoundMicroCents {
			return denied("terminal_identity_conflict")
		}
		if strings.HasSuffix(path, "/release") {
			if in.Revision != 0 || in.ActualMicroCents != 0 || in.ExpectedVersion != a.Version || (a.Status != "active" && a.Status != "reserved") {
				return denied("release_conflict")
			}
			f := s.funding[in.RequestID]
			if a.FundingAuthorizationID != "" && (f.Status != "released" || f.ActualMicroCents != 0) {
				return denied("funding_not_released")
			}
			a.Status = "released"
			a.Version++
		} else {
			f := s.funding[in.RequestID]
			if in.Revision <= 0 || a.FundingAuthorizationID == "" || (a.Status != "active" && a.Status != "settled") {
				return denied("settlement_conflict")
			}
			if in.Revision <= a.Revision {
				actual, recorded := s.delegationRevisions[in.RequestID][in.Revision]
				if !recorded {
					actual, recorded = s.fundingRevisions[in.RequestID][in.Revision]
				}
				if !recorded || actual != in.ActualMicroCents {
					return denied("revision_conflict")
				}
				// Old deliveries validate their original absolute amount before the
				// mutable version, just like the durable main-site revision records.
				s.saveOperation(in.OperationID, in.RequestID, body)
				return reservationResult(a)
			}
			if in.ExpectedVersion != a.Version {
				return denied("reservation_version_conflict")
			}
			if f.Status != "settled" || f.SettlementRevision != in.Revision || f.ActualMicroCents != in.ActualMicroCents {
				return denied("funding_not_settled")
			}
			if s.delegationRevisions[in.RequestID] == nil {
				s.delegationRevisions[in.RequestID] = map[int64]int64{}
			}
			s.delegationRevisions[in.RequestID][in.Revision] = in.ActualMicroCents
			s.spent += in.ActualMicroCents - a.ActualMicroCents
			a.ActualMicroCents = in.ActualMicroCents
			a.Revision = in.Revision
			a.Status = "settled"
			a.Version++
		}
		s.reservations[in.RequestID] = a
		s.saveOperation(in.OperationID, in.RequestID, body)
		return reservationResult(a)
	case "/internal/usage/report", "/internal/usage/report-batch":
		var events []UsageEvent
		if strings.HasSuffix(path, "-batch") {
			var in UsageBatchReportRequest
			_ = json.Unmarshal(body, &in)
			events = in.Events
		} else {
			var e UsageEvent
			_ = json.Unmarshal(body, &e)
			events = []UsageEvent{e}
		}
		for _, e := range events {
			a, ok := s.funding[e.RequestID]
			if !ok || e.FundingAuthorizationID != a.AuthorizationID || e.ActualMicroCents == nil {
				return denied("usage_conflict")
			}
			if e.SettlementRevision <= 0 || *e.ActualMicroCents < 0 || *e.ActualMicroCents > a.UpperBoundMicroCents || !e.Final {
				return denied("usage_terminal_conflict")
			}
			if actual, recorded := s.fundingRevisions[e.RequestID][e.SettlementRevision]; recorded {
				if actual != *e.ActualMicroCents {
					return denied("usage_revision_conflict")
				}
				continue
			}
			if s.fundingRevisions[e.RequestID] == nil {
				s.fundingRevisions[e.RequestID] = map[int64]int64{}
			}
			s.fundingRevisions[e.RequestID][e.SettlementRevision] = *e.ActualMicroCents
			if e.SettlementRevision < a.SettlementRevision {
				continue
			}
			a.ActualMicroCents = *e.ActualMicroCents
			a.SettlementRevision = e.SettlementRevision
			a.Status = "settled"
			s.funding[e.RequestID] = a
		}
		return map[string]any{"ok": true}, 200, ""
	default:
		if strings.HasPrefix(path, "/internal/billing/authorizations/") {
			id := strings.TrimPrefix(path, "/internal/billing/authorizations/")
			for _, a := range s.funding {
				if a.AuthorizationID == id {
					return fundingResult(a)
				}
			}
			return nil, 404, "not_found"
		}
		return nil, 404, "not_found"
	}
}

func oauthInvocationTestContext(t *testing.T, s *oauthInvocationTestServer, id string) context.Context {
	t.Helper()
	s.mu.Lock()
	resolved := cloneOAuthInvocationResolve(&s.resolved)
	s.mu.Unlock()
	ctx := ContextWithRequestMeta(context.Background(), RequestMeta{RequestID: id, KeyID: "od_test", UserID: 42, Region: "region-test", GatewayID: "node-test",
		EntryGatewayNodeID: 5, EntryGlobalGroupID: 50, BillingSubjectType: "user", BillingSubjectID: 42})
	ctx, err := ContextWithOAuthInvocationCredential(ctx, oauthInvocationTestToken, "node-test", "region-test", resolved)
	require.NoError(t, err)
	return ctx
}

func oauthInvocationTestEvent(t *testing.T, ctx context.Context, cost, revision int64) UsageEvent {
	t.Helper()
	funding, ok := FundingFromContext(ctx)
	require.True(t, ok)
	e := UsageEvent{RequestID: funding.RequestID, KeyID: funding.KeyID, UserID: funding.UserID, BillingSubjectType: funding.BillingSubjectType, BillingSubjectID: funding.BillingSubjectID,
		GatewayID: funding.GatewayCode, Region: funding.Region, RequestedModel: funding.RequestedModel, Model: funding.RequestedModel, Path: funding.Endpoint, Status: 200}
	AttachFundingUsage(&e, funding, cost, revision, true)
	require.NoError(t, AttachOAuthInvocationUsage(ctx, &e))
	return e
}

func oauthInvocationTestAdmit(t *testing.T, s *oauthInvocationTestServer, control *ControlPlaneClientRef, quota *RedisQuota, id string, upper int64) context.Context {
	t.Helper()
	ctx := oauthInvocationTestContext(t, s, id)
	ctx, err := EnsureOAuthInvocationUpperBound(ctx, control, quota, upper, "text:model-test,input_bytes_upper_tokens=8192,output_tokens=128,n=1,tariff_max", "model-test", "/v1/responses")
	require.NoError(t, err)
	return ctx
}

func oauthInvocationTestPrefix(t *testing.T) string {
	return fmt.Sprintf("oauth-test-%d", time.Now().UnixNano())
}
func oauthInvocationDeletePrefix(t *testing.T, client *redis.Client, prefix string) {
	t.Helper()
	ctx := context.Background()
	keys, err := client.Keys(ctx, prefix+":*").Result()
	require.NoError(t, err)
	if len(keys) > 0 {
		require.NoError(t, client.Del(ctx, keys...).Err())
	}
}
