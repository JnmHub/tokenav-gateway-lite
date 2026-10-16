<script setup>
/* 伪装与中继配置组（账号编辑器子面板，600 行约束拆分）
 * 旧实现契约：frontend/src/components/account/EditAccountModal.vue 配额控制区
 * （TLS Fingerprint / Session ID Masking / Cache TTL Override / Custom Base URL Relay）。
 * 挂载条件（父组件控制）：platform=anthropic 且 type∈{oauth, setup-token}——
 * 后端 dto/mappers.go 只对 IsAnthropicOAuthOrSetupToken 回读这些字段。
 * extra 键写入语义（旧 EditAccountModal 5141-5175 行逐一对照：开=写键，关=删键）：
 * - enable_tls_fingerprint=true + tls_fingerprint_profile_id：
 *   null=删键（按平台取内置默认）；-1=随机选一个配置
 *   （service/tls_fingerprint_profile_service.go:190）；>0=绑定指定配置
 * - session_id_masking_enabled=true / 删键（15 分钟内固定 metadata.user_id 会话 ID）
 * - cache_ttl_override_enabled=true + cache_ttl_override_target：
 *   枚举仅 '5m'|'1h'，取自后端 gateway_service.go:9522-9535 写回逻辑（无枚举接口）
 * - custom_base_url_enabled=true + custom_base_url / 删键 */
import { computed, onMounted, ref } from 'vue'
import { request } from '../../api'

const tlsOn = defineModel('tlsOn', { type: Boolean, default: false })
const tlsProfileId = defineModel('tlsProfileId', { type: Number, default: null })
const maskingOn = defineModel('maskingOn', { type: Boolean, default: false })
const ttlOn = defineModel('ttlOn', { type: Boolean, default: false })
const ttlTarget = defineModel('ttlTarget', { type: String, default: '5m' })
const relayOn = defineModel('relayOn', { type: Boolean, default: false })
const relayUrl = defineModel('relayUrl', { type: String, default: '' })

// TLS 指纹配置下拉：复用「TLS 指纹配置」弹窗同款列表接口（GET /admin/tls-fingerprint-profiles）
const profiles = ref([])
const profileOptions = computed(() => [
  { value: null, label: '默认（平台内置指纹）' },
  ...(profiles.value.length ? [{ value: -1, label: '随机选择配置' }] : []),
  ...profiles.value.map((p) => ({ value: p.id, label: p.name })),
])
onMounted(async () => {
  try { profiles.value = (await request('/admin/tls-fingerprint-profiles', { method: 'GET' })) || [] }
  catch { /* 列表拉取失败时下拉降级为 默认/随机，不打断编辑 */ }
})
</script>

<template>
  <div class="adp">
    <div class="grp-t">伪装与中继</div>
    <div class="frow"><span class="flabel">TLS 伪装</span>
      <div class="fctl"><a-switch v-model:checked="tlsOn" /><span class="hint">定制出站 TLS ClientHello 指纹（JA3），降低上游风控识别</span></div>
    </div>
    <div v-if="tlsOn" class="frow"><span class="flabel">指纹配置</span>
      <div class="fctl"><a-select v-model:value="tlsProfileId" :options="profileOptions" style="width: 220px" />
        <span class="hint">在「更多工具 → TLS 指纹配置」维护</span></div>
    </div>
    <div class="frow"><span class="flabel">会话 ID 伪装</span>
      <div class="fctl"><a-switch v-model:checked="maskingOn" /><span class="hint">15 分钟内固定 metadata.user_id 中的会话 ID，让上游视为同一会话</span></div>
    </div>
    <div class="frow"><span class="flabel">缓存 TTL 覆写</span>
      <div class="fctl"><a-switch v-model:checked="ttlOn" /><span class="hint">把 cache_creation tokens 统一归入指定 TTL 类型计费</span></div>
    </div>
    <div v-if="ttlOn" class="frow"><span class="flabel">目标 TTL</span>
      <div class="fctl"><a-segmented v-model:value="ttlTarget" :options="[{ value: '5m', label: '5m' }, { value: '1h', label: '1h' }]" /></div>
    </div>
    <div class="frow"><span class="flabel">自定义中继</span>
      <div class="fctl"><a-switch v-model:checked="relayOn" /><span class="hint">Anthropic 上游请求经自建中继转发</span></div>
    </div>
    <div v-if="relayOn" class="frow"><span class="flabel">中继地址</span>
      <div class="fctl"><a-input v-model:value="relayUrl" placeholder="https://relay.example.com" style="max-width: 360px" /></div>
    </div>
  </div>
</template>

<style scoped>
.grp-t { font-size: 12px; font-weight: 600; color: var(--txt2); padding: 6px 8px 2px; }
.adp { width: 100%; }
</style>
