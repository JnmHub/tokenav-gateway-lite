package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"golang.org/x/net/http2"
)

type codexModelsHTTPUpstream struct {
	HTTPUpstream
	status int
	body   string
	header http.Header
	last   *http.Request
}

type codexModelsHTTPUpstreamFunc struct {
	HTTPUpstream
	do func(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error)
}

func (u *codexModelsHTTPUpstreamFunc) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return u.do(req, proxyURL, accountID, accountConcurrency)
}

type codexModelsBlockingBody struct {
	ctx         context.Context
	readStarted chan struct{}
	startedOnce *sync.Once
	release     <-chan struct{}
	body        *strings.Reader
}

func (b *codexModelsBlockingBody) Read(p []byte) (int, error) {
	b.startedOnce.Do(func() { close(b.readStarted) })
	select {
	case <-b.release:
		return b.body.Read(p)
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
}

func (b *codexModelsBlockingBody) Close() error { return nil }

func (u *codexModelsHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.last = req
	status := u.status
	if status == 0 {
		status = http.StatusOK
	}
	header := u.header
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(u.body)),
	}, nil
}

func codexModelsTestAccount(id int64, groupID int64, mapping map[string]any) Account {
	return Account{
		ID:          id,
		Name:        "codex-account",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 2,
		GroupIDs:    []int64{groupID},
		Credentials: map[string]any{
			"access_token":       "test-access-token",
			"chatgpt_account_id": "chatgpt-account",
			"model_mapping":      mapping,
		},
	}
}

func codexModelsAPIKeyTestAccount(id int64, groupID int64, baseURL string) Account {
	credentials := map[string]any{"api_key": "sk-upstream"}
	if baseURL != "" {
		credentials["base_url"] = baseURL
	}
	return Account{
		ID:          id,
		Name:        "codex-api-key-account",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 3,
		GroupIDs:    []int64{groupID},
		Credentials: credentials,
	}
}

func codexModelsAPIKeyTestService(account Account, upstream HTTPUpstream) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{account}}},
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled: false,
		}}},
		httpUpstream: upstream,
	}
}

func codexModelsTestGroup(groupID int64) *Group {
	return &Group{ID: groupID, Platform: PlatformOpenAI, AllowImageGeneration: true}
}

func TestCodexVersionConstantsConsistency(t *testing.T) {
	require.Equal(t, "0.146.0", codexCLIVersion)
	require.Equal(t, codexCLIVersion, codexCanonicalClientVersion())
	require.Contains(t, codexCLIUserAgent, openai.CodexDefaultOriginator+"/"+codexCLIVersion)
	require.Contains(t, DefaultOpenAICodexUserAgent, codexCLIVersion)
}

func TestBuildCodexManifestPolicyFixedGroupDoesNotIncludeOtherGroups(t *testing.T) {
	groupOne := int64(11)
	groupTwo := int64(22)
	svc := &OpenAIGatewayService{
		accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{
			codexModelsTestAccount(1, groupOne, map[string]any{"public-sol": "gpt-5.6-sol"}),
			codexModelsTestAccount(2, groupTwo, map[string]any{"private-terra": "gpt-5.6-terra"}),
		}}},
	}

	policy, err := svc.buildCodexManifestModelPolicy(context.Background(), &groupOne, &Group{
		ID:                   groupOne,
		Platform:             PlatformOpenAI,
		AllowImageGeneration: true,
	}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"public-sol"}, policy.aliasesByUpstream["gpt-5.6-sol"])
	require.NotContains(t, policy.aliasesByUpstream, "gpt-5.6-terra")
	require.False(t, policy.allowOriginalModels)
}

func TestFetchCodexModelsManifestFiltersAndAliasesFixedGroup(t *testing.T) {
	groupID := int64(11)
	account := codexModelsTestAccount(1, groupID, map[string]any{
		"public-sol":   "gpt-5.6-sol",
		"hidden-terra": "gpt-5.6-terra",
	})
	upstream := &codexModelsHTTPUpstream{
		body: `{"models":[` +
			`{"slug":"gpt-5.6-sol","display_name":"GPT-5.6 Sol"},` +
			`{"slug":"gpt-5.6-terra","display_name":"GPT-5.6 Terra"},` +
			`{"slug":"gpt-5.6-luna","display_name":"GPT-5.6 Luna"}` +
			`]}`,
		header: http.Header{"ETag": []string{`W/"upstream"`}},
	}
	svc := &OpenAIGatewayService{
		accountRepo:  schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{account}}},
		httpUpstream: upstream,
	}
	group := &Group{
		ID:                   groupID,
		Platform:             PlatformOpenAI,
		AllowImageGeneration: true,
		ModelsListConfig: GroupModelsListConfig{
			Enabled: true,
			Models:  []string{"public-sol"},
		},
	}

	manifest, err := svc.FetchCodexModelsManifest(
		context.Background(), &account, &groupID, group, []string{"public-*"}, "0.144.1", "",
	)
	require.NoError(t, err)
	require.False(t, manifest.NotModified)
	require.NotEmpty(t, manifest.ETag)
	require.Equal(t, CodexModelsManifestCacheStateUpstream, manifest.CacheState)
	require.NotEqual(t, `W/"upstream"`, manifest.ETag)
	require.Equal(t, 1, int(gjson.GetBytes(manifest.Body, "models.#").Int()))
	require.Equal(t, "public-sol", gjson.GetBytes(manifest.Body, "models.0.slug").String())
	require.Equal(t, "public-sol", gjson.GetBytes(manifest.Body, "models.0.display_name").String())
	require.Equal(t, "Bearer test-access-token", upstream.last.Header.Get("authorization"))
	require.Equal(t, "0.144.1", upstream.last.URL.Query().Get("client_version"))

	notModified, err := svc.FetchCodexModelsManifest(
		context.Background(), &account, &groupID, group, []string{"public-*"}, "0.144.1", manifest.ETag,
	)
	require.NoError(t, err)
	require.True(t, notModified.NotModified)
	require.Equal(t, manifest.ETag, notModified.ETag)
}

func TestFilterCodexModelsManifestHidesImageModelsWhenGroupDisallowsImages(t *testing.T) {
	policy := codexManifestModelPolicy{
		allowOriginalModels: true,
		aliasesByUpstream:   map[string][]string{},
		allowImages:         false,
		scoped:              true,
	}
	body, err := filterCodexModelsManifest([]byte(`{"models":[{"slug":"gpt-5.6-sol"},{"slug":"gpt-image-2"}]}`), policy)
	require.NoError(t, err)
	require.Equal(t, int64(1), gjson.GetBytes(body, "models.#").Int())
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(body, "models.0.slug").String())
}

func TestIsRetryableCodexModelsManifestTransportError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{name: "nil", err: nil},
		{name: "configuration", err: errors.New("invalid proxy URL")},
		{name: "canceled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, retryable: true},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, retryable: true},
		{name: "closed", err: net.ErrClosed, retryable: true},
		{name: "network operation", err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}, retryable: true},
		{name: "DNS", err: &net.DNSError{Err: "temporary failure", Name: "upstream.example"}, retryable: true},
		{name: "typed HTTP2 GOAWAY", err: http2.GoAwayError{ErrCode: http2.ErrCodeNo}, retryable: true},
		{name: "stdlib HTTP2 GOAWAY", err: errors.New("http2: server sent GOAWAY and closed the connection"), retryable: true},
		{name: "stdlib refused stream", err: errors.New("stream error: stream ID 3; REFUSED_STREAM"), retryable: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.retryable, isRetryableCodexModelsManifestTransportError(tt.err))
		})
	}
}

func TestFetchCodexModelsManifestAPIKeyCustomUpstreamFiltersAndCaches(t *testing.T) {
	groupID := int64(31)
	account := codexModelsAPIKeyTestAccount(2, groupID, "https://upstream.example/v1?tenant=tokenav")
	var calls atomic.Int32
	var gotRequest *http.Request
	upstream := &codexModelsHTTPUpstreamFunc{do: func(req *http.Request, _ string, accountID int64, concurrency int) (*http.Response, error) {
		calls.Add(1)
		gotRequest = req.Clone(req.Context())
		require.Equal(t, int64(2), accountID)
		require.Equal(t, 3, concurrency)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Etag": []string{`W/"upstream"`}},
			Body:       io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-5.6-sol","use_responses_lite":true},{"slug":"gpt-image-2"}]}`)),
		}, nil
	}}
	svc := codexModelsAPIKeyTestService(account, upstream)
	group := codexModelsTestGroup(groupID)
	group.ModelsListConfig = GroupModelsListConfig{Enabled: true, Models: []string{"gpt-5.6-sol"}}

	manifest, err := svc.FetchCodexModelsManifest(
		context.Background(), &account, &groupID, group, nil, "0.144.1", "",
	)
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, "https://upstream.example/v1/models?client_version=0.144.1&tenant=tokenav", gotRequest.URL.String())
	require.Equal(t, "Bearer sk-upstream", gotRequest.Header.Get("Authorization"))
	require.Equal(t, openai.CodexDefaultOriginator, gotRequest.Header.Get("Originator"))
	require.Empty(t, gotRequest.Header.Get("chatgpt-account-id"))
	require.NotEmpty(t, manifest.ETag)
	require.NotEqual(t, `W/"upstream"`, manifest.ETag)
	require.Equal(t, int64(1), gjson.GetBytes(manifest.Body, "models.#").Int())
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(manifest.Body, "models.0.slug").String())
	require.False(t, gjson.GetBytes(manifest.Body, "models.0.use_responses_lite").Bool())

	group.ModelsListConfig.Models = []string{"gpt-5.6-sol", "gpt-image-2"}
	changed, err := svc.FetchCodexModelsManifest(
		context.Background(), &account, &groupID, group, nil, "0.144.1", manifest.ETag,
	)
	require.NoError(t, err)
	require.False(t, changed.NotModified)
	require.Equal(t, CodexModelsManifestCacheStateFresh, changed.CacheState)
	require.NotEqual(t, manifest.ETag, changed.ETag)
	require.Equal(t, int64(2), gjson.GetBytes(changed.Body, "models.#").Int())

	notModified, err := svc.FetchCodexModelsManifest(
		context.Background(), &account, &groupID, group, nil, "0.144.1", changed.ETag,
	)
	require.NoError(t, err)
	require.True(t, notModified.NotModified)
	require.Equal(t, CodexModelsManifestCacheStateFresh, notModified.CacheState)
	require.Equal(t, changed.ETag, notModified.ETag)
	require.Equal(t, int32(1), calls.Load(), "fresh cache must avoid a second upstream request")
}

func TestAdjustAPIKeyCodexModelsManifest(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "GPT-5.6 variants disable Responses Lite and preserve unknown fields",
			body: `{"models":[{"slug":"gpt-5.6-sol","use_responses_lite":true,"unknown_model":{"enabled":true}},{"slug":"gpt-5.6-terra","use_responses_lite":true},{"slug":"gpt-5.6-luna","use_responses_lite":true}],"unknown_top":{"version":1}}`,
			want: `{"models":[{"slug":"gpt-5.6-sol","unknown_model":{"enabled":true},"use_responses_lite":false},{"slug":"gpt-5.6-terra","use_responses_lite":false},{"slug":"gpt-5.6-luna","use_responses_lite":false}],"unknown_top":{"version":1}}`,
		},
		{
			name: "unaffected manifest stays byte-identical",
			body: ` {"models":[{"slug":"gpt-5.6-codex","use_responses_lite":true}]} `,
			want: ` {"models":[{"slug":"gpt-5.6-codex","use_responses_lite":true}]} `,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := adjustAPIKeyCodexModelsManifest([]byte(tt.body))
			require.NoError(t, err)
			require.Equal(t, tt.want, string(got))
		})
	}
}

func TestFetchCodexModelsManifestAPIKeyRejectsUnsupportedOrInvalidBaseURL(t *testing.T) {
	groupID := int64(32)
	tests := []struct {
		name       string
		baseURL    string
		wantReason string
	}{
		{name: "missing", wantReason: "OPENAI_CODEX_MODELS_API_KEY_UPSTREAM_UNSUPPORTED"},
		{name: "official", baseURL: "https://API.OPENAI.COM:443/v1/", wantReason: "OPENAI_CODEX_MODELS_API_KEY_UPSTREAM_UNSUPPORTED"},
		{name: "fragment", baseURL: "https://upstream.example/v1#models", wantReason: "OPENAI_CODEX_MODELS_API_KEY_UPSTREAM_INVALID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := codexModelsAPIKeyTestAccount(2, groupID, tt.baseURL)
			called := false
			svc := codexModelsAPIKeyTestService(account, &codexModelsHTTPUpstreamFunc{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				called = true
				return nil, nil
			}})
			_, err := svc.FetchCodexModelsManifest(
				context.Background(), &account, &groupID, codexModelsTestGroup(groupID), nil, "0.144.1", "",
			)
			require.Error(t, err)
			require.Equal(t, tt.wantReason, infraerrors.Reason(err))
			require.False(t, called)
		})
	}
}

func TestFetchCodexModelsManifestAPIKeyConcurrentRequestsShareRefresh(t *testing.T) {
	const callers = 8
	groupID := int64(33)
	account := codexModelsAPIKeyTestAccount(2, groupID, "https://upstream.example/v1")
	var calls atomic.Int32
	started := make(chan struct{})
	var startedOnce sync.Once
	release := make(chan struct{})
	upstream := &codexModelsHTTPUpstreamFunc{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-5.6-sol"}]}`)),
		}, nil
	}}
	svc := codexModelsAPIKeyTestService(account, upstream)
	group := codexModelsTestGroup(groupID)
	begin := make(chan struct{})
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			<-begin
			_, err := svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, group, nil, "0.144.1", "")
			errs <- err
		}()
	}
	close(begin)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(1), calls.Load())
	close(release)
	for i := 0; i < callers; i++ {
		require.NoError(t, <-errs)
	}
	require.Equal(t, int32(1), calls.Load())
}

func TestFetchCodexModelsManifestAPIKeyRefreshSurvivesCallerCancellation(t *testing.T) {
	groupID := int64(34)
	account := codexModelsAPIKeyTestAccount(2, groupID, "https://upstream.example/v1")
	var calls atomic.Int32
	readStarted := make(chan struct{})
	var readStartedOnce sync.Once
	release := make(chan struct{})
	upstream := &codexModelsHTTPUpstreamFunc{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: &codexModelsBlockingBody{
				ctx: req.Context(), readStarted: readStarted, startedOnce: &readStartedOnce,
				release: release, body: strings.NewReader(`{"models":[{"slug":"gpt-5.6-sol"}]}`),
			},
		}, nil
	}}
	svc := codexModelsAPIKeyTestService(account, upstream)
	group := codexModelsTestGroup(groupID)
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstErr := make(chan error, 1)
	go func() {
		_, err := svc.FetchCodexModelsManifest(firstCtx, &account, &groupID, group, nil, "0.144.1", "")
		firstErr <- err
	}()
	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("upstream body read did not start")
	}
	cancelFirst()
	select {
	case err := <-firstErr:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled caller did not return")
	}

	secondResult := make(chan error, 1)
	go func() {
		_, err := svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, group, nil, "0.144.1", "")
		secondResult <- err
	}()
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(1), calls.Load())
	close(release)
	select {
	case err := <-secondResult:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("second caller did not receive shared refresh")
	}
	require.Equal(t, int32(1), calls.Load())
}

func TestFetchCodexModelsManifestAPIKeyServesStaleWhileRefreshing(t *testing.T) {
	groupID := int64(35)
	account := codexModelsAPIKeyTestAccount(2, groupID, "https://upstream.example/v1")
	var calls atomic.Int32
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	upstream := &codexModelsHTTPUpstreamFunc{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		call := calls.Add(1)
		body := `{"models":[{"slug":"old"}]}`
		if call > 1 {
			if call == 2 {
				close(refreshStarted)
			}
			<-releaseRefresh
			body = `{"models":[{"slug":"new"}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	svc := codexModelsAPIKeyTestService(account, upstream)
	group := codexModelsTestGroup(groupID)
	first, err := svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, group, nil, "0.144.1", "")
	require.NoError(t, err)
	require.Equal(t, CodexModelsManifestCacheStateUpstream, first.CacheState)
	require.Equal(t, "old", gjson.GetBytes(first.Body, "models.0.slug").String())

	svc.codexModelsManifestCache.mu.Lock()
	for key, entry := range svc.codexModelsManifestCache.entries {
		entry.expiresAt = time.Now().Add(-time.Second)
		svc.codexModelsManifestCache.entries[key] = entry
	}
	svc.codexModelsManifestCache.mu.Unlock()

	stale, err := svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, group, nil, "0.144.1", "")
	require.NoError(t, err)
	require.Equal(t, CodexModelsManifestCacheStateStale, stale.CacheState)
	require.Equal(t, "old", gjson.GetBytes(stale.Body, "models.0.slug").String())
	select {
	case <-refreshStarted:
	case <-time.After(time.Second):
		t.Fatal("background refresh did not start")
	}
	close(releaseRefresh)

	require.Eventually(t, func() bool {
		request, buildErr := svc.buildCodexModelsManifestRequest(context.Background(), &account, "0.144.1")
		if buildErr != nil {
			return false
		}
		cached, state := svc.codexModelsManifestCache.get(buildCodexModelsManifestCacheKey(request), time.Now())
		return state == codexModelsManifestCacheFresh && gjson.GetBytes(cached.Body, "models.0.slug").String() == "new"
	}, time.Second, 10*time.Millisecond)
	refreshed, err := svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, group, nil, "0.144.1", "")
	require.NoError(t, err)
	require.Equal(t, CodexModelsManifestCacheStateFresh, refreshed.CacheState)
	require.Equal(t, "new", gjson.GetBytes(refreshed.Body, "models.0.slug").String())
	require.Equal(t, int32(2), calls.Load())
}

func TestFetchCodexModelsManifestAPIKeyCacheKeyIsolatesIdentity(t *testing.T) {
	groupID := int64(37)
	var calls atomic.Int32
	upstream := &codexModelsHTTPUpstreamFunc{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-5.6-sol"}]}`)),
		}, nil
	}}
	base := codexModelsAPIKeyTestAccount(2, groupID, "https://upstream.example/v1")
	svc := codexModelsAPIKeyTestService(base, upstream)
	group := codexModelsTestGroup(groupID)
	fetch := func(account Account, version string) {
		t.Helper()
		// Keep the policy repository aligned with the selected account while
		// retaining one shared cache instance for identity-isolation checks.
		svc.accountRepo = schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{account}}}
		_, err := svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, group, nil, version, "")
		require.NoError(t, err)
	}

	fetch(base, "0.144.1")
	fetch(base, "0.144.1")
	differentAccount := base
	differentAccount.ID = 3
	fetch(differentAccount, "0.144.1")
	differentToken := base
	differentToken.Credentials = map[string]any{"api_key": "sk-other", "base_url": "https://upstream.example/v1"}
	fetch(differentToken, "0.144.1")
	differentUpstream := base
	differentUpstream.Credentials = map[string]any{"api_key": "sk-upstream", "base_url": "https://other.example/v1"}
	fetch(differentUpstream, "0.144.1")
	fetch(base, "0.145.0")

	require.Equal(t, int32(5), calls.Load())
}

func TestValidateCodexModelsManifestEnvelope(t *testing.T) {
	for _, tt := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "empty models", body: `{"models":[]}`},
		{name: "slug and id", body: `{"models":[{"slug":"gpt-5.6-sol"},{"id":"gpt-5.4"}]}`},
		{name: "OpenAI list", body: `{"object":"list","data":[]}`, wantErr: true},
		{name: "invalid JSON", body: `{"models":`, wantErr: true},
		{name: "non object", body: `[]`, wantErr: true},
		{name: "null object", body: `null`, wantErr: true},
		{name: "models object", body: `{"models":{}}`, wantErr: true},
		{name: "models null", body: `{"models":null}`, wantErr: true},
		{name: "entry without id", body: `{"models":[{"name":"GPT"}]}`, wantErr: true},
		{name: "non object entry", body: `{"models":["gpt-5.6-sol"]}`, wantErr: true},
		{name: "duplicate id", body: `{"models":[{"slug":"gpt-5.6-sol"},{"id":"GPT-5.6-SOL"}]}`, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCodexModelsManifestEnvelope([]byte(tt.body))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestFetchCodexModelsManifestAPIKeyDoesNotCacheInvalidEnvelope(t *testing.T) {
	groupID := int64(41)
	account := codexModelsAPIKeyTestAccount(2, groupID, "https://upstream.example/v1")
	var calls atomic.Int32
	upstream := &codexModelsHTTPUpstreamFunc{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		body := `{"object":"list","data":[]}`
		if calls.Add(1) > 1 {
			body = `{"models":[{"slug":"gpt-5.6-sol"}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	svc := codexModelsAPIKeyTestService(account, upstream)
	group := codexModelsTestGroup(groupID)

	_, err := svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, group, nil, "0.144.1", "")
	require.Error(t, err)
	require.Equal(t, "OPENAI_CODEX_MODELS_UPSTREAM_INVALID_MANIFEST", infraerrors.Reason(err))
	require.True(t, IsRetryableCodexModelsManifestError(err))

	manifest, err := svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, group, nil, "0.144.1", "")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(manifest.Body, "models.0.slug").String())
	require.Equal(t, int32(2), calls.Load(), "invalid manifest must not enter the short cache")
}

func TestFetchCodexModelsManifestAPIKeyInvalidRefreshKeepsStaleManifest(t *testing.T) {
	groupID := int64(43)
	account := codexModelsAPIKeyTestAccount(2, groupID, "https://upstream.example/v1")
	var calls atomic.Int32
	upstream := &codexModelsHTTPUpstreamFunc{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"old"}]}`))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"object":"list","data":[]}`))}, nil
	}}
	svc := codexModelsAPIKeyTestService(account, upstream)
	group := codexModelsTestGroup(groupID)

	first, err := svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, group, nil, "0.144.1", "")
	require.NoError(t, err)
	require.Equal(t, "old", gjson.GetBytes(first.Body, "models.0.slug").String())

	svc.codexModelsManifestCache.mu.Lock()
	for key, entry := range svc.codexModelsManifestCache.entries {
		entry.expiresAt = time.Now().Add(-time.Second)
		svc.codexModelsManifestCache.entries[key] = entry
	}
	svc.codexModelsManifestCache.mu.Unlock()

	request, err := svc.buildCodexModelsManifestRequest(context.Background(), &account, "0.144.1")
	require.NoError(t, err)
	cacheKey := buildCodexModelsManifestCacheKey(request)
	cached, state := svc.codexModelsManifestCache.get(cacheKey, time.Now())
	require.Equal(t, codexModelsManifestCacheStale, state)
	require.Equal(t, "old", gjson.GetBytes(cached.Body, "models.0.slug").String())

	refreshResult := <-svc.refreshCachedAPIKeyCodexModelsManifest(cacheKey, request)
	require.Error(t, refreshResult.Err)
	require.Equal(t, "OPENAI_CODEX_MODELS_UPSTREAM_INVALID_MANIFEST", infraerrors.Reason(refreshResult.Err))

	cached, state = svc.codexModelsManifestCache.get(cacheKey, time.Now())
	require.Equal(t, codexModelsManifestCacheStale, state)
	require.Equal(t, "old", gjson.GetBytes(cached.Body, "models.0.slug").String(), "invalid refresh must not replace the last-known-good manifest")
	require.Equal(t, int32(2), calls.Load())
}

func TestFetchCodexModelsManifestAPIKeyRejectsOversizedBodyAsRetryable(t *testing.T) {
	groupID := int64(42)
	account := codexModelsAPIKeyTestAccount(2, groupID, "https://upstream.example/v1")
	body := `{"models":[],"padding":"` + strings.Repeat("x", int(config.DefaultModelsListReadMaxBytes)) + `"}`
	upstream := &codexModelsHTTPUpstreamFunc{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	svc := codexModelsAPIKeyTestService(account, upstream)

	_, err := svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, codexModelsTestGroup(groupID), nil, "0.144.1", "")
	require.Error(t, err)
	require.Equal(t, "OPENAI_CODEX_MODELS_BODY_TOO_LARGE", infraerrors.Reason(err))
	require.True(t, IsRetryableCodexModelsManifestError(err))
}

func TestFetchCodexModelsManifestAPIKeyCacheBoundsBodyAndEntries(t *testing.T) {
	groupID := int64(38)
	var calls atomic.Int32
	upstream := &codexModelsHTTPUpstreamFunc{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		calls.Add(1)
		body := `{"models":[]}`
		if strings.Contains(req.URL.Host, "large") {
			body = `{"models":[],"padding":"` + strings.Repeat("x", codexModelsManifestCacheBodyLimit+1) + `"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	base := codexModelsAPIKeyTestAccount(2, groupID, "https://small.example/v1")
	svc := codexModelsAPIKeyTestService(base, upstream)
	fetchRaw := func(account Account) {
		t.Helper()
		request, err := svc.buildCodexModelsManifestRequest(context.Background(), &account, "0.144.1")
		require.NoError(t, err)
		_, err = svc.fetchCachedAPIKeyCodexModelsManifest(context.Background(), request, "")
		require.NoError(t, err)
	}

	fetchRaw(base)
	fetchRaw(base)
	large := codexModelsAPIKeyTestAccount(3, groupID, "https://large.example/v1")
	fetchRaw(large)
	fetchRaw(large)
	require.Equal(t, int32(3), calls.Load(), "responses over 1 MiB must not enter the short cache")

	for id := int64(10); id < 75; id++ {
		account := codexModelsAPIKeyTestAccount(id, groupID, "https://bounded.example/v1")
		fetchRaw(account)
	}
	last := codexModelsAPIKeyTestAccount(74, groupID, "https://bounded.example/v1")
	fetchRaw(last)
	require.Equal(t, int32(68), calls.Load(), "most recent cache entry must remain available")
	first := codexModelsAPIKeyTestAccount(10, groupID, "https://bounded.example/v1")
	fetchRaw(first)
	require.Equal(t, int32(69), calls.Load(), "oldest entry must be evicted at the 64-entry bound")
}

func TestFetchCodexModelsManifestAPIKeyRevalidatesStaleETag(t *testing.T) {
	groupID := int64(39)
	account := codexModelsAPIKeyTestAccount(2, groupID, "https://upstream.example/v1")
	var calls atomic.Int32
	refreshDone := make(chan struct{})
	upstream := &codexModelsHTTPUpstreamFunc{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		call := calls.Add(1)
		if call == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Etag": []string{`"upstream-cached"`}},
				Body:       io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-5.6-sol","use_responses_lite":true}]}`)),
			}, nil
		}
		require.Equal(t, `"upstream-cached"`, req.Header.Get("If-None-Match"), "后台刷新必须发送上游 ETag，而不是改写正文的客户端 ETag")
		close(refreshDone)
		return &http.Response{
			StatusCode: http.StatusNotModified,
			Header:     http.Header{"Etag": []string{`"upstream-cached"`}},
			Body:       http.NoBody,
		}, nil
	}}
	svc := codexModelsAPIKeyTestService(account, upstream)
	group := codexModelsTestGroup(groupID)
	_, err := svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, group, nil, "0.144.1", "")
	require.NoError(t, err)

	svc.codexModelsManifestCache.mu.Lock()
	for key, entry := range svc.codexModelsManifestCache.entries {
		entry.expiresAt = time.Now().Add(-time.Second)
		svc.codexModelsManifestCache.entries[key] = entry
	}
	svc.codexModelsManifestCache.mu.Unlock()
	_, err = svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, group, nil, "0.144.1", "")
	require.NoError(t, err)
	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("stale cache was not revalidated with upstream ETag")
	}

	require.Eventually(t, func() bool {
		svc.codexModelsManifestCache.mu.Lock()
		defer svc.codexModelsManifestCache.mu.Unlock()
		for _, entry := range svc.codexModelsManifestCache.entries {
			return time.Now().Before(entry.expiresAt)
		}
		return false
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, int32(2), calls.Load())
}

func TestFetchCodexModelsManifestRetryClassificationByStatus(t *testing.T) {
	groupID := int64(36)
	for _, tt := range []struct {
		status    int
		retryable bool
	}{
		{status: http.StatusBadRequest, retryable: false},
		{status: http.StatusUnauthorized, retryable: false},
		{status: http.StatusTooManyRequests, retryable: true},
		{status: http.StatusBadGateway, retryable: true},
		{status: http.StatusServiceUnavailable, retryable: true},
	} {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			account := codexModelsAPIKeyTestAccount(int64(tt.status), groupID, "https://upstream.example/v1")
			svc := codexModelsAPIKeyTestService(account, &codexModelsHTTPUpstreamFunc{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				return &http.Response{
					StatusCode: tt.status,
					Status:     http.StatusText(tt.status),
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"error":"failed"}`)),
				}, nil
			}})
			_, err := svc.FetchCodexModelsManifest(context.Background(), &account, &groupID, codexModelsTestGroup(groupID), nil, "0.144.1", "")
			require.Error(t, err)
			require.Equal(t, tt.retryable, IsRetryableCodexModelsManifestError(err))
		})
	}
}
