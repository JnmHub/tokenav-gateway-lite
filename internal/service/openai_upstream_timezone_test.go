package service

import (
	"strings"
	"testing"
)

func TestRewriteUpstreamTimezone_Basic(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"<environment_context><cwd>/w</cwd><timezone>\n  Asia/Shanghai\n</timezone><current_date>2026-09-14</current_date></environment_context>"}]}]}`)
	got := rewriteUpstreamTimezone(body, "America/New_York")
	want := "<timezone>America/New_York</timezone>"
	if !strings.Contains(string(got), want) {
		t.Fatalf("应改写时区标签, got: %s", got)
	}
	if strings.Contains(string(got), "Asia/Shanghai") {
		t.Fatalf("旧时区应被替换, got: %s", got)
	}
	if !strings.Contains(string(got), "<current_date>2026-09-14</current_date>") {
		t.Fatalf("其他环境字段不得改动, got: %s", got)
	}
}

func TestRewriteUpstreamTimezone_MultipleOccurrences(t *testing.T) {
	body := []byte(`<timezone>Asia/Shanghai</timezone> middle <timezone> Asia/Taipei </timezone>`)
	got := string(rewriteUpstreamTimezone(body, "Europe/London"))
	if strings.Count(got, "<timezone>Europe/London</timezone>") != 2 {
		t.Fatalf("所有时区标签都应统一改写, got: %s", got)
	}
}

func TestRewriteUpstreamTimezone_Noop(t *testing.T) {
	body := []byte(`{"x":1,"text":"<timezone>Asia/Shanghai</timezone>"}`)
	if got := rewriteUpstreamTimezone(body, ""); string(got) != string(body) {
		t.Fatalf("未配置时区必须原样透传")
	}
	if got := rewriteUpstreamTimezone(body, "Not/AZone"); string(got) != string(body) {
		t.Fatalf("非法时区必须原样透传")
	}
	if got := rewriteUpstreamTimezone(nil, "UTC"); got != nil {
		t.Fatalf("空 body 应原样返回")
	}
	// 正文里未闭合/伪装的标签不受影响
	tricky := []byte(`text with <timezone> not a tag and code sample`)
	if got := rewriteUpstreamTimezone(tricky, "UTC"); string(got) != string(tricky) {
		t.Fatalf("无合法标签时不得改动")
	}
}

func TestAccountGetUpstreamTimezone(t *testing.T) {
	acc := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"upstream_timezone": " America/New_York "}}
	if got := acc.GetUpstreamTimezone(); got != "America/New_York" {
		t.Fatalf("getter 应去除空白, got %q", got)
	}
	empty := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	if got := empty.GetUpstreamTimezone(); got != "" {
		t.Fatalf("未配置应为空, got %q", got)
	}
	nonOpenAI := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"upstream_timezone": "UTC"}}
	if got := nonOpenAI.GetUpstreamTimezone(); got != "" {
		t.Fatalf("非 OpenAI 账号不适用, got %q", got)
	}
}
