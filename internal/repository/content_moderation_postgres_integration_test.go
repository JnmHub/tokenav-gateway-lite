package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestContentModerationRepositoryStoresKeywordRouteEvidencePostgreSQL(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("SUB2API_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set SUB2API_TEST_DATABASE_URL to run PostgreSQL integration test")
	}
	db, err := sql.Open("postgres", databaseURL)
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	schema := fmt.Sprintf("keyword_policy_%d", time.Now().UnixNano())
	_, err = db.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })
	_, err = db.ExecContext(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
CREATE TABLE users (id BIGINT PRIMARY KEY, status TEXT NOT NULL DEFAULT 'active');
CREATE TABLE groups (id BIGINT PRIMARY KEY);
CREATE TABLE content_moderation_logs (
 id BIGSERIAL PRIMARY KEY, request_id TEXT NOT NULL DEFAULT '', user_id BIGINT REFERENCES users(id) ON DELETE SET NULL, user_email TEXT NOT NULL DEFAULT '',
 api_key_id BIGINT, api_key_name TEXT NOT NULL DEFAULT '', group_id BIGINT, group_name TEXT NOT NULL DEFAULT '',
 endpoint TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', mode TEXT NOT NULL DEFAULT '',
 action TEXT NOT NULL DEFAULT '', flagged BOOLEAN NOT NULL DEFAULT false, highest_category TEXT NOT NULL DEFAULT '', highest_score DOUBLE PRECISION NOT NULL DEFAULT 0,
 category_scores JSONB NOT NULL DEFAULT '{}'::jsonb, threshold_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
 input_excerpt TEXT NOT NULL DEFAULT '', upstream_latency_ms INTEGER, error TEXT NOT NULL DEFAULT '', violation_count INTEGER NOT NULL DEFAULT 0,
 auto_banned BOOLEAN NOT NULL DEFAULT false, email_sent BOOLEAN NOT NULL DEFAULT false, queue_delay_ms INTEGER, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`)
	require.NoError(t, err)

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migration, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations", "174_group_allow_blocked_keyword_requests.sql"))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err, "migration must be repeatable")
	remoteUserMigration, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations", "175_content_moderation_remote_user.sql"))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(remoteUserMigration))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(remoteUserMigration))
	require.NoError(t, err, "remote user migration must be repeatable")
	_, err = db.ExecContext(ctx, `
INSERT INTO content_moderation_logs (request_id, action, input_excerpt) VALUES
('legacy-language-block', 'language_block', 'customer language content'),
('legacy-keyword-block', 'keyword_block', 'redacted keyword summary')`)
	require.NoError(t, err)
	languageEvidenceMigration, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations", "183_content_moderation_language_evidence.sql"))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(languageEvidenceMigration))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(languageEvidenceMigration))
	require.NoError(t, err, "language evidence migration must be repeatable")
	matchedKeywordMigration, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations", "184_content_moderation_matched_keyword.sql"))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(matchedKeywordMigration))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(matchedKeywordMigration))
	require.NoError(t, err, "matched keyword migration must be repeatable")
	var legacyLanguageExcerpt, legacyKeywordExcerpt string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT input_excerpt FROM content_moderation_logs WHERE request_id = 'legacy-language-block'`).Scan(&legacyLanguageExcerpt))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT input_excerpt FROM content_moderation_logs WHERE request_id = 'legacy-keyword-block'`).Scan(&legacyKeywordExcerpt))
	require.Empty(t, legacyLanguageExcerpt, "language evidence migration must purge legacy customer excerpts")
	require.Equal(t, "redacted keyword summary", legacyKeywordExcerpt, "non-language audit excerpts must remain unchanged")
	var foreignKeyCount int
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM pg_constraint
WHERE conrelid = 'content_moderation_logs'::regclass AND conname = 'content_moderation_logs_user_id_fkey'
`).Scan(&foreignKeyCount))
	require.Zero(t, foreignKeyCount)
	require.NoError(t, err)

	// Keep this isolated legacy-table fixture aligned with the repository's
	// current enterprise/member identity columns by exercising the real migration.
	identityMigration, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations", "216_content_moderation_enterprise_identity.sql"))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(identityMigration))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(identityMigration))
	require.NoError(t, err, "enterprise identity migration must be repeatable")
	originalID, effectiveID := int64(10), int64(20)
	repo := NewContentModerationRepository(db)
	log := &service.ContentModerationLog{
		RequestID: "route-evidence", UserID: int64PtrForModerationTest(9007), UserEmail: "remote-user@example.test",
		OrganizationID: 90, BillingSubjectType: "organization", BillingSubjectID: 90, ControlPlaneKeyID: "remote-stable-key",
		GroupID: &effectiveID, GroupName: "允许分组", OriginalGroupID: &originalID, OriginalGroupName: "严格分组",
		EffectiveGroupID: &effectiveID, EffectiveGroupName: "允许分组", ContentSource: service.ContentModerationSourceSkill,
		DetectedLanguages: []string{"en", "zh", "mixed"}, ContentFingerprint: "v1:0123456789abcdef0123456789abcdef",
		LanguageEvidence: service.ContentModerationLanguageEvidence{
			DetectorVersion: "v2", TotalLetterRunes: 20,
			LanguageCounts: map[string]int{"en": 12, "zh": 8}, LanguageRatioBPS: map[string]int{"en": 6000, "zh": 4000},
		}, KeywordPolicyRerouted: true, KeywordPolicyReason: "rerouted",
		MatchedKeyword: "exp",
		Endpoint:       "/v1/responses", Provider: "openai", Model: "gpt-test", Mode: "pre_block", Action: service.ContentModerationActionKeywordReroute,
		CategoryScores: map[string]float64{"keyword": 1}, ThresholdSnapshot: map[string]float64{},
	}
	require.NoError(t, repo.CreateLog(ctx, log))

	items, page, err := repo.ListLogs(ctx, service.ContentModerationLogFilter{
		Pagination: pagination.PaginationParams{Page: 1, PageSize: 10}, OriginalGroupID: &originalID,
		EffectiveGroupID: &effectiveID, ContentSource: "skill", DetectedLanguage: "zh", RouteReason: "rerouted",
	})
	require.NoError(t, err)
	require.NotNil(t, page)
	require.Len(t, items, 1)
	got := items[0]
	require.EqualValues(t, 90, got.OrganizationID)
	require.Equal(t, "organization", got.BillingSubjectType)
	require.EqualValues(t, 90, got.BillingSubjectID)
	require.Equal(t, "remote-stable-key", got.ControlPlaneKeyID)
	require.Equal(t, fmt.Sprint(originalID), fmt.Sprint(*got.OriginalGroupID))
	require.Equal(t, fmt.Sprint(effectiveID), fmt.Sprint(*got.EffectiveGroupID))
	require.Equal(t, []string{"en", "zh", "mixed"}, got.DetectedLanguages)
	require.Equal(t, "v1:0123456789abcdef0123456789abcdef", got.ContentFingerprint)
	require.Equal(t, 20, got.LanguageEvidence.TotalLetterRunes)
	require.Equal(t, 12, got.LanguageEvidence.LanguageCounts["en"])
	require.True(t, got.KeywordPolicyRerouted)
	require.Equal(t, "exp", got.MatchedKeyword)
}

func int64PtrForModerationTest(value int64) *int64 { return &value }
