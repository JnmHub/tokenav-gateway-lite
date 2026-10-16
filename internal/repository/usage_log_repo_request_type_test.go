package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogBestEffortAsyncMirrorPausesAfterDatabaseFailure(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{db: db, sql: db}
	log := &service.UsageLog{
		UserID: 1, APIKeyID: 2, AccountID: 3, RequestID: "mirror-pause-1",
		Model: "gpt-5", ActualCost: 1, RateMultiplier: 1,
	}
	mock.ExpectExec("(?s)WITH input.*INSERT INTO usage_logs").
		WillReturnError(errors.New("database busy"))

	repo.flushBestEffortBatch(db, []usageLogBestEffortRequest{{
		prepared: prepareUsageLogInsert(log),
		apiKeyID: log.APIKeyID,
	}})
	require.True(t, repo.bestEffortMirrorPaused(time.Now()))

	err := repo.EnqueueBestEffort(context.Background(), log)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	repo.bestEffortMirrorPausedUntil.Store(time.Now().Add(-time.Second).UnixNano())
	require.False(t, repo.bestEffortMirrorPaused(time.Now()))
}

func TestUsageLogRepositoryCreateSyncRequestTypeAndLegacyFields(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	createdAt := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	sessionID := "session-audit-1"
	log := &service.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		AccountID:      3,
		RequestID:      "req-1",
		Model:          "gpt-5",
		RequestedModel: "gpt-5",
		InputTokens:    10,
		OutputTokens:   20,
		TotalCost:      1,
		ActualCost:     1,
		BillingType:    service.BillingTypeBalance,
		RequestType:    service.RequestTypeWSV2,
		Stream:         false,
		OpenAIWSMode:   false,
		SessionID:      &sessionID,
		CreatedAt:      createdAt,
	}

	mock.ExpectQuery("INSERT INTO usage_logs").
		WithArgs(
			log.UserID,
			log.APIKeyID,
			log.AccountID,
			log.RequestID,
			log.Model,
			log.RequestedModel,
			sqlmock.AnyArg(), // upstream_model
			sqlmock.AnyArg(), // upstream_response_model
			sqlmock.AnyArg(), // upstream_model_mismatch
			sqlmock.AnyArg(), // group_id
			sqlmock.AnyArg(), // subscription_id
			log.InputTokens,
			log.OutputTokens,
			log.CacheCreationTokens,
			log.CacheReadTokens,
			log.CacheCreation5mTokens,
			log.CacheCreation1hTokens,
			log.ImageOutputTokens,
			log.ImageOutputCost,
			log.ImageInputTokens,
			log.ImageInputCost,
			log.ImageInputPrice,
			sqlmock.AnyArg(), // image_input_price_source
			log.InputCost,
			log.OutputCost,
			log.CacheCreationCost,
			log.CacheReadCost,
			log.TotalCost,
			log.ActualCost,
			log.RateMultiplier,
			sqlmock.AnyArg(), // rate_policy_audit
			log.AccountRateMultiplier,
			log.BillingType,
			int16(service.RequestTypeWSV2),
			true,
			true,
			sqlmock.AnyArg(), // duration_ms
			sqlmock.AnyArg(), // first_token_ms
			sqlmock.AnyArg(), // fake_first_token_ms
			sqlmock.AnyArg(), // user_agent
			sqlmock.AnyArg(), // ip_address
			log.ImageCount,
			sqlmock.AnyArg(), // image_size
			sqlmock.AnyArg(), // image_input_size
			sqlmock.AnyArg(), // image_output_size
			sqlmock.AnyArg(), // image_size_source
			sqlmock.AnyArg(), // image_size_breakdown
			log.VideoCount,
			sqlmock.AnyArg(), // video_resolution
			sqlmock.AnyArg(), // video_duration_seconds
			sqlmock.AnyArg(), // service_tier
			sqlmock.AnyArg(), // requested_reasoning_effort
			sqlmock.AnyArg(), // reasoning_effort
			sqlmock.AnyArg(), // inbound_endpoint
			sqlmock.AnyArg(), // upstream_endpoint
			log.CacheTTLOverridden,
			sqlmock.AnyArg(),  // channel_id
			sqlmock.AnyArg(),  // model_mapping_chain
			sqlmock.AnyArg(),  // billing_tier
			sqlmock.AnyArg(),  // billing_mode
			sqlmock.AnyArg(),  // account_stats_cost
			sqlmock.AnyArg(),  // requested_service_tier
			sqlmock.AnyArg(),  // priority_policy_mode
			sqlmock.AnyArg(),  // sent_service_tier
			sqlmock.AnyArg(),  // response_service_tier
			sqlmock.AnyArg(),  // priority_detection_source
			sqlmock.AnyArg(),  // priority_policy_action
			sqlmock.AnyArg(),  // priority_policy_rule_id
			false,             // priority_policy_fallback
			false,             // long_context_billing_applied
			sqlmock.AnyArg(),  // original_group_id
			false,             // keyword_policy_matched
			false,             // keyword_policy_rerouted
			"",                // keyword_policy_source
			"",                // keyword_policy_reason
			"[]",              // detected_languages
			"session-audit-1", // session_id
			createdAt,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(99), createdAt))

	inserted, err := repo.Create(context.Background(), log)
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, int64(99), log.ID)
	require.Nil(t, log.ServiceTier)
	require.Equal(t, service.RequestTypeWSV2, log.RequestType)
	require.True(t, log.Stream)
	require.True(t, log.OpenAIWSMode)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryCreate_PersistsServiceTier(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	createdAt := time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC)
	serviceTier := "priority"
	sentServiceTier := "priority"
	responseServiceTier := "priority"
	requestedServiceTier := "priority"
	priorityPolicyMode := service.OpenAIPriorityModeForcePriority
	priorityDetectionSource := "response"
	priorityPolicyAction := service.BetaPolicyActionPass
	priorityPolicyRuleID := "rule_priority_default"
	log := &service.UsageLog{
		UserID:                    1,
		APIKeyID:                  2,
		AccountID:                 3,
		RequestID:                 "req-service-tier",
		Model:                     "gpt-5.4",
		RequestedModel:            "gpt-5.4",
		ServiceTier:               &serviceTier,
		SentServiceTier:           &sentServiceTier,
		ResponseServiceTier:       &responseServiceTier,
		RequestedServiceTier:      &requestedServiceTier,
		PriorityPolicyMode:        &priorityPolicyMode,
		PriorityDetectionSource:   &priorityDetectionSource,
		PriorityPolicyAction:      &priorityPolicyAction,
		PriorityPolicyRuleID:      &priorityPolicyRuleID,
		PriorityPolicyFallback:    true,
		LongContextBillingApplied: true,
		CreatedAt:                 createdAt,
	}

	mock.ExpectQuery("INSERT INTO usage_logs").
		WithArgs(
			log.UserID,
			log.APIKeyID,
			log.AccountID,
			log.RequestID,
			log.Model,
			log.RequestedModel,
			sqlmock.AnyArg(), // upstream_model
			sqlmock.AnyArg(), // upstream_response_model
			sqlmock.AnyArg(), // upstream_model_mismatch
			sqlmock.AnyArg(), // group_id
			sqlmock.AnyArg(), // subscription_id
			log.InputTokens,
			log.OutputTokens,
			log.CacheCreationTokens,
			log.CacheReadTokens,
			log.CacheCreation5mTokens,
			log.CacheCreation1hTokens,
			log.ImageOutputTokens,
			log.ImageOutputCost,
			log.ImageInputTokens,
			log.ImageInputCost,
			log.ImageInputPrice,
			sqlmock.AnyArg(),
			log.InputCost,
			log.OutputCost,
			log.CacheCreationCost,
			log.CacheReadCost,
			log.TotalCost,
			log.ActualCost,
			log.RateMultiplier,
			sqlmock.AnyArg(), // rate_policy_audit
			log.AccountRateMultiplier,
			log.BillingType,
			int16(service.RequestTypeSync),
			false,
			false,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(), // first_token_ms
			sqlmock.AnyArg(), // fake_first_token_ms
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			log.ImageCount,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(), // image_input_size
			sqlmock.AnyArg(), // image_output_size
			sqlmock.AnyArg(), // image_size_source
			sqlmock.AnyArg(), // image_size_breakdown
			log.VideoCount,
			sqlmock.AnyArg(), // video_resolution
			sqlmock.AnyArg(), // video_duration_seconds
			serviceTier,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			log.CacheTTLOverridden,
			sqlmock.AnyArg(), // channel_id
			sqlmock.AnyArg(), // model_mapping_chain
			sqlmock.AnyArg(), // billing_tier
			sqlmock.AnyArg(), // billing_mode
			sqlmock.AnyArg(), // account_stats_cost
			requestedServiceTier,
			priorityPolicyMode,
			sentServiceTier,
			responseServiceTier,
			priorityDetectionSource,
			priorityPolicyAction,
			priorityPolicyRuleID,
			true,
			true,
			sqlmock.AnyArg(), // original_group_id
			false,            // keyword_policy_matched
			false,            // keyword_policy_rerouted
			"",               // keyword_policy_source
			"",               // keyword_policy_reason
			"[]",             // detected_languages
			sqlmock.AnyArg(), // session_id
			createdAt,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(100), createdAt))

	inserted, err := repo.Create(context.Background(), log)
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBuildUsageLogBestEffortInsertQuery_IncludesRequestedModelColumn(t *testing.T) {
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		AccountID:      3,
		RequestID:      "req-best-effort-query",
		Model:          "gpt-5",
		RequestedModel: "gpt-5",
		CreatedAt:      time.Date(2025, 1, 3, 12, 0, 0, 0, time.UTC),
	})

	query, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})

	require.Contains(t, query, "INSERT INTO usage_logs (")
	require.Contains(t, query, "\n\t\t\tmodel,\n\t\t\trequested_model,\n\t\t\tupstream_model,\n\t\t\tupstream_response_model,\n\t\t\tupstream_model_mismatch,")
	require.Contains(t, query, "\n\t\t\trequest_id,\n\t\t\tmodel,\n\t\t\trequested_model,\n\t\t\tupstream_model,\n\t\t\tupstream_response_model,\n\t\t\tupstream_model_mismatch,")
	require.Len(t, args, len(prepared.args))
	require.Equal(t, prepared.args[5], args[5])
}

func TestExecUsageLogInsertNoResult_PersistsRequestedModel(t *testing.T) {
	db, mock := newSQLMock(t)
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		AccountID:      3,
		RequestID:      "req-best-effort-exec",
		Model:          "gpt-5",
		RequestedModel: "gpt-5",
		CreatedAt:      time.Date(2025, 1, 4, 12, 0, 0, 0, time.UTC),
	})

	mock.ExpectExec("INSERT INTO usage_logs").
		WithArgs(anySliceToDriverValues(prepared.args)...).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := execUsageLogInsertNoResult(context.Background(), db, prepared)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPrepareUsageLogInsert_ArgCountMatchesTypes(t *testing.T) {
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID:         1,
		APIKeyID:       2,
		AccountID:      3,
		RequestID:      "req-arg-count",
		Model:          "gpt-5",
		RequestedModel: "gpt-5",
		CreatedAt:      time.Date(2025, 1, 5, 12, 0, 0, 0, time.UTC),
	})

	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
}

func TestPrepareUsageLogInsert_PersistsFrozenRatePolicyAudit(t *testing.T) {
	userRate := 1.25
	audit := gatewaylite.RatePolicyAudit{
		SchemaVersion:           1,
		BillingScope:            "token",
		GroupRateMultiplier:     1.1,
		UserRateMultiplier:      &userRate,
		ResolvedRateMultiplier:  1.25,
		PeakRateEnabled:         true,
		PeakStart:               "22:00",
		PeakEnd:                 "06:00",
		PeakRateMultiplier:      1.5,
		AppliedPeakMultiplier:   1.5,
		EffectiveRateMultiplier: 1.875,
		Timezone:                "Asia/Shanghai",
		ResolvedAtMillis:        1735689600000,
		Version:                 "v1:0123456789abcdef",
	}
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID: 1, APIKeyID: 2, AccountID: 3, RequestID: "req-rate-policy",
		Model: "gpt-5", RateMultiplier: audit.EffectiveRateMultiplier,
		RatePolicyAudit: &audit, CreatedAt: time.Date(2025, 1, 5, 12, 0, 0, 0, time.UTC),
	})

	encoded, ok := prepared.args[30].(string)
	require.True(t, ok)
	var decoded gatewaylite.RatePolicyAudit
	require.NoError(t, json.Unmarshal([]byte(encoded), &decoded))
	require.Equal(t, audit, decoded)
}

func TestPrepareUsageLogInsert_DropsInvalidRatePolicyAuditWithoutDroppingLog(t *testing.T) {
	audit := gatewaylite.RatePolicyAudit{
		SchemaVersion:           1,
		BillingScope:            "token",
		GroupRateMultiplier:     1,
		ResolvedRateMultiplier:  1,
		PeakRateMultiplier:      1,
		AppliedPeakMultiplier:   1,
		EffectiveRateMultiplier: 1,
		Timezone:                "UTC",
		ResolvedAtMillis:        1735689600000,
		Version:                 "unsupported",
	}
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID: 1, APIKeyID: 2, AccountID: 3, RequestID: "req-invalid-rate-policy",
		Model: "gpt-5", RateMultiplier: 1, RatePolicyAudit: &audit,
	})

	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
	require.Nil(t, prepared.args[30], "invalid optional audit must not block the usage log insert")
}

func TestPrepareUsageLogInsert_PersistsImageSizeMetadata(t *testing.T) {
	imageSize := "4K"
	inputSize := "1024x1024"
	outputSize := "3840x2160"
	source := "output"
	priceSource := "model_catalog"
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID:                1,
		APIKeyID:              2,
		AccountID:             3,
		RequestID:             "req-image-metadata",
		Model:                 "gpt-image-2",
		RequestedModel:        "gpt-image-2",
		ImageCount:            2,
		ImageInputTokens:      28,
		ImageInputCost:        0.007,
		ImageInputPrice:       0.00025,
		ImageInputPriceSource: &priceSource,
		ImageSize:             &imageSize,
		ImageInputSize:        &inputSize,
		ImageOutputSize:       &outputSize,
		ImageSizeSource:       &source,
		ImageSizeBreakdown:    map[string]int{"1K": 1, "4K": 1},
		CreatedAt:             time.Date(2025, 1, 6, 12, 0, 0, 0, time.UTC),
	})

	require.Equal(t, 28, prepared.args[19])
	require.Equal(t, 0.007, prepared.args[20])
	require.Equal(t, 0.00025, prepared.args[21])
	require.Equal(t, sql.NullString{String: priceSource, Valid: true}, prepared.args[22])
	require.Equal(t, sql.NullString{String: imageSize, Valid: true}, prepared.args[42])
	require.Equal(t, sql.NullString{String: inputSize, Valid: true}, prepared.args[43])
	require.Equal(t, sql.NullString{String: outputSize, Valid: true}, prepared.args[44])
	require.Equal(t, sql.NullString{String: source, Valid: true}, prepared.args[45])
	breakdownJSON, ok := prepared.args[46].(string)
	require.True(t, ok)
	require.JSONEq(t, `{"1K":1,"4K":1}`, breakdownJSON)
}

func TestCoalesceTrimmedString(t *testing.T) {
	require.Equal(t, "fallback", coalesceTrimmedString(sql.NullString{}, "fallback"))
	require.Equal(t, "fallback", coalesceTrimmedString(sql.NullString{Valid: true, String: "   "}, "fallback"))
	require.Equal(t, "value", coalesceTrimmedString(sql.NullString{Valid: true, String: "value"}, "fallback"))
}

func TestAppendUsageLogBillingModeWhereCondition(t *testing.T) {
	tests := []struct {
		name          string
		billingMode   string
		wantCondition string
	}{
		{
			name:          "image includes legacy image rows",
			billingMode:   string(service.BillingModeImage),
			wantCondition: "(billing_mode = $1 OR (COALESCE(image_count, 0) > 0 AND COALESCE(video_count, 0) <= 0))",
		},
		{
			name:          "video includes legacy video rows",
			billingMode:   string(service.BillingModeVideo),
			wantCondition: "(billing_mode = $1 OR COALESCE(video_count, 0) > 0)",
		},
		{
			name:          "token includes legacy non-image rows",
			billingMode:   string(service.BillingModeToken),
			wantCondition: "(billing_mode = $1 OR ((billing_mode IS NULL OR billing_mode = '') AND COALESCE(image_count, 0) <= 0 AND COALESCE(video_count, 0) <= 0))",
		},
		{
			name:          "per request remains exact",
			billingMode:   string(service.BillingModePerRequest),
			wantCondition: "billing_mode = $1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conditions, args := appendUsageLogBillingModeWhereCondition(nil, nil, tt.billingMode)
			require.Equal(t, []string{tt.wantCondition}, conditions)
			require.Equal(t, []any{tt.billingMode}, args)
		})
	}
}

func TestAppendUsageLogServiceTierWhereCondition(t *testing.T) {
	tests := []struct {
		name        string
		serviceTier string
		want        string
	}{
		{name: "priority", serviceTier: "priority", want: "IN ('priority', 'fast')"},
		{name: "fast alias", serviceTier: "fast", want: "IN ('priority', 'fast')"},
		{name: "standard", serviceTier: "standard", want: "IN ('', 'standard', 'default', 'auto', 'scale')"},
		{name: "flex", serviceTier: "flex", want: "= 'flex'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conditions, args := appendUsageLogServiceTierWhereCondition(nil, nil, tt.serviceTier)
			require.Len(t, conditions, 1)
			require.Contains(t, conditions[0], tt.want)
			require.Empty(t, args)
		})
	}
}

func anySliceToDriverValues(values []any) []driver.Value {
	out := make([]driver.Value, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func TestUsageLogRepositoryListWithFiltersRequestTypePriority(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	requestType := int16(service.RequestTypeWSV2)
	stream := false
	filters := usagestats.UsageLogFilters{
		RequestType: &requestType,
		Stream:      &stream,
		ExactTotal:  true,
	}

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM usage_logs WHERE \\(request_type = \\$1 OR \\(request_type = 0 AND openai_ws_mode = TRUE\\)\\)").
		WithArgs(requestType).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery("SELECT .* FROM usage_logs WHERE \\(request_type = \\$1 OR \\(request_type = 0 AND openai_ws_mode = TRUE\\)\\) ORDER BY id DESC LIMIT \\$2 OFFSET \\$3").
		WithArgs(requestType, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	logs, page, err := repo.ListWithFilters(context.Background(), pagination.PaginationParams{Page: 1, PageSize: 20}, filters)
	require.NoError(t, err)
	require.Empty(t, logs)
	require.NotNil(t, page)
	require.Equal(t, int64(0), page.Total)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetUsageTrendWithFiltersRequestTypePriority(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	requestType := int16(service.RequestTypeStream)
	stream := true

	mock.ExpectQuery("AND \\(request_type = \\$3 OR \\(request_type = 0 AND stream = TRUE AND openai_ws_mode = FALSE\\)\\)").
		WithArgs(start, end, requestType).
		WillReturnRows(sqlmock.NewRows([]string{"date", "requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_tokens", "cost", "actual_cost"}))

	trend, err := repo.GetUsageTrendWithFilters(context.Background(), start, end, "day", 0, 0, 0, 0, "", &requestType, &stream, nil)
	require.NoError(t, err)
	require.Empty(t, trend)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetModelStatsWithFiltersRequestTypePriority(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	requestType := int16(service.RequestTypeWSV2)
	stream := false

	mock.ExpectQuery("AND \\(request_type = \\$3 OR \\(request_type = 0 AND openai_ws_mode = TRUE\\)\\)").
		WithArgs(start, end, requestType).
		WillReturnRows(sqlmock.NewRows([]string{"model", "requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_tokens", "cost", "actual_cost", "account_cost"}))

	stats, err := repo.GetModelStatsWithFilters(context.Background(), start, end, 0, 0, 0, 0, &requestType, &stream, nil)
	require.NoError(t, err)
	require.Empty(t, stats)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetStatsWithFiltersRequestTypePriority(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	requestType := int16(service.RequestTypeSync)
	stream := true
	filters := usagestats.UsageLogFilters{
		RequestType: &requestType,
		Stream:      &stream,
	}

	mock.ExpectQuery("(?s)FROM usage_logs\\s+WHERE \\(request_type = \\$1 OR \\(request_type = 0 AND stream = FALSE AND openai_ws_mode = FALSE\\)\\).*GROUP BY GROUPING SETS").
		WithArgs(requestType).
		WillReturnRows(sqlmock.NewRows([]string{
			"inbound_grouped",
			"upstream_grouped",
			"inbound_endpoint",
			"upstream_endpoint",
			"requests",
			"input_tokens",
			"output_tokens",
			"cache_creation_tokens",
			"cache_read_tokens",
			"cost",
			"actual_cost",
			"account_cost",
			"avg_duration_ms",
		}).AddRow(1, 1, nil, nil, int64(1), int64(2), int64(3), int64(1), int64(3), 1.2, 1.0, 1.2, 20.0))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.TotalRequests)
	require.Equal(t, int64(1), stats.TotalCacheCreationTokens)
	require.Equal(t, int64(3), stats.TotalCacheReadTokens)
	require.Equal(t, int64(4), stats.TotalCacheTokens)
	require.Equal(t, int64(9), stats.TotalTokens)
	require.NotNil(t, stats.TotalAccountCost, "TotalAccountCost should always be returned")
	require.Equal(t, 1.2, *stats.TotalAccountCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetStatsWithFiltersUsesOneScanAndAllFilters(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	billingType := int8(service.BillingTypeBalance)
	mismatch := true
	filters := usagestats.UsageLogFilters{
		UserID:                7,
		Model:                 "gpt-5.6-luna",
		ModelFilterSource:     usagestats.ModelSourceUpstream,
		BillingType:           &billingType,
		BillingMode:           string(service.BillingModePerRequest),
		ServiceTier:           "priority",
		UpstreamModelMismatch: &mismatch,
		StartTime:             &start,
		EndTime:               &end,
	}

	mock.ExpectQuery("(?s)WITH scoped AS \\(.*FROM usage_logs.*user_id = \\$1.*COALESCE\\(NULLIF\\(TRIM\\(upstream_model\\), ''\\), COALESCE\\(NULLIF\\(TRIM\\(requested_model\\), ''\\), model\\)\\) = \\$2.*billing_type = \\$3.*billing_mode = \\$4.*service_tier.*upstream_model_mismatch IS TRUE.*created_at >= \\$5.*created_at < \\$6.*GROUP BY GROUPING SETS").
		WithArgs(int64(7), "gpt-5.6-luna", int16(billingType), string(service.BillingModePerRequest), start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"inbound_grouped", "upstream_grouped", "inbound_endpoint", "upstream_endpoint",
			"requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens",
			"cost", "actual_cost", "account_cost", "avg_duration_ms",
		}).
			AddRow(1, 1, nil, nil, int64(3), int64(10), int64(20), int64(2), int64(4), 1.2, 1.0, 0.8, 30.0).
			AddRow(0, 1, "/v1/responses", nil, int64(2), int64(8), int64(15), int64(1), int64(4), 0.9, 0.7, 0.6, 25.0).
			AddRow(0, 1, "/v1/chat/completions", nil, int64(1), int64(2), int64(5), int64(1), int64(0), 0.3, 0.3, 0.2, 40.0).
			AddRow(1, 0, nil, "/v1/responses", int64(3), int64(10), int64(20), int64(2), int64(4), 1.2, 1.0, 0.8, 30.0).
			AddRow(0, 0, "/v1/responses", "/v1/responses", int64(2), int64(8), int64(15), int64(1), int64(4), 0.9, 0.7, 0.6, 25.0).
			AddRow(0, 0, "/v1/chat/completions", "/v1/responses", int64(1), int64(2), int64(5), int64(1), int64(0), 0.3, 0.3, 0.2, 40.0))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Equal(t, int64(3), stats.TotalRequests)
	require.Equal(t, int64(36), stats.TotalTokens)
	require.Equal(t, []usagestats.EndpointStat{
		{Endpoint: "/v1/responses", Requests: 2, TotalTokens: 28, Cost: 0.9, ActualCost: 0.7},
		{Endpoint: "/v1/chat/completions", Requests: 1, TotalTokens: 8, Cost: 0.3, ActualCost: 0.3},
	}, stats.Endpoints)
	require.Equal(t, "/v1/responses", stats.UpstreamEndpoints[0].Endpoint)
	require.Len(t, stats.EndpointPaths, 2)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetGlobalStatsReturnsCacheBreakdown(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery("FROM usage_logs\\s+WHERE created_at >= \\$1 AND created_at < \\$2").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests",
			"total_input_tokens",
			"total_output_tokens",
			"total_cache_creation_tokens",
			"total_cache_read_tokens",
			"total_cost",
			"total_actual_cost",
			"avg_duration_ms",
		}).AddRow(int64(7), int64(100), int64(50), int64(11), int64(29), 2.5, 2.0, 125.0))

	stats, err := repo.GetGlobalStats(context.Background(), start, end)
	require.NoError(t, err)
	require.Equal(t, int64(11), stats.TotalCacheCreationTokens)
	require.Equal(t, int64(29), stats.TotalCacheReadTokens)
	require.Equal(t, int64(40), stats.TotalCacheTokens)
	require.Equal(t, int64(190), stats.TotalTokens)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetModelStatsAccountCostColumn(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery("FROM usage_logs").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"model", "requests", "input_tokens", "output_tokens",
			"cache_creation_tokens", "cache_read_tokens", "total_tokens",
			"cost", "actual_cost", "account_cost",
		}).
			AddRow("claude-opus-4-6", int64(10), int64(100), int64(200), int64(5), int64(3), int64(308), 2.5, 2.0, 1.8).
			AddRow("claude-sonnet-4-6", int64(5), int64(50), int64(100), int64(0), int64(0), int64(150), 1.0, 0.8, 0.7))

	results, err := repo.GetModelStatsWithFilters(context.Background(), start, end, 0, 0, 0, 0, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, "claude-opus-4-6", results[0].Model)
	require.Equal(t, 2.5, results[0].Cost)
	require.Equal(t, 2.0, results[0].ActualCost)
	require.Equal(t, 1.8, results[0].AccountCost)
	require.Equal(t, "claude-sonnet-4-6", results[1].Model)
	require.Equal(t, 0.7, results[1].AccountCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetGroupStatsAccountCostColumn(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery("FROM usage_logs").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "group_name", "requests", "total_tokens",
			"cost", "actual_cost", "account_cost",
		}).
			AddRow(int64(1), "azure-cc", int64(100), int64(5000), 10.0, 8.5, 7.2).
			AddRow(int64(2), "max", int64(50), int64(2000), 5.0, 4.0, 3.5))

	results, err := repo.GetGroupStatsWithFilters(context.Background(), start, end, 0, 0, 0, 0, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, int64(1), results[0].GroupID)
	require.Equal(t, "azure-cc", results[0].GroupName)
	require.Equal(t, 10.0, results[0].Cost)
	require.Equal(t, 8.5, results[0].ActualCost)
	require.Equal(t, 7.2, results[0].AccountCost)
	require.Equal(t, int64(2), results[1].GroupID)
	require.Equal(t, 3.5, results[1].AccountCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetAllGroupUsageSummaryIncludesRequestCounts(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	todayStart := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("FROM groups g").
		WithArgs(todayStart).
		WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "total_cost", "today_cost", "total_requests", "today_requests",
			"total_rerouted_requests", "today_rerouted_requests",
		}).
			AddRow(int64(10), 12.5, 2.5, int64(7), int64(2), int64(1), int64(1)).
			AddRow(int64(20), 8.0, 8.0, int64(4), int64(4), int64(1), int64(1)))

	results, err := repo.GetAllGroupUsageSummary(context.Background(), todayStart)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, int64(7), results[0].TotalRequests)
	require.Equal(t, int64(2), results[0].TodayRequests)
	require.Equal(t, int64(1), results[0].TotalReroutedRequests)
	require.Equal(t, int64(1), results[0].TodayReroutedRequests)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetStatsWithFiltersAlwaysReturnsAccountCost(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	// No AccountID filter set - TotalAccountCost should still be returned
	filters := usagestats.UsageLogFilters{}

	mock.ExpectQuery("(?s)FROM usage_logs.*GROUP BY GROUPING SETS").
		WillReturnRows(sqlmock.NewRows([]string{
			"inbound_grouped", "upstream_grouped", "inbound_endpoint", "upstream_endpoint",
			"requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens",
			"cost", "actual_cost", "account_cost", "avg_duration_ms",
		}).AddRow(1, 1, nil, nil, int64(50), int64(1000), int64(2000), int64(40), int64(60), 15.0, 12.5, 11.0, 100.0))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Equal(t, int64(40), stats.TotalCacheCreationTokens)
	require.Equal(t, int64(60), stats.TotalCacheReadTokens)
	require.Equal(t, int64(100), stats.TotalCacheTokens)
	require.NotNil(t, stats.TotalAccountCost, "TotalAccountCost must always be returned, even without AccountID filter")
	require.Equal(t, 11.0, *stats.TotalAccountCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetUserSpendingRanking(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	rows := sqlmock.NewRows([]string{"user_id", "email", "actual_cost", "requests", "tokens", "total_actual_cost", "total_requests", "total_tokens"}).
		AddRow(int64(2), "beta@example.com", 12.5, int64(9), int64(900), 40.0, int64(30), int64(2600)).
		AddRow(int64(1), "alpha@example.com", 12.5, int64(8), int64(800), 40.0, int64(30), int64(2600)).
		AddRow(int64(3), "gamma@example.com", 4.25, int64(5), int64(300), 40.0, int64(30), int64(2600))

	mock.ExpectQuery("WITH user_spend AS \\(").
		WithArgs(start, end, 12).
		WillReturnRows(rows)

	got, err := repo.GetUserSpendingRanking(context.Background(), start, end, 12)
	require.NoError(t, err)
	require.Equal(t, &usagestats.UserSpendingRankingResponse{
		Ranking: []usagestats.UserSpendingRankingItem{
			{UserID: 2, Email: "beta@example.com", ActualCost: 12.5, Requests: 9, Tokens: 900},
			{UserID: 1, Email: "alpha@example.com", ActualCost: 12.5, Requests: 8, Tokens: 800},
			{UserID: 3, Email: "gamma@example.com", ActualCost: 4.25, Requests: 5, Tokens: 300},
		},
		TotalActualCost: 40.0,
		TotalRequests:   30,
		TotalTokens:     2600,
	}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBuildRequestTypeFilterConditionLegacyFallback(t *testing.T) {
	tests := []struct {
		name      string
		request   int16
		wantWhere string
		wantArg   int16
	}{
		{
			name:      "sync_with_legacy_fallback",
			request:   int16(service.RequestTypeSync),
			wantWhere: "(request_type = $3 OR (request_type = 0 AND stream = FALSE AND openai_ws_mode = FALSE))",
			wantArg:   int16(service.RequestTypeSync),
		},
		{
			name:      "stream_with_legacy_fallback",
			request:   int16(service.RequestTypeStream),
			wantWhere: "(request_type = $3 OR (request_type = 0 AND stream = TRUE AND openai_ws_mode = FALSE))",
			wantArg:   int16(service.RequestTypeStream),
		},
		{
			name:      "ws_v2_with_legacy_fallback",
			request:   int16(service.RequestTypeWSV2),
			wantWhere: "(request_type = $3 OR (request_type = 0 AND openai_ws_mode = TRUE))",
			wantArg:   int16(service.RequestTypeWSV2),
		},
		{
			name:      "invalid_request_type_normalized_to_unknown",
			request:   int16(99),
			wantWhere: "request_type = $3",
			wantArg:   int16(service.RequestTypeUnknown),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, args := buildRequestTypeFilterCondition(3, tt.request)
			require.Equal(t, tt.wantWhere, where)
			require.Equal(t, []any{tt.wantArg}, args)
		})
	}
}

type usageLogScannerStub struct {
	values []any
}

func (s usageLogScannerStub) Scan(dest ...any) error {
	if len(dest) != len(s.values) {
		return fmt.Errorf("scan arg count mismatch: got %d want %d", len(dest), len(s.values))
	}
	for i := range dest {
		dv := reflect.ValueOf(dest[i])
		if dv.Kind() != reflect.Ptr {
			return fmt.Errorf("dest[%d] is not pointer", i)
		}
		dv.Elem().Set(reflect.ValueOf(s.values[i]))
	}
	return nil
}

func TestScanUsageLogRequestTypeAndLegacyFallback(t *testing.T) {
	t.Run("image_size_metadata_is_scanned", func(t *testing.T) {
		now := time.Now().UTC()
		log, err := scanUsageLog(usageLogScannerStub{values: []any{
			int64(4),
			int64(13),
			int64(23),
			int64(33),
			sql.NullString{Valid: true, String: "req-image-metadata"},
			"gpt-image-2",
			sql.NullString{Valid: true, String: "gpt-image-2"},
			sql.NullString{},
			sql.NullString{},
			sql.NullBool{},
			sql.NullInt64{},
			sql.NullInt64{},
			0, 0, 0, 0, 0, 0,
			0, 0.0, // image_output_tokens, image_output_cost
			28, 0.007, 0.00025, sql.NullString{Valid: true, String: "channel"},
			0.0, 0.0, 0.0, 0.0, 0.8, 0.8,
			1.0,
			sql.NullString{},
			sql.NullFloat64{},
			int16(service.BillingTypeBalance),
			int16(service.RequestTypeSync),
			false,
			false,
			sql.NullInt64{},
			sql.NullInt64{},
			sql.NullInt64{}, // fake_first_token_ms
			sql.NullString{},
			sql.NullString{},
			2,
			sql.NullString{Valid: true, String: "4K"},
			sql.NullString{Valid: true, String: "1024x1024"},
			sql.NullString{Valid: true, String: "3840x2160"},
			sql.NullString{Valid: true, String: "output"},
			sql.NullString{Valid: true, String: `{"4K":2}`},
			0,
			sql.NullString{},
			sql.NullInt64{},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			false,
			sql.NullInt64{},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			sql.NullFloat64{},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			false,
			false,
			sql.NullInt64{},
			false,
			false,
			"",
			"",
			sql.NullString{Valid: true, String: "[]"},
			sql.NullString{Valid: true, String: "session-image"},
			now,
		}})
		require.NoError(t, err)
		require.Equal(t, 2, log.ImageCount)
		require.NotNil(t, log.ImageSize)
		require.Equal(t, "4K", *log.ImageSize)
		require.NotNil(t, log.ImageInputSize)
		require.Equal(t, "1024x1024", *log.ImageInputSize)
		require.NotNil(t, log.ImageOutputSize)
		require.Equal(t, "3840x2160", *log.ImageOutputSize)
		require.NotNil(t, log.ImageSizeSource)
		require.Equal(t, "output", *log.ImageSizeSource)
		require.Equal(t, map[string]int{"4K": 2}, log.ImageSizeBreakdown)
		require.Equal(t, 28, log.ImageInputTokens)
		require.InDelta(t, 0.007, log.ImageInputCost, 1e-12)
		require.InDelta(t, 0.00025, log.ImageInputPrice, 1e-12)
		require.NotNil(t, log.ImageInputPriceSource)
		require.Equal(t, "channel", *log.ImageInputPriceSource)
		require.NotNil(t, log.SessionID)
		require.Equal(t, "session-image", *log.SessionID)
	})

	t.Run("request_type_ws_v2_overrides_legacy", func(t *testing.T) {
		now := time.Now().UTC()
		log, err := scanUsageLog(usageLogScannerStub{values: []any{
			int64(1),  // id
			int64(10), // user_id
			int64(20), // api_key_id
			int64(30), // account_id
			sql.NullString{Valid: true, String: "req-1"},
			"gpt-5", // model
			sql.NullString{Valid: true, String: "gpt-5"}, // requested_model
			sql.NullString{},  // upstream_model
			sql.NullString{},  // upstream_response_model
			sql.NullBool{},    // upstream_model_mismatch
			sql.NullInt64{},   // group_id
			sql.NullInt64{},   // subscription_id
			1,                 // input_tokens
			2,                 // output_tokens
			3,                 // cache_creation_tokens
			4,                 // cache_read_tokens
			5,                 // cache_creation_5m_tokens
			6,                 // cache_creation_1h_tokens
			0,                 // image_output_tokens
			0.0,               // image_output_cost
			0,                 // image_input_tokens
			0.0,               // image_input_cost
			0.0,               // image_input_price
			sql.NullString{},  // image_input_price_source
			0.1,               // input_cost
			0.2,               // output_cost
			0.3,               // cache_creation_cost
			0.4,               // cache_read_cost
			1.0,               // total_cost
			0.9,               // actual_cost
			1.0,               // rate_multiplier
			sql.NullString{},  // rate_policy_audit
			sql.NullFloat64{}, // account_rate_multiplier
			int16(service.BillingTypeBalance),
			int16(service.RequestTypeWSV2),
			false, // legacy stream
			false, // legacy openai ws
			sql.NullInt64{},
			sql.NullInt64{},
			sql.NullInt64{}, // fake_first_token_ms
			sql.NullString{},
			sql.NullString{},
			0,
			sql.NullString{},
			sql.NullString{}, // image_input_size
			sql.NullString{}, // image_output_size
			sql.NullString{}, // image_size_source
			sql.NullString{}, // image_size_breakdown
			0,                // video_count
			sql.NullString{}, // video_resolution
			sql.NullInt64{},  // video_duration_seconds
			sql.NullString{Valid: true, String: "priority"},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			false,
			sql.NullInt64{},   // channel_id
			sql.NullString{},  // model_mapping_chain
			sql.NullString{},  // billing_tier
			sql.NullString{},  // billing_mode
			sql.NullFloat64{}, // account_stats_cost
			sql.NullString{},  // requested_service_tier
			sql.NullString{},  // priority_policy_mode
			sql.NullString{},  // sent_service_tier
			sql.NullString{},  // response_service_tier
			sql.NullString{},  // priority_detection_source
			sql.NullString{},  // priority_policy_action
			sql.NullString{},  // priority_policy_rule_id
			false,             // priority_policy_fallback
			false,             // long_context_billing_applied
			sql.NullInt64{},   // original_group_id
			false,             // keyword_policy_matched
			false,             // keyword_policy_rerouted
			"",                // keyword_policy_source
			"",                // keyword_policy_reason
			sql.NullString{Valid: true, String: "[]"}, // detected_languages
			sql.NullString{}, // session_id
			now,
		}})
		require.NoError(t, err)
		require.NotNil(t, log.ServiceTier)
		require.Equal(t, "priority", *log.ServiceTier)
		require.Equal(t, service.RequestTypeWSV2, log.RequestType)
		require.True(t, log.Stream)
		require.True(t, log.OpenAIWSMode)
	})

	t.Run("request_type_unknown_falls_back_to_legacy", func(t *testing.T) {
		now := time.Now().UTC()
		log, err := scanUsageLog(usageLogScannerStub{values: []any{
			int64(2),
			int64(11),
			int64(21),
			int64(31),
			sql.NullString{Valid: true, String: "req-2"},
			"gpt-5",
			sql.NullString{Valid: true, String: "gpt-5"},
			sql.NullString{},
			sql.NullString{},
			sql.NullBool{},
			sql.NullInt64{},
			sql.NullInt64{},
			1, 2, 3, 4, 5, 6,
			0, 0.0, // image_output_tokens, image_output_cost
			0, 0.0, 0.0, sql.NullString{}, // image input evidence
			0.1, 0.2, 0.3, 0.4, 1.0, 0.9,
			1.0,
			sql.NullString{},
			sql.NullFloat64{},
			int16(service.BillingTypeBalance),
			int16(service.RequestTypeUnknown),
			true,
			false,
			sql.NullInt64{},
			sql.NullInt64{},
			sql.NullInt64{}, // fake_first_token_ms
			sql.NullString{},
			sql.NullString{},
			0,
			sql.NullString{},
			sql.NullString{}, // image_input_size
			sql.NullString{}, // image_output_size
			sql.NullString{}, // image_size_source
			sql.NullString{}, // image_size_breakdown
			0,                // video_count
			sql.NullString{}, // video_resolution
			sql.NullInt64{},  // video_duration_seconds
			sql.NullString{Valid: true, String: "flex"},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			false,
			sql.NullInt64{},   // channel_id
			sql.NullString{},  // model_mapping_chain
			sql.NullString{},  // billing_tier
			sql.NullString{},  // billing_mode
			sql.NullFloat64{}, // account_stats_cost
			sql.NullString{},  // requested_service_tier
			sql.NullString{},  // priority_policy_mode
			sql.NullString{},  // sent_service_tier
			sql.NullString{},  // response_service_tier
			sql.NullString{},  // priority_detection_source
			sql.NullString{},  // priority_policy_action
			sql.NullString{},  // priority_policy_rule_id
			false,             // priority_policy_fallback
			false,             // long_context_billing_applied
			sql.NullInt64{},   // original_group_id
			false,             // keyword_policy_matched
			false,             // keyword_policy_rerouted
			"",                // keyword_policy_source
			"",                // keyword_policy_reason
			sql.NullString{Valid: true, String: "[]"}, // detected_languages
			sql.NullString{}, // session_id
			now,
		}})
		require.NoError(t, err)
		require.NotNil(t, log.ServiceTier)
		require.Equal(t, "flex", *log.ServiceTier)
		require.Equal(t, service.RequestTypeStream, log.RequestType)
		require.True(t, log.Stream)
		require.False(t, log.OpenAIWSMode)
	})

	t.Run("service_tier_is_scanned", func(t *testing.T) {
		now := time.Now().UTC()
		log, err := scanUsageLog(usageLogScannerStub{values: []any{
			int64(3),
			int64(12),
			int64(22),
			int64(32),
			sql.NullString{Valid: true, String: "req-3"},
			"gpt-5.4",
			sql.NullString{Valid: true, String: "gpt-5.4"},
			sql.NullString{},
			sql.NullString{},
			sql.NullBool{},
			sql.NullInt64{},
			sql.NullInt64{},
			1, 2, 3, 4, 5, 6,
			0, 0.0, // image_output_tokens, image_output_cost
			0, 0.0, 0.0, sql.NullString{}, // image input evidence
			0.1, 0.2, 0.3, 0.4, 1.0, 0.9,
			1.0,
			sql.NullString{},
			sql.NullFloat64{},
			int16(service.BillingTypeBalance),
			int16(service.RequestTypeSync),
			false,
			false,
			sql.NullInt64{},
			sql.NullInt64{},
			sql.NullInt64{}, // fake_first_token_ms
			sql.NullString{},
			sql.NullString{},
			0,
			sql.NullString{},
			sql.NullString{}, // image_input_size
			sql.NullString{}, // image_output_size
			sql.NullString{}, // image_size_source
			sql.NullString{}, // image_size_breakdown
			0,                // video_count
			sql.NullString{}, // video_resolution
			sql.NullInt64{},  // video_duration_seconds
			sql.NullString{Valid: true, String: "priority"},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			sql.NullString{},
			false,
			sql.NullInt64{},   // channel_id
			sql.NullString{},  // model_mapping_chain
			sql.NullString{},  // billing_tier
			sql.NullString{},  // billing_mode
			sql.NullFloat64{}, // account_stats_cost
			sql.NullString{Valid: true, String: "priority"},
			sql.NullString{Valid: true, String: service.OpenAIPriorityModeFollowRequest},
			sql.NullString{Valid: true, String: "priority"},
			sql.NullString{Valid: true, String: "default"},
			sql.NullString{Valid: true, String: "response"},
			sql.NullString{Valid: true, String: service.BetaPolicyActionPass},
			sql.NullString{Valid: true, String: "rule_priority_default"},
			true,
			true,
			sql.NullInt64{},
			false,
			false,
			"",
			"",
			sql.NullString{Valid: true, String: "[]"},
			sql.NullString{},
			now,
		}})
		require.NoError(t, err)
		require.NotNil(t, log.ServiceTier)
		require.Equal(t, "priority", *log.ServiceTier)
		require.NotNil(t, log.RequestedServiceTier)
		require.Equal(t, "priority", *log.RequestedServiceTier)
		require.NotNil(t, log.PriorityPolicyMode)
		require.Equal(t, service.OpenAIPriorityModeFollowRequest, *log.PriorityPolicyMode)
		require.NotNil(t, log.SentServiceTier)
		require.Equal(t, "priority", *log.SentServiceTier)
		require.NotNil(t, log.ResponseServiceTier)
		require.Equal(t, "default", *log.ResponseServiceTier)
		require.NotNil(t, log.PriorityDetectionSource)
		require.Equal(t, "response", *log.PriorityDetectionSource)
		require.NotNil(t, log.PriorityPolicyAction)
		require.Equal(t, service.BetaPolicyActionPass, *log.PriorityPolicyAction)
		require.NotNil(t, log.PriorityPolicyRuleID)
		require.Equal(t, "rule_priority_default", *log.PriorityPolicyRuleID)
		require.True(t, log.PriorityPolicyFallback)
		require.True(t, log.LongContextBillingApplied)
	})

}

func TestBuildUsageLogBestEffortInsertQuery_PersistsFakeFirstTokenMs(t *testing.T) {
	fakeMs := 180
	firstMs := 1200
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID:           1,
		APIKeyID:         2,
		AccountID:        3,
		RequestID:        "req-fake-first-token",
		Model:            "gpt-5",
		FirstTokenMs:     &firstMs,
		FakeFirstTokenMs: &fakeMs,
		CreatedAt:        time.Date(2025, 1, 5, 12, 0, 0, 0, time.UTC),
	})

	query, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})

	// input CTE、最终 INSERT 列清单、SELECT 列清单三处都必须带上该列。
	// 任何一处缺失都会把值静默丢掉（CTE 带了、落表没带，且列数一致不报错）。
	require.Equal(t, 3, strings.Count(query, "fake_first_token_ms"),
		"CTE/INSERT/SELECT 都应包含 fake_first_token_ms: %s", query)
	require.Contains(t, query, "first_token_ms,\n\t\t\tfake_first_token_ms,\n\t\t\tuser_agent,")

	// CTE 列数必须与每行 VALUES 占位符数（args 数）一致。
	columns := usageLogInsertColumnNamesBetween(t, query, "WITH input (", ") AS (VALUES")
	require.Len(t, columns, len(prepared.args))
	require.Len(t, prepared.args, len(usageLogInsertArgTypes))
	require.Len(t, args, len(prepared.args))

	// duration(36) 之后必须依次是 first_token(37)、fake_first_token(38)。
	require.Equal(t, sql.NullInt64{Int64: 1200, Valid: true}, args[37])
	require.Equal(t, sql.NullInt64{Int64: 180, Valid: true}, args[38])
}

func TestExecUsageLogInsertNoResult_PersistsFakeFirstTokenMs(t *testing.T) {
	db, mock := newSQLMock(t)
	fakeMs := 150
	firstMs := 900
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID:           1,
		APIKeyID:         2,
		AccountID:        3,
		RequestID:        "req-fake-first-token-exec",
		Model:            "gpt-5",
		FirstTokenMs:     &firstMs,
		FakeFirstTokenMs: &fakeMs,
		CreatedAt:        time.Date(2025, 1, 6, 12, 0, 0, 0, time.UTC),
	})

	// 占位符数量必须等于 args 数量：少一个 $N 会让该列静默为 NULL 或直接报错。
	mock.ExpectExec("INSERT INTO usage_logs").
		WithArgs(anySliceToDriverValues(prepared.args)...).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := execUsageLogInsertNoResult(context.Background(), db, prepared)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// usageLogInsertColumnNamesBetween 解析 SQL 中起止标记之间的列名清单。
func usageLogInsertColumnNamesBetween(t *testing.T, query, startMarker, endMarker string) []string {
	t.Helper()
	start := strings.Index(query, startMarker)
	require.GreaterOrEqual(t, start, 0, "找不到 %q", startMarker)
	rest := query[start+len(startMarker):]
	end := strings.Index(rest, endMarker)
	require.GreaterOrEqual(t, end, 0, "找不到 %q", endMarker)
	names := make([]string, 0, 80)
	for _, field := range strings.Split(rest[:end], ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		names = append(names, field)
	}
	return names
}
