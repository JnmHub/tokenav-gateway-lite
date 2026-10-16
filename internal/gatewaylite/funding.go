package gatewaylite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// These wire types mirror control-plane/internal/types/time_cards.go. The
// gateway never selects items or recomputes the control plane's wallet split.
type AuthorizeFundingRequest struct {
	RequestID               string         `json:"request_id"`
	KeyID                   string         `json:"key_id"`
	UserID                  int64          `json:"user_id"`
	GatewayCode             string         `json:"gateway_code"`
	Region                  string         `json:"region"`
	EntryGatewayNodeID      int64          `json:"entry_gateway_node_id"`
	EntryGroupID            int64          `json:"entry_group_id"`
	RequestedModel          string         `json:"requested_model"`
	Endpoint                string         `json:"endpoint"`
	UpperBoundMicroCents    int64          `json:"upper_bound_micro_cents"`
	MaximumCents            int64          `json:"maximum_cents,omitempty"`
	BoundKind               string         `json:"bound_kind"`
	BoundEvidence           map[string]any `json:"bound_evidence"`
	ExecutionDeadlineMillis int64          `json:"execution_deadline_millis"`
	// ForceAuthorization is set only by a trusted delegated-invocation admission.
	ForceAuthorization bool `json:"force_authorization,omitempty"`
}

type FundingAuthorizationItem struct {
	ItemID             int64  `json:"item_id"`
	Ordinal            int    `json:"ordinal"`
	Source             string `json:"source"`
	EntitlementID      int64  `json:"entitlement_id,omitempty"`
	CardVersion        int64  `json:"card_version,omitempty"`
	Unlimited          bool   `json:"unlimited"`
	ReservedMicroCents int64  `json:"reserved_micro_cents"`
	SettledMicroCents  int64  `json:"settled_micro_cents"`
	MaximumCents       int64  `json:"maximum_cents"`
}

type FundingAuthorization struct {
	AuthorizationID         string                     `json:"authorization_id"`
	RequestID               string                     `json:"request_id"`
	KeyID                   string                     `json:"key_id"`
	UserID                  int64                      `json:"user_id"`
	OrganizationID          int64                      `json:"organization_id,omitempty"`
	BillingSubjectType      string                     `json:"billing_subject_type"`
	BillingSubjectID        int64                      `json:"billing_subject_id"`
	GatewayCode             string                     `json:"gateway_code"`
	Region                  string                     `json:"region"`
	EntryGatewayNodeID      int64                      `json:"entry_gateway_node_id"`
	EntryGroupID            int64                      `json:"entry_group_id"`
	RequestedModel          string                     `json:"requested_model"`
	Endpoint                string                     `json:"endpoint"`
	PolicyVersion           int64                      `json:"policy_version"`
	Version                 int64                      `json:"version"`
	Revision                int64                      `json:"revision"`
	Status                  string                     `json:"status"`
	Disposition             string                     `json:"disposition"`
	UpperBoundMicroCents    int64                      `json:"upper_bound_micro_cents"`
	MaximumCents            int64                      `json:"maximum_cents"`
	ExecutionDeadlineMillis int64                      `json:"execution_deadline_millis"`
	BoundKind               string                     `json:"bound_kind"`
	BoundEvidence           map[string]any             `json:"bound_evidence"`
	Items                   []FundingAuthorizationItem `json:"items"`
	CreatedAtMillis         int64                      `json:"created_at_millis"`
	SettlementRevision      int64                      `json:"settlement_revision"`
	ActualMicroCents        int64                      `json:"actual_micro_cents"`
	CardCoveredMicroCents   int64                      `json:"card_covered_micro_cents"`
	WalletChargedMicroCents int64                      `json:"wallet_charged_micro_cents"`
	UnfundedMicroCents      int64                      `json:"unfunded_micro_cents"`
	LastOperationID         string                     `json:"last_operation_id,omitempty"`
	ForceAuthorization      bool                       `json:"force_authorization,omitempty"`
}

type FundingAuthorizationResponse struct {
	OK            bool                  `json:"ok"`
	Disposition   string                `json:"disposition,omitempty"`
	Authorization *FundingAuthorization `json:"authorization,omitempty"`
	Error         string                `json:"error,omitempty"`
}

type ResizeFundingAuthorizationRequest struct {
	AuthorizationID      string         `json:"authorization_id"`
	RequestID            string         `json:"request_id"`
	GatewayCode          string         `json:"gateway_code"`
	ExpectedVersion      int64          `json:"expected_version"`
	OperationID          string         `json:"operation_id"`
	UpperBoundMicroCents int64          `json:"upper_bound_micro_cents"`
	BoundEvidence        map[string]any `json:"bound_evidence"`
}

type ReleaseFundingAuthorizationRequest struct {
	AuthorizationID   string `json:"authorization_id"`
	RequestID         string `json:"request_id"`
	GatewayCode       string `json:"gateway_code"`
	ExpectedVersion   int64  `json:"expected_version"`
	OperationID       string `json:"operation_id"`
	TerminalConfirmed bool   `json:"terminal_confirmed"`
	Reason            string `json:"reason"`
}

type FundingControlPlane interface {
	AuthorizeFunding(context.Context, AuthorizeFundingRequest) (*FundingAuthorizationResponse, error)
	ResizeFundingAuthorization(context.Context, ResizeFundingAuthorizationRequest) (*FundingAuthorizationResponse, error)
	ReleaseFundingAuthorization(context.Context, ReleaseFundingAuthorizationRequest) (*FundingAuthorizationResponse, error)
	LookupFundingAuthorization(context.Context, string) (*FundingAuthorizationResponse, error)
}

func (c *ControlPlaneClient) AuthorizeFunding(ctx context.Context, req AuthorizeFundingRequest) (*FundingAuthorizationResponse, error) {
	var out FundingAuthorizationResponse
	err := c.postJSON(ctx, "/internal/billing/authorize", req, &out)
	return &out, err
}
func (c *ControlPlaneClient) ResizeFundingAuthorization(ctx context.Context, req ResizeFundingAuthorizationRequest) (*FundingAuthorizationResponse, error) {
	var out FundingAuthorizationResponse
	err := c.postJSON(ctx, "/internal/billing/resize-authorization", req, &out)
	return &out, err
}
func (c *ControlPlaneClient) ReleaseFundingAuthorization(ctx context.Context, req ReleaseFundingAuthorizationRequest) (*FundingAuthorizationResponse, error) {
	var out FundingAuthorizationResponse
	err := c.postJSON(ctx, "/internal/billing/release-authorization", req, &out)
	return &out, err
}
func (c *ControlPlaneClient) LookupFundingAuthorization(ctx context.Context, id string) (*FundingAuthorizationResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/internal/billing/authorizations/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &ControlPlaneHTTPError{Path: req.URL.Path, StatusCode: resp.StatusCode}
	}
	return decodeFundingResponse(resp.Body)
}
func decodeFundingResponse(body io.Reader) (*FundingAuthorizationResponse, error) {
	var out FundingAuthorizationResponse
	err := json.NewDecoder(body).Decode(&out)
	return &out, err
}

func (r *ControlPlaneClientRef) AuthorizeFunding(ctx context.Context, req AuthorizeFundingRequest) (*FundingAuthorizationResponse, error) {
	c, err := r.current()
	if err != nil {
		return nil, err
	}
	return c.AuthorizeFunding(ctx, req)
}
func (r *ControlPlaneClientRef) ResizeFundingAuthorization(ctx context.Context, req ResizeFundingAuthorizationRequest) (*FundingAuthorizationResponse, error) {
	c, err := r.current()
	if err != nil {
		return nil, err
	}
	return c.ResizeFundingAuthorization(ctx, req)
}
func (r *ControlPlaneClientRef) ReleaseFundingAuthorization(ctx context.Context, req ReleaseFundingAuthorizationRequest) (*FundingAuthorizationResponse, error) {
	c, err := r.current()
	if err != nil {
		return nil, err
	}
	return c.ReleaseFundingAuthorization(ctx, req)
}
func (r *ControlPlaneClientRef) LookupFundingAuthorization(ctx context.Context, id string) (*FundingAuthorizationResponse, error) {
	c, err := r.current()
	if err != nil {
		return nil, err
	}
	return c.LookupFundingAuthorization(ctx, id)
}

type turnAdmissionKey struct{}
type TurnAdmission func(context.Context, string, string) (RequestMeta, error)

func ContextWithTurnAdmission(ctx context.Context, admit TurnAdmission) context.Context {
	return context.WithValue(ctx, turnAdmissionKey{}, wrapBindingTurnAdmission(ctx, admit))
}
func HasFundingTurnAdmission(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	admit, _ := ctx.Value(turnAdmissionKey{}).(TurnAdmission)
	return admit != nil
}

func AdmitFundingTurn(ctx context.Context, requestID, model string) (context.Context, error) {
	admit, _ := ctx.Value(turnAdmissionKey{}).(TurnAdmission)
	if admit == nil {
		return ctx, nil
	}
	meta, err := admit(ctx, requestID, model)
	if err != nil {
		return nil, err
	}
	return ContextWithRequestMeta(ctx, meta), nil
}

// RequestFunding is shared by detached usage workers, the HTTP finalizer and
// async submissions. Holding this mutex serializes expansion, not user funds;
// cross-node allocation remains solely in the main site's database transaction.
type RequestFunding struct {
	mu            sync.Mutex
	authorization FundingAuthorization
	control       FundingControlPlane
	quota         *RedisQuota
	dispatching   bool
	dispatched    bool
	blocked       bool
}

func NewRequestFunding(auth FundingAuthorization, control FundingControlPlane, quota *RedisQuota) *RequestFunding {
	return &RequestFunding{authorization: auth.Clone(), control: control, quota: quota}
}

// NewUsageFundingSnapshot 仅携带结算原身份；后台记账不能扩容、释放未知或再次派发消费。
func NewUsageFundingSnapshot(auth FundingAuthorization) *RequestFunding {
	return &RequestFunding{authorization: auth.Clone(), blocked: true}
}
func (a FundingAuthorization) Clone() FundingAuthorization {
	a.Items = append([]FundingAuthorizationItem(nil), a.Items...)
	if a.BoundEvidence != nil {
		copyEvidence := make(map[string]any, len(a.BoundEvidence))
		for k, v := range a.BoundEvidence {
			copyEvidence[k] = v
		}
		a.BoundEvidence = copyEvidence
	}
	return a
}
func (r *RequestFunding) Snapshot() FundingAuthorization {
	if r == nil {
		return FundingAuthorization{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.authorization.Clone()
}
func FundingFromContext(ctx context.Context) (FundingAuthorization, bool) {
	meta, ok := RequestMetaFromContext(ctx)
	if !ok || meta.Funding == nil {
		return FundingAuthorization{}, false
	}
	return meta.Funding.Snapshot(), true
}
func HasRequestFunding(ctx context.Context) bool { _, ok := FundingFromContext(ctx); return ok }

func (r *RequestFunding) Ensure(ctx context.Context, microCents int64, evidence string) (FundingAuthorization, error) {
	if r == nil {
		return FundingAuthorization{}, ErrRequestQuotaUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.authorization
	if r.dispatching || r.dispatched || r.blocked {
		return FundingAuthorization{}, ErrRequestQuotaUnavailable
	}
	if microCents <= 0 || microCents > maxFundingExactInteger || a.ExecutionDeadlineMillis <= time.Now().UnixMilli() || a.Status != "active" && a.Status != "selected" {
		return FundingAuthorization{}, ErrRequestQuotaUnavailable
	}
	if r.control == nil || r.quota == nil || !r.quota.Enabled() {
		return FundingAuthorization{}, ErrRequestQuotaUnavailable
	}
	if a.Disposition == "authorized" && microCents <= a.UpperBoundMicroCents {
		if err := r.quota.ReserveFunding(ctx, a); err != nil {
			return FundingAuthorization{}, err
		}
		return a.Clone(), nil
	}
	proof := FundingBoundEvidence(evidence)
	var response *FundingAuthorizationResponse
	var err error
	operationID := ""
	if a.Disposition == "authorization_required" {
		response, err = r.control.AuthorizeFunding(ctx, AuthorizeFundingRequest{
			RequestID: a.RequestID, KeyID: a.KeyID, UserID: a.UserID, GatewayCode: a.GatewayCode, Region: a.Region,
			EntryGatewayNodeID: a.EntryGatewayNodeID, EntryGroupID: a.EntryGroupID, RequestedModel: a.RequestedModel, Endpoint: a.Endpoint,
			UpperBoundMicroCents: microCents, BoundKind: "bounded", BoundEvidence: proof, ExecutionDeadlineMillis: a.ExecutionDeadlineMillis, ForceAuthorization: a.ForceAuthorization,
		})
	} else if a.Disposition == "authorized" {
		operationID = fmt.Sprintf("%s:resize:%d:%d", a.RequestID, a.Version, microCents)
		response, err = r.control.ResizeFundingAuthorization(ctx, ResizeFundingAuthorizationRequest{
			AuthorizationID: a.AuthorizationID, RequestID: a.RequestID, GatewayCode: a.GatewayCode, ExpectedVersion: a.Version,
			OperationID: operationID, UpperBoundMicroCents: microCents, BoundEvidence: proof,
		})
	} else {
		return FundingAuthorization{}, ErrRequestQuotaUnavailable
	}
	if err != nil {
		// A timed-out transaction may already have reserved funds. Read its durable
		// identity before deciding; never issue a new request or use a wallet lease.
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		recovered, lookupErr := r.control.LookupFundingAuthorization(recoveryCtx, a.AuthorizationID)
		cancel()
		if lookupErr != nil || recovered == nil || !recovered.OK || recovered.Authorization == nil || !sameFundingScope(a, *recovered.Authorization) {
			return FundingAuthorization{}, errors.Join(ErrRequestQuotaUnavailable, err, lookupErr)
		}
		next := recovered.Authorization.Clone()
		if next.Disposition == "" {
			next.Disposition = recovered.Disposition
		}
		r.authorization = next
		if next.Disposition != "authorized" || next.Status != "active" || next.UpperBoundMicroCents < microCents || (operationID != "" && next.LastOperationID != operationID) {
			return FundingAuthorization{}, errors.Join(ErrRequestQuotaUnavailable, err)
		}
		response = recovered
	}
	if response == nil || !response.OK || response.Authorization == nil {
		if response != nil && (strings.Contains(response.Error, "insufficient") || strings.Contains(response.Error, "budget") || strings.Contains(response.Error, "usage_limit")) {
			return FundingAuthorization{}, ErrRequestQuotaInsufficient
		}
		return FundingAuthorization{}, ErrRequestQuotaUnavailable
	}
	next := response.Authorization.Clone()
	if next.Disposition == "" {
		next.Disposition = response.Disposition
	}
	if !sameFundingScope(a, next) || next.AuthorizationID != a.AuthorizationID || next.Disposition != "authorized" || next.Status != "active" || next.Version < a.Version || next.UpperBoundMicroCents < microCents {
		return FundingAuthorization{}, ErrRequestQuotaUnavailable
	}
	// Preserve a successful main-site allocation when local installation fails.
	r.authorization = next
	if err := r.quota.ReserveFunding(ctx, next); err != nil {
		return FundingAuthorization{}, err
	}
	return next.Clone(), nil
}

func FundingBoundEvidence(evidence string) map[string]any {
	kind := "fixed_media"
	if strings.HasPrefix(evidence, "text:") {
		kind = "text_max_output"
	}
	if strings.HasPrefix(evidence, "per_request:") {
		kind = "fixed_request"
	}
	out := map[string]any{"kind": kind, "price_source": "gateway_runtime", "gateway_bound": evidence}
	for _, part := range strings.Split(evidence, ",") {
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			continue
		}
		switch name {
		case "input_bytes_upper_tokens":
			out["input_tokens_upper"] = parsed
		case "output_tokens":
			out["max_output_tokens"] = parsed
		case "n":
			out["completions"] = parsed
		}
	}
	return out
}

type fundingDispatchCheckpointKey struct{}

// ContextWithFundingDispatchCheckpoint persists the final allocation before a
// durable media task may send work. Only non-secret funding facts enter storage.
func ContextWithFundingDispatchCheckpoint(ctx context.Context, checkpoint func(context.Context, FundingAuthorization) error) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, fundingDispatchCheckpointKey{}, checkpoint)
}

// PrepareFundingDispatch is the last gate before sending bytes. A retry of an
// already dispatched authorization is ambiguous and must not execute again.
func PrepareFundingDispatch(ctx context.Context) error {
	meta, ok := RequestMetaFromContext(ctx)
	if !ok || meta.Funding == nil {
		if HasOAuthInvocationCredential(ctx) {
			return ErrRequestQuotaUnavailable
		}
		return nil
	}
	funding := meta.Funding
	funding.mu.Lock()
	if funding.dispatching || funding.dispatched || funding.blocked {
		funding.mu.Unlock()
		return ErrRequestQuotaUnavailable
	}
	authorization := funding.authorization.Clone()
	if authorization.Status != "active" || authorization.Disposition != "authorized" || authorization.ExecutionDeadlineMillis <= time.Now().UnixMilli() || validateFundingAuthorization(authorization) != nil {
		funding.mu.Unlock()
		return ErrRequestQuotaUnavailable
	}
	funding.dispatching = true
	funding.mu.Unlock()

	// Never call the delegation controller or a durable checkpoint with the
	// funding lock held. Delegation admission installs Redis while holding its
	// own lock; the inverse lock order would deadlock dispatch against expansion.
	var dispatchErr error
	if checkpoint, ok := ctx.Value(fundingDispatchCheckpointKey{}).(func(context.Context, FundingAuthorization) error); ok && checkpoint != nil {
		checkpointCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		dispatchErr = checkpoint(checkpointCtx, authorization)
		cancel()
	}
	if dispatchErr == nil {
		dispatchErr = MarkOAuthInvocationExecutionStarted(ctx)
	}
	funding.mu.Lock()
	funding.dispatching = false
	if dispatchErr == nil {
		funding.dispatched = true
	} else {
		// A failed durable checkpoint may have committed. Retain its original
		// allocation until recovery verifies it instead of granting another send.
		funding.blocked = true
	}
	funding.mu.Unlock()
	if dispatchErr != nil {
		return errors.Join(ErrRequestQuotaUnavailable, dispatchErr)
	}
	return nil
}

// MarkFundingDispatched is conservative: once bytes might have reached an
// upstream, an HTTP error is not proof that the original hold can be released.
func MarkFundingDispatched(ctx context.Context) {
	meta, ok := RequestMetaFromContext(ctx)
	if !ok || meta.Funding == nil {
		return
	}
	meta.Funding.mu.Lock()
	meta.Funding.dispatched = true
	meta.Funding.mu.Unlock()
}
func (r *RequestFunding) Dispatched() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dispatching || r.dispatched || r.blocked
}
func sameFundingScope(a, b FundingAuthorization) bool {
	return a.RequestID == b.RequestID && a.KeyID == b.KeyID && a.UserID == b.UserID && a.OrganizationID == b.OrganizationID && a.BillingSubjectType == b.BillingSubjectType && a.BillingSubjectID == b.BillingSubjectID && a.EntryGatewayNodeID == b.EntryGatewayNodeID && a.EntryGroupID == b.EntryGroupID && a.RequestedModel == b.RequestedModel && a.Endpoint == b.Endpoint && a.GatewayCode == b.GatewayCode && a.Region == b.Region && a.ForceAuthorization == b.ForceAuthorization
}
func EnsureFundingUpperBound(ctx context.Context, microCents int64, evidence string) error {
	if handled, err := EnsureOAuthInvocationFundingForContext(ctx, microCents, evidence); handled {
		return err
	}
	meta, ok := RequestMetaFromContext(ctx)
	if !ok || meta.Funding == nil {
		return nil
	}
	_, err := meta.Funding.Ensure(ctx, microCents, evidence)
	return err
}

// ReleaseFundingPreDispatch is safe only before any upstream send. Authorized
// releases use the durable usage queue (and its database outbox on Redis failure).
func ReleaseFundingPreDispatch(ctx context.Context, reason string) error {
	if request, delegated := OAuthInvocationRequestFromContext(ctx); delegated {
		snapshot := request.Snapshot()
		if snapshot.ExecutionStarted || snapshot.OutcomeUnknown {
			return ErrOAuthInvocationUnknown
		}
		// The existing helper freezes both release operations and confirms funding
		// release before delegation release. A separate zero-usage queue event would
		// race the released/settled states and could strand the other reservation.
		return ReleaseOAuthInvocationRequest(ctx, true)
	}
	meta, ok := RequestMetaFromContext(ctx)
	if !ok || meta.Funding == nil {
		return nil
	}
	if meta.Funding.Dispatched() {
		return ErrRequestQuotaUnavailable
	}
	a := meta.Funding.Snapshot()
	if a.Disposition == "authorized" {
		event := UsageEvent{RequestID: a.RequestID, KeyID: a.KeyID, UserID: a.UserID, OrganizationID: a.OrganizationID, BillingSubjectType: a.BillingSubjectType, BillingSubjectID: a.BillingSubjectID, Region: a.Region, GatewayID: a.GatewayCode, RequestedModel: a.RequestedModel, Model: a.RequestedModel, Path: a.Endpoint, Method: "POST", Status: 400, SettlementMode: string(BillingSettlementModeIdempotentV2), EndedAtMillis: time.Now().UnixMilli()}
		AttachFundingUsage(&event, a, 0, 1, true)
		return ReportUsageCorrection(ctx, event)
	}
	// A selection has no allocated items, so a failed release never gives spendable
	// capacity back locally. It stays visible in main-site overdue reconciliation.
	if meta.Funding.control == nil {
		return ErrRequestQuotaUnavailable
	}
	response, err := meta.Funding.control.ReleaseFundingAuthorization(ctx, ReleaseFundingAuthorizationRequest{AuthorizationID: a.AuthorizationID, RequestID: a.RequestID, GatewayCode: a.GatewayCode, ExpectedVersion: a.Version, OperationID: a.RequestID + ":release:" + reason, TerminalConfirmed: true, Reason: reason})
	if err != nil || response == nil || !response.OK {
		return errors.Join(ErrRequestQuotaUnavailable, err)
	}
	return nil
}

func AttachFundingUsage(event *UsageEvent, auth FundingAuthorization, micro int64, revision int64, final bool) {
	if auth.BillingSubjectType != "" {
		event.BillingSubjectType = auth.BillingSubjectType
		event.BillingSubjectID = auth.BillingSubjectID
		event.OrganizationID = auth.OrganizationID
	}
	event.RequestID = auth.RequestID
	// Funding scope uses global entry IDs; task/routing metadata may contain local
	// groups or mapped models and must never replace these settlement claims.
	event.RequestedModel = auth.RequestedModel
	event.RequestedGatewayGroupID = auth.EntryGroupID
	event.FundingAuthorizationID = auth.AuthorizationID
	event.FundingRevision = auth.Version
	event.SettlementRevision = revision
	event.ActualMicroCents = &micro
	event.Final = final
	snapshot := auth.Clone()
	event.FundingSnapshot = &snapshot
	event.LeaseID = ""
	event.SettlementPending = false
	event.ActualCents = micro / actualCostMicroCentsScale
}
