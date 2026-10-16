package handler

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// [官方 v0.2.13 移植] OAuth 登录起点的行为验证码闸门。
//
// 为什么需要它：验证码（Turnstile / 腾讯 / 阿里云）开启后，官方要求「登录起点」
// 也必须带 proof，否则未授权方可以无成本地反复触发 OAuth 登录跳转（刷注册、
// 消耗上游 OAuth 配额）。我方此前只有 passkey 一个调用点，OAuth start 是绕过口。
type oauthStartCaptchaRequest struct {
	// TurnstileToken 承载 Cloudflare Turnstile token；阿里云验证码复用同一字段
	// 承载 captchaVerifyParam（与 passkey 请求体保持同名字段，前端可复用同一组件）。
	TurnstileToken        string `json:"turnstile_token"`
	TencentCaptchaTicket  string `json:"tencent_captcha_ticket"`
	TencentCaptchaRandstr string `json:"tencent_captcha_randstr"`
}

// oauthStartResponse 是 POST 形态的返回体：SPA 需要自己跳转（弹窗内先过验证码，
// 拿到 URL 再跳），GET 形态则直接 302，保持老链接/书签可用。
type oauthStartResponse struct {
	AuthorizeURL string `json:"authorize_url"`
}

// requireActionCaptchaForOAuthLoginStart 在需要时校验行为验证码。
// 返回 false 表示已经写出错误响应，调用方必须立即 return（fail-close）。
//
// 绑定流程（路径以 /bind/start 结尾）刻意豁免：那是已登录用户在自己账号上做绑定，
// 不是匿名登录入口，加验证码只会挡住正常用户。官方语义一致。
func (h *AuthHandler) requireActionCaptchaForOAuthLoginStart(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	if strings.HasSuffix(strings.TrimRight(c.Request.URL.Path, "/"), "/bind/start") {
		return true
	}

	var req oauthStartCaptchaRequest
	// GET（链接/书签直达、第三方跳转）不带正文，此时 proof 必然为空；
	// 验证码未开启时直接放行，开启时按缺 proof 处理 → 400，绝不静默跳过。
	if c.Request.Method == http.MethodPost {
		_ = c.ShouldBindJSON(&req)
	}
	if err := h.authService.VerifyActionCaptchaIfEnabled(c.Request.Context(), service.CaptchaProof{
		TurnstileToken: req.TurnstileToken,
		TencentTicket:  req.TencentCaptchaTicket,
		TencentRandstr: req.TencentCaptchaRandstr,
	}, ip.GetClientIP(c)); err != nil {
		response.ErrorFrom(c, err)
		return false
	}
	return true
}

// respondOAuthStart 统一 OAuth 起点的响应形态：POST 返回 JSON 授权地址，
// 其它方法沿用 302 重定向（不改变既有 GET 行为）。
func respondOAuthStart(c *gin.Context, authorizeURL string) {
	if c.Request.Method == http.MethodPost {
		response.Success(c, oauthStartResponse{AuthorizeURL: authorizeURL})
		return
	}
	c.Redirect(http.StatusFound, authorizeURL)
}
