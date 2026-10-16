package gatewaylite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

// Start runs a single bounded recovery consumer. SQL row claims also protect
// deployments with more than one process; Stop waits for the consumer to exit.
func (o *OAuthInvocationRecoveryOutbox) Start(ctx context.Context) {
	if o == nil || o.db == nil || o.control == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cancel != nil {
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	o.cancel, o.done = cancel, done
	go func() {
		// Freeze this run's channel. A new Start after exit must not replace the
		// channel an earlier Stop is waiting on, or leave a cancelled run installed.
		defer func() {
			cancel()
			o.mu.Lock()
			o.cancel = nil
			close(done)
			o.mu.Unlock()
		}()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if _, err := o.RecoverDue(workerCtx, 16); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("gateway-lite oauth recovery scan deferred", "code", "recovery_unavailable")
			}
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (o *OAuthInvocationRecoveryOutbox) Stop(ctx context.Context) error {
	if o == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	o.mu.Lock()
	cancel, done := o.cancel, o.done
	o.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RecoverDue is the production consumer's bounded scan, also useful for an
// operator-triggered recovery. It has no upstream dispatcher or credential API.
func (o *OAuthInvocationRecoveryOutbox) RecoverDue(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 100 {
		limit = 16
	}
	processed := 0
	for processed < limit {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		item, err := o.claimNext(ctx)
		if err != nil || item == nil {
			return processed, err
		}
		processed++
		if err := o.recoverItem(ctx, item); err != nil {
			return processed, err
		}
	}
	return processed, nil
}

func (o *OAuthInvocationRecoveryOutbox) recoverItem(ctx context.Context, item *oauthInvocationRecoveryItem) (resultErr error) {
	defer func() { resultErr = errors.Join(resultErr, o.releaseClaim(ctx, item)) }()
	var snapshot OAuthInvocationRequestSnapshot
	digest := sha256.Sum256(item.payload)
	if err := json.Unmarshal(item.payload, &snapshot); err != nil || hex.EncodeToString(digest[:]) != item.digest ||
		validateOAuthInvocationRecoverySnapshot(snapshot) != nil || snapshot.ReserveRequest.GatewayCode != item.gateway || snapshot.ReserveRequest.RequestID != item.requestID {
		// A valid JSON document can still belong to another financial row. Verify
		// the stored digest and row identity before attempting any remote mutation.
		queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthRecoveryDBTimeout)
		defer cancel()
		_, err = o.db.ExecContext(queryCtx, `UPDATE gateway_lite_oauth_invocation_recovery SET state='attention',last_error='invalid_recovery_snapshot'
			WHERE gateway_code=$1 AND request_id=$2 AND generation=$3 AND claim_token=$4`, item.gateway, item.requestID, item.generation, item.claim)
		return err
	}
	if item.mayDispatch || snapshot.ExecutionStarted {
		// No response or a cancelled client cannot prove zero cost. Check the
		// original reservation's terminal authority without executing or releasing.
		response, err := o.status(ctx, snapshot)
		if err == nil && (response.Status == "released" || response.Status == "settled") {
			snapshot.Reservation, snapshot.Closed = response, true
			return o.saveClaim(ctx, item, snapshot, "completed", nil)
		}
		slog.Warn("gateway-lite oauth consumption outcome requires review", "gateway_code", item.gateway, "request_id", item.requestID, "code", "execution_outcome_unknown")
		return o.saveClaim(ctx, item, snapshot, "attention", ErrOAuthInvocationUnknown)
	}
	if snapshot.Closed {
		// A confirmed live zero-cost terminal may retain its last phase after a
		// definite reserve denial. Replaying it would create a new hold after close.
		return o.saveClaim(ctx, item, snapshot, "completed", nil)
	}
	// The owner has been fenced in SQL, and may_dispatch was never set. Late
	// live code cannot pass its next checkpoint/dispatch CAS. This is the proof
	// permitting original zero-cost financial cleanup, not a timeout inference.
	snapshot.ExecutionStarted = false
	snapshot.DispatchUncertain = false
	if err := o.replayPending(ctx, item, &snapshot); err != nil {
		if errors.Is(err, ErrOAuthInvocationConflict) {
			return o.saveClaim(ctx, item, snapshot, "attention", err)
		}
		return o.saveClaim(ctx, item, snapshot, "pending", err)
	}
	if snapshot.Closed {
		return o.saveClaim(ctx, item, snapshot, "completed", nil)
	}
	reservation, err := o.status(ctx, snapshot)
	if err != nil {
		return o.saveClaim(ctx, item, snapshot, "pending", err)
	}
	snapshot.Reservation = reservation
	if reservation.Status == "released" || reservation.Status == "settled" {
		snapshot.Closed = true
		return o.saveClaim(ctx, item, snapshot, "completed", nil)
	}
	if snapshot.Funding != nil {
		response, callErr := o.control.LookupFundingAuthorization(ctx, snapshot.Funding.AuthorizationID)
		if err := oauthInvocationFundingError(callErr, response); err != nil {
			return o.saveClaim(ctx, item, snapshot, "pending", err)
		}
		funding := response.Authorization.Clone()
		if funding.AuthorizationID != snapshot.Funding.AuthorizationID || !sameFundingScope(*snapshot.Funding, funding) ||
			(reservation.FundingAuthorizationID != "" && reservation.FundingAuthorizationID != funding.AuthorizationID) {
			return o.saveClaim(ctx, item, snapshot, "attention", ErrOAuthInvocationConflict)
		}
		if funding.Status != "released" && funding.Status != "active" {
			return o.saveClaim(ctx, item, snapshot, "attention", ErrOAuthInvocationUnknown)
		}
		if funding.ActualMicroCents != 0 {
			return o.saveClaim(ctx, item, snapshot, "attention", ErrOAuthInvocationConflict)
		}
		// Preserve an already-attempted operation's original ExpectedVersion.
		if snapshot.PendingFundingRelease == nil {
			snapshot.Funding = &funding
			request := ReleaseFundingAuthorizationRequest{AuthorizationID: funding.AuthorizationID, RequestID: funding.RequestID,
				GatewayCode: funding.GatewayCode, ExpectedVersion: funding.Version, OperationID: funding.RequestID + ":oauth-release:zero-cost",
				TerminalConfirmed: true, Reason: "oauth_invocation_zero_cost_terminal"}
			snapshot.PendingFundingRelease = &request
		}
		snapshot.PendingPhase = "funding_release"
		if err := o.saveClaim(ctx, item, snapshot, "pending", nil); err != nil {
			return err
		}
		if err := o.replayPending(ctx, item, &snapshot); err != nil {
			return o.saveClaim(ctx, item, snapshot, "pending", err)
		}
	} else if reservation.FundingAuthorizationID != "" {
		return o.saveClaim(ctx, item, snapshot, "attention", ErrOAuthInvocationConflict)
	}
	if snapshot.PendingRelease == nil {
		request := OAuthInvocationFinalizeRequest{ReservationID: reservation.ReservationID, RequestID: reservation.RequestID,
			GatewayCode: reservation.GatewayCode, FundingAuthorizationID: reservation.FundingAuthorizationID,
			OperationID: reservation.RequestID + ":oauth-release:zero-cost", ExpectedVersion: reservation.Version,
			TerminalConfirmed: true, Revision: 0, ActualMicroCents: 0}
		snapshot.PendingRelease = &request
	}
	snapshot.PendingPhase = "delegation_release"
	if err := o.saveClaim(ctx, item, snapshot, "pending", nil); err != nil {
		return err
	}
	if err := o.replayPending(ctx, item, &snapshot); err != nil {
		return o.saveClaim(ctx, item, snapshot, "pending", err)
	}
	return o.saveClaim(ctx, item, snapshot, "completed", nil)
}

func (o *OAuthInvocationRecoveryOutbox) status(ctx context.Context, s OAuthInvocationRequestSnapshot) (*OAuthInvocationReservation, error) {
	control, ok := o.control.(OAuthInvocationStatusControl)
	if !ok {
		return nil, ErrOAuthInvocationUnavailable
	}
	request := OAuthInvocationStatusRequest{RequestID: s.ReserveRequest.RequestID, GatewayCode: s.ReserveRequest.GatewayCode, DelegationID: s.ReserveRequest.DelegationID}
	if s.Reservation != nil {
		request.ReservationID = s.Reservation.ReservationID
	}
	response, callErr := control.OAuthInvocationStatus(ctx, request)
	if err := oauthInvocationOperationError(callErr, response); err != nil {
		return nil, err
	}
	if !sameOAuthInvocationReserve(s.ReserveRequest, *response.Reservation) ||
		(s.Reservation != nil && (!sameOAuthInvocationReservation(*s.Reservation, *response.Reservation) ||
			(s.Reservation.FundingAuthorizationID != "" && s.Reservation.FundingAuthorizationID != response.Reservation.FundingAuthorizationID))) ||
		(s.Funding != nil && response.Reservation.FundingAuthorizationID != "" && s.Funding.AuthorizationID != response.Reservation.FundingAuthorizationID) {
		return nil, ErrOAuthInvocationConflict
	}
	return response.Reservation, nil
}
