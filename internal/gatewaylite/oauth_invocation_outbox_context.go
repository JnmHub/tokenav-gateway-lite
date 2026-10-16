package gatewaylite

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
)

var defaultOAuthInvocationRecovery atomic.Pointer[OAuthInvocationRecoveryOutbox]

// NewOAuthInvocationRecoveryOutbox uses the gateway's existing SQL connection.
// Its table is installed by migration 241; constructing it does not run DDL.
func NewOAuthInvocationRecoveryOutbox(db *sql.DB, control OAuthInvocationFundingControl) *OAuthInvocationRecoveryOutbox {
	return newOAuthInvocationRecoveryOutbox(db, control)
}

func SetDefaultOAuthInvocationRecoveryOutbox(outbox *OAuthInvocationRecoveryOutbox) {
	defaultOAuthInvocationRecovery.Store(outbox)
}

func DefaultOAuthInvocationRecoveryOutbox() *OAuthInvocationRecoveryOutbox {
	return defaultOAuthInvocationRecovery.Load()
}

// FinishOAuthInvocationRecovery transfers a finished request/turn's immutable
// financial record to recovery. It never infers zero cost from cancellation.
// Call with the original admission context, including on BeforeDispatch errors.
func FinishOAuthInvocationRecovery(ctx context.Context) error {
	request, ok := OAuthInvocationRequestFromContext(ctx)
	if !ok {
		return nil
	}
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.recovery == nil {
		return nil
	}
	if request.recoveryFinished {
		return nil
	}
	request.blocked = true
	request.ready = false
	if err := request.recovery.finish(ctx, request.snapshot); err != nil {
		return errors.Join(ErrOAuthInvocationUnavailable, err)
	}
	request.recoveryFinished = true
	return nil
}
