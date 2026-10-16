package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type contentModerationRepository struct {
	db *sql.DB
}

func NewContentModerationRepository(db *sql.DB) *contentModerationRepository {
	return &contentModerationRepository{db: db}
}

func (r *contentModerationRepository) CreateLog(ctx context.Context, log *service.ContentModerationLog) error {
	if log == nil {
		return nil
	}
	categoryScores, err := json.Marshal(log.CategoryScores)
	if err != nil {
		return fmt.Errorf("marshal moderation category scores: %w", err)
	}
	thresholdSnapshot, err := json.Marshal(log.ThresholdSnapshot)
	if err != nil {
		return fmt.Errorf("marshal moderation thresholds: %w", err)
	}
	detectedLanguages, err := json.Marshal(log.DetectedLanguages)
	if err != nil {
		return fmt.Errorf("marshal detected languages: %w", err)
	}
	languageEvidence, err := json.Marshal(log.LanguageEvidence)
	if err != nil {
		return fmt.Errorf("marshal language evidence: %w", err)
	}
	// AI 判定证据可整体为空（非 AI 引擎路径）；为空时落 {}，列默认值一致。
	aiReviewDetail := []byte("{}")
	if log.AIReviewDetail != nil {
		aiReviewDetail, err = json.Marshal(log.AIReviewDetail)
		if err != nil {
			return fmt.Errorf("marshal ai review detail: %w", err)
		}
	}
	var userID any
	if log.UserID != nil {
		userID = *log.UserID
	}
	var apiKeyID any
	if log.APIKeyID != nil {
		apiKeyID = *log.APIKeyID
	}
	var groupID any
	if log.GroupID != nil {
		groupID = *log.GroupID
	}
	var latency any
	if log.UpstreamLatencyMS != nil {
		latency = *log.UpstreamLatencyMS
	}
	err = r.db.QueryRowContext(ctx, `
INSERT INTO content_moderation_logs (
    request_id, user_id, user_email, organization_id, billing_subject_type,
    billing_subject_id, control_plane_key_id, api_key_id, api_key_name, group_id, group_name,
    endpoint, provider, model, mode, action, flagged, highest_category, highest_score,
    category_scores, threshold_snapshot, input_excerpt, upstream_latency_ms, error,
    violation_count, auto_banned, email_sent, queue_delay_ms,
    original_group_id, original_group_name, effective_group_id, effective_group_name,
    content_source, detected_languages, content_fingerprint, language_evidence,
    keyword_policy_rerouted, keyword_policy_reason, matched_keyword,
    ai_review_detail, review_text_excerpt, full_input_text
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
    $12, $13, $14, $15, $16, $17, $18, $19,
    $20::jsonb, $21::jsonb, $22, $23, $24,
    $25, $26, $27, $28,
    $29, $30, $31, $32, $33, $34::jsonb, $35, $36::jsonb, $37, $38, $39,
    $40::jsonb, $41, $42
) RETURNING id, created_at`,
		log.RequestID, userID, log.UserEmail, log.OrganizationID, log.BillingSubjectType,
		log.BillingSubjectID, log.ControlPlaneKeyID, apiKeyID, log.APIKeyName, groupID, log.GroupName,
		log.Endpoint, log.Provider, log.Model, log.Mode, log.Action, log.Flagged, log.HighestCategory, log.HighestScore,
		string(categoryScores), string(thresholdSnapshot), log.InputExcerpt, latency, log.Error,
		log.ViolationCount, log.AutoBanned, log.EmailSent, nullableIntPtr(log.QueueDelayMS),
		nullableInt64Ptr(log.OriginalGroupID), log.OriginalGroupName,
		nullableInt64Ptr(log.EffectiveGroupID), log.EffectiveGroupName,
		log.ContentSource, string(detectedLanguages), log.ContentFingerprint, string(languageEvidence),
		log.KeywordPolicyRerouted, log.KeywordPolicyReason, log.MatchedKeyword,
		string(aiReviewDetail), log.ReviewTextExcerpt, log.FullInputText,
	).Scan(&log.ID, &log.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert content moderation log: %w", err)
	}
	return nil
}

// 列表与详情共用元数据列和扫描顺序；两份全文仅在管理员按 ID 查详情时读取，
// 避免列表每次分页都把 PostgreSQL 的大文本加载进内存。
const contentModerationLogMetadataColumns = `
    l.id, l.request_id, l.user_id, l.user_email, l.organization_id, l.billing_subject_type,
    l.billing_subject_id, l.control_plane_key_id, l.api_key_id, l.api_key_name, l.group_id, l.group_name,
    l.endpoint, l.provider, l.model, l.mode, l.action, l.flagged, l.highest_category, l.highest_score,
    l.category_scores, l.threshold_snapshot, l.input_excerpt, l.upstream_latency_ms, l.error,
    l.violation_count, l.auto_banned, l.email_sent, COALESCE(u.status, ''), l.queue_delay_ms,
    l.original_group_id, l.original_group_name, l.effective_group_id, l.effective_group_name,
    l.content_source, l.detected_languages, l.content_fingerprint, l.language_evidence,
    l.keyword_policy_rerouted, l.keyword_policy_reason, l.matched_keyword,
    l.ai_review_detail, l.created_at`

func (r *contentModerationRepository) ListLogs(ctx context.Context, filter service.ContentModerationLogFilter) ([]service.ContentModerationLog, *pagination.PaginationResult, error) {
	where, args := buildContentModerationLogWhere(filter)
	whereSQL := "WHERE " + strings.Join(where, " AND ")

	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM content_moderation_logs l "+whereSQL, args...).Scan(&total); err != nil {
		return nil, nil, fmt.Errorf("count content moderation logs: %w", err)
	}

	params := filter.Pagination
	if params.Page <= 0 {
		params.Page = 1
	}
	if params.PageSize <= 0 {
		params.PageSize = 20
	}
	if params.PageSize > 100 {
		params.PageSize = 100
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, params.Limit(), params.Offset())
	rows, err := r.db.QueryContext(ctx, `
SELECT `+contentModerationLogMetadataColumns+`
FROM content_moderation_logs l
LEFT JOIN users u ON u.id = l.user_id `+whereSQL+`
ORDER BY l.created_at DESC, l.id DESC
LIMIT $`+fmt.Sprint(len(queryArgs)-1)+` OFFSET $`+fmt.Sprint(len(queryArgs)),
		queryArgs...,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("list content moderation logs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]service.ContentModerationLog, 0)
	for rows.Next() {
		item, err := scanContentModerationLog(rows, false)
		if err != nil {
			return nil, nil, fmt.Errorf("scan content moderation log: %w", err)
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate content moderation logs: %w", err)
	}
	return items, paginationResultFromTotal(total, params), nil
}

// GetLog 为详情提供可选能力，不扩大 service.ContentModerationRepository，
// 使只用于写入或列表的现有替身不必实现全文读取。
func (r *contentModerationRepository) GetLog(ctx context.Context, id int64) (*service.ContentModerationLog, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT `+contentModerationLogMetadataColumns+`, l.full_input_text, l.review_text_excerpt
FROM content_moderation_logs l
LEFT JOIN users u ON u.id = l.user_id
WHERE l.id = $1`, id)
	item, err := scanContentModerationLog(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get content moderation log: %w", err)
	}
	return item, nil
}

type contentModerationLogScanner interface {
	Scan(dest ...any) error
}

func scanContentModerationLog(row contentModerationLogScanner, includeFullText bool) (*service.ContentModerationLog, error) {
	var item service.ContentModerationLog
	var userID, apiKeyID, groupID, latency, queueDelay, originalGroupID, effectiveGroupID sql.NullInt64
	var scoresRaw, thresholdsRaw, languagesRaw, languageEvidenceRaw, aiReviewDetailRaw []byte
	dest := []any{
		&item.ID,
		&item.RequestID,
		&userID,
		&item.UserEmail,
		&item.OrganizationID,
		&item.BillingSubjectType,
		&item.BillingSubjectID,
		&item.ControlPlaneKeyID,
		&apiKeyID,
		&item.APIKeyName,
		&groupID,
		&item.GroupName,
		&item.Endpoint,
		&item.Provider,
		&item.Model,
		&item.Mode,
		&item.Action,
		&item.Flagged,
		&item.HighestCategory,
		&item.HighestScore,
		&scoresRaw,
		&thresholdsRaw,
		&item.InputExcerpt,
		&latency,
		&item.Error,
		&item.ViolationCount,
		&item.AutoBanned,
		&item.EmailSent,
		&item.UserStatus,
		&queueDelay,
		&originalGroupID,
		&item.OriginalGroupName,
		&effectiveGroupID,
		&item.EffectiveGroupName,
		&item.ContentSource,
		&languagesRaw,
		&item.ContentFingerprint,
		&languageEvidenceRaw,
		&item.KeywordPolicyRerouted,
		&item.KeywordPolicyReason,
		&item.MatchedKeyword,
		&aiReviewDetailRaw,
		&item.CreatedAt,
	}
	if includeFullText {
		dest = append(dest, &item.FullInputText, &item.ReviewTextExcerpt)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	if userID.Valid {
		item.UserID = &userID.Int64
	}
	if apiKeyID.Valid {
		item.APIKeyID = &apiKeyID.Int64
	}
	if groupID.Valid {
		item.GroupID = &groupID.Int64
	}
	if latency.Valid {
		v := int(latency.Int64)
		item.UpstreamLatencyMS = &v
	}
	if queueDelay.Valid {
		v := int(queueDelay.Int64)
		item.QueueDelayMS = &v
	}
	if originalGroupID.Valid {
		item.OriginalGroupID = &originalGroupID.Int64
	}
	if effectiveGroupID.Valid {
		item.EffectiveGroupID = &effectiveGroupID.Int64
	}
	item.CategoryScores = map[string]float64{}
	_ = json.Unmarshal(scoresRaw, &item.CategoryScores)
	item.ThresholdSnapshot = map[string]float64{}
	_ = json.Unmarshal(thresholdsRaw, &item.ThresholdSnapshot)
	item.DetectedLanguages = []string{}
	_ = json.Unmarshal(languagesRaw, &item.DetectedLanguages)
	_ = json.Unmarshal(languageEvidenceRaw, &item.LanguageEvidence)
	if len(aiReviewDetailRaw) > 2 {
		// 空 {} 不反序列化，保持 nil 让 JSON 响应省略该字段。
		_ = json.Unmarshal(aiReviewDetailRaw, &item.AIReviewDetail)
	}
	return &item, nil
}

func (r *contentModerationRepository) CountFlaggedBySubjectSince(ctx context.Context, scope service.ContentModerationPenaltyScope, since time.Time, excludeCyberPolicy bool) (int, error) {
	if scope.UserID <= 0 {
		return 0, nil
	}
	// 只用已明确会触发处置的 action 白名单计数。language_block 等日志虽然
	// flagged=true，但只拒绝当次请求，不能在后续真实违规时间接推高封号计数。
	// SQL 中的 action 字面量须与 service.contentModerationShouldApplyUserPenalty 保持一致。
	var count int
	err := r.db.QueryRowContext(ctx, `
WITH last_auto_ban AS (
    SELECT MAX(created_at) AS at
    FROM content_moderation_logs
    WHERE user_id = $1
      AND organization_id = $4
      AND auto_banned = TRUE
)
SELECT COUNT(*)
FROM content_moderation_logs
WHERE user_id = $1
  AND organization_id = $4
  AND flagged = TRUE
  AND (
    action IN ('block', 'keyword_block')
    OR ($3::bool IS FALSE AND action = 'cyber_policy' AND mode <> 'cyber_log_only')
  )
  AND created_at >= $2
  AND created_at > COALESCE((SELECT at FROM last_auto_ban), '-infinity'::timestamptz)
`, scope.UserID, since, excludeCyberPolicy, scope.OrganizationID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count user content moderation flagged logs: %w", err)
	}
	return count, nil
}

func (r *contentModerationRepository) CountFlaggedByUserSince(ctx context.Context, userID int64, since time.Time, excludeCyberPolicy bool) (int, error) {
	if userID <= 0 {
		return 0, nil
	}
	var count int
	err := r.db.QueryRowContext(ctx, `
WITH last_auto_ban AS (
    SELECT MAX(created_at) AS at
    FROM content_moderation_logs
    WHERE user_id = $1 AND organization_id = 0 AND auto_banned = TRUE
)
SELECT COUNT(*)
FROM content_moderation_logs
WHERE user_id = $1
  AND organization_id = 0
  AND flagged = TRUE
  AND (
    action IN ('block', 'keyword_block')
    OR ($3::bool IS FALSE AND action = 'cyber_policy' AND mode <> 'cyber_log_only')
  )
  AND created_at >= $2
  AND created_at > COALESCE((SELECT at FROM last_auto_ban), '-infinity'::timestamptz)
`, userID, since, excludeCyberPolicy).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count user content moderation flagged logs: %w", err)
	}
	return count, nil
}

func (r *contentModerationRepository) RecordUserPenalty(ctx context.Context, input service.ContentModerationRecordUserPenaltyInput) (*service.ContentModerationUserPenalty, error) {
	if input.UserID <= 0 {
		return nil, nil
	}
	if input.DeductBalance < 0 {
		input.DeductBalance = 0
	}
	if input.PermanentBanThreshold <= 0 {
		input.PermanentBanEnabled = false
	}
	var item service.ContentModerationUserPenalty
	var lastBlockedAt, permanentlyBannedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `
INSERT INTO content_moderation_user_penalties (
    user_id, blocked_count, deducted_balance, permanently_banned, permanent_ban_reason,
    last_blocked_at, permanently_banned_at, created_at, updated_at
) VALUES (
    $1, 1, $2, $3::bool AND 1 >= $4, CASE WHEN $3::bool AND 1 >= $4 THEN $5 ELSE '' END,
    NOW(), CASE WHEN $3::bool AND 1 >= $4 THEN NOW() ELSE NULL END, NOW(), NOW()
)
ON CONFLICT (user_id) DO UPDATE SET
    blocked_count = content_moderation_user_penalties.blocked_count + 1,
    deducted_balance = content_moderation_user_penalties.deducted_balance + EXCLUDED.deducted_balance,
    permanently_banned = content_moderation_user_penalties.permanently_banned
        OR ($3::bool AND content_moderation_user_penalties.blocked_count + 1 >= $4),
    permanent_ban_reason = CASE
        WHEN content_moderation_user_penalties.permanently_banned THEN content_moderation_user_penalties.permanent_ban_reason
        WHEN $3::bool AND content_moderation_user_penalties.blocked_count + 1 >= $4 THEN $5
        ELSE content_moderation_user_penalties.permanent_ban_reason
    END,
    last_blocked_at = NOW(),
    permanently_banned_at = CASE
        WHEN content_moderation_user_penalties.permanently_banned THEN content_moderation_user_penalties.permanently_banned_at
        WHEN $3::bool AND content_moderation_user_penalties.blocked_count + 1 >= $4 THEN NOW()
        ELSE content_moderation_user_penalties.permanently_banned_at
    END,
    updated_at = NOW()
RETURNING user_id, blocked_count, deducted_balance::double precision, permanently_banned,
          permanent_ban_reason, last_blocked_at, permanently_banned_at
`, input.UserID, input.DeductBalance, input.PermanentBanEnabled, input.PermanentBanThreshold, strings.TrimSpace(input.PermanentBanReason)).
		Scan(&item.UserID, &item.BlockedCount, &item.DeductedBalance, &item.PermanentlyBanned, &item.PermanentBanReason, &lastBlockedAt, &permanentlyBannedAt)
	if err != nil {
		return nil, fmt.Errorf("record content moderation user penalty: %w", err)
	}
	if lastBlockedAt.Valid {
		item.LastBlockedAt = &lastBlockedAt.Time
	}
	if permanentlyBannedAt.Valid {
		item.PermanentlyBannedAt = &permanentlyBannedAt.Time
	}
	return &item, nil
}

func (r *contentModerationRepository) IsUserPermanentlyBanned(ctx context.Context, userID int64) (bool, error) {
	if userID <= 0 {
		return false, nil
	}
	var banned bool
	err := r.db.QueryRowContext(ctx, `
SELECT permanently_banned
FROM content_moderation_user_penalties
WHERE user_id = $1
`, userID).Scan(&banned)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("get content moderation permanent ban state: %w", err)
	}
	return banned, nil
}

func (r *contentModerationRepository) UpdateLogEmailSent(ctx context.Context, id int64, sent bool) error {
	_, err := r.db.ExecContext(ctx, `UPDATE content_moderation_logs SET email_sent = $1 WHERE id = $2`, sent, id)
	if err != nil {
		return fmt.Errorf("update content moderation log email_sent: %w", err)
	}
	return nil
}

func (r *contentModerationRepository) CleanupExpiredLogs(ctx context.Context, hitBefore time.Time, nonHitBefore time.Time) (*service.ContentModerationCleanupResult, error) {
	result := &service.ContentModerationCleanupResult{FinishedAt: time.Now()}
	if r == nil || r.db == nil {
		return result, nil
	}
	hitExec, err := r.db.ExecContext(ctx, `
DELETE FROM content_moderation_logs
WHERE flagged = TRUE AND created_at < $1
`, hitBefore)
	if err != nil {
		return nil, fmt.Errorf("delete expired hit content moderation logs: %w", err)
	}
	result.DeletedHit, _ = hitExec.RowsAffected()

	nonHitExec, err := r.db.ExecContext(ctx, `
DELETE FROM content_moderation_logs
WHERE flagged = FALSE AND created_at < $1
`, nonHitBefore)
	if err != nil {
		return nil, fmt.Errorf("delete expired non-hit content moderation logs: %w", err)
	}
	result.DeletedNonHit, _ = nonHitExec.RowsAffected()

	result.FinishedAt = time.Now()
	return result, nil
}

func nullableIntPtr(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64Ptr(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func buildContentModerationLogWhere(filter service.ContentModerationLogFilter) ([]string, []any) {
	where := []string{"l.id IS NOT NULL"}
	args := make([]any, 0)
	add := func(expr string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(expr, len(args)))
	}
	switch strings.ToLower(strings.TrimSpace(filter.Result)) {
	case "hit", "flagged":
		// 历史关键词放行和观察模式违规都可能 flagged=false；它们仍属于命中，
		// 但 ai_shadow_block 只观察，不能因此混入真实拒绝筛选。
		where = append(where, "(l.flagged = TRUE OR l.action IN ('keyword_audit', 'keyword_allow_original', 'keyword_reroute', 'ai_shadow_block'))")
	case "blocked", "block":
		// 审查失败按 fail-closed（失败拒绝）处理，包括观察模式失败；
		// 观察模式的有效违规判定 ai_shadow_block 本身不拒绝请求。
		where = append(where, "l.action IN ('block', 'keyword_block', 'hash_block', 'language_block', 'cyber_policy', 'error', 'ai_shadow_error', 'shadow_error')")
	case "pass", "allow":
		where = append(where, "l.action IN ('allow', 'ai_shadow_allow') AND l.flagged = FALSE AND l.error = ''")
	case "error":
		// 有效拦截后的处罚投递错误也会写 error；这不代表审查未判定。
		// 失败 action 即使缺少错误文案也要显示，旧数据再用 error 非空补齐。
		where = append(where, "(l.action IN ('error', 'ai_shadow_error', 'shadow_error') OR (l.error <> '' AND l.action NOT IN ('block', 'keyword_block', 'hash_block', 'language_block', 'cyber_policy', 'ai_shadow_block')))")
	}
	if filter.GroupID != nil {
		add("l.group_id = $%d", *filter.GroupID)
	}
	if filter.OriginalGroupID != nil {
		add("l.original_group_id = $%d", *filter.OriginalGroupID)
	}
	if filter.EffectiveGroupID != nil {
		add("l.effective_group_id = $%d", *filter.EffectiveGroupID)
	}
	if source := strings.TrimSpace(filter.ContentSource); source != "" {
		add("l.content_source = $%d", source)
	}
	if language := strings.TrimSpace(filter.DetectedLanguage); language != "" {
		add("l.detected_languages ? $%d", language)
	}
	if reason := strings.TrimSpace(filter.RouteReason); reason != "" {
		add("l.keyword_policy_reason = $%d", reason)
	}
	if endpoint := strings.TrimSpace(filter.Endpoint); endpoint != "" {
		add("l.endpoint = $%d", endpoint)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		args = append(args, "%"+search+"%")
		idx := len(args)
		// WHERE 可搜索两份全文，但 SELECT 仍只返回轻量预览与判定元数据。
		where = append(where, fmt.Sprintf("(l.request_id ILIKE $%[1]d OR l.user_email ILIKE $%[1]d OR l.api_key_name ILIKE $%[1]d OR l.model ILIKE $%[1]d OR l.input_excerpt ILIKE $%[1]d OR l.full_input_text ILIKE $%[1]d OR l.review_text_excerpt ILIKE $%[1]d OR l.error ILIKE $%[1]d)", idx))
	}
	if filter.From != nil && !filter.From.IsZero() {
		add("l.created_at >= $%d", *filter.From)
	}
	if filter.To != nil && !filter.To.IsZero() {
		add("l.created_at <= $%d", *filter.To)
	}
	return where, args
}
