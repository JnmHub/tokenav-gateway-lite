package gatewaylite

import (
	"context"
	"errors"
	"strings"
)

// OAuthInvocation mirrors the explicit paid consent; GroupID is the global
// entry group, never a gateway-local group or a route selected by failover.
type OAuthInvocation struct {
	GatewayNodeID int64    `json:"gateway_node_id"`
	GroupID       int64    `json:"group_id"`
	Models        []string `json:"models"`
	MaxCostCents  int64    `json:"max_cost_cents"`
	ExpiresIn     int      `json:"expires_in"`
	RateLimitRPM  int      `json:"rate_limit_rpm"`
	Concurrency   int      `json:"concurrency"`
	CostConsent   bool     `json:"cost_consent"`
}

type OAuthInvocationResolveRequest struct {
	InvocationToken string `json:"invocation_token"`
	GatewayCode     string `json:"gateway_code"`
	Region          string `json:"region"`
}

type OAuthInvocationResolveResponse struct {
	OK              bool             `json:"ok"`
	DelegationID    string           `json:"delegation_id,omitempty"`
	Key             *KeySnapshot     `json:"key,omitempty"`
	Invocation      *OAuthInvocation `json:"invocation,omitempty"`
	ExpiresAtMillis int64            `json:"expires_at_millis,omitempty"`
	Error           string           `json:"error,omitempty"`
}

type OAuthInvocationReserveRequest struct {
	DelegationID            string `json:"delegation_id"`
	RequestID               string `json:"request_id"`
	GatewayCode             string `json:"gateway_code"`
	KeyID                   string `json:"key_id"`
	EntryGatewayNodeID      int64  `json:"entry_gateway_node_id"`
	EntryGroupID            int64  `json:"entry_group_id"`
	RequestedModel          string `json:"requested_model"`
	Endpoint                string `json:"endpoint"`
	UpperBoundMicroCents    int64  `json:"upper_bound_micro_cents"`
	ExecutionDeadlineMillis int64  `json:"execution_deadline_millis"`
}

type OAuthInvocationReservation struct {
	ReservationID           string `json:"reservation_id"`
	DelegationID            string `json:"delegation_id"`
	RequestID               string `json:"request_id"`
	GatewayCode             string `json:"gateway_code"`
	KeyID                   string `json:"key_id"`
	EntryGatewayNodeID      int64  `json:"entry_gateway_node_id"`
	EntryGroupID            int64  `json:"entry_group_id"`
	RequestedModel          string `json:"requested_model"`
	Endpoint                string `json:"endpoint"`
	UpperBoundMicroCents    int64  `json:"upper_bound_micro_cents"`
	ActualMicroCents        int64  `json:"actual_micro_cents"`
	ExecutionDeadlineMillis int64  `json:"execution_deadline_millis"`
	FundingAuthorizationID  string `json:"funding_authorization_id"`
	Status                  string `json:"status"`
	Version                 int64  `json:"version"`
	Revision                int64  `json:"revision"`
}

type OAuthInvocationBindRequest struct {
	ReservationID          string `json:"reservation_id"`
	RequestID              string `json:"request_id"`
	GatewayCode            string `json:"gateway_code"`
	FundingAuthorizationID string `json:"funding_authorization_id"`
	OperationID            string `json:"operation_id"`
	ExpectedVersion        int64  `json:"expected_version"`
}

type OAuthInvocationResizeRequest struct {
	OAuthInvocationBindRequest
	UpperBoundMicroCents int64 `json:"upper_bound_micro_cents"`
}

type OAuthInvocationFinalizeRequest struct {
	ReservationID          string `json:"reservation_id"`
	RequestID              string `json:"request_id"`
	GatewayCode            string `json:"gateway_code"`
	FundingAuthorizationID string `json:"funding_authorization_id,omitempty"`
	OperationID            string `json:"operation_id"`
	ExpectedVersion        int64  `json:"expected_version"`
	Revision               int64  `json:"revision"`
	ActualMicroCents       int64  `json:"actual_micro_cents"`
	TerminalConfirmed      bool   `json:"terminal_confirmed"`
}

type OAuthInvocationReservationResponse struct {
	OK          bool                        `json:"ok"`
	Reservation *OAuthInvocationReservation `json:"reservation,omitempty"`
	Error       string                      `json:"error,omitempty"`
}

type OAuthInvocationControlPlane interface {
	ResolveOAuthInvocation(context.Context, OAuthInvocationResolveRequest) (*OAuthInvocationResolveResponse, error)
	ReserveOAuthInvocation(context.Context, OAuthInvocationReserveRequest) (*OAuthInvocationReservationResponse, error)
	BindOAuthInvocation(context.Context, OAuthInvocationBindRequest) (*OAuthInvocationReservationResponse, error)
	ResizeOAuthInvocation(context.Context, OAuthInvocationResizeRequest) (*OAuthInvocationReservationResponse, error)
	SettleOAuthInvocation(context.Context, OAuthInvocationFinalizeRequest) (*OAuthInvocationReservationResponse, error)
	ReleaseOAuthInvocation(context.Context, OAuthInvocationFinalizeRequest) (*OAuthInvocationReservationResponse, error)
}

type OAuthInvocationStatusRequest struct {
	ReservationID string `json:"reservation_id,omitempty"`
	RequestID     string `json:"request_id"`
	GatewayCode   string `json:"gateway_code"`
	DelegationID  string `json:"delegation_id,omitempty"`
}

type OAuthInvocationStatusControl interface {
	OAuthInvocationStatus(context.Context, OAuthInvocationStatusRequest) (*OAuthInvocationReservationResponse, error)
}

type OAuthInvocationFundingControl interface {
	OAuthInvocationControlPlane
	FundingControlPlane
}

type OAuthInvocationFundingInstaller interface {
	ReserveFunding(context.Context, FundingAuthorization) error
}

var (
	ErrOAuthInvocationDenied      = errors.New("oauth_invocation_denied")
	ErrOAuthInvocationExpired     = errors.New("oauth_invocation_expired")
	ErrOAuthInvocationUnavailable = errors.New("oauth_invocation_unavailable")
	ErrOAuthInvocationUnknown     = errors.New("oauth_invocation_outcome_unknown")
	ErrOAuthInvocationConflict    = errors.New("oauth_invocation_scope_conflict")
	ErrOAuthInvocationUnsupported = errors.New("oauth_invocation_endpoint_unsupported")
)

// Denial is an explicit server result. Transport failures and 5xx do not prove
// that an allocation failed, so compensating them would manufacture new funds.
func oauthInvocationOperationError(err error, response *OAuthInvocationReservationResponse) error {
	if err != nil {
		var httpErr *ControlPlaneHTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode >= 400 && httpErr.StatusCode < 500 && httpErr.StatusCode != 408 {
			return errors.Join(ErrOAuthInvocationDenied, err)
		}
		return errors.Join(ErrOAuthInvocationUnknown, err)
	}
	if response == nil {
		return ErrOAuthInvocationUnknown
	}
	if !response.OK {
		return errors.Join(ErrOAuthInvocationDenied, errors.New(strings.TrimSpace(response.Error)))
	}
	if response.Reservation == nil {
		return ErrOAuthInvocationUnknown
	}
	return nil
}
