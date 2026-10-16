package gatewaylite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	oauthRecoveryDBTimeout = 3 * time.Second
	oauthRecoveryOwnerTTL  = 30 * time.Second
	oauthRecoveryClaimTTL  = 60 * time.Second
)

// OAuthInvocationRecoveryOutbox owns only financial recovery, never execution.
// Claiming a row atomically fences its former live owner. An expired owner lease
// alone proves nothing; only the immutable no-dispatch fence allows zero release.
type OAuthInvocationRecoveryOutbox struct {
	db      *sql.DB
	control OAuthInvocationFundingControl
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
}

type oauthInvocationRecoveryLease struct {
	outbox     *OAuthInvocationRecoveryOutbox
	owner      string
	gateway    string
	requestID  string
	generation int64
}

type oauthInvocationRecoveryItem struct {
	gateway, requestID, claim string
	generation                int64
	payload                   []byte
	digest                    string
	mayDispatch               bool
	attempts                  int
}

func newOAuthInvocationRecoveryOutbox(db *sql.DB, control OAuthInvocationFundingControl) *OAuthInvocationRecoveryOutbox {
	return &OAuthInvocationRecoveryOutbox{db: db, control: control}
}

func oauthRecoveryPayload(snapshot OAuthInvocationRequestSnapshot) ([]byte, string, error) {
	if err := validateOAuthInvocationRecoverySnapshot(snapshot); err != nil {
		return nil, "", err
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(body)
	return body, hex.EncodeToString(digest[:]), nil
}

func validateOAuthInvocationRecoverySnapshot(s OAuthInvocationRequestSnapshot) error {
	r, f := s.ReserveRequest, s.FundingRequest
	if strings.TrimSpace(r.RequestID) == "" || r.GatewayCode == "" || r.DelegationID == "" || r.KeyID == "" ||
		r.RequestID != f.RequestID || r.KeyID != f.KeyID || r.GatewayCode != f.GatewayCode || f.UserID <= 0 ||
		r.EntryGatewayNodeID != f.EntryGatewayNodeID || r.EntryGroupID != f.EntryGroupID || r.RequestedModel != f.RequestedModel ||
		r.Endpoint != f.Endpoint || r.ExecutionDeadlineMillis != f.ExecutionDeadlineMillis || !f.ForceAuthorization {
		return ErrOAuthInvocationConflict
	}
	if s.Reservation != nil && !sameOAuthInvocationReserve(r, *s.Reservation) {
		return ErrOAuthInvocationConflict
	}
	if s.Funding != nil && !(&RequestOAuthInvocation{snapshot: s}).fundingMatchesIdentity(*s.Funding) {
		return ErrOAuthInvocationConflict
	}
	return nil
}

func (o *OAuthInvocationRecoveryOutbox) begin(ctx context.Context, snapshot OAuthInvocationRequestSnapshot) (*oauthInvocationRecoveryLease, error) {
	if o == nil || o.db == nil || o.control == nil {
		return nil, ErrOAuthInvocationUnavailable
	}
	body, digest, err := oauthRecoveryPayload(snapshot)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	lease := &oauthInvocationRecoveryLease{outbox: o, owner: uuid.NewString(), gateway: snapshot.ReserveRequest.GatewayCode, requestID: snapshot.ReserveRequest.RequestID, generation: 1}
	queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthRecoveryDBTimeout)
	defer cancel()
	result, err := o.db.ExecContext(queryCtx, `INSERT INTO gateway_lite_oauth_invocation_recovery
		(gateway_code,request_id,snapshot_payload,snapshot_sha256,generation,owner_token,owner_until_millis,may_dispatch,finished,phase,created_millis,updated_millis)
		VALUES ($1,$2,$3,$4,1,$5,$6,FALSE,FALSE,$7,$8,$8) ON CONFLICT (gateway_code,request_id) DO NOTHING`,
		lease.gateway, lease.requestID, string(body), digest, lease.owner, now+oauthRecoveryOwnerTTL.Milliseconds(), snapshot.PendingPhase, now)
	if err != nil {
		return nil, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return nil, errors.Join(ErrOAuthInvocationConflict, err)
	}
	return lease, nil
}

func (l *oauthInvocationRecoveryLease) checkpoint(ctx context.Context, snapshot OAuthInvocationRequestSnapshot, dispatch bool) error {
	body, digest, err := oauthRecoveryPayload(snapshot)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthRecoveryDBTimeout)
	defer cancel()
	result, err := l.outbox.db.ExecContext(queryCtx, `UPDATE gateway_lite_oauth_invocation_recovery
		SET snapshot_payload=$5,snapshot_sha256=$6,generation=generation+1,phase=$7,
		may_dispatch=(may_dispatch OR $8),owner_until_millis=$9,updated_millis=$10
		WHERE gateway_code=$1 AND request_id=$2 AND owner_token=$3 AND generation=$4 AND finished=FALSE AND state='pending' AND claim_token=''`,
		l.gateway, l.requestID, l.owner, l.generation, string(body), digest, snapshot.PendingPhase, dispatch, now+oauthRecoveryOwnerTTL.Milliseconds(), now)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errors.Join(ErrOAuthInvocationConflict, err)
	}
	l.generation++
	return nil
}

func (l *oauthInvocationRecoveryLease) finish(ctx context.Context, snapshot OAuthInvocationRequestSnapshot) error {
	body, digest, err := oauthRecoveryPayload(snapshot)
	if err != nil {
		return err
	}
	queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthRecoveryDBTimeout)
	defer cancel()
	result, err := l.outbox.db.ExecContext(queryCtx, `UPDATE gateway_lite_oauth_invocation_recovery
		SET snapshot_payload=$5,snapshot_sha256=$6,generation=generation+1,phase=$7,finished=TRUE,
		owner_token='',owner_until_millis=0,next_retry_millis=0,updated_millis=$8
		WHERE gateway_code=$1 AND request_id=$2 AND owner_token=$3 AND generation=$4 AND state='pending' AND claim_token=''`,
		l.gateway, l.requestID, l.owner, l.generation, string(body), digest, snapshot.PendingPhase, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errors.Join(ErrOAuthInvocationConflict, err)
	}
	l.generation++
	return nil
}

// checkpointLocked is called under the delegation mutex before every remote
// mutation and after its result. A SQL error cannot be bypassed by a later send.
func (r *RequestOAuthInvocation) checkpointLocked(ctx context.Context, dispatch bool) error {
	if r.recovery == nil {
		return nil
	}
	if err := r.recovery.checkpoint(ctx, r.snapshot, dispatch); err != nil {
		r.ready = false
		r.blocked = true
		return errors.Join(ErrOAuthInvocationUnavailable, err)
	}
	return nil
}

func (o *OAuthInvocationRecoveryOutbox) claimNext(ctx context.Context) (*oauthInvocationRecoveryItem, error) {
	if o == nil || o.db == nil || o.control == nil {
		return nil, ErrOAuthInvocationUnavailable
	}
	queryCtx, cancel := context.WithTimeout(ctx, oauthRecoveryDBTimeout)
	defer cancel()
	now, claim := time.Now().UnixMilli(), uuid.NewString()
	// One conditional UPDATE is portable to PostgreSQL and real SQLite tests.
	// Contending workers may see no row, but can never own the same generation.
	row := o.db.QueryRowContext(queryCtx, `UPDATE gateway_lite_oauth_invocation_recovery SET
		claim_token=$1,claim_until_millis=$2,owner_token='',generation=generation+1,updated_millis=$3
		WHERE (gateway_code,request_id)=(SELECT gateway_code,request_id FROM gateway_lite_oauth_invocation_recovery
		WHERE state='pending' AND next_retry_millis<=$3 AND owner_until_millis<=$3 AND claim_until_millis<=$3
		ORDER BY next_retry_millis,created_millis LIMIT 1)
		AND state='pending' AND next_retry_millis<=$3 AND owner_until_millis<=$3 AND claim_until_millis<=$3
		RETURNING gateway_code,request_id,generation,snapshot_payload,snapshot_sha256,may_dispatch,attempts`, claim, now+oauthRecoveryClaimTTL.Milliseconds(), now)
	item := &oauthInvocationRecoveryItem{claim: claim}
	if err := row.Scan(&item.gateway, &item.requestID, &item.generation, &item.payload, &item.digest, &item.mayDispatch, &item.attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return item, nil
}

func (o *OAuthInvocationRecoveryOutbox) saveClaim(ctx context.Context, item *oauthInvocationRecoveryItem, s OAuthInvocationRequestSnapshot, state string, retryErr error) error {
	body, digest, err := oauthRecoveryPayload(s)
	if err != nil {
		return err
	}
	next := int64(0)
	if state == "pending" && retryErr != nil {
		next = time.Now().Add(usageOutboxRetryDelay(item.attempts + 1)).UnixMilli()
	}
	// Store only fixed error categories: remote errors may contain credentials or
	// bodies. The persisted snapshot already holds the exact retryable operation.
	code := ""
	if retryErr != nil {
		code = "financial_recovery_pending"
		if state == "attention" {
			code = "execution_outcome_unknown"
		}
		if errors.Is(retryErr, ErrOAuthInvocationConflict) {
			code = "financial_identity_conflict"
		}
	}
	queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthRecoveryDBTimeout)
	defer cancel()
	result, err := o.db.ExecContext(queryCtx, `UPDATE gateway_lite_oauth_invocation_recovery
		SET snapshot_payload=$5,snapshot_sha256=$6,generation=generation+1,phase=$7,state=$8,
		attempts=attempts+1,next_retry_millis=$9,last_error=$10,updated_millis=$11
		WHERE gateway_code=$1 AND request_id=$2 AND claim_token=$3 AND generation=$4`,
		item.gateway, item.requestID, item.claim, item.generation, string(body), digest, s.PendingPhase, state, next, code, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("%w: recovery claim changed", ErrOAuthInvocationConflict)
	}
	item.generation++
	item.payload = body
	return nil
}

func (o *OAuthInvocationRecoveryOutbox) releaseClaim(ctx context.Context, item *oauthInvocationRecoveryItem) error {
	queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthRecoveryDBTimeout)
	defer cancel()
	_, err := o.db.ExecContext(queryCtx, `UPDATE gateway_lite_oauth_invocation_recovery SET claim_token='',claim_until_millis=0
		WHERE gateway_code=$1 AND request_id=$2 AND claim_token=$3 AND generation=$4`, item.gateway, item.requestID, item.claim, item.generation)
	return err
}
