<script setup>
/* ============================================================
 * Grok 运行时自检卡（GET /admin/grok/runtime-sanity）
 * 响应结构对照 internal/pkg/xai/oauth.go RuntimeSanityReport：
 *   base_url / oauth_authorize_url / oauth_token_url / oauth_redirect_uri
 *     各为 RuntimeSanityCheck{ value, valid, error?, is_default? }
 *   unsafe_url_overrides / unsafe_high_concurrency / public_gateway_scope / proxy_policy
 * 校验明细逐条列出：全部通过给绿色标签，任一失败给红色标签。
 * 拆成独立组件，避免 GatewayTab.vue 超过单文件 600 行上限。
 * ============================================================ */
import { ref, computed } from 'vue'
import { request as apiRequest, isCancellation } from '../../api'
import { useRequestLifetime } from '../../composables/useRequestLifetime'
import { message } from 'ant-design-vue'
import { SafetyCertificateOutlined } from '@ant-design/icons-vue'

const loading = ref(false)
const report = ref(null)
const lifetime = useRequestLifetime(() => { report.value = null; loading.value = false })
const request = (path, options) => apiRequest(path, { ...options, ...lifetime })

const CHECK_ITEMS = [
  { key: 'base_url', label: 'Grok Base URL' },
  { key: 'oauth_authorize_url', label: 'OAuth 授权地址' },
  { key: 'oauth_token_url', label: 'OAuth Token 地址' },
  { key: 'oauth_redirect_uri', label: 'OAuth 回调地址' },
]
// 全部检查项通过才算健康（URL 校验失败时后端会把 error 写在对应项里）
const allValid = computed(() => CHECK_ITEMS.every((it) => report.value?.[it.key]?.valid))

const run = async () => {
  if (loading.value || !lifetime.isAlive()) return
  loading.value = true
  try {
    report.value = await request('/admin/grok/runtime-sanity', { method: 'GET' })
    if (allValid.value) message.success('Grok 运行时配置自检全部通过')
    else message.warning('Grok 运行时配置自检存在未通过项，请检查明细')
  } catch (e) {
    if (!isCancellation(e) && lifetime.isAlive()) message.error(`自检请求失败：${e.message}`)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="card">
    <div class="card-title">
      Grok 运行时自检
      <a-tag v-if="report" :color="allValid ? 'green' : 'red'" class="state">{{ allValid ? '全部通过' : '存在失败项' }}</a-tag>
    </div>
    <div class="card-sub">校验 Grok 上游 base_url 与 OAuth 各地址的格式与可信主机策略，结果来自进程当前生效配置，不发起外部请求。</div>

    <template v-if="report">
      <div class="form">
        <div v-for="it in CHECK_ITEMS" :key="it.key" class="frow">
          <label>{{ it.label }}</label>
          <div class="fmain">
            <a-tag :color="report[it.key]?.valid ? 'green' : 'red'" class="state">{{ report[it.key]?.valid ? '通过' : '失败' }}</a-tag>
            <span class="static mono">{{ report[it.key]?.value || '-' }}</span>
            <span v-if="report[it.key]?.is_default" class="static">默认值</span>
          </div>
        </div>
        <div v-for="it in CHECK_ITEMS" :key="`err-${it.key}`">
          <div v-if="!report[it.key]?.valid && report[it.key]?.error" class="frow">
            <label>{{ it.label }} 错误</label>
            <div class="fmain"><span class="errtxt">{{ report[it.key].error }}</span></div>
          </div>
        </div>
        <div class="frow">
          <label>不安全覆盖</label>
          <div class="fmain">
            <span class="static mono">URL 覆盖：{{ report.unsafe_url_overrides ? '允许' : '禁止' }} · 高并发：{{ report.unsafe_high_concurrency ? '允许' : '禁止' }}</span>
          </div>
        </div>
        <div class="frow">
          <label>公网范围</label>
          <div class="fmain"><span class="static mono">{{ report.public_gateway_scope || '-' }}</span></div>
        </div>
        <div class="frow">
          <label>代理策略</label>
          <div class="fmain"><span class="static">{{ report.proxy_policy || '-' }}</span></div>
        </div>
      </div>
    </template>
    <div v-else class="hint" style="padding-left: 8px">尚未执行自检，点击下方按钮获取当前配置校验明细。</div>

    <div class="savebar">
      <a-button type="primary" class="save" :loading="loading" @click="run"><SafetyCertificateOutlined /> 执行自检</a-button>
    </div>
  </div>
</template>

<style scoped>
@import './settings.css';
.state { border-radius: 4px; }
.errtxt { color: #d4380d; font-size: 12px; word-break: break-all; }
</style>
