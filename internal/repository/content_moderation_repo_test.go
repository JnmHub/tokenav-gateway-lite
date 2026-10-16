package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildContentModerationLogWhere_BlockedIncludesAllBlockActions(t *testing.T) {
	where, args := buildContentModerationLogWhere(service.ContentModerationLogFilter{Result: "blocked"})

	require.Empty(t, args)
	sql := strings.Join(where, " AND ")
	require.Contains(t, sql, "l.action IN ('block', 'keyword_block', 'hash_block', 'language_block', 'cyber_policy', 'error', 'ai_shadow_error', 'shadow_error')")
	require.NotContains(t, sql, "ai_shadow_block")
}

func TestBuildContentModerationLogWhere_PassOnlyIncludesTrueNonHits(t *testing.T) {
	where, args := buildContentModerationLogWhere(service.ContentModerationLogFilter{Result: "pass"})

	require.Empty(t, args)
	sql := strings.Join(where, " AND ")
	require.Contains(t, sql, "l.action IN ('allow', 'ai_shadow_allow') AND l.flagged = FALSE AND l.error = ''")
	require.NotContains(t, sql, "keyword_allow_original")
	require.NotContains(t, sql, "ai_shadow_block")
}

func TestBuildContentModerationLogWhere_HitIncludesKeywordAndShadowHitActions(t *testing.T) {
	where, args := buildContentModerationLogWhere(service.ContentModerationLogFilter{Result: "hit"})

	require.Empty(t, args)
	sql := strings.Join(where, " AND ")
	require.Contains(t, sql, "l.flagged = TRUE OR l.action IN ('keyword_audit', 'keyword_allow_original', 'keyword_reroute', 'ai_shadow_block')")
}

func TestBuildContentModerationLogWhere_ErrorExcludesValidBlocksWithPenaltyErrors(t *testing.T) {
	where, args := buildContentModerationLogWhere(service.ContentModerationLogFilter{Result: "error"})

	require.Empty(t, args)
	sql := strings.Join(where, " AND ")
	require.Contains(t, sql, "l.action IN ('error', 'ai_shadow_error', 'shadow_error') OR")
	require.Contains(t, sql, "l.error <> '' AND l.action NOT IN ('block', 'keyword_block', 'hash_block', 'language_block', 'cyber_policy', 'ai_shadow_block')")
}

func TestBuildContentModerationLogWhere_SearchFullTextsAndErrorsWithOtherFilters(t *testing.T) {
	groupID := int64(7)
	from := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	where, args := buildContentModerationLogWhere(service.ContentModerationLogFilter{
		GroupID: &groupID,
		Search:  "  完整输入  ",
		From:    &from,
	})

	require.Equal(t, []any{groupID, "%完整输入%", from}, args)
	sql := strings.Join(where, " AND ")
	require.Contains(t, sql, "l.group_id = $1")
	for _, column := range []string{"request_id", "user_email", "api_key_name", "model", "input_excerpt", "full_input_text", "review_text_excerpt", "error"} {
		require.Contains(t, sql, "l."+column+" ILIKE $2")
	}
	require.Contains(t, sql, "l.created_at >= $3")
}

func TestContentModerationRepositoryCreateLog_PersistsFullTextsAndMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	userID, apiKeyID, groupID := int64(11), int64(12), int64(13)
	latency, queueDelay := 17, 0
	createdAt := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	fullText := strings.Repeat("原始完整文本含换行\n", 5000) + "原文末尾"
	reviewText := "[user]\n" + fullText + "\n送审末尾"
	item := &service.ContentModerationLog{
		RequestID:             "request-1",
		UserID:                &userID,
		UserEmail:             "tester@example.invalid",
		OrganizationID:        21,
		BillingSubjectType:    "organization",
		BillingSubjectID:      21,
		ControlPlaneKeyID:     "key-ref",
		APIKeyID:              &apiKeyID,
		APIKeyName:            "test key",
		GroupID:               &groupID,
		GroupName:             "original",
		Endpoint:              "/v1/responses",
		Provider:              "openai",
		Model:                 "business-model",
		Mode:                  "pre_block",
		Action:                "block",
		Flagged:               true,
		HighestCategory:       "violence",
		HighestScore:          0.9,
		CategoryScores:        map[string]float64{"violence": 0.9},
		ThresholdSnapshot:     map[string]float64{"violence": 0.8},
		InputExcerpt:          "原始完整文本预览",
		FullInputText:         fullText,
		UpstreamLatencyMS:     &latency,
		Error:                 "penalty_enqueue_uncertain",
		ViolationCount:        2,
		AutoBanned:            false,
		EmailSent:             true,
		QueueDelayMS:          &queueDelay,
		OriginalGroupID:       &groupID,
		OriginalGroupName:     "original",
		EffectiveGroupID:      &groupID,
		EffectiveGroupName:    "effective",
		ContentSource:         "latest_user_input",
		DetectedLanguages:     []string{"zh"},
		ContentFingerprint:    "fingerprint",
		KeywordPolicyRerouted: false,
		KeywordPolicyReason:   "original",
		MatchedKeyword:        "",
		AIReviewDetail: map[string]any{
			"phase":           "completed",
			"provider_called": true,
			"reason":          "内容违规",
			"categories":      []string{"violence"},
			"model":           "review-model",
			"latency_ms":      17,
		},
		ReviewTextExcerpt: reviewText,
	}
	languageEvidenceJSON, err := json.Marshal(item.LanguageEvidence)
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)INSERT INTO content_moderation_logs .*ai_review_detail, review_text_excerpt, full_input_text.*\$40::jsonb, \$41, \$42.*RETURNING id, created_at`).
		WithArgs(
			"request-1", userID, "tester@example.invalid", int64(21), "organization",
			int64(21), "key-ref", apiKeyID, "test key", groupID, "original",
			"/v1/responses", "openai", "business-model", "pre_block", "block", true, "violence", 0.9,
			`{"violence":0.9}`, `{"violence":0.8}`, "原始完整文本预览", latency, "penalty_enqueue_uncertain",
			2, false, true, queueDelay,
			groupID, "original", groupID, "effective", "latest_user_input", `["zh"]`, "fingerprint", string(languageEvidenceJSON), false, "original", "",
			`{"categories":["violence"],"latency_ms":17,"model":"review-model","phase":"completed","provider_called":true,"reason":"内容违规"}`, reviewText, fullText,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(99, createdAt))

	err = NewContentModerationRepository(db).CreateLog(context.Background(), item)

	require.NoError(t, err)
	require.Equal(t, int64(99), item.ID)
	require.Equal(t, createdAt, item.CreatedAt)
	require.Equal(t, fullText, item.FullInputText)
	require.Equal(t, reviewText, item.ReviewTextExcerpt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func contentModerationLogTestColumns() []string {
	return []string{
		"id", "request_id", "user_id", "user_email", "organization_id", "billing_subject_type",
		"billing_subject_id", "control_plane_key_id", "api_key_id", "api_key_name", "group_id", "group_name",
		"endpoint", "provider", "model", "mode", "action", "flagged", "highest_category", "highest_score",
		"category_scores", "threshold_snapshot", "input_excerpt", "upstream_latency_ms", "error",
		"violation_count", "auto_banned", "email_sent", "user_status", "queue_delay_ms",
		"original_group_id", "original_group_name", "effective_group_id", "effective_group_name",
		"content_source", "detected_languages", "content_fingerprint", "language_evidence",
		"keyword_policy_rerouted", "keyword_policy_reason", "matched_keyword", "ai_review_detail", "created_at",
	}
}

func contentModerationLogTestValues(createdAt time.Time) []driver.Value {
	return []driver.Value{
		int64(99), "request-1", int64(11), "tester@example.invalid", int64(21), "organization",
		int64(21), "key-ref", int64(12), "test key", int64(13), "original",
		"/v1/responses", "openai", "business-model", "pre_block", "block", true, "violence", 0.9,
		`{"violence":0.9}`, `{"violence":0.8}`, "原始完整文本预览", int64(17), "penalty_enqueue_uncertain",
		int64(2), false, true, "active", int64(0),
		int64(13), "original", int64(14), "effective",
		"latest_user_input", `["zh"]`, "fingerprint", `{"detector_version":"test-detector","content_runes":500}`,
		true, "reroute", "keyword", `{"phase":"completed","provider_called":true,"reason":"内容违规","categories":["violence"],"model":"review-model","latency_ms":17}`, createdAt,
	}
}

func TestContentModerationRepositoryGetLog_ReturnsFullTextsAndMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	createdAt := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	fullText := strings.Repeat("完整原文\n", 5000) + "原文末尾"
	reviewText := "[user]\n" + fullText + "\n送审末尾"
	columns := append(contentModerationLogTestColumns(), "full_input_text", "review_text_excerpt")
	values := append(contentModerationLogTestValues(createdAt), fullText, reviewText)
	mock.ExpectQuery(`(?s)SELECT .*l.ai_review_detail, l.created_at, l.full_input_text, l.review_text_excerpt.*LEFT JOIN users u ON u.id = l.user_id.*WHERE l.id = \$1`).
		WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))

	item, err := NewContentModerationRepository(db).GetLog(context.Background(), 99)

	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, int64(99), item.ID)
	require.Equal(t, fullText, item.FullInputText)
	require.Equal(t, reviewText, item.ReviewTextExcerpt)
	require.Equal(t, "原始完整文本预览", item.InputExcerpt)
	require.Equal(t, int64(11), *item.UserID)
	require.Equal(t, int64(12), *item.APIKeyID)
	require.Equal(t, int64(13), *item.GroupID)
	require.Equal(t, int64(13), *item.OriginalGroupID)
	require.Equal(t, int64(14), *item.EffectiveGroupID)
	require.Equal(t, 17, *item.UpstreamLatencyMS)
	require.Equal(t, 0, *item.QueueDelayMS)
	require.Equal(t, "active", item.UserStatus)
	require.Equal(t, "penalty_enqueue_uncertain", item.Error)
	require.Equal(t, map[string]float64{"violence": 0.9}, item.CategoryScores)
	require.Equal(t, map[string]float64{"violence": 0.8}, item.ThresholdSnapshot)
	require.Equal(t, []string{"zh"}, item.DetectedLanguages)
	require.Equal(t, "test-detector", item.LanguageEvidence.DetectorVersion)
	require.Equal(t, 500, item.LanguageEvidence.ContentRunes)
	require.Equal(t, "completed", item.AIReviewDetail["phase"])
	require.Equal(t, true, item.AIReviewDetail["provider_called"])
	require.Equal(t, "review-model", item.AIReviewDetail["model"])
	require.Equal(t, float64(17), item.AIReviewDetail["latency_ms"])
	require.Equal(t, createdAt, item.CreatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContentModerationRepositoryGetLog_NotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(regexp.QuoteMeta("WHERE l.id = $1")).WithArgs(int64(404)).
		WillReturnRows(sqlmock.NewRows(append(contentModerationLogTestColumns(), "full_input_text", "review_text_excerpt")))

	item, err := NewContentModerationRepository(db).GetLog(context.Background(), 404)

	require.NoError(t, err)
	require.Nil(t, item)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContentModerationRepositoryGetLog_PropagatesDatabaseError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	dbErr := errors.New("query failed")
	mock.ExpectQuery(regexp.QuoteMeta("WHERE l.id = $1")).WithArgs(int64(99)).WillReturnError(dbErr)

	item, err := NewContentModerationRepository(db).GetLog(context.Background(), 99)

	require.Nil(t, item)
	require.ErrorIs(t, err, dbErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContentModerationRepositoryListLogs_ProjectsMetadataWithoutFullTexts(t *testing.T) {
	// 校验真正送给数据库的投影；仅断言返回空文本不足以阻止列表 SELECT 全文后丢弃。
	queryMatcher := sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		if expectedSQL == "metadata-only-list" {
			selectSQL, _, found := strings.Cut(actualSQL, "FROM content_moderation_logs l")
			if !found || !strings.Contains(selectSQL, "l.ai_review_detail") || !strings.Contains(selectSQL, "l.input_excerpt") {
				return errors.New("list must select preview and AI metadata")
			}
			if strings.Contains(selectSQL, "full_input_text") || strings.Contains(selectSQL, "review_text_excerpt") {
				return errors.New("list must not select full texts")
			}
			return sqlmock.QueryMatcherRegexp.Match(`(?s)LEFT JOIN users u ON u.id = l.user_id.*ORDER BY l.created_at DESC, l.id DESC.*LIMIT \$1 OFFSET \$2`, actualSQL)
		}
		return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(queryMatcher))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	createdAt := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM content_moderation_logs l WHERE l.id IS NOT NULL")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("metadata-only-list").WithArgs(100, 0).
		WillReturnRows(sqlmock.NewRows(contentModerationLogTestColumns()).AddRow(contentModerationLogTestValues(createdAt)...))

	items, page, err := NewContentModerationRepository(db).ListLogs(context.Background(), service.ContentModerationLogFilter{
		Pagination: pagination.PaginationParams{PageSize: 200},
	})

	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, 1, page.Page)
	require.Equal(t, 100, page.PageSize)
	require.Empty(t, items[0].FullInputText)
	require.Empty(t, items[0].ReviewTextExcerpt)
	require.Equal(t, "原始完整文本预览", items[0].InputExcerpt)
	require.Equal(t, "completed", items[0].AIReviewDetail["phase"])
	require.Equal(t, true, items[0].AIReviewDetail["provider_called"])
	require.Equal(t, "内容违规", items[0].AIReviewDetail["reason"])
	require.Equal(t, []any{"violence"}, items[0].AIReviewDetail["categories"])
	require.Equal(t, "review-model", items[0].AIReviewDetail["model"])
	require.Equal(t, float64(17), items[0].AIReviewDetail["latency_ms"])
	require.Equal(t, createdAt, items[0].CreatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContentModerationRepositoryGetLog_NullOptionalFields(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	values := contentModerationLogTestValues(time.Now())
	for _, index := range []int{2, 8, 10, 23, 29, 30, 32} {
		values[index] = nil
	}
	values[41] = "{}"
	mock.ExpectQuery(regexp.QuoteMeta("WHERE l.id = $1")).WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows(append(contentModerationLogTestColumns(), "full_input_text", "review_text_excerpt")).
			AddRow(append(values, "", "历史摘要")...))

	item, err := NewContentModerationRepository(db).GetLog(context.Background(), 99)

	require.NoError(t, err)
	require.NotNil(t, item)
	require.Nil(t, item.UserID)
	require.Nil(t, item.APIKeyID)
	require.Nil(t, item.GroupID)
	require.Nil(t, item.UpstreamLatencyMS)
	require.Nil(t, item.QueueDelayMS)
	require.Nil(t, item.OriginalGroupID)
	require.Nil(t, item.EffectiveGroupID)
	require.Nil(t, item.AIReviewDetail)
	require.Empty(t, item.FullInputText)
	require.Equal(t, "历史摘要", item.ReviewTextExcerpt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContentModerationRepositoryCountFlaggedByUserSince_WhitelistsPenaltyActions(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := NewContentModerationRepository(db)
	since := time.Now().Add(-time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("action IN ('block', 'keyword_block')")).
		WithArgs(int64(1001), since, false).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	count, err := repo.CountFlaggedByUserSince(context.Background(), 1001, since, false)

	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContentModerationRepositoryCountFlaggedByUserSince_ExcludesCyberPolicyWhenRequested(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := NewContentModerationRepository(db)
	since := time.Now().Add(-time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("OR ($3::bool IS FALSE AND action = 'cyber_policy' AND mode <> 'cyber_log_only')")).
		WithArgs(int64(1001), since, true).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	count, err := repo.CountFlaggedByUserSince(context.Background(), 1001, since, true)

	require.NoError(t, err)
	require.Equal(t, 3, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestContentModerationRepositoryCountFlaggedBySubjectSince_EnterpriseCountIgnoresKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := NewContentModerationRepository(db)
	since := time.Now().Add(-time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("WHERE user_id = $1\n  AND organization_id = $4\n  AND flagged = TRUE")).
		WithArgs(int64(1001), since, false, int64(2001)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))

	count, err := repo.CountFlaggedBySubjectSince(context.Background(), service.ContentModerationPenaltyScope{
		UserID:            1001,
		OrganizationID:    2001,
		ControlPlaneKeyID: "key-that-must-not-scope-penalties",
	}, since, false)

	require.NoError(t, err)
	require.Equal(t, 4, count)
	require.NoError(t, mock.ExpectationsWereMet())
}
