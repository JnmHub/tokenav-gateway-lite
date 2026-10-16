package gatewaylite

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestDurableContentModerationPenaltyPayloadConflictPostgreSQL(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("SUB2API_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set SUB2API_TEST_DATABASE_URL to run PostgreSQL integration test")
	}
	db, err := sql.Open("postgres", databaseURL)
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	schema := "penalty_outbox_" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	_, err = db.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })
	_, err = db.ExecContext(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migration, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations", "217_content_moderation_penalty_outbox.sql"))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)

	reporter := NewDurableContentModerationPenaltyReporter(db, nil)
	original := penaltyRequest("penalty-postgres-conflict")
	require.NoError(t, reporter.persist(original, context.DeadlineExceeded, true))
	require.NoError(t, reporter.persist(original, context.DeadlineExceeded, true), "same request must remain idempotent")

	conflicting := original
	conflicting.OrganizationID = 9001
	conflicting.DeductBalanceCents = 500
	err = reporter.persist(conflicting, context.DeadlineExceeded, true)
	require.ErrorIs(t, err, errContentModerationPenaltyPayloadConflict)

	var storedPayload []byte
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT event_payload
		FROM gateway_lite_content_moderation_penalty_outbox
		WHERE request_id = $1
	`, original.RequestID).Scan(&storedPayload))
	var stored ContentModerationPenaltyRequest
	require.NoError(t, json.Unmarshal(storedPayload, &stored))
	require.Equal(t, original, stored, "conflicting retry must not replace the original penalty")
}
