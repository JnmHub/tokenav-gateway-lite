package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const openAIPriorityLongContextE2EModel = "custom-priority-long-context-e2e"

type openAIPriorityLongContextE2EHTTPUpstream struct {
	client *http.Client
}

func (u *openAIPriorityLongContextE2EHTTPUpstream) Do(
	req *http.Request,
	_ string,
	_ int64,
	_ int,
) (*http.Response, error) {
	return u.client.Do(req)
}

func (u *openAIPriorityLongContextE2EHTTPUpstream) DoWithTLS(
	req *http.Request,
	proxyURL string,
	accountID int64,
	accountConcurrency int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

type openAIPriorityLongContextE2EUpstreamObservation struct {
	path               string
	authorization      string
	model              string
	serviceTier        string
	serviceTierPresent bool
}

func TestOpenAIPriorityAndLongContextControlledUpstreamMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ptr := func(value string) *string { return &value }
	cases := []struct {
		name                    string
		inputTokens             int
		requestTier             string
		responseTier            *string
		detectByRequest         bool
		detectByResponse        bool
		wantEffectiveTier       *string
		wantDetectionSource     string
		wantAccountID           int64
		wantLongContextBilling  bool
		wantInputPricePerToken  float64
		wantOutputPricePerToken float64
	}{
		{
			name:                    "both_sources_response_priority_below_threshold",
			inputTokens:             271999,
			requestTier:             "priority",
			responseTier:            ptr("priority"),
			detectByRequest:         true,
			detectByResponse:        true,
			wantEffectiveTier:       ptr("priority"),
			wantDetectionSource:     "response",
			wantAccountID:           101,
			wantInputPricePerToken:  3e-6,
			wantOutputPricePerToken: 4e-6,
		},
		{
			name:                    "both_sources_request_fallback_at_threshold",
			inputTokens:             272000,
			requestTier:             "priority",
			responseTier:            nil,
			detectByRequest:         true,
			detectByResponse:        true,
			wantEffectiveTier:       ptr("priority"),
			wantDetectionSource:     "request",
			wantAccountID:           101,
			wantInputPricePerToken:  3e-6,
			wantOutputPricePerToken: 4e-6,
		},
		{
			name:                    "both_sources_response_default_wins_above_threshold",
			inputTokens:             272001,
			requestTier:             "priority",
			responseTier:            ptr("default"),
			detectByRequest:         true,
			detectByResponse:        true,
			wantEffectiveTier:       ptr("default"),
			wantDetectionSource:     "response",
			wantAccountID:           202,
			wantLongContextBilling:  true,
			wantInputPricePerToken:  7e-6,
			wantOutputPricePerToken: 8e-6,
		},
		{
			name:                    "both_sources_response_priority_wins_above_threshold",
			inputTokens:             272001,
			requestTier:             "default",
			responseTier:            ptr("priority"),
			detectByRequest:         true,
			detectByResponse:        true,
			wantEffectiveTier:       ptr("priority"),
			wantDetectionSource:     "response",
			wantAccountID:           202,
			wantLongContextBilling:  true,
			wantInputPricePerToken:  11e-6,
			wantOutputPricePerToken: 12e-6,
		},
		{
			name:                    "request_only_ignores_conflicting_response",
			inputTokens:             271999,
			requestTier:             "priority",
			responseTier:            ptr("default"),
			detectByRequest:         true,
			detectByResponse:        false,
			wantEffectiveTier:       ptr("priority"),
			wantDetectionSource:     "request",
			wantAccountID:           101,
			wantInputPricePerToken:  3e-6,
			wantOutputPricePerToken: 4e-6,
		},
		{
			name:                    "response_only_missing_uses_standard_long_context_billing",
			inputTokens:             272001,
			requestTier:             "priority",
			responseTier:            nil,
			detectByRequest:         false,
			detectByResponse:        true,
			wantEffectiveTier:       nil,
			wantDetectionSource:     "standard_default",
			wantAccountID:           202,
			wantLongContextBilling:  true,
			wantInputPricePerToken:  7e-6,
			wantOutputPricePerToken: 8e-6,
		},
	}

	pricingService := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		openAIPriorityLongContextE2EModel: {
			InputCostPerToken:                   1e-6,
			OutputCostPerToken:                  2e-6,
			InputCostPerTokenPriority:           3e-6,
			OutputCostPerTokenPriority:          4e-6,
			LongContextInputTokenThreshold:      272000,
			InputCostPerTokenAbove272K:          7e-6,
			OutputCostPerTokenAbove272K:         8e-6,
			InputCostPerTokenPriorityAbove272K:  11e-6,
			OutputCostPerTokenPriorityAbove272K: 12e-6,
			SupportsServiceTier:                 true,
		},
	}}
	requestBodies := make(map[string][]byte)

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			bodyKey := fmt.Sprintf("%d/%s", tt.inputTokens, tt.requestTier)
			body := requestBodies[bodyKey]
			if body == nil {
				body = buildOpenAIPriorityLongContextE2ERequestBody(t, tt.inputTokens, tt.requestTier)
				requestBodies[bodyKey] = body
			}
			body = append([]byte(nil), body...)

			observations := make(chan openAIPriorityLongContextE2EUpstreamObservation, 1)
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestBody, readErr := io.ReadAll(r.Body)
				if readErr != nil {
					http.Error(w, readErr.Error(), http.StatusBadRequest)
					return
				}
				serviceTier := gjson.GetBytes(requestBody, "service_tier")
				observations <- openAIPriorityLongContextE2EUpstreamObservation{
					path:               r.URL.Path,
					authorization:      r.Header.Get("Authorization"),
					model:              gjson.GetBytes(requestBody, "model").String(),
					serviceTier:        serviceTier.String(),
					serviceTierPresent: serviceTier.Exists(),
				}

				response := map[string]any{
					"id":    "resp_" + tt.name,
					"model": openAIPriorityLongContextE2EModel,
					"usage": map[string]any{
						"input_tokens":  tt.inputTokens,
						"output_tokens": 17,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
						},
					},
				}
				if tt.responseTier != nil {
					response["service_tier"] = *tt.responseTier
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("x-request-id", "req_"+tt.name)
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer upstreamServer.Close()

			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			subRepo := &openAIRecordUsageSubRepoStub{}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
			svc.cfg.Default.RateMultiplier = 1
			svc.cfg.Gateway.Scheduling.LoadBatchEnabled = false
			svc.cfg.Security.URLAllowlist.Enabled = false
			svc.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			svc.billingService = NewBillingService(svc.cfg, pricingService)
			svc.httpUpstream = &openAIPriorityLongContextE2EHTTPUpstream{client: upstreamServer.Client()}

			groupID := int64(9001)
			group := &Group{
				ID:                            groupID,
				Name:                          "priority-long-context-e2e",
				Platform:                      PlatformOpenAI,
				RateMultiplier:                1,
				AllowOpenAILongContextBilling: true,
			}
			standardAccount := newOpenAIPriorityLongContextE2EAccount(101, groupID, upstreamServer.URL, false, 0)
			longContextAccount := newOpenAIPriorityLongContextE2EAccount(202, groupID, upstreamServer.URL, true, 5)
			svc.accountRepo = schedulerGroupAwareOpenAIAccountRepo{
				schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{
					accounts: []Account{standardAccount, longContextAccount},
				},
			}
			svc.cache = &schedulerTestGatewayCache{}
			svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{})

			settings := DefaultOpenAIFastPolicySettings()
			settings.DetectionByRequestEnabled = tt.detectByRequest
			settings.DetectionByResponseEnabled = tt.detectByResponse
			policyCtx := withOpenAIFastPolicyContext(context.Background(), settings)
			routingCtx, routingDecision, err := svc.PrepareOpenAILongContextRouting(
				policyCtx,
				group,
				PlatformOpenAI,
				body,
				openAIPriorityLongContextE2EModel,
			)
			require.NoError(t, err)
			require.Equal(t, tt.inputTokens, routingDecision.EstimatedInputTokens)
			require.Equal(t, 272000, routingDecision.InputTokenThreshold)
			require.Equal(t, tt.inputTokens > 272000, routingDecision.Active)
			require.Equal(t, tt.inputTokens > 272000, routingDecision.RequireProviderLongContextCost)

			selection, _, err := svc.selectAccountWithScheduler(
				routingCtx,
				&groupID,
				"",
				"",
				openAIPriorityLongContextE2EModel,
				"",
				nil,
				OpenAIUpstreamTransportAny,
				OpenAIEndpointCapabilityChatCompletions,
				"",
				false,
				PlatformOpenAI,
			)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.NotNil(t, selection.Account)
			if selection.ReleaseFunc != nil {
				defer selection.ReleaseFunc()
			}
			require.Equal(t, tt.wantAccountID, selection.Account.ID)
			require.Equal(t, tt.inputTokens > 272000, selection.Account.IsOpenAILongContextBillingEnabled())

			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(routingCtx)
			request.Header.Set("Content-Type", "application/json")
			ginContext.Request = request

			result, err := svc.Forward(routingCtx, ginContext, selection.Account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, tt.inputTokens, result.Usage.InputTokens)
			require.Equal(t, 17, result.Usage.OutputTokens)
			requireOptionalOpenAIString(t, tt.wantEffectiveTier, result.ServiceTier)
			requireOptionalOpenAIString(t, ptr(tt.requestTier), result.RequestedServiceTier)
			requireOptionalOpenAIString(t, ptr(tt.requestTier), result.SentServiceTier)
			requireOptionalOpenAIString(t, tt.responseTier, result.ResponseServiceTier)
			require.Equal(t, tt.wantDetectionSource, result.PriorityDetectionSource)

			observation := <-observations
			require.Equal(t, "/v1/responses", observation.path)
			require.Equal(t, "Bearer sk-e2e", observation.authorization)
			require.Equal(t, openAIPriorityLongContextE2EModel, observation.model)
			require.True(t, observation.serviceTierPresent)
			require.Equal(t, tt.requestTier, observation.serviceTier)

			user := &User{ID: 5001, Email: "priority-long-context@example.test", Balance: 100}
			apiKey := &APIKey{ID: 6001, UserID: user.ID, User: user, GroupID: &groupID, Group: group}
			err = svc.RecordUsage(routingCtx, &OpenAIRecordUsageInput{
				Result:  result,
				APIKey:  apiKey,
				User:    user,
				Account: selection.Account,
			})
			require.NoError(t, err)
			require.Equal(t, 1, usageRepo.calls)
			require.NotNil(t, usageRepo.lastLog)

			usageLog := usageRepo.lastLog
			require.Equal(t, tt.wantAccountID, usageLog.AccountID)
			require.Equal(t, tt.inputTokens, usageLog.InputTokens)
			require.Equal(t, 17, usageLog.OutputTokens)
			requireOptionalOpenAIString(t, tt.wantEffectiveTier, usageLog.ServiceTier)
			requireOptionalOpenAIString(t, ptr(tt.requestTier), usageLog.RequestedServiceTier)
			requireOptionalOpenAIString(t, ptr(tt.requestTier), usageLog.SentServiceTier)
			requireOptionalOpenAIString(t, tt.responseTier, usageLog.ResponseServiceTier)
			requireOptionalOpenAIString(t, ptr(tt.wantDetectionSource), usageLog.PriorityDetectionSource)
			require.Nil(t, usageLog.BillingTier, "token billing must not invent a per-request/image tier")
			requireOptionalOpenAIString(t, ptr(string(BillingModeToken)), usageLog.BillingMode)
			require.Equal(t, tt.wantLongContextBilling, usageLog.LongContextBillingApplied)

			expectedInputCost := float64(tt.inputTokens) * tt.wantInputPricePerToken
			expectedOutputCost := float64(17) * tt.wantOutputPricePerToken
			expectedTotalCost := expectedInputCost + expectedOutputCost
			require.InDelta(t, expectedInputCost, usageLog.InputCost, 1e-10)
			require.InDelta(t, expectedOutputCost, usageLog.OutputCost, 1e-10)
			require.InDelta(t, expectedTotalCost, usageLog.TotalCost, 1e-10)
			require.InDelta(t, expectedTotalCost, usageLog.ActualCost, 1e-10)
			require.Equal(t, 1, userRepo.deductCalls)
			require.InDelta(t, expectedTotalCost, userRepo.lastAmount, 1e-10)
		})
	}
}

func buildOpenAIPriorityLongContextE2ERequestBody(t *testing.T, targetTokens int, serviceTier string) []byte {
	t.Helper()

	repetitions := targetTokens
	for attempt := 0; attempt < 8; attempt++ {
		require.GreaterOrEqual(t, repetitions, 0)
		body, err := json.Marshal(map[string]any{
			"model":        openAIPriorityLongContextE2EModel,
			"stream":       false,
			"service_tier": serviceTier,
			"input":        strings.Repeat("x ", repetitions),
		})
		require.NoError(t, err)

		estimated, err := estimateOpenAIInputTokensFromRequestBody(openAIPriorityLongContextE2EModel, body)
		require.NoError(t, err)
		if estimated == targetTokens {
			return body
		}
		repetitions += targetTokens - estimated
	}

	require.FailNow(t, "failed to construct exact OpenAI input-token boundary body", "target=%d tier=%s", targetTokens, serviceTier)
	return nil
}

func newOpenAIPriorityLongContextE2EAccount(
	id int64,
	groupID int64,
	baseURL string,
	longContextBilling bool,
	priority int,
) Account {
	return Account{
		ID:          id,
		Name:        fmt.Sprintf("openai-e2e-%d", id),
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    priority,
		GroupIDs:    []int64{groupID},
		Credentials: map[string]any{
			"api_key":  "sk-e2e",
			"base_url": baseURL,
			"model_mapping": map[string]any{
				openAIPriorityLongContextE2EModel: openAIPriorityLongContextE2EModel,
			},
		},
		Extra: map[string]any{
			openAILongContextBillingEnabledKey:       longContextBilling,
			openai_compat.ExtraKeyResponsesMode:      string(openai_compat.ResponsesSupportModeAuto),
			openai_compat.ExtraKeyResponsesSupported: true,
		},
	}
}

func requireOptionalOpenAIString(t *testing.T, expected, actual *string) {
	t.Helper()
	if expected == nil {
		require.Nil(t, actual)
		return
	}
	require.NotNil(t, actual)
	require.Equal(t, *expected, *actual)
}
