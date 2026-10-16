package gatewaylite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
)

const bindingMediaSchema = `CREATE TABLE IF NOT EXISTS gateway_lite_binding_media (
 binding_id TEXT NOT NULL, request_id TEXT NOT NULL, task_id TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'pending', updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(binding_id,request_id)
);
CREATE TABLE IF NOT EXISTS gateway_lite_binding_media_coverage (
 binding_id TEXT PRIMARY KEY, source TEXT NOT NULL, evidence_sha256 TEXT NOT NULL,
 complete_history BOOLEAN NOT NULL DEFAULT FALSE, unsettled_count BIGINT NOT NULL,
 checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`

type bindingMediaKey struct{}
type bindingMediaDispatch struct {
	manager              *BindingManager
	bindingID, requestID string
}

// Called only after auth, payload/capability validation and durable funding,
// immediately before the upstream send. A 401/400 that never dispatched cannot
// poison the binding's financial state.
func PrepareBindingMediaDispatch(ctx context.Context) (context.Context, error) {
	m, _ := ctx.Value(bindingTurnKey{}).(*BindingManager)
	if m == nil {
		return ctx, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mediaDB == nil || m.record.Current == nil {
		return nil, errors.New("binding_media_registry_unavailable")
	}
	if m.record.Phase != "bound" && !(m.record.Phase == "activating" && m.record.Current.Legacy) {
		return nil, ErrBindingDraining
	}
	if m.record.DisconnectOperation != "" {
		return nil, ErrBindingDraining
	}
	id, ok := CanonicalRequestIDFromContext(ctx)
	if !ok {
		id = uuid.NewString()
	}
	bindingID := m.record.Current.BindingID
	_, err := m.mediaDB.ExecContext(ctx, `INSERT INTO gateway_lite_binding_media(binding_id,request_id) VALUES($1,$2) ON CONFLICT(binding_id,request_id) DO NOTHING`, bindingID, id)
	if err != nil {
		return nil, errors.New("binding_media_registry_unavailable")
	}
	return context.WithValue(ctx, bindingMediaKey{}, bindingMediaDispatch{m, bindingID, id}), nil
}
func RecordBindingMediaTask(ctx context.Context, taskID string) error {
	d, ok := ctx.Value(bindingMediaKey{}).(bindingMediaDispatch)
	if !ok {
		return nil
	}
	_, err := d.manager.mediaDB.ExecContext(ctx, `UPDATE gateway_lite_binding_media SET task_id=$3,updated_at=NOW() WHERE binding_id=$1 AND request_id=$2 AND state='pending'`, d.bindingID, d.requestID, taskID)
	return err
}
func ReleaseBindingMediaBeforeAcceptance(ctx context.Context) error {
	d, ok := ctx.Value(bindingMediaKey{}).(bindingMediaDispatch)
	if !ok {
		return nil
	}
	_, err := d.manager.mediaDB.ExecContext(ctx, `UPDATE gateway_lite_binding_media SET state='released',updated_at=NOW() WHERE binding_id=$1 AND request_id=$2`, d.bindingID, d.requestID)
	return err
}
func ConfirmBindingMediaSettlement(ctx context.Context, taskID string) error {
	m := DefaultBindingManager()
	if m == nil || m.mediaDB == nil {
		return nil
	}
	r := m.Record()
	if r.Current == nil {
		return nil
	}
	_, err := m.mediaDB.ExecContext(ctx, `UPDATE gateway_lite_binding_media SET state='settled',updated_at=NOW() WHERE binding_id=$1 AND task_id=$2 AND state='pending'`, r.Current.BindingID, taskID)
	return err
}
func bindingLegacyMediaEvidence(ctx context.Context, db *sql.DB, bindingID string) (bool, error) {
	var source, digest string
	var complete bool
	var unsettled int64
	err := db.QueryRowContext(ctx, `SELECT source,evidence_sha256,complete_history,unsettled_count FROM gateway_lite_binding_media_coverage WHERE binding_id=$1`, bindingID).Scan(&source, &digest, &complete, &unsettled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// This record may only be produced from complete provider/durable historical
	// reconciliation. No administrator checkbox or Redis TTL creates this proof.
	return complete && unsettled == 0 && len(digest) == 64 && strings.HasPrefix(source, "provider_terminal_reconciliation:"), nil
}
