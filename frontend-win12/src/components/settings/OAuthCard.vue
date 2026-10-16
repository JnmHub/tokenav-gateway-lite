<script setup>
/* ============================================================
 * 第三方登录卡：6 家 OAuth，a-collapse 每家一折叠面板
 * 字段名逐一对照 internal/handler/dto/settings.go（响应回显）与
 * internal/handler/admin/setting_handler.go UpdateSettingsRequest（写入键）：
 * - linuxdo_connect_*（4 项）
 * - dingtalk_connect_*（16 项：企业限制策略 + 身份同步 attr key/name）
 * - wechat_connect_*（16 项：开放平台/公众号/移动应用三渠道）
 * - oidc_connect_*（22 项）
 * - github_oauth_* / google_oauth_*（各 5 项）
 * secret 类字段：响应只有 *_configured 回显位；password 型输入留空 = 不提交，
 * 后端在启用时校验并保留已存值（UpdateSettings 各 OAuth 分支）。
 * ============================================================ */
import { ref, reactive } from 'vue'
import { SaveOutlined } from '@ant-design/icons-vue'

const props = defineProps({
  s: { type: Object, required: true },
  save: { type: Function, required: true },
})

/* 字段定义驱动渲染：type = text | password | switch | select | number
 * cfgKey = 后端「已配置」回显位（仅 password 使用）；secret 草稿存 secretDraft 不入快照 */
const PROVIDERS = [
  {
    key: 'linuxdo', title: 'LinuxDO Connect', fields: [
      { k: 'linuxdo_connect_enabled', label: '启用', type: 'switch' },
      { k: 'linuxdo_connect_client_id', label: 'Client ID', type: 'text', ph: 'LinuxDo OAuth App ID' },
      { k: 'linuxdo_connect_client_secret', label: 'Client Secret', type: 'password', cfgKey: 'linuxdo_connect_client_secret_configured' },
      { k: 'linuxdo_connect_redirect_url', label: '回调地址', type: 'text', ph: 'https://.../oauth/linuxdo/callback' },
    ],
  },
  {
    key: 'dingtalk', title: '钉钉（DingTalk Connect）', fields: [
      { k: 'dingtalk_connect_enabled', label: '启用', type: 'switch' },
      { k: 'dingtalk_connect_client_id', label: 'Client ID', type: 'text' },
      { k: 'dingtalk_connect_client_secret', label: 'Client Secret', type: 'password', cfgKey: 'dingtalk_connect_client_secret_configured' },
      { k: 'dingtalk_connect_redirect_url', label: '回调地址', type: 'text', ph: 'https://.../oauth/dingtalk/callback' },
      { k: 'dingtalk_connect_corp_restriction_policy', label: '企业限制策略', type: 'select', ph: '留空 = 沿用已保存策略',
        options: [{ value: 'internal_only', label: '仅本企业内部' }, { value: 'none', label: '不限制' }] },
      { k: 'dingtalk_connect_internal_corp_id', label: '企业 CorpId', type: 'text', ph: 'internal_only 时必填' },
      { k: 'dingtalk_connect_bypass_registration', label: '免注册登录', type: 'switch' },
      { k: 'dingtalk_connect_sync_corp_email', label: '同步企业邮箱', type: 'switch' },
      { k: 'dingtalk_connect_sync_corp_email_attr_key', label: '邮箱属性 Key', type: 'text', ph: '默认 dingtalk_email' },
      { k: 'dingtalk_connect_sync_corp_email_attr_name', label: '邮箱属性名', type: 'text', ph: '默认 钉钉企业邮箱' },
      { k: 'dingtalk_connect_sync_display_name', label: '同步姓名', type: 'switch' },
      { k: 'dingtalk_connect_sync_display_name_attr_key', label: '姓名属性 Key', type: 'text', ph: '默认 dingtalk_name' },
      { k: 'dingtalk_connect_sync_display_name_attr_name', label: '姓名属性名', type: 'text', ph: '默认 钉钉姓名' },
      { k: 'dingtalk_connect_sync_dept', label: '同步部门', type: 'switch' },
      { k: 'dingtalk_connect_sync_dept_attr_key', label: '部门属性 Key', type: 'text', ph: '默认 dingtalk_department' },
      { k: 'dingtalk_connect_sync_dept_attr_name', label: '部门属性名', type: 'text', ph: '默认 钉钉部门' },
    ],
  },
  {
    key: 'wechat', title: '微信（WeChat Connect）', fields: [
      { k: 'wechat_connect_enabled', label: '启用', type: 'switch' },
      { k: 'wechat_connect_mode', label: '登录渠道模式', type: 'select', ph: '留空自动判断',
        options: [{ value: 'open', label: '开放平台（PC 扫码）' }, { value: 'mp', label: '公众号（网页授权）' }, { value: 'mobile', label: '移动应用' }] },
      { k: 'wechat_connect_app_id', label: '通用 AppID', type: 'text', ph: '三渠道共用（可选）' },
      { k: 'wechat_connect_app_secret', label: '通用 Secret', type: 'password', cfgKey: 'wechat_connect_app_secret_configured' },
      { k: 'wechat_connect_open_enabled', label: '启用开放平台', type: 'switch' },
      { k: 'wechat_connect_open_app_id', label: '开放平台 AppID', type: 'text' },
      { k: 'wechat_connect_open_app_secret', label: '开放平台 Secret', type: 'password', cfgKey: 'wechat_connect_open_app_secret_configured' },
      { k: 'wechat_connect_mp_enabled', label: '启用公众号', type: 'switch' },
      { k: 'wechat_connect_mp_app_id', label: '公众号 AppID', type: 'text' },
      { k: 'wechat_connect_mp_app_secret', label: '公众号 Secret', type: 'password', cfgKey: 'wechat_connect_mp_app_secret_configured' },
      { k: 'wechat_connect_mobile_enabled', label: '启用移动应用', type: 'switch' },
      { k: 'wechat_connect_mobile_app_id', label: '移动应用 AppID', type: 'text' },
      { k: 'wechat_connect_mobile_app_secret', label: '移动应用 Secret', type: 'password', cfgKey: 'wechat_connect_mobile_app_secret_configured' },
      { k: 'wechat_connect_scopes', label: '授权 Scopes', type: 'text', ph: '留空 = 按渠道默认值' },
      { k: 'wechat_connect_redirect_url', label: '回调地址', type: 'text', ph: 'https://.../auth/wechat/callback' },
      { k: 'wechat_connect_frontend_redirect_url', label: '前端跳转地址', type: 'text', ph: '/auth/wechat/callback' },
    ],
  },
  {
    key: 'oidc', title: 'OIDC（通用协议）', fields: [
      { k: 'oidc_connect_enabled', label: '启用', type: 'switch' },
      { k: 'oidc_connect_provider_name', label: '提供商名称', type: 'text', ph: '默认 OIDC' },
      { k: 'oidc_connect_client_id', label: 'Client ID', type: 'text' },
      { k: 'oidc_connect_client_secret', label: 'Client Secret', type: 'password', cfgKey: 'oidc_connect_client_secret_configured' },
      { k: 'oidc_connect_issuer_url', label: 'Issuer URL', type: 'text', ph: 'https://idp.example.com' },
      { k: 'oidc_connect_discovery_url', label: 'Discovery URL', type: 'text', ph: '选填，自动发现端点' },
      { k: 'oidc_connect_authorize_url', label: 'Authorize URL', type: 'text', ph: '选填' },
      { k: 'oidc_connect_token_url', label: 'Token URL', type: 'text', ph: '选填' },
      { k: 'oidc_connect_userinfo_url', label: 'UserInfo URL', type: 'text', ph: '选填' },
      { k: 'oidc_connect_jwks_url', label: 'JWKS URL', type: 'text', ph: '选填' },
      { k: 'oidc_connect_scopes', label: '授权 Scopes', type: 'text', ph: '默认 openid email profile' },
      { k: 'oidc_connect_redirect_url', label: '回调地址', type: 'text' },
      { k: 'oidc_connect_frontend_redirect_url', label: '前端跳转地址', type: 'text', ph: '默认 /auth/oidc/callback' },
      { k: 'oidc_connect_token_auth_method', label: 'Token 认证方式', type: 'select', ph: '默认 client_secret_post',
        options: [{ value: 'client_secret_post', label: 'client_secret_post' }, { value: 'client_secret_basic', label: 'client_secret_basic' }, { value: 'none', label: 'none（PKCE）' }] },
      { k: 'oidc_connect_use_pkce', label: '使用 PKCE', type: 'switch' },
      { k: 'oidc_connect_validate_id_token', label: '校验 ID Token', type: 'switch' },
      { k: 'oidc_connect_allowed_signing_algs', label: '允许签名算法', type: 'text', ph: '默认 RS256,ES256,PS256' },
      { k: 'oidc_connect_clock_skew_seconds', label: '时钟偏移容差(秒)', type: 'number', min: 0, max: 600 },
      { k: 'oidc_connect_require_email_verified', label: '要求已验证邮箱', type: 'switch' },
      { k: 'oidc_connect_userinfo_email_path', label: '邮箱取值路径', type: 'text', ph: 'userinfo JSON path，选填' },
      { k: 'oidc_connect_userinfo_id_path', label: '用户 ID 路径', type: 'text', ph: '选填' },
      { k: 'oidc_connect_userinfo_username_path', label: '用户名路径', type: 'text', ph: '选填' },
    ],
  },
  {
    key: 'github', title: 'GitHub OAuth', fields: [
      { k: 'github_oauth_enabled', label: '启用', type: 'switch' },
      { k: 'github_oauth_client_id', label: 'Client ID', type: 'text' },
      { k: 'github_oauth_client_secret', label: 'Client Secret', type: 'password', cfgKey: 'github_oauth_client_secret_configured' },
      { k: 'github_oauth_redirect_url', label: '回调地址', type: 'text' },
      { k: 'github_oauth_frontend_redirect_url', label: '前端跳转地址', type: 'text', ph: '/auth/github/callback' },
    ],
  },
  {
    key: 'google', title: 'Google OAuth', fields: [
      { k: 'google_oauth_enabled', label: '启用', type: 'switch' },
      { k: 'google_oauth_client_id', label: 'Client ID', type: 'text' },
      { k: 'google_oauth_client_secret', label: 'Client Secret', type: 'password', cfgKey: 'google_oauth_client_secret_configured' },
      { k: 'google_oauth_redirect_url', label: '回调地址', type: 'text' },
      { k: 'google_oauth_frontend_redirect_url', label: '前端跳转地址', type: 'text', ph: '/auth/google/callback' },
    ],
  },
]

const activeKeys = ref([]) // 默认全收起，避免 6 家面板撑爆窗口

// secret 草稿：仅暂存本次输入的新值，保存成功后清空；绝不写进 settings 快照
const secretDraft = reactive({})
const secretPlaceholder = (f) => {
  const configured = !!props.s[f.cfgKey]
  return configured ? '已配置，留空保留' : '未配置'
}

// 每家独立保存：patch 只含本家字段（secret 留空不提交）
const savingKeys = reactive({})
const saveProvider = async (p) => {
  const patch = {}
  for (const f of p.fields) {
    if (f.type === 'switch') patch[f.k] = !!props.s[f.k]
    else if (f.type === 'number') patch[f.k] = Number(props.s[f.k]) || 0
    else if (f.type === 'password') {
      const v = String(secretDraft[f.k] || '').trim()
      if (v) patch[f.k] = v // 留空 = 保留后端已存 secret
    } else patch[f.k] = String(props.s[f.k] ?? '').trim()
  }
  savingKeys[p.key] = true
  try {
    await props.save(`oauth-${p.key}`, patch)
    for (const f of p.fields) if (f.type === 'password') secretDraft[f.k] = ''
  } finally {
    savingKeys[p.key] = false
  }
}

// 面板标题后缀：启用中的厂商打点提示
const isEnabled = (p) => !!props.s[p.fields[0].k]
</script>

<template>
  <div class="card">
    <div class="card-title">第三方登录（OAuth）</div>
    <div class="card-sub">展开各厂商面板配置 Client ID / Secret 与回调地址；Secret 留空 = 保留已保存值。</div>
    <a-collapse v-model:active-key="activeKeys" ghost expand-icon-position="end" class="oauth">
      <a-collapse-panel v-for="p in PROVIDERS" :key="p.key">
        <template #header>
          <span class="ptitle">{{ p.title }}</span>
          <a-tag v-if="isEnabled(p)" color="blue" class="ptag">已启用</a-tag>
        </template>
        <div class="form">
          <div v-for="f in p.fields" :key="f.k" class="frow">
            <template v-if="f.type === 'switch'">
              <label>{{ f.label }}</label>
              <div class="fmain"><a-switch v-model:checked="s[f.k]" /></div>
            </template>
            <template v-else-if="f.type === 'select'">
              <label>{{ f.label }}</label>
              <div class="fmain"><a-select v-model:value="s[f.k]" class="fld" :options="f.options" :placeholder="f.ph" allow-clear /></div>
            </template>
            <template v-else-if="f.type === 'number'">
              <label>{{ f.label }}</label>
              <div class="fmain"><a-input-number v-model:value="s[f.k]" class="fld-n" :min="f.min" :max="f.max" /></div>
            </template>
            <template v-else-if="f.type === 'password'">
              <label>{{ f.label }}</label>
              <div class="fmain">
                <a-input-password v-model:value="secretDraft[f.k]" class="fld" autocomplete="new-password"
                  :placeholder="secretPlaceholder(f)" />
                <span class="static">{{ s[f.cfgKey] ? '已配置' : '未配置' }}</span>
              </div>
            </template>
            <template v-else>
              <label>{{ f.label }}</label>
              <div class="fmain"><a-input v-model:value="s[f.k]" class="fld" :placeholder="f.ph" /></div>
            </template>
          </div>
        </div>
        <div class="savebar">
          <a-button type="primary" class="save" :loading="!!savingKeys[p.key]" @click="saveProvider(p)">
            <SaveOutlined /> 保存{{ p.title }}
          </a-button>
        </div>
      </a-collapse-panel>
    </a-collapse>
  </div>
</template>

<style scoped>
@import './settings.css';
.oauth :deep(.ant-collapse-header) { padding-left: 8px !important; }
.ptitle { font-size: 13px; }
.ptag { margin-left: 8px; }
</style>
