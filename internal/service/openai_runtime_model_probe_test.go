package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type runtimeProbeFakeUpstream struct {
	steps      []runtimeProbeFakeStep
	requests   []*http.Request
	bodies     [][]byte
	accountIDs []int64
}

type runtimeProbeFakeStep struct {
	statusCode   int
	body         string
	err          error
	wrong        bool
	empty        bool
	requestDelay time.Duration
	bodyDelay    time.Duration
	trackClose   *bool
}

type runtimeProbeDelayedReader struct {
	reader  io.Reader
	delay   time.Duration
	delayed bool
}

func (r *runtimeProbeDelayedReader) Read(p []byte) (int, error) {
	if !r.delayed {
		r.delayed = true
		time.Sleep(r.delay)
	}
	return r.reader.Read(p)
}

type runtimeProbeTrackingReadCloser struct {
	io.Reader
	closed *bool
}

type runtimeProbeAccountReloadRepo struct {
	AccountRepository
	account *Account
	reads   int
}

func (r *runtimeProbeAccountReloadRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	r.reads++
	return r.account, nil
}

func (r *runtimeProbeTrackingReadCloser) Close() error {
	if r != nil && r.closed != nil {
		*r.closed = true
	}
	return nil
}

func (u *runtimeProbeFakeUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.requests = append(u.requests, req)
	u.accountIDs = append(u.accountIDs, accountID)

	var body []byte
	if req != nil && req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		u.bodies = append(u.bodies, append([]byte(nil), body...))
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}

	step := runtimeProbeFakeStep{statusCode: http.StatusOK}
	if len(u.steps) > 0 {
		step = u.steps[0]
		u.steps = u.steps[1:]
	}
	if step.requestDelay > 0 {
		timer := time.NewTimer(step.requestDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	if step.err != nil {
		return nil, step.err
	}

	if step.statusCode == 0 {
		step.statusCode = http.StatusOK
	}
	if step.body == "" && step.statusCode >= 200 && step.statusCode < 300 {
		content := runtimeProbeAnswerFromBody(body)
		switch {
		case step.empty:
			content = ""
		case step.wrong:
			content = "99999"
		}
		if gjson.GetBytes(body, "input.0.content.0.text").Exists() {
			step.body = fmt.Sprintf(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n"+
					"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}]}}\n\n"+
					"data: [DONE]\n\n",
				content,
				content,
			)
		} else {
			step.body = fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, content)
		}
	}
	if step.body == "" {
		step.body = fmt.Sprintf(`{"error":{"message":"upstream returned %d"}}`, step.statusCode)
	}
	var responseReader io.Reader = strings.NewReader(step.body)
	if step.bodyDelay > 0 {
		responseReader = &runtimeProbeDelayedReader{reader: responseReader, delay: step.bodyDelay}
	}
	responseBody := io.ReadCloser(io.NopCloser(responseReader))
	if step.trackClose != nil {
		responseBody = &runtimeProbeTrackingReadCloser{Reader: responseReader, closed: step.trackClose}
	}
	return &http.Response{
		StatusCode: step.statusCode,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       responseBody,
	}, nil
}

func (u *runtimeProbeFakeUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

var runtimeProbeQuestionRegex = regexp.MustCompile(`Q:\s*(\d+)\s*([+-])\s*(\d+)\s*=\s*\?\s*A:`)

func runtimeProbeAnswerFromBody(body []byte) string {
	prompt := gjson.GetBytes(body, "messages.0.content").String()
	if prompt == "" {
		prompt = gjson.GetBytes(body, "input.0.content.0.text").String()
	}
	matches := runtimeProbeQuestionRegex.FindAllStringSubmatch(prompt, -1)
	if len(matches) == 0 {
		return ""
	}
	last := matches[len(matches)-1]
	a, _ := strconv.Atoi(last[1])
	b, _ := strconv.Atoi(last[3])
	switch last[2] {
	case "+":
		return strconv.Itoa(a + b)
	case "-":
		return strconv.Itoa(a - b)
	default:
		return ""
	}
}

func runtimeProbeTestAccount(id int64, name string) Account {
	return Account{
		ID:          id,
		Name:        name,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-runtime-probe-test",
			"base_url": "https://example.com/v1",
			"model_mapping": map[string]any{
				"gpt-5.5": "gpt-5.5",
			},
		},
	}
}

func runtimeProbeOAuthTestAccount(id int64, name string) Account {
	return Account{
		ID:          id,
		Name:        name,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-runtime-probe-test",
			"chatgpt_account_id": "account-runtime-probe-test",
			"model_mapping": map[string]any{
				"gpt-5.5": "gpt-5.5",
			},
		},
	}
}

func newRuntimeProbeOpenAIService(upstream HTTPUpstream, accounts ...Account) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		accountRepo:  stubOpenAIAccountRepo{accounts: accounts},
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}
}

func TestProbeOpenAIAccountRuntimeModelStatusMatrix(t *testing.T) {
	tests := []struct {
		name       string
		step       runtimeProbeFakeStep
		wantOK     bool
		wantStatus string
		wantCode   int
		wantError  string
	}{
		{
			name:       "correct challenge answer is operational",
			step:       runtimeProbeFakeStep{statusCode: http.StatusOK},
			wantOK:     true,
			wantStatus: MonitorStatusOperational,
			wantCode:   http.StatusOK,
		},
		{
			name:       "2xx wrong answer is content failure",
			step:       runtimeProbeFakeStep{statusCode: http.StatusOK, wrong: true},
			wantStatus: MonitorStatusFailed,
			wantCode:   http.StatusOK,
			wantError:  "challenge mismatch",
		},
		{
			name:       "2xx empty answer is content failure",
			step:       runtimeProbeFakeStep{statusCode: http.StatusOK, empty: true},
			wantStatus: MonitorStatusFailed,
			wantCode:   http.StatusOK,
			wantError:  "challenge mismatch",
		},
		{
			name:       "429 is request error",
			step:       runtimeProbeFakeStep{statusCode: http.StatusTooManyRequests},
			wantStatus: MonitorStatusError,
			wantCode:   http.StatusTooManyRequests,
			wantError:  "upstream returned 429",
		},
		{
			name:       "502 is request error",
			step:       runtimeProbeFakeStep{statusCode: http.StatusBadGateway},
			wantStatus: MonitorStatusError,
			wantCode:   http.StatusBadGateway,
			wantError:  "upstream returned 502",
		},
		{
			name:       "503 is request error",
			step:       runtimeProbeFakeStep{statusCode: http.StatusServiceUnavailable},
			wantStatus: MonitorStatusError,
			wantCode:   http.StatusServiceUnavailable,
			wantError:  "upstream returned 503",
		},
		{
			name:       "timeout is request error",
			step:       runtimeProbeFakeStep{err: context.DeadlineExceeded},
			wantStatus: MonitorStatusError,
			wantError:  "upstream request failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &runtimeProbeFakeUpstream{steps: []runtimeProbeFakeStep{tt.step}}
			svc := newRuntimeProbeOpenAIService(upstream)

			result := svc.probeOpenAIAccountRuntimeModel(context.Background(), ptrAccount(runtimeProbeTestAccount(1, "primary")), "gpt-5.5", time.Now())

			require.Equal(t, tt.wantOK, result.OK)
			require.Equal(t, tt.wantStatus, result.Status)
			if tt.wantCode > 0 {
				require.Equal(t, tt.wantCode, result.StatusCode)
			}
			if tt.wantError != "" {
				require.Contains(t, result.Error, tt.wantError)
			}
			require.Equal(t, int64(1), result.AccountID)
			require.Len(t, upstream.bodies, 1)
			require.Equal(t, "gpt-5.5", gjson.GetBytes(upstream.bodies[0], "model").String())
			require.False(t, gjson.GetBytes(upstream.bodies[0], "stream").Bool())
			require.Equal(t, "application/json", upstream.requests[0].Header.Get("Accept"))
		})
	}
}

func TestProbeRuntimeModelFailsOverToSecondAccount(t *testing.T) {
	upstream := &runtimeProbeFakeUpstream{steps: []runtimeProbeFakeStep{
		{statusCode: http.StatusServiceUnavailable},
		{statusCode: http.StatusOK},
	}}
	primary := runtimeProbeTestAccount(1, "bad-account")
	secondary := runtimeProbeTestAccount(2, "good-account")
	secondary.Priority = 1
	svc := newRuntimeProbeOpenAIService(upstream, primary, secondary)

	result := svc.ProbeRuntimeModel(context.Background(), "gpt-5.5")

	require.True(t, result.OK)
	require.Equal(t, MonitorStatusOperational, result.Status)
	require.Equal(t, int64(2), result.AccountID)
	require.Equal(t, []int64{1, 2}, upstream.accountIDs)
}

func TestProbeRuntimeModelSlowAccountsKeepBudgetForLaterCandidate(t *testing.T) {
	upstream := &runtimeProbeFakeUpstream{steps: []runtimeProbeFakeStep{
		{requestDelay: time.Second},
		{requestDelay: time.Second},
		{statusCode: http.StatusOK},
	}}
	primary := runtimeProbeTestAccount(1, "slow-primary")
	secondary := runtimeProbeTestAccount(2, "slow-secondary")
	secondary.Priority = 1
	tertiary := runtimeProbeTestAccount(3, "healthy-tertiary")
	tertiary.Priority = 1
	svc := newRuntimeProbeOpenAIService(upstream, primary, secondary, tertiary)

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	started := time.Now()
	result := svc.ProbeRuntimeModel(ctx, "gpt-5.5")

	require.True(t, result.OK)
	require.Equal(t, MonitorStatusOperational, result.Status)
	require.Equal(t, int64(3), result.AccountID)
	require.Equal(t, []int64{1, 2, 3}, upstream.accountIDs)
	require.Less(t, time.Since(started), 600*time.Millisecond)
}

func TestProbeRuntimeModelLargePoolKeepsUsefulTimeoutForNormalUpstream(t *testing.T) {
	steps := make([]runtimeProbeFakeStep, runtimeModelProbeMaxAccountAttempts)
	steps[0] = runtimeProbeFakeStep{requestDelay: 2500 * time.Millisecond, statusCode: http.StatusOK}
	accounts := make([]Account, 0, runtimeModelProbeMaxAccountAttempts)
	for index := 0; index < runtimeModelProbeMaxAccountAttempts; index++ {
		account := runtimeProbeTestAccount(int64(index+1), fmt.Sprintf("pool-%02d", index+1))
		account.Priority = index
		accounts = append(accounts, account)
	}
	svc := newRuntimeProbeOpenAIService(&runtimeProbeFakeUpstream{steps: steps}, accounts...)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := svc.ProbeRuntimeModel(ctx, "gpt-5.5")

	require.True(t, result.OK)
	require.Equal(t, int64(1), result.AccountID)
	require.GreaterOrEqual(t, result.Latency, 2500*time.Millisecond)
	require.Less(t, result.Latency, runtimeModelProbeDefaultAttemptTimeout)
}

func TestRuntimeModelProbeAttemptTimeoutMatchesHealthyLatencyBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	single := runtimeModelProbeAttemptTimeout(ctx, 1)
	largePool := runtimeModelProbeAttemptTimeout(ctx, runtimeModelProbeMaxAccountAttempts)

	require.GreaterOrEqual(t, single, 5900*time.Millisecond, "single account must be allowed to reach the 6s degraded threshold")
	require.GreaterOrEqual(t, largePool, 3*time.Second, "large pools must not compress healthy 2.5s responses into errors")
	require.LessOrEqual(t, single, monitorDegradedThreshold)
}

func TestProbeRuntimeModelSlowAccountsFailOnlyAfterEveryCandidate(t *testing.T) {
	upstream := &runtimeProbeFakeUpstream{steps: []runtimeProbeFakeStep{
		{requestDelay: time.Second},
		{requestDelay: time.Second},
		{requestDelay: time.Second},
	}}
	primary := runtimeProbeTestAccount(1, "slow-primary")
	secondary := runtimeProbeTestAccount(2, "slow-secondary")
	secondary.Priority = 1
	tertiary := runtimeProbeTestAccount(3, "slow-tertiary")
	tertiary.Priority = 1
	svc := newRuntimeProbeOpenAIService(upstream, primary, secondary, tertiary)

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	result := svc.ProbeRuntimeModel(ctx, "gpt-5.5")

	require.False(t, result.OK)
	require.Equal(t, MonitorStatusError, result.Status)
	require.Equal(t, int64(3), result.AccountID)
	require.Equal(t, []int64{1, 2, 3}, upstream.accountIDs)
}

func TestProbeRuntimeModelReturnsLastFailureWhenAllAccountsFail(t *testing.T) {
	upstream := &runtimeProbeFakeUpstream{steps: []runtimeProbeFakeStep{
		{statusCode: http.StatusBadGateway},
		{statusCode: http.StatusTooManyRequests},
	}}
	primary := runtimeProbeTestAccount(1, "bad-account")
	secondary := runtimeProbeTestAccount(2, "rate-limited-account")
	secondary.Priority = 1
	svc := newRuntimeProbeOpenAIService(upstream, primary, secondary)

	result := svc.ProbeRuntimeModel(context.Background(), "gpt-5.5")

	require.False(t, result.OK)
	require.Equal(t, MonitorStatusError, result.Status)
	require.Equal(t, http.StatusTooManyRequests, result.StatusCode)
	require.Equal(t, int64(2), result.AccountID)
	require.Equal(t, []int64{1, 2}, upstream.accountIDs)
}

func TestProbeRuntimeModelReportsNoAccounts(t *testing.T) {
	svc := newRuntimeProbeOpenAIService(&runtimeProbeFakeUpstream{})

	result := svc.ProbeRuntimeModel(context.Background(), "gpt-5.5")

	require.False(t, result.OK)
	require.Equal(t, MonitorStatusError, result.Status)
	require.Contains(t, result.Error, "no available")
}

func TestProbeOpenAIAccountRuntimeModelRequestErrorIsSanitized(t *testing.T) {
	upstream := &runtimeProbeFakeUpstream{steps: []runtimeProbeFakeStep{
		{err: errors.New("dial tcp 10.0.0.1:443: connect: connection refused")},
	}}
	svc := newRuntimeProbeOpenAIService(upstream)

	result := svc.probeOpenAIAccountRuntimeModel(context.Background(), ptrAccount(runtimeProbeTestAccount(1, "primary")), "gpt-5.5", time.Now())

	require.False(t, result.OK)
	require.Equal(t, MonitorStatusError, result.Status)
	require.Contains(t, result.Error, "upstream request failed")
}

func TestProbeOpenAIAccountRuntimeModelOAuthUsesStreamingResponses(t *testing.T) {
	closed := false
	upstream := &runtimeProbeFakeUpstream{steps: []runtimeProbeFakeStep{{trackClose: &closed}}}
	svc := newRuntimeProbeOpenAIService(upstream)

	result := svc.probeOpenAIAccountRuntimeModel(
		context.Background(),
		ptrAccount(runtimeProbeOAuthTestAccount(2, "oauth-runtime-probe")),
		"gpt-5.5",
		time.Now(),
	)

	require.True(t, result.OK)
	require.Equal(t, MonitorStatusOperational, result.Status)
	require.True(t, closed, "OAuth probe response body must always be closed")
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "text/event-stream", upstream.requests[0].Header.Get("Accept"))
	require.True(t, gjson.GetBytes(upstream.bodies[0], "stream").Bool())
	require.False(t, gjson.GetBytes(upstream.bodies[0], "max_output_tokens").Exists())
	require.Equal(t, chatgptCodexURL, upstream.requests[0].URL.String())
}

func TestProbeOpenAIAccountRuntimeModelOAuthLatencyIncludesStreamBody(t *testing.T) {
	const bodyDelay = 50 * time.Millisecond
	upstream := &runtimeProbeFakeUpstream{steps: []runtimeProbeFakeStep{{bodyDelay: bodyDelay}}}
	svc := newRuntimeProbeOpenAIService(upstream)

	result := svc.probeOpenAIAccountRuntimeModel(
		context.Background(),
		ptrAccount(runtimeProbeOAuthTestAccount(2, "oauth-runtime-probe")),
		"gpt-5.5",
		time.Now(),
	)

	require.True(t, result.OK)
	require.GreaterOrEqual(t, result.Latency, 40*time.Millisecond)
}

func TestReadOpenAIRuntimeResponsesProbeStream(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantText  string
		wantError string
	}{
		{
			name: "aggregates deltas until completed",
			body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"1\"}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"2\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{}}\n\n",
			wantText: "12",
		},
		{
			name:     "uses final text from completed response",
			body:     "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"42\"}]}]}}\n\n",
			wantText: "42",
		},
		{
			name:      "returns failed event message",
			body:      "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"capacity unavailable\"}}}\n\n",
			wantError: "capacity unavailable",
		},
		{
			name:      "returns top level error message",
			body:      "data: {\"error\":{\"message\":\"upstream rejected probe\"}}\n\n",
			wantError: "upstream rejected probe",
		},
		{
			name:      "rejects done before terminal event",
			body:      "data: [DONE]\n\n",
			wantError: "before response.completed",
		},
		{
			name:      "rejects incomplete EOF",
			body:      "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n",
			wantError: "before response.completed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, err := readOpenAIRuntimeResponsesProbeStream(strings.NewReader(tt.body))
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantText, text)
		})
	}
}

func TestProbeOpenAIAccountRuntimeModelOAuthClosesBodyOnStreamErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "failed event",
			body: "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"probe failed\"}}}\n\n",
		},
		{
			name: "incomplete EOF",
			body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			closed := false
			upstream := &runtimeProbeFakeUpstream{steps: []runtimeProbeFakeStep{{
				statusCode: http.StatusOK,
				body:       tt.body,
				trackClose: &closed,
			}}}
			svc := newRuntimeProbeOpenAIService(upstream)

			result := svc.probeOpenAIAccountRuntimeModel(
				context.Background(),
				ptrAccount(runtimeProbeOAuthTestAccount(2, "oauth-runtime-probe")),
				"gpt-5.5",
				time.Now(),
			)

			require.False(t, result.OK)
			require.Equal(t, MonitorStatusError, result.Status)
			require.True(t, closed, "OAuth probe response body must close on stream errors")
		})
	}
}

func TestProbeOpenAIAccountRuntimeModelOAuthSanitizesAndTruncatesStreamError(t *testing.T) {
	const apiKey = "sk-123456789012345678901234567890"
	const jwt = "eyJ12345678.eyJabcdefgh.abcdefgh"
	const shortCustomToken = "jnm"
	message := "upstream leaked " + apiKey + " and " + jwt + " and " + shortCustomToken + " " + strings.Repeat("x", monitorMessageMaxBytes*2)
	body := fmt.Sprintf("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":%q}}}\n\n", message)
	upstream := &runtimeProbeFakeUpstream{steps: []runtimeProbeFakeStep{{statusCode: http.StatusOK, body: body}}}
	svc := newRuntimeProbeOpenAIService(upstream)
	account := runtimeProbeOAuthTestAccount(2, "oauth-runtime-probe")
	account.Credentials["access_token"] = shortCustomToken

	result := svc.probeOpenAIAccountRuntimeModel(
		context.Background(),
		&account,
		"gpt-5.5",
		time.Now(),
	)

	require.False(t, result.OK)
	require.Equal(t, MonitorStatusError, result.Status)
	require.NotContains(t, result.Error, apiKey)
	require.NotContains(t, result.Error, jwt)
	require.NotContains(t, result.Error, shortCustomToken)
	require.Contains(t, result.Error, "REDACTED")
	require.LessOrEqual(t, len(result.Error), monitorMessageMaxBytes)
	require.Contains(t, result.Error, "...(truncated)")
}

func TestProbeOpenAIAccountRuntimeModelAgentIdentityRecoversInvalidTaskOnce(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	account := &Account{
		ID:          3,
		Name:        "agent-runtime-probe",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"auth_mode":                  OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":           key.runtimeID,
			"agent_private_key":          privateKey,
			"task_id":                    "task-probe-old",
			"chatgpt_account_id":         "account-runtime-probe",
			"chatgpt_account_is_fedramp": true,
			"model_mapping": map[string]any{
				"gpt-5.5": "gpt-5.5",
			},
		},
	}
	repo := &accountTestAgentIdentityRepo{account: account}
	registerCalls := 0
	registerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		registerCalls++
		_, _ = io.WriteString(w, `{"task_id":"task-probe-new"}`)
	}))
	defer registerServer.Close()
	oldBase := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = registerServer.URL
	t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = oldBase })

	upstream := &runtimeProbeFakeUpstream{steps: []runtimeProbeFakeStep{
		{statusCode: http.StatusUnauthorized, body: `{"error":{"code":"invalid_task_id"}}`},
		{statusCode: http.StatusOK},
	}}
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: &config.Config{}, httpUpstream: upstream}

	result := svc.probeOpenAIAccountRuntimeModel(context.Background(), account, "gpt-5.5", time.Now())

	require.True(t, result.OK)
	require.Equal(t, MonitorStatusOperational, result.Status)
	require.Equal(t, 1, registerCalls)
	require.Len(t, upstream.requests, 2)
	for _, request := range upstream.requests {
		require.True(t, strings.HasPrefix(request.Header.Get("Authorization"), "AgentAssertion "))
		require.NotContains(t, request.Header.Get("Authorization"), privateKey)
		require.Equal(t, "account-runtime-probe", request.Header.Get("ChatGPT-Account-ID"))
		require.Equal(t, "true", request.Header.Get("X-OpenAI-Fedramp"))
	}
	require.Equal(t, "task-probe-old", decodeAgentAssertionTask(t, upstream.requests[0].Header.Get("Authorization")))
	require.Equal(t, "task-probe-new", decodeAgentAssertionTask(t, upstream.requests[1].Header.Get("Authorization")))
	require.Equal(t, "task-probe-new", account.GetCredential("task_id"))
}

func TestProbeOpenAIAccountRuntimeModelAgentIdentityAuthenticatesWithoutOAuthTokens(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	account := &Account{
		ID:          4,
		Name:        "agent-runtime-probe-no-token",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"auth_mode":         OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":  key.runtimeID,
			"agent_private_key": privateKey,
			"task_id":           "task-probe-ready",
		},
	}
	provider := NewOpenAITokenProvider(nil, nil, nil)
	upstream := &runtimeProbeFakeUpstream{}
	svc := &OpenAIGatewayService{
		cfg:                 &config.Config{},
		httpUpstream:        upstream,
		openAITokenProvider: provider,
	}

	before := provider.SnapshotRuntimeMetrics()
	result := svc.probeOpenAIAccountRuntimeModel(context.Background(), account, "gpt-5.5", time.Now())
	after := provider.SnapshotRuntimeMetrics()

	require.True(t, result.OK)
	require.Equal(t, MonitorStatusOperational, result.Status)
	require.Equal(t, before, after, "Agent Identity probe must not enter OAuth token provider")
	require.Len(t, upstream.requests, 1)
	require.True(t, strings.HasPrefix(upstream.requests[0].Header.Get("Authorization"), "AgentAssertion "))
	require.NotContains(t, upstream.requests[0].Header.Get("Authorization"), privateKey)
}

func TestProbeOpenAIAccountRuntimeModelReloadsAgentIdentityCandidateMetadata(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	metadata := &Account{
		ID:          6,
		Name:        "agent-runtime-probe-metadata",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"auth_mode":     OpenAIAuthModeAgentIdentity,
			"model_mapping": map[string]any{"gpt-5.5": "gpt-5.5"},
		},
	}
	full := *metadata
	full.Credentials = map[string]any{
		"auth_mode":         OpenAIAuthModeAgentIdentity,
		"agent_runtime_id":  key.runtimeID,
		"agent_private_key": privateKey,
		"task_id":           "task-probe-reloaded",
		"model_mapping":     map[string]any{"gpt-5.5": "gpt-5.5"},
	}
	repo := &runtimeProbeAccountReloadRepo{account: &full}
	upstream := &runtimeProbeFakeUpstream{}
	svc := &OpenAIGatewayService{
		accountRepo:  repo,
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}

	result := svc.probeOpenAIAccountRuntimeModel(context.Background(), metadata, "gpt-5.5", time.Now())

	require.True(t, result.OK)
	require.Equal(t, MonitorStatusOperational, result.Status)
	require.Equal(t, 1, repo.reads)
	require.Len(t, upstream.requests, 1)
	require.True(t, strings.HasPrefix(upstream.requests[0].Header.Get("Authorization"), "AgentAssertion "))
	require.Equal(t, "task-probe-reloaded", decodeAgentAssertionTask(t, upstream.requests[0].Header.Get("Authorization")))
}

func TestProbeOpenAIAccountRuntimeModelAgentIdentityRejectsInvalidPrivateKeySafely(t *testing.T) {
	const invalidPrivateKey = "not-a-private-key"
	account := &Account{
		ID:          5,
		Name:        "agent-runtime-probe-invalid-key",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"auth_mode":         OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":  "runtime-invalid-key",
			"agent_private_key": invalidPrivateKey,
			"task_id":           "task-invalid-key",
		},
	}
	upstream := &runtimeProbeFakeUpstream{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}

	result := svc.probeOpenAIAccountRuntimeModel(context.Background(), account, "gpt-5.5", time.Now())

	require.False(t, result.OK)
	require.Equal(t, MonitorStatusError, result.Status)
	require.Contains(t, result.Error, "private key is not valid base64")
	require.NotContains(t, result.Error, invalidPrivateKey)
	require.Empty(t, upstream.requests, "invalid credentials must fail before any upstream request")
}

func ptrAccount(account Account) *Account {
	return &account
}
