package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestContentModerationAIServiceURLs(t *testing.T) {
	for _, base := range []string{"http://192.168.1.20:8000/v1", "http://review.internal:8080", "http://example.com/review", "https://review.example:8443/v1", "http://[fd00::1]:9000/v1", "http://localhost:9000"} {
		t.Run(base, func(t *testing.T) {
			repo := &aiSettingRepo{values: map[string]string{}}
			s := NewContentModerationService(repo, nil, nil, nil, nil, nil, nil)
			view, err := s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{AI: &UpdateContentModerationAIConfigInput{BaseURL: &base}})
			require.NoError(t, err)
			require.Equal(t, base, view.AI.BaseURL)
			again, err := s.GetConfig(context.Background())
			require.NoError(t, err)
			require.Equal(t, base, again.AI.BaseURL)
			require.Equal(t, view.AI.ConfigFingerprint, again.AI.ConfigFingerprint)
		})
	}
	for _, base := range []string{"ftp://review.internal", "file:///review", "http:///missing", "http://user:secret@review.internal", "http://review.internal?token=secret", "http://review.internal?", "http://review.internal/#fragment", "http://review.internal/#", "http://review.internal:70000", "http://review.internal:abc", "http://:8080"} {
		t.Run("reject/"+base, func(t *testing.T) {
			cfg := defaultContentModerationAIConfig()
			cfg.BaseURL = base
			require.Error(t, cfg.validate())
		})
	}
}

func TestContentModerationAIAdaptersHTTPHTTPSNonLoopback(t *testing.T) {
	for _, adapter := range []string{ContentModerationAIAdapterChat, ContentModerationAIAdapterResponses} {
		for _, tls := range []bool{false, true} {
			t.Run(adapter+"/"+map[bool]string{false: "http", true: "https"}[tls], func(t *testing.T) {
				provider, calls, _ := aiSyntheticProvider(t, adapter, nil, 200, 0)
				defer provider.Close()
				var server *httptest.Server
				if tls {
					server = httptest.NewTLSServer(provider.Config.Handler)
				} else {
					server = httptest.NewServer(provider.Config.Handler)
				}
				defer server.Close()
				cfg := aiTestConfig(server.URL, adapter)
				s := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
				client := server.Client()
				s.httpClient = client
				if !tls {
					cfg.AI.BaseURL = "http://review.internal:8080"
					transport := client.Transport
					if transport == nil {
						transport = http.DefaultTransport
					}
					s.httpClient = &http.Client{Transport: aiReviewRoundTripper(func(req *http.Request) (*http.Response, error) {
						clone := req.Clone(req.Context())
						clone.URL.Host = strings.TrimPrefix(server.URL, "http://")
						return transport.RoundTrip(clone)
					})}
				}
				require.NoError(t, cfg.AI.validate())
				s.SetAIStore(&aiTestStore{})
				d := s.checkAI(context.Background(), aiTestInput(`{"input":"safe synthetic"}`), cfg, false)
				require.Equal(t, "allow", d.Verdict)
				require.True(t, d.Coverage.Complete)
				require.EqualValues(t, 1, calls.Load())
			})
		}
	}
}

type aiReviewRoundTripper func(*http.Request) (*http.Response, error)

func (f aiReviewRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestContentModerationAIProviderErrorsAreClassifiedAndPrivate(t *testing.T) {
	for _, adapter := range []string{ContentModerationAIAdapterChat, ContentModerationAIAdapterResponses} {
		for _, tc := range []struct {
			status int
			code   string
		}{
			{401, "ai_provider_invalid_credential"}, {403, "ai_provider_forbidden"}, {404, "ai_provider_not_found"},
			{400, "ai_provider_invalid_request"}, {422, "ai_provider_invalid_request"}, {429, "ai_provider_rate_limit"}, {503, "ai_provider_http_error"},
		} {
			t.Run(adapter+"/"+tc.code, func(t *testing.T) {
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, `{"error":"synthetic-key private-provider-body private-customer-text"}`)
				}))
				defer provider.Close()
				s := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
				store := &aiTestStore{}
				s.SetAIStore(store)
				cfg := aiTestConfig(provider.URL, adapter)
				d := s.checkAI(context.Background(), aiTestInput(`{"input":"safe"}`), cfg, false)
				require.Equal(t, "error", d.Verdict)
				require.Equal(t, tc.code, d.ErrorCode)
				require.Equal(t, tc.status, d.ProviderHTTPStatus)
				require.True(t, d.Blocked)
				require.False(t, d.Flagged)
				require.Empty(t, store.penalties)
				require.True(t, d.ProviderCalled)
				require.Equal(t, "provider", d.Phase)
				require.NotContains(t, aiJSON(d), "synthetic-key")
				require.NotContains(t, aiJSON(d), "private-provider-body")
				require.NotContains(t, aiJSON(d), "private-customer-text")
			})
		}
	}
}

func TestContentModerationAIDoesNotFollowRedirects(t *testing.T) {
	var forwarded atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer target.Close()
	for _, adapter := range []string{ContentModerationAIAdapterChat, ContentModerationAIAdapterResponses} {
		t.Run(adapter, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			}))
			defer provider.Close()
			s := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
			s.SetAIStore(&aiTestStore{})
			d := s.checkAI(context.Background(), aiTestInput(`{"input":"safe"}`), aiTestConfig(provider.URL, adapter), false)
			require.Equal(t, "error", d.Verdict)
			require.Equal(t, 307, d.ProviderHTTPStatus)
			require.Zero(t, forwarded.Load())
		})
	}
}

func aiReviewService(t *testing.T, cfg *ContentModerationConfig) (*ContentModerationService, *aiReviewSettings) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &aiReviewSettings{values: map[string]string{SettingKeyContentModerationConfig: string(raw), SettingKeyRiskControlEnabled: "true"}}
	s := NewContentModerationService(repo, nil, nil, nil, nil, nil, nil)
	s.SetAIStore(&aiTestStore{})
	return s, repo
}

type aiReviewSettings struct {
	SettingRepository
	mu     sync.Mutex
	values map[string]string
}

func (r *aiReviewSettings) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", ErrSettingNotFound
}
func (r *aiReviewSettings) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (r *aiReviewSettings) SetMultiple(_ context.Context, values map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func TestContentModerationAIReadinessSeparatesSavedTestedAndProduction(t *testing.T) {
	provider, calls, _ := aiSyntheticProvider(t, ContentModerationAIAdapterChat, nil, 200, 0)
	defer provider.Close()
	cfg := aiTestConfig(provider.URL, ContentModerationAIAdapterChat)
	cfg.AI.ExternalDataConsent = false
	s, _ := aiReviewService(t, cfg)
	m, err := s.GetAIMetrics(context.Background())
	require.NoError(t, err)
	require.True(t, m.ConfigSaved)
	require.Equal(t, "not_tested", m.Test.Status)
	require.False(t, m.Production.Ready)
	require.Equal(t, "production_not_ready", m.Production.State)
	first, err := s.TestAI(context.Background(), TestContentModerationAIInput{CaseID: "allow", ConfigFingerprint: m.ConfigFingerprint})
	require.NoError(t, err)
	require.Equal(t, "passed", first.Test.Status)
	require.Equal(t, "allow", first.Verdict)
	m, err = s.GetAIMetrics(context.Background())
	require.NoError(t, err)
	require.Equal(t, "passed", m.Test.Status)
	require.False(t, m.Production.Ready, "synthetic bypass of consent must not enable production")
	require.Zero(t, m.ProductionChecked)
	require.EqualValues(t, 1, m.Checked)
	require.Contains(t, aiReviewMissingChecks(m), "external_data_consent")
	_, err = s.TestAI(context.Background(), TestContentModerationAIInput{CaseID: "allow"})
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load(), "explicit testing requires fresh provider evidence")

	consent := true
	view, err := s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{AI: &UpdateContentModerationAIConfigInput{ExternalDataConsent: &consent}})
	require.NoError(t, err)
	require.NotEqual(t, first.ConfigFingerprint, view.AI.ConfigFingerprint)
	m, err = s.GetAIMetrics(context.Background())
	require.NoError(t, err)
	require.Equal(t, "stale", m.Test.Status)
	require.True(t, m.Production.Ready, "saving consent must not require another synthetic test")
	old, err := s.TestAI(context.Background(), TestContentModerationAIInput{CaseID: "allow", ConfigFingerprint: first.ConfigFingerprint})
	require.NoError(t, err)
	require.Equal(t, "ai_test_config_changed", old.ErrorCode)
	require.EqualValues(t, 2, calls.Load())
	fresh, err := s.TestAI(context.Background(), TestContentModerationAIInput{CaseID: "block", ConfigFingerprint: view.AI.ConfigFingerprint})
	require.NoError(t, err)
	require.Equal(t, "passed", fresh.Test.Status)
	require.Equal(t, "block", fresh.Verdict, "a valid block result can pass the harmful synthetic case")
	m, err = s.GetAIMetrics(context.Background())
	require.NoError(t, err)
	require.True(t, m.Production.Ready)
	require.Equal(t, "production_conditions_met", m.Production.State)
	require.Zero(t, m.ProductionChecked)
	// 策略改动后再改回也不能复活早先的通过证据，但不要求为生产运行重新测试。
	policy := cfg.AI.Policy + " Synthetic policy revision."
	_, err = s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{AI: &UpdateContentModerationAIConfigInput{Policy: &policy}})
	require.NoError(t, err)
	policy = cfg.AI.Policy
	_, err = s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{AI: &UpdateContentModerationAIConfigInput{Policy: &policy}})
	require.NoError(t, err)
	m, err = s.GetAIMetrics(context.Background())
	require.NoError(t, err)
	require.Equal(t, "stale", m.Test.Status)
	require.True(t, m.Production.Ready, "stale evidence does not change production conditions")

	d := s.checkAI(context.Background(), aiTestInput(`{"input":"safe production"}`), cfg, false)
	require.Equal(t, "ai_external_data_not_authorized", d.ErrorCode)
	m, err = s.GetAIMetrics(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, m.ProductionChecked)
	require.EqualValues(t, 1, m.ProductionErrors)
}

func aiReviewMissingChecks(m *ContentModerationAIMetrics) []string {
	var codes []string
	for _, check := range m.Production.Checks {
		if !check.Ready {
			codes = append(codes, check.Code)
		}
	}
	return codes
}

func TestContentModerationAIUncoveredSyntheticRequiresCoverageEvidence(t *testing.T) {
	for _, caseID := range []string{"unsupported_media", "uncovered_field"} {
		for _, tc := range []struct {
			verdict   string
			uncovered []string
			want      string
		}{
			{"allow", []string{"unknown:synthetic"}, "passed"},
			{"allow", nil, "failed"},
			{"block", []string{"unknown:synthetic"}, "failed"},
		} {
			d := &ContentModerationDecision{Verdict: tc.verdict, Coverage: ContentModerationAICoverage{Complete: true, UncoveredFields: tc.uncovered}}
			status := contentModerationAISyntheticStatus(caseID, "synthetic", d)
			require.Equal(t, tc.want, status.Status, caseID)
			if tc.want == "failed" {
				require.Equal(t, "ai_test_unexpected_verdict", status.ErrorCode)
			}
		}
	}
}

func TestContentModerationAIReadinessReportsEachMissingGate(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.Enabled, cfg.AllGroups = false, false
	cfg.GroupIDs = nil
	cfg.ModelFilter = ContentModerationModelFilter{Type: ContentModerationModelFilterInclude}
	m := &ContentModerationAIMetrics{}
	m.Production = buildContentModerationAIReadiness(cfg, false, false, m)
	for _, code := range []string{"config_saved", "risk_control_enabled", "content_moderation_enabled", "ai_engine", "group_scope", "model_scope", "dedicated_key", "external_data_consent"} {
		require.Contains(t, aiReviewMissingChecks(m), code)
	}
	for _, check := range m.Production.Checks {
		require.NotContains(t, []string{"input_price", "output_price", "node_money_budget", "tenant_money_budget", "node_token_budget", "tenant_token_budget", "budget_store", "penalty_store", "saved_config_tested"}, check.Code)
	}
	cfg.Mode = ContentModerationModeOff
	m.Production = buildContentModerationAIReadiness(cfg, false, false, m)
	require.Contains(t, aiReviewMissingChecks(m), "review_mode")
}

func TestContentModerationAIConfigChangedWhileTestResponsePending(t *testing.T) {
	provider, _, _ := aiSyntheticProvider(t, ContentModerationAIAdapterChat, nil, 200, 0)
	defer provider.Close()
	arrived, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		provider.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	cfg := aiTestConfig(server.URL, ContentModerationAIAdapterChat)
	s, _ := aiReviewService(t, cfg)
	result := make(chan *ContentModerationDecision, 1)
	go func() {
		d, _ := s.TestAI(context.Background(), TestContentModerationAIInput{CaseID: "allow"})
		result <- d
	}()
	select {
	case <-arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("test never reached synthetic provider")
	}
	model := "another-model"
	view, err := s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{AI: &UpdateContentModerationAIConfigInput{Model: &model}})
	require.NoError(t, err)
	close(release)
	d := <-result
	require.Equal(t, "allow", d.Verdict)
	require.Equal(t, "stale", d.Test.Status)
	require.NotEqual(t, view.AI.ConfigFingerprint, d.ConfigFingerprint)
	m, err := s.GetAIMetrics(context.Background())
	require.NoError(t, err)
	require.Equal(t, "stale", m.Test.Status)
	require.True(t, m.Production.Ready, "stale evidence does not change production conditions")
}

func TestContentModerationAITestEvidenceDoesNotUseRevisionOrOverrideNewerTest(t *testing.T) {
	cfg := aiTestConfig("http://review.internal", ContentModerationAIAdapterChat)
	original := contentModerationAIConfigFingerprint(cfg)
	for _, mutate := range []func(*ContentModerationConfig){
		func(c *ContentModerationConfig) { c.AI.APIKey = "another-synthetic-secret" },
		func(c *ContentModerationConfig) { c.AI.ModelRevision = "verified-by-text" },
		func(c *ContentModerationConfig) { c.AI.Policy += " Synthetic policy revision." },
		func(c *ContentModerationConfig) { c.AI.ExternalDataConsent = false },
		func(c *ContentModerationConfig) { c.Mode = "shadow" },
		func(c *ContentModerationConfig) { c.AllGroups = false; c.GroupIDs = []int64{9} },
	} {
		changed := cloneContentModerationConfig(cfg)
		mutate(changed)
		require.NotEqual(t, original, contentModerationAIConfigFingerprint(changed))
	}
	r := &contentModerationAIRuntime{}
	old := r.beginTest(original, "allow")
	newer := r.beginTest(original, "block")
	r.finishTest(newer, ContentModerationAITestStatus{Status: "failed", ConfigFingerprint: original})
	r.finishTest(old, ContentModerationAITestStatus{Status: "passed", ConfigFingerprint: original})
	require.Equal(t, "failed", r.testStatus(original).Status)
	r.finishTest(newer, ContentModerationAITestStatus{Status: "passed", ConfigFingerprint: original})
	require.Equal(t, "stale", r.testStatus("another-fingerprint").Status)
	require.Equal(t, "stale", r.testStatus(original).Status, "observed configuration changes cannot revive old test evidence")
	pending := r.beginTest(original, "allow")
	r.testStatus("changed-during-test")
	require.False(t, r.finishTest(pending, ContentModerationAITestStatus{Status: "passed", ConfigFingerprint: original}))
	require.Equal(t, "stale", r.testStatus(original).Status, "a reverted in-flight config remains stale")
	require.Equal(t, "not_tested", (&contentModerationAIRuntime{}).testStatus(original).Status, "restart has no test evidence")
	m := &ContentModerationAIMetrics{ConfigFingerprint: original, StoreReady: true}
	cfg.AI.ModelRevision = "verified-by-text"
	m.ConfigFingerprint = contentModerationAIConfigFingerprint(cfg)
	m.Test = (&contentModerationAIRuntime{}).testStatus(m.ConfigFingerprint)
	m.Production = buildContentModerationAIReadiness(cfg, true, true, m)
	require.True(t, m.Production.Ready)
	require.Equal(t, "not_tested", m.Test.Status, "readiness and an editable revision cannot manufacture evidence")
}

func TestContentModerationAIReadinessOnlyNeedsStoreForPenalties(t *testing.T) {
	cfg := aiTestConfig("http://review.internal", ContentModerationAIAdapterChat)
	fingerprint := contentModerationAIConfigFingerprint(cfg)
	m := &ContentModerationAIMetrics{ConfigFingerprint: fingerprint, StoreReady: false, Test: ContentModerationAITestStatus{Status: "passed", ConfigFingerprint: fingerprint}}
	m.Production = buildContentModerationAIReadiness(cfg, true, true, m)
	require.True(t, m.Production.Ready, "valid review must not require price, budget or an unused penalty store")
	cfg.AI.PenaltiesEnabled = true
	m.ConfigFingerprint = contentModerationAIConfigFingerprint(cfg)
	m.Test.ConfigFingerprint = m.ConfigFingerprint
	m.Production = buildContentModerationAIReadiness(cfg, true, true, m)
	require.False(t, m.Production.Ready)
	require.Contains(t, aiReviewMissingChecks(m), "penalty_store")
	m.StoreReady = true
	m.Production = buildContentModerationAIReadiness(cfg, true, true, m)
	require.True(t, m.Production.Ready)
}

func TestContentModerationAIUnselectedInputIsSkippedWithoutExternalCall(t *testing.T) {
	server, calls, _ := aiSyntheticProvider(t, ContentModerationAIAdapterChat, nil, 200, 0)
	defer server.Close()
	cfg := aiTestConfig(server.URL, ContentModerationAIAdapterChat)
	cfg.AI.APIKey, cfg.AI.ExternalDataConsent = "", false
	s := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
	for _, body := range []string{
		`{"input":[],"vendor_extension":{"hidden":"private unknown content"}}`,
		`{"input":[{"role":"user","content":[{"type":"vendor_private","payload":"unrecognized"}]}]}`,
		`{"input":[{"type":"input_audio","data":"synthetic"}]}`,
		`{"input":[{"type":"input_file","file_id":"synthetic"}]}`,
		`{"input":[{"type":"input_image","image_url":"https://example.invalid/synthetic.png"}]}`,
	} {
		d := s.checkAI(context.Background(), aiTestInput(body), cfg, false)
		require.Equal(t, "allow", d.Verdict, body)
		require.True(t, d.Allowed)
		require.False(t, d.Flagged)
		require.False(t, d.ProviderCalled)
		require.Equal(t, "skipped", d.Phase)
		require.False(t, d.Coverage.Complete, "skipping content is not proof of review")
		require.Zero(t, d.Coverage.ReviewedCount)
		require.NotEmpty(t, d.Coverage.UncoveredFields, body)
	}
	require.Zero(t, calls.Load(), "no external payload means no consent/key prerequisite or provider call")
	cfg.AI.APIKey, cfg.AI.ExternalDataConsent = "synthetic-key", true
	d := s.checkAI(context.Background(), aiTestInput(`{"input":"safe selected text","vendor_extension":{"hidden":"unknown"}}`), cfg, false)
	require.Equal(t, "allow", d.Verdict)
	require.True(t, d.ProviderCalled)
	require.Equal(t, "completed", d.Phase)
	require.True(t, d.Coverage.Complete, "complete describes selected content, not unknown fields")
	require.NotEmpty(t, d.Coverage.UncoveredFields)
	require.EqualValues(t, 1, calls.Load())
}

func TestContentModerationAIConcurrentCallsBeyondFormerCapacityAllReachProvider(t *testing.T) {
	const requests = 160
	provider, calls, _ := aiSyntheticProvider(t, ContentModerationAIAdapterChat, nil, 200, 0)
	defer provider.Close()
	arrived, release := make(chan struct{}, requests), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		select {
		case <-release:
			provider.Config.Handler.ServeHTTP(w, r)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer unblock()
	cfg := aiTestConfig(server.URL, ContentModerationAIAdapterChat)
	s := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := make(chan *ContentModerationDecision, requests)
	for i := 0; i < requests; i++ {
		go func() { results <- s.checkAI(ctx, aiTestInput(`{"input":"safe concurrent text"}`), cfg, false) }()
	}
	// 所有请求同时停在模拟上游，证明它们没有被本地并发门、队列或额度挡住。
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	received := 0
waitForProvider:
	for received < requests {
		select {
		case <-arrived:
			received++
		case <-deadline.C:
			break waitForProvider
		}
	}
	unblock()
	require.Equal(t, requests, received, "every concurrent request must reach the provider before any completes")
	for i := 0; i < requests; i++ {
		select {
		case d := <-results:
			require.Equal(t, "allow", d.Verdict)
			require.True(t, d.ProviderCalled)
			require.True(t, d.Coverage.Complete)
		case <-ctx.Done():
			t.Fatal("concurrent review did not complete within the parent deadline")
		}
	}
	require.EqualValues(t, requests, calls.Load())
}

func TestContentModerationAIErrorsAreSingleCallsAndDoNotOpenLocalCircuit(t *testing.T) {
	server, calls, _ := aiSyntheticProvider(t, ContentModerationAIAdapterChat, nil, 503, 0)
	defer server.Close()
	cfg := aiTestConfig(server.URL, ContentModerationAIAdapterChat)
	cfg.AI.PenaltiesEnabled = true
	store := &aiTestStore{}
	s := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
	s.SetAIStore(store)
	for i := int64(1); i <= 8; i++ {
		d := s.checkAI(context.Background(), aiTestInput(`{"input":"safe repeated request"}`), cfg, false)
		require.Equal(t, "error", d.Verdict)
		require.Equal(t, "ai_provider_http_error", d.ErrorCode)
		require.True(t, d.ProviderCalled)
		require.False(t, d.Flagged)
		require.EqualValues(t, i, calls.Load(), "no retry, verdict cache or local circuit may alter one-call semantics")
	}
	require.Empty(t, store.penalties)
}

func TestContentModerationAICancelledParentStopsProviderAndNeverPenalizes(t *testing.T) {
	arrived, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(arrived)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	cfg := aiTestConfig(server.URL, ContentModerationAIAdapterChat)
	cfg.AI.PenaltiesEnabled = true
	store := &aiTestStore{}
	s := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
	s.SetAIStore(store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := aiTestInput(`{"input":"forbidden synthetic"}`)
	in.ControlPlaneKeyID, in.BillingSubjectType, in.BillingSubjectID = "stable-key", "user", in.UserID
	result := make(chan *ContentModerationDecision, 1)
	go func() { result <- s.checkAI(ctx, in, cfg, false) }()
	select {
	case <-arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("review never reached provider")
	}
	cancel()
	select {
	case d := <-result:
		require.Equal(t, "error", d.Verdict)
		require.False(t, d.Allowed)
		require.False(t, d.Flagged)
		require.True(t, d.ProviderCalled)
		require.Equal(t, "none", d.PenaltyState)
		require.Empty(t, store.penalties)
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not stop review")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("provider context did not inherit cancellation")
	}
}
