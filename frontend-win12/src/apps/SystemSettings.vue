<script setup>
/* ============================================================
 * 系统配置 v2（SystemSettings 总表，对照 internal/handler/dto/settings.go 全量字段）
 * - GET /admin/settings 全量回显；PUT /admin/settings 保存
 * - 保存模式：快照+patch —— UpdateSettingsRequest 的非指针 bool/数组「未传=零值」，
 *   每次保存以 GET 快照为底叠加本卡改动，剔除响应专用位（*_configured/*_synced）、
 *   支付域（payment_*，独立子域整包覆盖）与独立子接口字段，避免误清其他分区
 * - 卡片流：主题外观 / 注册默认与安全 / 站点 OEM / Turnstile / 管理 API Key
 *   + 邮件服务 EmailCard / 通知 NotifyCard / 注册与登录策略 RegisterPolicyCard
 *   / 第三方登录 OAuthCard / 门户定制 PortalCard / 高级 AdvancedCard
 * ============================================================ */
import { ref, onMounted } from 'vue'
import { confirmAction } from '../utils/confirmAction'
import { request } from '../api'
import { message } from 'ant-design-vue'
import ThemeCard from '../components/settings/ThemeCard.vue'
import EmailCard from '../components/settings/EmailCard.vue'
import NotifyCard from '../components/settings/NotifyCard.vue'
import RegisterPolicyCard from '../components/settings/RegisterPolicyCard.vue'
import OAuthCard from '../components/settings/OAuthCard.vue'
import PortalCard from '../components/settings/PortalCard.vue'
import AdvancedCard from '../components/settings/AdvancedCard.vue'
// 备份恢复与系统版本：standard（主站）模式专属路由，gateway-lite 下 404 时卡片内部优雅降级提示
import BackupCard from '../components/settings/BackupCard.vue'
import VersionCard from '../components/settings/VersionCard.vue'
// v0.2.5 新功能卡：站点类型三态 / 人机验证（腾讯+阿里）/ 面板 API 限流
import SiteBillingCard from '../components/settings/SiteBillingCard.vue'
import CaptchaCard from '../components/settings/CaptchaCard.vue'
import PanelRateLimitCard from '../components/settings/PanelRateLimitCard.vue'

const loading = ref(true)
const savingKey = ref('')
const s = ref(null)
const akStatus = ref(null) // 独立接口：/admin/settings/admin-api-key（GET 状态）
const err = ref('')

const load = async () => {
  loading.value = true
  try {
    const [a, ak] = await Promise.all([
      request('/admin/settings', { method: 'GET' }),
      request('/admin/settings/admin-api-key', { method: 'GET' }).catch(() => null),
    ])
    s.value = a
    akStatus.value = ak
    err.value = ''
  } catch (e) {
    err.value = e.message
  } finally {
    loading.value = false
  }
}

/**
 * 构建保存 body：GET 快照为底 + patch 覆盖。
 * 剔除三类字段防止误覆盖：
 * - *_configured / *_synced：响应只读回显位，请求结构体无对应键
 * - payment_*：支付配置走独立保存域（hasPaymentFields 整包覆盖），不随总表提交
 * - openai_fast_policy_settings：fast/flex 策略独立子域
 */
const buildSaveBody = (patch) => {
  const body = { ...s.value }
  for (const k of Object.keys(body)) {
    if (k.endsWith('_configured') || k.endsWith('_synced')) delete body[k]
    // payment_balance_disabled（余额充值关闭开关）属于站点类型三态单选，
    // 与 subscription_enabled 一起随总表提交，豁免支付域剔除；其余 payment_* 仍走独立保存域
    else if (k.startsWith('payment_') && k !== 'payment_balance_disabled') delete body[k]
  }
  delete body.openai_fast_policy_settings
  for (const [k, v] of Object.entries(patch || {})) {
    if (v !== undefined) body[k] = v // undefined 键不提交（如留空的 password）
  }
  return body
}

// 统一保存入口：key 标识发起保存的卡片（按钮 loading）；PUT 响应即最新全量，直接回填
const savePatch = async (key, patch) => {
  savingKey.value = key
  try {
    const updated = await request('/admin/settings', { method: 'PUT', body: buildSaveBody(patch) })
    if (updated && typeof updated === 'object') s.value = updated
    message.success('已保存')
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    savingKey.value = ''
  }
}

/* ── 注册默认值与安全卡：字段局部 patch（沿用 v1 分组） ── */
const saveReg = () => savePatch('reg', {
  default_balance: Number(s.value.default_balance) || 0,
  default_concurrency: Number(s.value.default_concurrency) || 1,
  default_user_rpm_limit: Number(s.value.default_user_rpm_limit) || 0,
  totp_enabled: !!s.value.totp_enabled,
})

/* ── 站点 OEM 卡 ── */
const saveOem = () => savePatch('oem', {
  site_name: String(s.value.site_name || '').trim(),
  site_subtitle: String(s.value.site_subtitle || '').trim(),
  contact_info: String(s.value.contact_info || '').trim(),
  doc_url: String(s.value.doc_url || '').trim(),
  frontend_url: String(s.value.frontend_url || '').trim(),
})

/* ── Cloudflare Turnstile 卡 ── */
const saveTs = () => savePatch('ts', {
  turnstile_enabled: !!s.value.turnstile_enabled,
  turnstile_site_key: String(s.value.turnstile_site_key || '').trim(),
  // 注：turnstile_secret_key 留空不提交（响应无该字段），后端在启用时保留已存值
})

/* ── Codex 票据卡（v0.2.6：ChatGPT OAuth 292 x-codex-turn-state 打票） ── */
const saveCodexTicket = () => savePatch('codex_ticket', {
  openai_codex_ticket_enabled: !!s.value.openai_codex_ticket_enabled,
  openai_codex_ticket_harvest_proxy_url: String(s.value.openai_codex_ticket_harvest_proxy_url || '').trim(),
})

/* ── 管理 API Key：揭示 / 重生成 / 删除 ──
 * 明文仅揭示后暂存于前端内存（服务端只存哈希），刷新即消失 */
const akRevealed = ref('')
const akBusy = ref(false)

const akReveal = async () => {
  akBusy.value = true
  try {
    const res = await request('/admin/settings/admin-api-key/reveal', { method: 'POST' })
    akRevealed.value = res.key
    message.success('已获取明文，请立即复制保存')
  } catch (e) {
    message.error(e.message)
  } finally {
    akBusy.value = false
  }
}

const akRegenerate = async () => {
  if (!(await confirmAction({ title: '重新生成管理 API Key', content: '重新生成将使旧管理 API Key 立即失效，确认继续？', okText: '重新生成', danger: true }))) return
  akBusy.value = true
  try {
    const res = await request('/admin/settings/admin-api-key/regenerate', { method: 'POST' })
    akRevealed.value = res.key
    message.success('已重新生成，请立即复制保存新 Key')
    await load()
  } catch (e) {
    message.error(e.message)
  } finally {
    akBusy.value = false
  }
}

const akDelete = async () => {
  if (!(await confirmAction({ title: '删除管理 API Key', content: '删除后依赖管理 API Key 的调用将立即失败，确认删除？', okText: '删除', danger: true }))) return
  akBusy.value = true
  try {
    await request('/admin/settings/admin-api-key', { method: 'DELETE' })
    akRevealed.value = ''
    message.success('已删除')
    await load()
  } catch (e) {
    message.error(e.message)
  } finally {
    akBusy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="sysset">
    <div class="toolbar">
      <span class="total">系统配置 · 总表（SystemSettings）</span>
      <a-button v-if="err" size="small" danger @click="load">加载失败，点击重试</a-button>
    </div>
    <a-spin :spinning="loading">
      <!-- 主题外观（本地偏好，不依赖后端配置加载） -->
      <ThemeCard />
      <div style="height: 14px" />
      <div v-if="s" class="grid">
        <!-- 注册默认 + TOTP -->
        <div class="card">
          <div class="card-title">注册默认值与安全</div>
          <div class="form">
            <div class="frow"><label>新用户默认余额</label><div class="fmain"><a-input-number v-model:value="s.default_balance" :min="0" class="fld-n" /></div></div>
            <div class="frow"><label>默认并发数</label><div class="fmain"><a-input-number v-model:value="s.default_concurrency" :min="1" class="fld-n" /></div></div>
            <div class="frow"><label>默认 RPM 限制</label><div class="fmain"><a-input-number v-model:value="s.default_user_rpm_limit" :min="0" class="fld-n" /></div></div>
            <div class="frow"><label>TOTP 双因素认证</label><div class="fmain"><a-switch v-model:checked="s.totp_enabled" /></div></div>
            <div class="frow"><label>TOTP 加密密钥</label><div class="fmain"><span class="static">{{ s.totp_encryption_key_configured ? '已配置' : '未配置' }}</span></div></div>
          </div>
          <div class="savebar"><a-button type="primary" class="save" :loading="savingKey === 'reg'" @click="saveReg">保存</a-button></div>
        </div>
        <!-- 站点 OEM -->
        <div class="card">
          <div class="card-title">站点信息（OEM）</div>
          <div class="form">
            <div class="frow"><label>站点名称</label><div class="fmain"><a-input v-model:value="s.site_name" class="fld" /></div></div>
            <div class="frow"><label>副标题</label><div class="fmain"><a-input v-model:value="s.site_subtitle" class="fld" /></div></div>
            <div class="frow"><label>联系方式</label><div class="fmain"><a-input v-model:value="s.contact_info" class="fld" /></div></div>
            <div class="frow"><label>文档地址</label><div class="fmain"><a-input v-model:value="s.doc_url" class="fld" /></div></div>
            <div class="frow"><label>前端地址</label><div class="fmain"><a-input v-model:value="s.frontend_url" class="fld" /></div></div>
          </div>
          <div class="savebar"><a-button type="primary" class="save" :loading="savingKey === 'oem'" @click="saveOem">保存</a-button></div>
        </div>
        <!-- 邮件服务（SMTP + 测试邮件 + 邮件模板） -->
        <EmailCard :s="s" :save="savePatch" />
        <!-- 通知（余额/订阅到期/账号限额） -->
        <NotifyCard :s="s" :save="savePatch" />
        <!-- 注册与登录策略 -->
        <RegisterPolicyCard :s="s" :save="savePatch" />
        <!-- 第三方登录（6 家 OAuth 折叠面板） -->
        <OAuthCard :s="s" :save="savePatch" />
        <!-- 门户定制（首页内容/菜单/端点/购买入口） -->
        <PortalCard :s="s" :save="savePatch" />
        <!-- 杂项高级（调度权重/默认订阅/平台配额/Web 搜索开关） -->
        <AdvancedCard :s="s" :save="savePatch" />
        <!-- 站点类型（v0.2.5：subscription_enabled + payment_balance_disabled 三态派生） -->
        <SiteBillingCard :s="s" :save="savePatch" />
        <!-- 人机验证服务（v0.2.5：腾讯天御 + 阿里云验证码） -->
        <CaptchaCard :s="s" :save="savePatch" />
        <!-- 面板 API 限流（v0.2.5：独立子接口 /admin/ops/runtime/panel-rate-limit） -->
        <PanelRateLimitCard />
        <!-- 备份与恢复（S3 配置/立即备份/恢复/下载；standard 模式专属，lite 下显示降级提示） -->
        <BackupCard />
        <!-- 系统版本（当前版本/检查更新/更新/回滚；standard 模式专属，lite 下显示降级提示） -->
        <VersionCard />
        <!-- Turnstile -->
        <div class="card">
          <div class="card-title">Cloudflare Turnstile（人机验证）</div>
          <div class="form">
            <div class="frow"><label>启用</label><div class="fmain"><a-switch v-model:checked="s.turnstile_enabled" /></div></div>
            <div class="frow"><label>Site Key</label><div class="fmain"><a-input v-model:value="s.turnstile_site_key" class="fld" /></div></div>
            <div class="frow"><label>Secret Key</label><div class="fmain"><span class="static">{{ s.turnstile_secret_key_configured ? '已配置' : '未配置' }}</span></div></div>
          </div>
          <div class="savebar"><a-button type="primary" class="save" :loading="savingKey === 'ts'" @click="saveTs">保存</a-button></div>
        </div>
        <!-- Codex 票据（v0.2.6：ChatGPT OAuth 292 x-codex-turn-state 打票） -->
        <div class="card">
          <div class="card-title">Codex 票据（x-codex-turn-state）</div>
          <div class="form">
            <div class="frow"><label>启用打票</label><div class="fmain"><a-switch v-model:checked="s.openai_codex_ticket_enabled" /></div></div>
            <div class="frow"><label>打票代理 URL</label><div class="fmain"><a-input v-model:value="s.openai_codex_ticket_harvest_proxy_url" class="fld" placeholder="socks5h://user:pass@host:port（留空回退 yaml/env）" /></div></div>
            <div class="frow"><label>说明</label><div class="fmain"><span class="static">按 (账号, 模型) 捕获 292 长度 turn-state 并在住宅 IP 请求中注入；默认有效期 1 小时</span></div></div>
          </div>
          <div class="savebar"><a-button type="primary" class="save" :loading="savingKey === 'codex_ticket'" @click="saveCodexTicket">保存</a-button></div>
        </div>
        <!-- 管理 API Key -->
        <div class="card" v-if="akStatus">
          <div class="card-title">管理 API Key</div>
          <div class="form">
            <div class="frow"><label>已配置</label><div class="fmain"><span class="static">{{ akStatus.exists ? '是' : '否' }}</span></div></div>
            <div class="frow"><label>密钥（揭示后显示）</label><div class="fmain"><span class="mono">{{ akRevealed || akStatus.masked_key || '（未揭示）' }}</span></div></div>
          </div>
          <div class="savebar">
            <a-button size="small" :loading="akBusy" @click="akReveal">显示明文</a-button>
            <a-popconfirm title="旧 Key 将立即失效，确认重新生成？" @confirm="akRegenerate">
              <a-button size="small">重新生成</a-button>
            </a-popconfirm>
            <a-popconfirm title="依赖此 Key 的调用将失败，确认删除？" @confirm="akDelete">
              <a-button size="small" danger>删除</a-button>
            </a-popconfirm>
          </div>
        </div>
      </div>
    </a-spin>
  </div>
</template>

<style scoped>
@import '../components/settings/settings.css';
.sysset { height: 100%; overflow-y: auto; }
.toolbar { display: flex; align-items: center; gap: 12px; margin-bottom: 10px; }
.total { font-size: 12px; color: var(--text2); }
.grid { display: grid; grid-template-columns: 1fr; gap: 14px; }
.mono { font-family: Consolas, monospace; font-size: 12px; word-break: break-all; }
.card {
  padding: 12px 16px;
  border-radius: 8px;
  background: var(--card-solid);
  backdrop-filter: blur(24px) saturate(1.2);
  -webkit-backdrop-filter: blur(24px) saturate(1.2);
  border: 1px solid var(--stroke-card);
}
.card-title { font-weight: 600; font-size: 13px; margin-bottom: 6px; }
</style>
