package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/google/uuid"
)

// 历史预算表由既有迁移保留；运行链不再读写预算，只持久化真实违规处置。
const ContentModerationAISchema = `
CREATE TABLE IF NOT EXISTS content_moderation_ai_penalty_outbox (
 request_id TEXT PRIMARY KEY, payload JSONB NOT NULL, status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','delivered','dead')),
 attempts INTEGER NOT NULL DEFAULT 0, next_retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 claim_token TEXT NOT NULL DEFAULT '', claimed_at TIMESTAMPTZ, last_error TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS content_moderation_ai_penalty_due ON content_moderation_ai_penalty_outbox(next_retry_at) WHERE status='pending';
`

type SQLContentModerationAIStore struct{ db ContentModerationAISQLDB }

func NewSQLContentModerationAIStore(db ContentModerationAISQLDB) *SQLContentModerationAIStore {
	return &SQLContentModerationAIStore{db: db}
}
func (s *SQLContentModerationAIStore) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return aiError("ai_penalty_store_unavailable", 503)
	}
	_, err := s.db.ExecContext(ctx, ContentModerationAISchema)
	return err
}

func (s *SQLContentModerationAIStore) EnqueuePenalty(ctx context.Context, req gatewaylite.ContentModerationPenaltyRequest) error {
	if s == nil || s.db == nil || req.RequestID == "" || req.KeyID == "" {
		return aiError("ai_penalty_store_unavailable", 503)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO content_moderation_ai_penalty_outbox(request_id,payload) VALUES($1,$2)
 ON CONFLICT(request_id) DO UPDATE SET request_id=EXCLUDED.request_id
 WHERE content_moderation_ai_penalty_outbox.payload-'created_at_millis'=EXCLUDED.payload-'created_at_millis'`, req.RequestID, payload)
	if err != nil {
		return aiError("ai_penalty_store_unavailable", 503)
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return aiError("ai_penalty_identity_conflict", 503)
	}
	return nil
}

func (s *SQLContentModerationAIStore) ClaimPenalty(ctx context.Context, now time.Time) (*ContentModerationAIPenaltyEvent, error) {
	if s == nil || s.db == nil {
		return nil, aiError("ai_penalty_store_unavailable", 503)
	}
	token := uuid.NewString()
	var payload []byte
	event := &ContentModerationAIPenaltyEvent{Token: token}
	err := s.db.QueryRowContext(ctx, `WITH due AS (
 SELECT request_id FROM content_moderation_ai_penalty_outbox WHERE status='pending' AND next_retry_at<=$1
 AND (claim_token='' OR claimed_at<$1-INTERVAL '30 seconds') ORDER BY next_retry_at,request_id FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE content_moderation_ai_penalty_outbox AS o SET claim_token=$2,claimed_at=$1,updated_at=$1 FROM due WHERE o.request_id=due.request_id RETURNING o.payload,o.attempts`, now, token).Scan(&payload, &event.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload, &event.Request); err != nil {
		return nil, err
	}
	return event, nil
}

func (s *SQLContentModerationAIStore) FinishPenalty(ctx context.Context, event ContentModerationAIPenaltyEvent, sendErr error, retryable bool) error {
	if s == nil || s.db == nil {
		return aiError("ai_penalty_store_unavailable", 503)
	}
	status, code := "delivered", ""
	if sendErr != nil {
		status = "dead"
		if retryable {
			status = "pending"
		}
		code = "penalty_delivery_failed"
	}
	attempt := event.Attempts + 1
	delay := time.Second * time.Duration(1<<min(attempt, 8))
	_, err := s.db.ExecContext(ctx, `UPDATE content_moderation_ai_penalty_outbox SET status=$3,attempts=attempts+1,next_retry_at=$4,last_error=$5,claim_token='',claimed_at=NULL,updated_at=NOW() WHERE request_id=$1 AND claim_token=$2`, event.Request.RequestID, event.Token, status, time.Now().Add(delay), code)
	return err
}

func (s *SQLContentModerationAIStore) Stats(ctx context.Context, _ string, _ time.Time) (ContentModerationAIStoreStats, error) {
	var stats ContentModerationAIStoreStats
	if s == nil || s.db == nil {
		return stats, aiError("ai_penalty_store_unavailable", 503)
	}
	err := s.db.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM content_moderation_ai_penalty_outbox WHERE status='pending'),
 (SELECT COUNT(*) FROM content_moderation_ai_penalty_outbox WHERE status='dead')`).Scan(&stats.PenaltyPending, &stats.PenaltyDead)
	return stats, err
}

func contentModerationAIPenaltyRetryable(err error) bool {
	var httpErr *gatewaylite.ControlPlaneHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == 408 || httpErr.StatusCode == 425 || httpErr.StatusCode == 429 || httpErr.StatusCode >= 500
	}
	return err != nil
}

func contentModerationAINodeScope(node string) string {
	if node = strings.TrimSpace(node); node != "" {
		return node
	}
	if runtime := gatewaylite.DefaultRuntimeConfigRef(); runtime != nil {
		if configured := strings.TrimSpace(runtime.Current().GatewayScope()); configured != "" {
			return configured
		}
	}
	return "standalone"
}

func contentModerationAISubject(input ContentModerationCheckInput) (string, string, error) {
	node := contentModerationAINodeScope(input.GatewayNodeID)
	if input.UserID <= 0 {
		return "", "", aiError("ai_missing_subject", 503)
	}
	if input.ControlPlaneKeyID != "" {
		if input.OrganizationID > 0 {
			if input.BillingSubjectType != "organization" || input.BillingSubjectID != input.OrganizationID {
				return "", "", aiError("ai_invalid_subject", 503)
			}
			return node, fmt.Sprintf("organization:%d", input.OrganizationID), nil
		}
		if input.BillingSubjectType != "user" || input.BillingSubjectID != input.UserID {
			return "", "", aiError("ai_invalid_subject", 503)
		}
		return node, fmt.Sprintf("user:%d", input.UserID), nil
	}
	if input.OrganizationID > 0 || strings.TrimSpace(input.BillingSubjectType) != "" || input.BillingSubjectID > 0 || input.APIKeyID <= 0 {
		return "", "", aiError("ai_invalid_subject", 503)
	}
	return node, fmt.Sprintf("local-user:%d", input.UserID), nil
}
