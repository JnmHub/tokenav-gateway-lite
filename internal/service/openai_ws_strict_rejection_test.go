package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIWSStrictRejectionIdentityGate(t *testing.T) {
	valid := `{"type":"response.create","input":"safe","previous_response_id":"resp_old"}`
	deepInput := strings.Repeat("[", 66) + `"safe"` + strings.Repeat("]", 66)
	deepCreate := `{"type":"response.create","input":` + deepInput + `}`
	deepHistory := `{"type":"response.create","input":` + deepInput + `,"previous_response_id":"resp_old"}`
	// 深度不再是审查身份门槛；实际传输仍因远程历史拒绝，合法嵌套不能令拒绝通知丢失。
	require.NoError(t, validateOpenAIWSStrictClientFrame(coderws.MessageText, []byte(deepCreate), true))
	require.Error(t, validateOpenAIWSStrictClientFrame(coderws.MessageText, []byte(deepHistory), true))
	for _, tc := range []struct {
		name, body   string
		binary, want bool
	}{
		{"exact", valid, false, true},
		{"invalid event metadata is still create", `{"type":"response.create","input":"safe","event_id":{}}`, false, true},
		{"binary JSON", valid, true, false},
		{"broken", `{"type":"response.create",`, false, false},
		{"trailing", valid + ` {}`, false, false},
		{"duplicate", `{"type":"response.create","input":"a","input":"b"}`, false, false},
		{"nested case collision", `{"type":"response.create","input":{"text":"a","TEXT":"b"}}`, false, false},
		{"alias", `{"Type":"response.create"}`, false, false},
		{"type collision", `{"type":"response.create","TYPE":"response.create"}`, false, false},
		{"wrong type", `{"type":1}`, false, false},
		{"missing type", `{"input":"safe"}`, false, false},
		{"whitespace type", `{"type":" response.create "}`, false, false},
		{"array", `[{"type":"response.create"}]`, false, false},
		{"cancel", `{"type":"response.cancel"}`, false, false},
		{"unknown", `{"type":"response.future"}`, false, false},
		{"invalid utf8", `{"type":"response.create","input":"` + string([]byte{0xff}) + `"}`, false, false},
		{"nested beyond former limit", deepCreate, false, true},
		{"nested history rejection retains identity", deepHistory, false, true},
		{"nested malformed", `{"type":"response.create","input":` + strings.TrimSuffix(deepInput, "]") + `}`, false, false},
		{"nested case collision beyond former limit", `{"type":"response.create","input":` + strings.Repeat("[", 66) + `{"text":"a","TEXT":"b"}` + strings.Repeat("]", 66) + `}`, false, false},
		{"nested binary JSON", deepHistory, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind := coderws.MessageText
			if tc.binary {
				kind = coderws.MessageBinary
			}
			require.Equal(t, tc.want, isOpenAIWSRejectedResponseCreate(kind, []byte(tc.body)))
			calls := 0
			hooks := &OpenAIWSIngressHooks{OnStrictResponseCreateRejected: func(payload []byte, _ string) { calls++; payload[0] = '!' }}
			body := []byte(tc.body)
			notifyOpenAIWSStrictRejection(hooks, kind, body, false, "session-model")
			require.Zero(t, calls, "non-strict mode must not notify")
			notifyOpenAIWSStrictRejection(hooks, kind, body, true, "session-model")
			want := 0
			if tc.want {
				want = 1
			}
			require.Equal(t, want, calls)
			require.Equal(t, tc.body, string(body), "observer cannot rewrite the rejected bytes")
		})
	}
	require.NotPanics(t, func() {
		notifyOpenAIWSStrictRejection(&OpenAIWSIngressHooks{OnStrictResponseCreateRejected: func([]byte, string) { panic("synthetic observer failure") }}, coderws.MessageText, []byte(valid), true, "session-model")
	})
}

// Wrap only transport observation; frame validation and dispatch remain in the
// real adapter. A channel proves accepted create/cancel frames reached upstream.
type strictRejectionCapture struct {
	*openAIWSCaptureConn
	written chan []byte
	creates chan struct{}
}

func (c *strictRejectionCapture) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	select {
	case <-c.creates:
		return c.openAIWSCaptureConn.ReadFrame(ctx)
	case <-ctx.Done():
		return coderws.MessageText, nil, ctx.Err()
	}
}

func (c *strictRejectionCapture) WriteFrame(ctx context.Context, kind coderws.MessageType, payload []byte) error {
	if err := c.openAIWSCaptureConn.WriteFrame(ctx, kind, payload); err != nil {
		return err
	}
	if gjson.GetBytes(payload, "type").String() == "response.create" {
		select {
		case c.creates <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case c.written <- append([]byte(nil), payload...):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type strictRejectionDialer struct {
	*openAIWSCaptureDialer
	capture *strictRejectionCapture
}

func (d *strictRejectionDialer) Dial(ctx context.Context, url string, headers http.Header, proxy string) (openAIWSClientConn, int, http.Header, error) {
	_, status, responseHeaders, err := d.openAIWSCaptureDialer.Dial(ctx, url, headers, proxy)
	return d.capture, status, responseHeaders, err
}

func TestOpenAIWSStrictRejectionAdapter(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	valid := `{"type":"response.create","model":"gpt-5.1","input":"safe","max_output_tokens":32}`
	hidden := `{"type":"response.create","model":"gpt-5.1","input":"safe","max_output_tokens":32,"previous_response_id":"resp_old"}`
	for _, tc := range []struct {
		name, body                                                          string
		first, binary, panicHook, failedWrite, nonStrict, allowed, switched bool
		wantModel                                                           string
		notifications                                                       int64
	}{
		{name: "hidden", body: hidden, notifications: 1},
		{name: "current model", body: strings.Replace(hidden, "gpt-5.1", "gpt-5.2", 1), wantModel: "gpt-5.2", notifications: 1},
		{name: "omitted model after explicit switch", body: strings.Replace(hidden, `"model":"gpt-5.1",`, "", 1), switched: true, notifications: 1},
		{name: "observer panic", body: hidden, panicHook: true, notifications: 1},
		{name: "observer write failure", body: hidden, failedWrite: true, notifications: 1},
		{name: "non-strict hidden followup", body: hidden, nonStrict: true, allowed: true},
		{name: "first frame", body: hidden, first: true},
		{name: "unknown frame", body: `{"type":"response.future"}`},
		{name: "malformed", body: `{"type":"response.create",`},
		{name: "duplicate history", body: `{"type":"response.create","input":"safe","previous_response_id":"a","previous_response_id":"b"}`},
		{name: "binary create", body: hidden, binary: true},
		{name: "normal followup", body: valid, allowed: true},
		{name: "cancel", body: `{"type":"response.cancel"}`, allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			cfg.Gateway.OpenAIWS.Enabled, cfg.Gateway.OpenAIWS.APIKeyEnabled = true, true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2, cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true, true
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds, cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3, 2
			upstream := &openAIWSCaptureConn{events: [][]byte{
				[]byte(`{"type":"response.completed","response":{"id":"resp_first","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
				[]byte(`{"type":"response.completed","response":{"id":"resp_second","model":"gpt-5.2","usage":{"input_tokens":1,"output_tokens":1}}}`),
				[]byte(`{"type":"response.completed","response":{"id":"must_not_arrive"}}`),
			}, readDelays: []time.Duration{0, 0, 10 * time.Second}}
			capture := &strictRejectionCapture{openAIWSCaptureConn: upstream, written: make(chan []byte, 4), creates: make(chan struct{}, 4)}
			dialer := &strictRejectionDialer{openAIWSCaptureDialer: &openAIWSCaptureDialer{conn: upstream}, capture: capture}
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{},
				openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPassthroughDialer: dialer}
			account := &Account{ID: 454, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"api_key": "synthetic"}, Extra: map[string]any{"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough}}
			var notified, before, mapped, slots, dispatch, writesFailed atomic.Int64
			firstDone, returned := make(chan struct{}, 1), make(chan error, 1)
			observedModels := make(chan string, 1)
			var observerConn *coderws.Conn
			hooks := &OpenAIWSIngressHooks{
				StrictClientFrames: func() (bool, error) { return !tc.nonStrict, nil },
				OnStrictResponseCreateRejected: func(_ []byte, model string) {
					select {
					case observedModels <- model:
					default:
					}
					notified.Add(1)
					if tc.panicHook {
						panic("synthetic observer failure")
					}
					if tc.failedWrite {
						// A rejected notification write may close the socket; the adapter
						// must still return the original strict error, not the write error.
						writeCtx, cancelWrite := context.WithTimeout(context.Background(), 2*time.Second)
						defer cancelWrite()
						_ = observerConn.CloseNow()
						if err := observerConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"error"}`)); err != nil {
							writesFailed.Add(1)
						}
					}
				},
				BeforeRequest: func(turn int, body []byte, _ string) ([]byte, error) {
					if turn > 1 || tc.first {
						before.Add(1)
					}
					return body, nil
				},
				MapRequestModel: func(turn int, model string) (string, error) {
					if turn > 1 || tc.first {
						mapped.Add(1)
					}
					return model, nil
				},
				BeforeTurn: func(turn int) error {
					if turn > 1 || tc.first {
						slots.Add(1)
					}
					return nil
				},
				BeforeDispatch: func(turn int, _ []byte, _ string) error {
					if turn > 1 || tc.first {
						dispatch.Add(1)
					}
					return nil
				},
				AfterTurn: func(turn int, _ *OpenAIForwardResult, _ error) {
					if turn == 1 {
						select {
						case firstDone <- struct{}{}:
						default:
						}
					}
				},
			}
			handlerDone := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					returned <- err
					return
				}
				observerConn = conn
				defer conn.CloseNow()
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				kind, body, err := conn.Read(ctx)
				if err != nil {
					returned <- err
					return
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r
				err = svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "synthetic", body, hooks, kind)
				returned <- err
				var closeErr *OpenAIWSClientCloseError
				if errors.As(err, &closeErr) {
					_ = conn.Close(closeErr.StatusCode(), closeErr.Reason())
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer func() {
				_ = client.CloseNow()
				cancel()
				select {
				case <-handlerDone:
				case <-time.After(5 * time.Second):
					t.Error("owned websocket handler did not stop")
				}
			}()
			first := valid
			if tc.first {
				first = tc.body
			}
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(first)))
			if !tc.first {
				_, frame, err := client.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, "resp_first", gjson.GetBytes(frame, "response.id").String())
				select {
				case <-firstDone:
				case <-ctx.Done():
					t.Fatal("first turn callback did not complete")
				}
				select {
				case firstSent := <-capture.written:
					require.Equal(t, "response.create", gjson.GetBytes(firstSent, "type").String())
				case <-ctx.Done():
					t.Fatal("first create did not reach upstream")
				}
				if tc.switched {
					switchBody := strings.Replace(valid, "gpt-5.1", "gpt-5.2", 1)
					require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(switchBody)))
					_, second, readErr := client.Read(ctx)
					require.NoError(t, readErr)
					require.Equal(t, "resp_second", gjson.GetBytes(second, "response.id").String())
					select {
					case sent := <-capture.written:
						require.Equal(t, "gpt-5.2", gjson.GetBytes(sent, "model").String())
					case <-ctx.Done():
						t.Fatal("explicit model switch did not reach upstream")
					}
				}
				kind := coderws.MessageText
				if tc.binary {
					kind = coderws.MessageBinary
				}
				require.NoError(t, client.Write(ctx, kind, []byte(tc.body)))
			}
			if tc.allowed {
				select {
				case sent := <-capture.written:
					require.Equal(t, gjson.Get(tc.body, "type").String(), gjson.GetBytes(sent, "type").String())
				case <-ctx.Done():
					t.Fatal("accepted followup did not reach upstream")
				}
				if tc.name != "cancel" {
					_, frame, readErr := client.Read(ctx)
					require.NoError(t, readErr)
					require.Equal(t, "resp_second", gjson.GetBytes(frame, "response.id").String())
				}
				_ = client.CloseNow()
			} else {
				_, _, readErr := client.Read(ctx)
				if tc.failedWrite {
					require.Error(t, readErr, "closed socket cannot complete the close handshake")
				} else {
					require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(readErr))
				}
			}
			var proxyErr error
			select {
			case proxyErr = <-returned:
			case <-ctx.Done():
				t.Fatal("adapter did not terminate")
			}
			require.Equal(t, tc.notifications, notified.Load())
			require.Len(t, observedModels, int(tc.notifications))
			if tc.notifications == 1 {
				wantModel := tc.wantModel
				if wantModel == "" {
					wantModel = "gpt-5.1"
				}
				// An explicit B turn does not change the existing session-A resolver.
				// Rejection observation must preserve that normal-path behavior.
				require.Equal(t, wantModel, <-observedModels)
			}
			if tc.failedWrite {
				require.EqualValues(t, 1, writesFailed.Load())
			}
			if tc.allowed {
				wantHooks := int64(1)
				if tc.name == "cancel" {
					wantHooks = 0
				}
				require.Equal(t, wantHooks, before.Load())
				require.Equal(t, wantHooks, mapped.Load())
				require.Equal(t, wantHooks, slots.Load())
				require.Equal(t, wantHooks, dispatch.Load())
				upstream.mu.Lock()
				writes := len(upstream.writes)
				upstream.mu.Unlock()
				require.Equal(t, 2, writes)
			}
			if !tc.allowed {
				var closeErr *OpenAIWSClientCloseError
				require.ErrorAs(t, proxyErr, &closeErr)
				require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
				kind := coderws.MessageText
				if tc.binary {
					kind = coderws.MessageBinary
				}
				original := validateOpenAIWSStrictClientFrame(kind, []byte(tc.body), true)
				require.Equal(t, original.Error(), proxyErr.Error(), "notification cannot replace original strict rejection")
				wantPreviousHooks := int64(0)
				if tc.switched {
					wantPreviousHooks = 1
				}
				require.Equal(t, wantPreviousHooks, before.Load())
				require.Equal(t, wantPreviousHooks, mapped.Load())
				require.Equal(t, wantPreviousHooks, slots.Load())
				require.Equal(t, wantPreviousHooks, dispatch.Load())
				wantDials, wantWrites := 1, 1
				if tc.first {
					wantDials, wantWrites = 0, 0
				}
				if tc.switched {
					wantWrites = 2
				}
				require.Equal(t, wantDials, dialer.DialCount())
				upstream.mu.Lock()
				writes := len(upstream.writes)
				upstream.mu.Unlock()
				require.Equal(t, wantWrites, writes, "rejected frame never reaches upstream")
			}
		})
	}
}
